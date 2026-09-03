package target

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
)

// DDL renders CREATE SCHEMA / CREATE TABLE statements for a fixture, enough to
// receive hydrated rows. It creates columns with their types and structural
// nullability plus primary-key and unique constraints — the constraints the
// synthesis engine reliably satisfies (RFC §13).
//
// CHECK expressions are NOT emitted here either, but they are no longer dropped:
// they are rendered by DeferredConstraints and applied after the rows, each in
// its own savepoint. This file used to say CHECKs were "intentionally NOT
// emitted" because one "can carry domain logic that obviously-fake values needn't
// satisfy", deferring them to `validate`. `validate` never picked them up, so the
// deferral became a permanent hole in the ordinary case — nearly every production
// table has a CHECK — and it was demonstrated to produce a WRONG PASS: an UPDATE
// setting `status` to a value outside `CHECK (status IN (...))` returned PASS with
// exit 0 while the source database refused it outright. See DeferredConstraints
// for why the savepoint, not the omission, is the right answer to the original
// worry.
func DDL(f *fixture.Fixture) []string {
	var stmts []string

	// Create every referenced schema first (sorted for determinism). A user-defined
	// type can live in a schema no table does, so the type names contribute here
	// too — otherwise CREATE TYPE would target a schema that does not exist yet.
	schemas := map[string]bool{}
	for name := range f.Tables {
		schemas[schemaOf(name)] = true
	}
	for name := range f.Types {
		schemas[schemaOf(name)] = true
	}
	// An extension can be installed into a schema no table lives in, and CREATE
	// EXTENSION ... WITH SCHEMA requires that schema to exist already.
	for _, e := range f.Extensions {
		schemas[e.Schema] = true
	}
	for _, s := range sortedStrings(schemas) {
		if s != "" && s != "public" {
			stmts = append(stmts, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(s))
		}
	}

	// Then the extensions, before the types — because an extension IS how some types
	// arrive. A `citext` column named a type a fresh disposable database has never
	// heard of, so the whole DDL failed with `type "citext" does not exist` and
	// `validate` could not run at all on any schema using an extension type: citext
	// on an email column, hstore, ltree, postgis geometry, pgvector's `vector`.
	//
	// IF NOT EXISTS, and no version: the fixture's claim is that the schema NEEDS
	// citext, not that it needs a particular citext. Pinning would make a fixture
	// refuse to hydrate on a server shipping a different one, for nothing — the
	// disposable database only has to provide the type.
	for _, name := range sortedKeys(f.Extensions) {
		stmts = append(stmts, createExtension(name, f.Extensions[name]))
	}

	// Then the sequences a column DEFAULT names. A `serial`/`bigserial` column is an
	// ordinary column whose default is `nextval('t_id_seq'::regclass)` plus an owned
	// sequence — and the sequence is a separate object the fixture does not otherwise
	// carry, so the CREATE TABLE failed with `relation "app.events_id_seq" does not
	// exist`. Creating it keeps the column's real DDL rather than translating serial
	// into an identity column, which would report the column as something it is not.
	for _, name := range sequencesNamedByDefaults(f) {
		stmts = append(stmts, "CREATE SEQUENCE IF NOT EXISTS "+name)
	}

	// Then the user-defined types, before any table that carries one as a column
	// type (RFC §6.7). A column typed `z.mood` names something a fresh disposable
	// database has never heard of, so without these the whole DDL failed with
	// `type "z.mood" does not exist` and no enum- or domain-using schema could be
	// hydrated at all.
	for _, name := range sortedKeys(f.Types) {
		if stmt := createType(name, f.Types[name]); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}

	for _, name := range sortedKeys(f.Tables) {
		stmts = append(stmts, createTable(name, f.Tables[name]))
	}
	return stmts
}

