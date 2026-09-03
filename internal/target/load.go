package target

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/hydrate"
)

// LoadReport summarizes what a hydration loaded.
type LoadReport struct {
	Tables map[string]int64 // qualified table name -> rows inserted
	// SkippedIndexes names the secondary indexes that could not be created, with the
	// reason. It is never silently empty-on-failure: an index that production has and
	// the disposable database does not is a difference that can change a verdict, so
	// the caller reports these rather than discarding them.
	SkippedIndexes []string
	// SkippedConstraints names the deferred constraints (today: CHECKs) that could
	// not be applied, with the reason. Same rule as SkippedIndexes and for a sharper
	// reason: a CHECK production enforces and the target does not is a constraint a
	// migration can violate and still be certified for.
	SkippedConstraints []string
	// UnreproducibleGenerated names the STORED generated columns the fixture does
	// not describe well enough to recreate (no expression, or `opaque` under
	// privacy:strict), so they were created as ordinary columns.
	//
	// Reported rather than passed over because the target then ACCEPTS writes
	// production rejects: an UPDATE of a generated column fails in production with
	// `column "total" can only be updated to DEFAULT` and succeeds here.
	UnreproducibleGenerated []string
	// UnreproducedPartitionCount names partitioned tables whose partition COUNT the
	// target does not match, with what it has instead. Count changes lock behaviour
	// — a DDL statement on a parent locks every partition — so a target standing one
	// partition in for ninety understates it.
	UnreproducedPartitionCount []string
	// UnreproducibleDefaults names NOT NULL columns whose DEFAULT the fixture
	// withholds (`opaque` under privacy:strict), so the target has a bare NOT NULL.
	//
	// The opposite direction to UnreproducibleGenerated: the target now REJECTS
	// writes production accepts, so an ordinary INSERT that omits the column fails
	// here and succeeds there.
	UnreproducibleDefaults []string
}

// Load orchestrates a full hydration into a target: it connects, creates the
// schema and tables, generates rows with the deterministic engine, and inserts
// them — all inside one transaction so a failure leaves the target clean.
//
// The caller owns the target's lifecycle (Close tears a disposable one down);
// Load only fills it.
func Load(ctx context.Context, t Target, f *fixture.Fixture, opts hydrate.Options) (*LoadReport, error) {
	res, err := hydrate.Generate(f, opts)
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	conn, err := t.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to target: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	for _, stmt := range DDL(f) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			// Name the statement, not just the server's complaint. A missing extension
			// reports as `extension "citext" is not available`, which says nothing about
			// WHY rowshape wanted it or what to do; with the statement attached the
			// operator can see that the fixture requires citext and either install it or
			// point --ephemeral at an image that has it. Deliberately fatal rather than
			// best-effort: an extension type cannot be degraded to text, because a citext
			// column is case-insensitive and a text one is not, and quietly swapping them
			// would be a wrong verdict rather than a loud failure.
			return nil, fmt.Errorf("ddl failed on `%s`: %w", stmt, err)
		}
	}

	// Partitions come after the parents and BEFORE any row: a partitioned parent
	// accepts no rows until a partition exists that can hold them, so a COPY into one
	// fails outright with "no partition of relation found for row".
	for _, stmt := range Partitions(f) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return nil, fmt.Errorf("partition failed on `%s`: %w", stmt, err)
		}
	}

	report := &LoadReport{
		Tables:                     map[string]int64{},
		UnreproducibleGenerated:    UnreproducibleGenerated(f),
		UnreproducedPartitionCount: UnreproducedPartitionCounts(f),
		UnreproducibleDefaults:     UnreproducibleDefaults(f),
	}

	for _, gt := range res.Tables {
		n, err := insertRows(ctx, tx, gt)
		if err != nil {
			return nil, fmt.Errorf("insert into %s: %w", gt.Name, err)
		}
		report.Tables[gt.Name] = n
	}

	// Secondary indexes are built AFTER the rows, each inside a savepoint.
	//
	// Best-effort, because an index key can be an arbitrary expression and the fixture
	// records only the expression's TEXT — not the immutable functions or operator
	// classes it may call. Run unguarded, one such index would abort the whole load for
	// a schema that is otherwise perfectly hydratable. The savepoint keeps the failure
	// local: without one, the first error poisons the transaction and every later
	// statement fails with "current transaction is aborted" regardless of merit.
	//
	// AFTER, because partial and expression UNIQUE indexes are now emitted. Built
	// before the COPY, such an index turns any duplicate the synthesis engine produces
	// into a failed COPY that takes the entire load down — and a partial unique index
	// is precisely where duplicates are expected, since
	// `UNIQUE (email) WHERE deleted_at IS NULL` leaves `email` non-unique overall, so
	// the fixture marks it non-unique and the engine duly generates repeats. Building
	// afterwards means the index build is what fails, inside its savepoint, and the
	// index is REPORTED as skipped rather than ending the run.
	skipped, err := execGuarded(ctx, tx, "rowshape_idx", SecondaryIndexes(f))
	if err != nil {
		return nil, err
	}
	report.SkippedIndexes = skipped

	// Deferred constraints (CHECKs) run the same way, and after the rows for the
	// same reason: a CHECK the synthesis engine cannot satisfy should fail as its
	// own ADD CONSTRAINT inside a savepoint and be REPORTED, not take the load down
	// and not — as it used to — be dropped silently, which let a migration violating
	// a production CHECK come back PASS.
	skipped, err = execGuarded(ctx, tx, "rowshape_con", DeferredConstraints(f))
	if err != nil {
		return nil, err
	}
	report.SkippedConstraints = skipped

	// Advance each identity column's sequence past the values just loaded.
	//
	// Hydration supplies explicit ids (a child table's foreign keys have to point at
	// the ids its parent was actually loaded with), and an identity sequence does not
	// notice. It therefore still sits at 1 while the table holds 1..200, so the first
	// INSERT that OMITS the column — the ordinary way to insert into an identity
	// table, and the thing production does all day — collides with the primary key.
	// That is a wrong FAIL: the migration is fine and the target's bookkeeping is not.
	// pg_dump does the same thing for the same reason.
	if _, err := execGuarded(ctx, tx, "rowshape_seq", identityResets(f)); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return report, nil
}