// SecondaryIndexes renders the CREATE INDEX statements for a fixture's secondary
// indexes, separately from DDL because they are BEST-EFFORT.
//
// The distinction is about blast radius. A table must exist or nothing works, but
// an index key can be an arbitrary expression — `lower(email)`, a call to an
// immutable user-defined function, a custom operator class — and the fixture records
// the expression's TEXT without any of the functions or operator classes it may
// depend on. Emitting such an index inside the one big DDL transaction would abort
// the entire load for a schema that is otherwise perfectly hydratable.
//
// So the caller runs each of these in a savepoint and reports the ones that fail,
// rather than either dropping them silently (which is what used to happen, losing
// production uniqueness constraints) or letting one exotic expression break
// hydration outright.
func SecondaryIndexes(f *fixture.Fixture) []string {
	var stmts []string

	// Recreating them at all is what lets a migration reindex or build against the
	// indexes production has. On hydrated (small) data the builds are cheap; the
	// fixture-recorded bytes/bloat drive extrapolation, not the real build.
	//
	// SECONDARY is the operative word. Postgres backs every PRIMARY KEY and UNIQUE
	// constraint with an implicit index NAMED AFTER THE CONSTRAINT, and a conformant
	// `pull` records both the constraint (§6.4) and that index (§6.5) — they are both
	// really there. createTable already emits the constraint, which recreates its
	// index, so emitting the index again is a duplicate:
	//
	//	ERROR: relation "orders_pkey" already exists (SQLSTATE 42P07)
	//
	// Every hand-written test fixture lists constraints without their backing
	// indexes, so only a fixture from a real `pull` ever triggers this — that is,
	// every real schema with a primary key.
	for _, name := range sortedKeys(f.Tables) {
		backed := constraintBackedIndexes(f.Tables[name])
		for _, ix := range f.Tables[name].Indexes {
			if backed[ix.Name] {
				continue
			}
			if stmt := createIndex(name, ix); stmt != "" {
				stmts = append(stmts, stmt)
			}
		}
	}
	return stmts
}

// DeferredConstraints renders the ALTER TABLE ... ADD CONSTRAINT statements for
// the constraints that cannot go in CREATE TABLE, separately from DDL because
// they are BEST-EFFORT in the same sense SecondaryIndexes is.
//
// Today that is CHECK constraints. They used to be dropped entirely, on the
// reasoning that a CHECK "can carry domain logic that obviously-fake values
// needn't satisfy" — true, but the wrong remedy, and the cost was a demonstrated
// wrong PASS: an UPDATE writing a status outside `CHECK (status IN
// ('pending','paid','shipped'))` returned PASS with exit 0 while the source
// database refused it. A constraint the disposable database does not enforce is a
// constraint a migration can violate and be certified for.
//
// The savepoint is what makes emitting them safe. There are three possible
// behaviours and only one of them is honest:
//
//   - drop the constraint (what happened before) — silent, and the target
//     certifies migrations production rejects;
//   - emit it inside the main DDL transaction — one CHECK the synthesis engine
//     cannot satisfy takes down the whole load for an otherwise fine schema;
//   - emit it in a savepoint and REPORT the ones that fail — the operator learns
//     exactly which constraints the target is not enforcing.
//
// The caller runs these the same way it runs SecondaryIndexes, and after the rows
// for the same reason: a CHECK the hydrated data violates should fail as an ADD
// CONSTRAINT inside its savepoint, not as a failed COPY that ends the run.
func DeferredConstraints(f *fixture.Fixture) []string {
	var stmts []string
	for _, name := range sortedKeys(f.Tables) {
		tbl := f.Tables[name]
		for _, con := range tbl.Constraints {
			if stmt := addConstraint(name, con); stmt != "" {
				stmts = append(stmts, stmt)
			}
		}
		stmts = append(stmts, addForeignKeys(name, tbl.References)...)
	}
	return stmts
}

// addForeignKeys renders one ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY per
// recorded reference constraint (RFC §6.6).
//
// Foreign keys used to be dropped for a reason ddl.go stated as "a foreign key
// needs dependency-ordered loading". It does not, if the constraint is ADDED AFTER
// the rows rather than declared in CREATE TABLE — which is also what pg_dump does,
// and it makes table load order irrelevant, self-references work, and cycles
// between two tables work. The cost of dropping them was a demonstrated wrong
// PASS: an INSERT of a row whose parent does not exist returned PASS with exit 0
// while the source database refused it.
//
// A composite key arrives as one Reference per column pair sharing a constraint
// name, so entries are grouped by name and the column order within each group is
// preserved — `FOREIGN KEY (a, b) REFERENCES t (x, y)` is not two single-column
// keys, and rebuilding it as two would constrain something production does not.
func addForeignKeys(table string, refs []fixture.Reference) []string {
	type group struct {
		cols, refCols      []string
		refTable           string
		onDelete, onUpdate string
		validated          *bool
	}
	var order []string
	groups := map[string]*group{}

	for _, ref := range refs {
		refTable, refCol, ok := splitReference(ref.To)
		if !ok || ref.Column == "" {
			continue
		}
		// A reference with no recorded name predates the field (or came from a
		// hand-authored fixture). Key it uniquely so it still becomes a constraint
		// rather than being merged with an unrelated one — the server names it.
		key := ref.Name
		if key == "" {
			key = "\x00" + ref.Column + "\x00" + ref.To
		}
		g, seen := groups[key]
		if !seen {
			g = &group{refTable: refTable, onDelete: ref.OnDelete, onUpdate: ref.OnUpdate, validated: ref.Validated}
			groups[key] = g
			order = append(order, key)
		}
		g.cols = append(g.cols, ref.Column)
		g.refCols = append(g.refCols, refCol)
	}

	var stmts []string
	for _, key := range order {
		g := groups[key]
		if len(g.cols) != len(g.refCols) {
			continue
		}
		name := key
		if strings.HasPrefix(key, "\x00") {
			// Synthesized from the column pair; let Postgres pick the conventional name.
			name = table[strings.LastIndex(table, ".")+1:] + "_" + g.cols[0] + "_fkey"
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
			quoteTable(table), quoteIdent(name), quoteCols(g.cols), quoteTable(g.refTable), quoteCols(g.refCols))
		// no_action is the default and adding it changes nothing, so it is omitted to
		// keep the statement close to what the source database would render.
		if act := referentialAction(g.onDelete); act != "" {
			stmt += " ON DELETE " + act
		}
		if act := referentialAction(g.onUpdate); act != "" {
			stmt += " ON UPDATE " + act
		}
		// NOT VALID must be preserved: such a key is not enforced against existing
		// rows, so validating it here would make the target reject data production
		// holds — and would leave a migration's own VALIDATE CONSTRAINT nothing to do.
		if g.validated != nil && !*g.validated {
			stmt += " NOT VALID"
		}
		stmts = append(stmts, stmt)
	}
	return stmts
}

// splitReference splits a `schema.table.column` reference into `schema.table` and
// `column`. Anything without both separators is not a reference this can rebuild.
func splitReference(to string) (string, string, bool) {
	last := strings.LastIndex(to, ".")
	if last <= 0 || strings.Index(to, ".") == last {
		return "", "", false
	}
	return to[:last], to[last+1:], true
}

// referentialAction maps the fixture's snake_case action to SQL, returning "" for
// the default (no_action) and for anything unrecognized — a guessed action would
// change what a parent-row delete does, which is the question being asked.
func referentialAction(a string) string {
	switch a {
	case "cascade":
		return "CASCADE"
	case "restrict":
		return "RESTRICT"
	case "set_null":
		return "SET NULL"
	case "set_default":
		return "SET DEFAULT"
	default:
		return ""
	}
}

// addConstraint renders one ALTER TABLE ... ADD CONSTRAINT, or "" for a kind that
// CREATE TABLE already handled or that this version does not reconstruct.
func addConstraint(table string, con fixture.Constraint) string {
	if con.Name == "" {
		return ""
	}
	switch con.Kind {
	case "check":
		// "opaque" is the placeholder privacy:strict leaves behind (§6.4). It is not a
		// predicate, and inventing one would constrain hydrated data in a way production
		// may not — the same call createType already makes for an opaque domain CHECK.
		if con.Expression == "" || con.Expression == "opaque" {
			return ""
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)",
			quoteTable(table), quoteIdent(con.Name), con.Expression)
		// NOT VALID must be preserved. A NOT VALID constraint is not enforced against
		// existing rows, so recreating it as validated would make the target reject data
		// production holds — and a migration's own VALIDATE CONSTRAINT, which is the
		// interesting statement, would then have nothing to validate.
		if con.Validated != nil && !*con.Validated {
			stmt += " NOT VALID"
		}
		return stmt
	}
	// primary_key and unique are emitted inline by createTable; exclusion and
	// foreign_key are not reconstructed here. An unrecognized kind renders nothing
	// rather than a guess.
	return ""
}