// execGuarded runs each statement inside its own savepoint and returns the ones
// that failed, with the reason, instead of aborting.
//
// The savepoint is not defensive style, it is what makes best-effort possible at
// all: without one, the first error poisons the transaction and every later
// statement fails with "current transaction is aborted" regardless of merit — so
// one exotic index expression or one unsatisfiable CHECK would cost the whole
// load. A returned entry is a difference between the target and production that
// the caller MUST surface; silently discarding it is how a constraint production
// enforces goes missing without anyone knowing.
func execGuarded(ctx context.Context, tx pgx.Tx, prefix string, stmts []string) ([]string, error) {
	var skipped []string
	for i, stmt := range stmts {
		sp := fmt.Sprintf("%s_%d", prefix, i)
		if _, err := tx.Exec(ctx, "SAVEPOINT "+sp); err != nil {
			return nil, fmt.Errorf("savepoint: %w", err)
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+sp); rbErr != nil {
				return nil, fmt.Errorf("rollback to savepoint: %w", rbErr)
			}
			skipped = append(skipped, fmt.Sprintf("%s: %v", stmt, err))
			continue
		}
		if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+sp); err != nil {
			return nil, fmt.Errorf("release savepoint: %w", err)
		}
	}
	return skipped, nil
}

// insertRows bulk-loads one table's generated rows using the binary COPY
// protocol, which is both fast and avoids any SQL-literal quoting concerns.
func insertRows(ctx context.Context, tx pgx.Tx, gt hydrate.GeneratedTable) (int64, error) {
	if len(gt.Rows) == 0 || len(gt.Columns) == 0 {
		return 0, nil
	}
	rows := make([][]any, len(gt.Rows))
	copy(rows, gt.Rows)
	return tx.CopyFrom(ctx, tableIdentifier(gt.Name), gt.Columns, pgx.CopyFromRows(rows))
}

// tableIdentifier splits a qualified name into a pgx.Identifier for COPY.
func tableIdentifier(name string) pgx.Identifier {
	if i := indexByte(name, '.'); i >= 0 {
		return pgx.Identifier{name[:i], name[i+1:]}
	}
	return pgx.Identifier{name}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