// constraintBackedIndexes returns the index names that a PRIMARY KEY or UNIQUE
// constraint on this table already creates.
//
// Postgres names the implicit index after the constraint that owns it, which is
// what makes name matching exact rather than a guess: `orders_pkey` the
// constraint and `orders_pkey` the index are the same object viewed twice.
func constraintBackedIndexes(tbl fixture.Table) map[string]bool {
	backed := make(map[string]bool)
	for _, con := range tbl.Constraints {
		switch con.Kind {
		case "primary_key", "unique":
			if con.Name != "" {
				backed[con.Name] = true
			}
		}
	}
	return backed
}

// createIndex renders a secondary index, including the expression and partial
// forms. Both used to be skipped, which quietly removed uniqueness constraints
// production enforces — see the Keys and Partial handling below.
func createIndex(table string, ix fixture.Index) string {
	if ix.Name == "" {
		return ""
	}

	// Keys first: they are the only description of an EXPRESSION index. An index on
	// lower(email) has no column key, so keying off `columns` alone rendered it
	// unbuildable and it was dropped — taking a uniqueness constraint production
	// enforces with it, which is how a migration that violates that constraint could
	// PASS. Keys come from pg_get_indexdef and are already valid SQL (including any
	// DESC or operator class), so they are emitted verbatim, exactly as the recorded
	// partial predicate and domain CHECK are.
	keys := ix.Keys
	if len(keys) == 0 {
		if len(ix.Columns) == 0 {
			return ""
		}
		keys = quotedCols(ix.Columns)
	}

	unique := ""
	if ix.Unique {
		unique = "UNIQUE "
	}
	using := ""
	if ix.Method != "" && !strings.EqualFold(ix.Method, "btree") {
		using = " USING " + ix.Method
	}

	stmt := fmt.Sprintf("CREATE %sINDEX %s ON %s%s (%s)",
		unique, quoteIdent(ix.Name), quoteTable(table), using, strings.Join(keys, ", "))

	// INCLUDE payload, which must stay OUT of the key list above. Folding it in is
	// what turned `UNIQUE (customer_id, created_at) INCLUDE (total)` into a
	// three-column unique index — strictly weaker than what production enforces, so
	// the disposable database accepted rows the source database rejects.
	if len(ix.Include) > 0 {
		stmt += " INCLUDE (" + strings.Join(quotedCols(ix.Include), ", ") + ")"
	}

	// A PARTIAL index used to be skipped entirely. That is the dangerous one to drop:
	// a partial UNIQUE index is the standard soft-delete uniqueness pattern
	// (`UNIQUE (email) WHERE deleted_at IS NULL`), and without it the disposable
	// database does not enforce what production does.
	//
	// "opaque" is the placeholder privacy:strict leaves behind (§6.4). It is not a
	// predicate, and inventing one would change what the index enforces, so the index
	// is skipped rather than built with the wrong scope.
	if ix.Partial != "" {
		if ix.Partial == "opaque" {
			return ""
		}
		stmt += " WHERE " + ix.Partial
	}
	return stmt
}

// createExtension renders CREATE EXTENSION for one required extension (RFC §6.8).
//
// WITH SCHEMA only when the fixture recorded one: a type name in a column can be
// schema-qualified (`ext.citext`), and installing the extension somewhere else
// would leave that name unresolvable. Where the source did not record a schema,
// the server's default is the honest choice — inventing `public` would be a guess
// that silently disagrees with a search_path this code does not control.
func createExtension(name string, e fixture.Extension) string {
	stmt := "CREATE EXTENSION IF NOT EXISTS " + quoteIdent(name)
	if e.Schema != "" {
		stmt += " WITH SCHEMA " + quoteIdent(e.Schema)
	}
	return stmt
}

// createType renders CREATE TYPE / CREATE DOMAIN for one user-defined type
// (RFC §6.7). An unrecognized kind returns "" so an extension to the vocabulary
// degrades to the pre-existing behavior — the DDL names the missing type and fails
// there — rather than emitting a statement built from a guess.
func createType(name string, t fixture.UserType) string {
	switch t.Kind {
	case "enum":
		// EffectiveLabels, not t.Labels: under privacy:strict the vocabulary is
		// withheld and only the count survives, and the placeholders it invents must
		// be the SAME ones the synthesis engine draws from — a single disagreement
		// would make every insert of the missing label fail as not a member of the
		// type. Hence one shared implementation on the model rather than a copy here.
		labels := t.EffectiveLabels()
		if len(labels) == 0 {
			return "" // an enum with neither labels nor a count cannot be created
		}
		quoted := make([]string, len(labels))
		for i, l := range labels {
			quoted[i] = quoteLiteral(l)
		}
		return fmt.Sprintf("CREATE TYPE %s AS ENUM (%s)", quoteTable(name), strings.Join(quoted, ", "))
	case "domain":
		if t.Base == "" {
			return ""
		}
		stmt := fmt.Sprintf("CREATE DOMAIN %s AS %s", quoteTable(name), t.Base)
		if t.NotNull {
			stmt += " NOT NULL"
		}
		// An opaque CHECK (privacy:strict) is deliberately NOT reconstructed: the
		// expression is unknown, and inventing one would constrain hydrated data in a
		// way production may not. The domain is created without it, which is the
		// weaker but honest reading of what the fixture actually says.
		if t.Check != "" && t.Check != "opaque" {
			stmt += " CHECK " + t.Check
		}
		return stmt
	}
	return ""
}

// quoteLiteral renders a SQL string literal, doubling embedded quotes. Enum labels
// are arbitrary text from the source catalog, so a label containing an apostrophe
// must not be able to break out of the statement.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// createTable renders one CREATE TABLE statement.
func createTable(name string, tbl fixture.Table) string {
	var lines []string
	for _, col := range sortedKeys(tbl.Columns) {
		c := tbl.Columns[col]
		line := fmt.Sprintf("  %s %s", quoteIdent(col), c.Type)
		if !c.Nullable {
			line += " NOT NULL"
		}
		line += defaultClause(c) + generatedClause(c)
		lines = append(lines, line)
	}
	for _, con := range tbl.Constraints {
		// On a PARTITIONED table, Postgres requires every UNIQUE or PRIMARY KEY to
		// INCLUDE the partition key, and refuses the whole CREATE TABLE otherwise
		// ("unique constraint on partitioned table must include all partitioning
		// columns"). Production's constraint satisfies that by construction; the
		// fixture's may not describe it well enough to prove so — and taking the whole
		// table down is worse than the constraint being reported as unenforced, which
		// is what skipping it here achieves via the same savepoint path everything
		// else uses.
		if !constraintFitsPartitionKey(con, tbl.Partitions) {
			continue
		}
		switch con.Kind {
		case "primary_key":
			lines = append(lines, fmt.Sprintf("  PRIMARY KEY (%s)", quoteCols(con.Columns)))
		case "unique":
			lines = append(lines, fmt.Sprintf("  UNIQUE (%s)", quoteCols(con.Columns)))
		}
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n)%s", quoteTable(name), strings.Join(lines, ",\n"),
		partitionClause(tbl.Partitions))
}

// sequencesNamedByDefaults returns, sorted and deduplicated, the sequences that
// column DEFAULTs reference through nextval().
//
// The name is taken verbatim from the recorded expression — already quoted and
// schema-qualified exactly as the source rendered it — so CREATE SEQUENCE and the
// DEFAULT that uses it cannot disagree about which object they mean.
func sequencesNamedByDefaults(f *fixture.Fixture) []string {
	seen := map[string]bool{}
	for _, tname := range sortedKeys(f.Tables) {
		tbl := f.Tables[tname]
		for _, cname := range sortedKeys(tbl.Columns) {
			if name, ok := nextvalSequence(defaultClause(tbl.Columns[cname])); ok {
				seen[name] = true
			}
		}
	}
	return sortedStrings(seen)
}

// nextvalSequence extracts the sequence name from a `nextval('x'::regclass)`
// default, returning false for anything else. Anything it does not recognize with
// certainty yields nothing, so an unusual default is left to fail loudly on its
// own terms rather than having a sequence invented for it.
func nextvalSequence(clause string) (string, bool) {
	i := strings.Index(clause, "nextval(")
	if i < 0 {
		return "", false
	}
	rest := clause[i+len("nextval("):]
	open := strings.Index(rest, "'")
	if open < 0 {
		return "", false
	}
	closeAt := strings.Index(rest[open+1:], "'")
	if closeAt < 0 {
		return "", false
	}
	name := strings.TrimSpace(rest[open+1 : open+1+closeAt])
	if name == "" {
		return "", false
	}
	return name, true
}

// defaultClause renders the DEFAULT part of a column definition, or "" when the
// column has none this can reproduce (RFC §6.1).
//
// Without it, every migration that inserts or backfills without naming every NOT
// NULL column failed in the target and succeeded in production — the target had a
// bare `NOT NULL` where production has `NOT NULL DEFAULT '{}'::jsonb`. That is a
// wrong FAIL on the most ordinary statement there is.
//
// A generated column never gets one: `generated` already carries how its value
// arrives, and DEFAULT is not legal alongside GENERATED. "opaque" is the
// privacy:strict placeholder, not an expression, and inventing one would give the
// target a default production does not have.
func defaultClause(c fixture.Column) string {
	if c.Generated != "" || c.Default == "" || c.Default == "opaque" {
		return ""
	}
	return " DEFAULT " + c.Default
}

// UnreproducibleDefaults names the columns a fixture says carry a DEFAULT that
// this cannot reproduce, so the caller can report that the target REJECTS writes
// production accepts.
//
// The opposite direction to UnreproducibleGenerated, and worth reporting for the
// same reason: a difference between target and production that changes a verdict
// must never be silent, whichever way it leans.
func UnreproducibleDefaults(f *fixture.Fixture) []string {
	var out []string
	for _, tname := range sortedKeys(f.Tables) {
		tbl := f.Tables[tname]
		for _, cname := range sortedKeys(tbl.Columns) {
			c := tbl.Columns[cname]
			if c.Default == "opaque" && !c.Nullable && c.Generated == "" {
				out = append(out, tname+"."+cname)
			}
		}
	}
	return out
}

// generatedClause renders the GENERATED part of a column definition, or "" for an
// ordinary column (RFC §6.1).
//
// Dropping it was a wrong PASS in two directions at once. A STORED generated
// column rebuilt as an ordinary one ACCEPTS an UPDATE that production rejects with
// `column "total" can only be updated to DEFAULT`; an identity column rebuilt as
// an ordinary NOT NULL one REJECTS an INSERT that omits it, which production
// accepts. Both were reproduced.
//
// A stored column with no usable expression falls back to an ordinary column
// rather than a guessed one — an invented expression would compute values
// production never held. The caller reports that, so the operator knows the target
// permits writes production does not.
func generatedClause(c fixture.Column) string {
	switch c.Generated {
	case "identity":
		// ALWAYS is the stricter reading and is what the catalog said; BY DEFAULT is
		// the default spelling for a fixture that predates the `identity` field, where
		// the permissive form is the safer guess: it accepts explicit values, so a
		// hydrated INSERT that supplies one still works.
		if c.Identity == "always" {
			return " GENERATED ALWAYS AS IDENTITY"
		}
		return " GENERATED BY DEFAULT AS IDENTITY"
	case "stored":
		// "opaque" is the placeholder privacy:strict leaves behind. It is not an
		// expression, and inventing one would compute values production never had.
		if c.GeneratedExpression == "" || c.GeneratedExpression == "opaque" {
			return ""
		}
		return " GENERATED ALWAYS AS (" + c.GeneratedExpression + ") STORED"
	case "virtual":
		// PostgreSQL 18's VIRTUAL generated columns. Not reproduced: the syntax does
		// not exist on any earlier major, so emitting it would make a fixture from an
		// 18 source unhydratable on the 10-17 targets rowshape supports. The column
		// becomes ordinary and is REPORTED, which is the same honest degradation a
		// stored column with a withheld expression gets — the target then accepts
		// writes production rejects, and the operator is told so.
		return ""
	}
	return ""
}

// partitionClause renders the `PARTITION BY <strategy> (<key>)` a partitioned
// parent is declared with, or "" for an ordinary table (RFC §14.2).
//
// It is what stopped a partitioned table being rebuilt as an ordinary one, which
// was a wrong PASS with no findings at all: `CREATE INDEX CONCURRENTLY` on a
// partitioned parent is refused OUTRIGHT by Postgres (`cannot create index on
// partitioned table "events" concurrently`), and the target happily built it.
// Partitioned tables are the norm on exactly the large tables rowshape exists for.
//
// An unrecognized strategy, or a missing key, renders nothing: the table is then
// created unpartitioned and REPORTED, rather than declared over a key that is a
// guess.
func partitionClause(p *fixture.Partitions) string {
	if p == nil || p.Key == "" {
		return ""
	}
	switch p.Strategy {
	case "range", "list", "hash":
		return " PARTITION BY " + strings.ToUpper(p.Strategy) + " (" + p.Key + ")"
	}
	return ""
}

// Partitions renders the CREATE TABLE ... PARTITION OF statements for every
// partitioned parent, which must run after the parents exist and before any row is
// loaded — a partitioned parent accepts no rows until a partition can hold them.
//
// Bounds deliberately do NOT match production, and do not need to: what changes a
// migration's behaviour is the strategy, the partition COUNT, and the fact that
// the table is partitioned at all. Reproducing real bounds would need the key
// column's true extremes, which for a sampled `range` are not known (PR-T7) and
// for a text or composite key are not recorded at all.
//
// The shapes are chosen so rows always land somewhere:
//
//   - HASH: MODULUS/REMAINDER covers the whole key space by construction, needs no
//     knowledge of the values, and reproduces the count exactly.
//   - RANGE and LIST: a single DEFAULT partition. It catches every row whatever the
//     synthesised values are, which matters because a row that matches no partition
//     fails the COPY outright and takes the load down.
//
// Where the count could not be reproduced, UnreproducedPartitionCounts says so:
// partition count changes lock behaviour, so a target with one partition standing
// in for ninety is a difference the operator has to know about.
func Partitions(f *fixture.Fixture) []string {
	var stmts []string
	for _, tname := range sortedKeys(f.Tables) {
		p := f.Tables[tname].Partitions
		if partitionClause(p) == "" {
			continue
		}
		// The parent's qualified name is the stem, so a synthesized partition lands in
		// the parent's schema rather than in whatever the search_path happens to be.
		base := tname
		switch p.Strategy {
		case "hash":
			n := p.Count
			if n < 1 {
				n = 1
			}
			for i := 0; i < n; i++ {
				stmts = append(stmts, fmt.Sprintf(
					"CREATE TABLE %s PARTITION OF %s FOR VALUES WITH (MODULUS %d, REMAINDER %d)",
					quoteTable(base+fmt.Sprintf("_p%d", i)), quoteTable(tname), n, i))
			}
		default:
			stmts = append(stmts, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s DEFAULT",
				quoteTable(base+"_default"), quoteTable(tname)))
		}
	}
	return stmts
}

// UnreproducedPartitionCounts names the partitioned tables whose partition COUNT
// the target does not reproduce, with what it has instead.
//
// Reported rather than passed over because count is not decoration: a DDL
// statement on a partitioned parent takes locks on every partition, and index
// builds and ATTACH/DETACH scale with the number of them. A target standing one
// partition in for ninety understates that, and understating a lock is the
// direction that turns a real finding into a clean run.
func UnreproducedPartitionCounts(f *fixture.Fixture) []string {
	var out []string
	for _, tname := range sortedKeys(f.Tables) {
		p := f.Tables[tname].Partitions
		if p == nil {
			continue
		}
		if partitionClause(p) == "" {
			out = append(out, fmt.Sprintf(
				"%s is partitioned (%s, %d partitions) but the fixture does not record a usable partition key, so it was created as an ordinary table",
				tname, p.Strategy, p.Count))
			continue
		}
		if p.Strategy != "hash" && p.Count > 1 {
			out = append(out, fmt.Sprintf(
				"%s has %d %s partitions in production; the target has 1 (a DEFAULT partition), because %s bounds cannot be reconstructed from a fixture",
				tname, p.Count, p.Strategy, p.Strategy))
		}
	}
	return out
}

// identityResets renders the statements that move each identity column's sequence
// past the rows just loaded.
//
// Needed because hydration supplies explicit ids — a child table's foreign keys
// must point at the ids its parent was actually loaded with — and an identity
// sequence does not observe explicit inserts. Left alone it still reads 1 while
// the table holds 1..N, so the first INSERT that omits the column (the ordinary
// way to insert into an identity table) collides with the primary key and the
// migration is reported as broken when it is not.
//
// setval over a subquery rather than a computed constant, so the statement is
// correct whatever `--scale` or `--max-rows` actually loaded. COALESCE handles the
// empty table, where the sequence must simply stay where it started.
func identityResets(f *fixture.Fixture) []string {
	var stmts []string
	for _, tname := range sortedKeys(f.Tables) {
		tbl := f.Tables[tname]
		for _, cname := range sortedKeys(tbl.Columns) {
			if tbl.Columns[cname].Generated != "identity" {
				continue
			}
			stmts = append(stmts, fmt.Sprintf(
				"SELECT setval(pg_get_serial_sequence(%s, %s), (SELECT COALESCE(MAX(%s), 1) FROM %s), true)",
				quoteLiteral(quoteTable(tname)), quoteLiteral(cname), quoteIdent(cname), quoteTable(tname)))
		}
	}
	return stmts
}

// UnreproducibleGenerated names the columns a fixture says are generated but which
// the target could not declare as such, so the caller can report that the target
// permits writes production rejects.
//
// Two ways in: a STORED column whose expression the fixture withholds, and any
// VIRTUAL column (PostgreSQL 18), whose syntax does not exist on the earlier
// majors rowshape supports as targets.
func UnreproducibleGenerated(f *fixture.Fixture) []string {
	var out []string
	for _, tname := range sortedKeys(f.Tables) {
		tbl := f.Tables[tname]
		for _, cname := range sortedKeys(tbl.Columns) {
			c := tbl.Columns[cname]
			if (c.Generated == "stored" || c.Generated == "virtual") && generatedClause(c) == "" {
				out = append(out, tname+"."+cname)
			}
		}
	}
	return out
}

// constraintFitsPartitionKey reports whether a PRIMARY KEY / UNIQUE can be
// declared on a partitioned table: Postgres requires it to contain every
// partitioning column.
//
// Only a plain single-column key is checked, because that is the only shape a
// fixture describes unambiguously; a composite or expression key returns false,
// which skips the constraint rather than emitting one Postgres will reject. An
// unpartitioned table always fits.
func constraintFitsPartitionKey(con fixture.Constraint, p *fixture.Partitions) bool {
	if p == nil || p.Key == "" {
		return true
	}
	if con.Kind != "primary_key" && con.Kind != "unique" {
		return true
	}
	key := strings.TrimSpace(p.Key)
	if key == "" || strings.ContainsAny(key, "(), ") {
		return false // composite or expression key: not provably contained
	}
	for _, c := range con.Columns {
		if c == key {
			return true
		}
	}
	return false
}

// quotedCols quotes each column separately, for callers that assemble the list
// themselves (an index whose keys may mix plain columns and expressions).
func quotedCols(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteIdent(c)
	}
	return out
}

// quoteCols quotes a column list for a constraint definition.
func quoteCols(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteIdent(c)
	}
	return strings.Join(out, ", ")
}

// quoteTable quotes a schema.table identifier.
func quoteTable(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return quoteIdent(name[:i]) + "." + quoteIdent(name[i+1:])
	}
	return quoteIdent(name)
}

// schemaOf returns the schema part of a qualified name, or "" if unqualified.
func schemaOf(name string) string {
	if i := strings.Index(name, "."); i >= 0 {
		return name[:i]
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
