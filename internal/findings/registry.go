package findings

import "sort"

// Explanation is the canonical documentation for a finding code: what it means,
// why it matters, and how to fix it. It is the SINGLE source of the remediation
// text — analyzers set a finding's Remediation from here, and `rowshape explain`
// renders the same entry, so the two can never drift (PRD §8.1, §10).
type Explanation struct {
	Code        string   `json:"code"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Remediation string   `json:"remediation"`
	References  []string `json:"references"`
}

// catalog documents every finding code the analyzers can emit. A code missing
// here has no remediation (and no explain entry), which the tests forbid.
var catalog = map[string]Explanation{
	"RS-LOCK-001": {
		Code:        "RS-LOCK-001",
		Title:       "ACCESS EXCLUSIVE lock for a full table rewrite",
		Summary:     "Adding a column with a volatile default, or changing a column's type, rewrites every row while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed until it finishes. On a large table that is a write outage.",
		Remediation: "Avoid the full-table ACCESS EXCLUSIVE rewrite. For a volatile default: ADD the column nullable with no default, backfill in batches, then attach the default and SET NOT NULL via a validated CHECK. For a type change: add a new column of the target type, backfill it in batches, swap reads/writes, and drop the old column. Each step is online.",
		References:  []string{"RFC §9.1", "PRD §10"},
	},
	"RS-DATA-001": {
		Code:        "RS-DATA-001",
		Title:       "SET NOT NULL against existing NULLs",
		Summary:     "SET NOT NULL scans the table and rejects rows that are NULL. If the column's null_fraction is above zero the migration fails; if the zero is only estimated, it cannot be certified safe.",
		Remediation: "Backfill or delete the NULL rows first, or add a DEFAULT; then SET NOT NULL. A validated CHECK (col IS NOT NULL) lets SET NOT NULL skip the full-table scan on PG 12+.",
		References:  []string{"RFC §7.4", "PRD §10"},
	},
	"RS-DATA-014": {
		Code:        "RS-DATA-014",
		Title:       "ADD UNIQUE without proven uniqueness",
		Summary:     "ADD CONSTRAINT UNIQUE can only succeed if the column is actually unique. Uniqueness is never inferred from a sample (INV-UNIQUENESS): unproven uniqueness cannot certify PASS, and proven duplicates make the constraint fail to build.",
		Remediation: "Prove uniqueness before adding the constraint. If duplicates already exist, de-duplicate the column first (remove or merge the duplicate rows).",
		References:  []string{"RFC §7.2", "RFC §7.4", "PRD §10"},
	},
	"RS-DATA-020": {
		Code:        "RS-DATA-020",
		Title:       "FOREIGN KEY validated against pre-existing orphans",
		Summary:     "Validating a foreign key scans every child row for a matching parent. If the reference's orphan_fraction is above zero, rows already violate the key and the VALIDATE fails.",
		Remediation: "Delete or repair the orphaned rows before validating the foreign key: ADD the constraint NOT VALID, clean up the orphans, then VALIDATE CONSTRAINT.",
		References:  []string{"RFC §6.6", "PRD §10"},
	},
	"RS-CONSTRAINT-001": {
		Code:        "RS-CONSTRAINT-001",
		Title:       "NOT VALID constraint validated in the same transaction",
		Summary:     "Adding a constraint NOT VALID and VALIDATE-ing it in one transaction still runs the full validating scan under the transaction's locks — the two-step split whose entire purpose is to avoid a long lock buys nothing.",
		Remediation: "Split across transactions: ADD CONSTRAINT ... NOT VALID and COMMIT, then VALIDATE CONSTRAINT in a separate transaction. VALIDATE then takes only a SHARE UPDATE EXCLUSIVE lock and does not block reads or writes.",
		References:  []string{"RFC §6.4", "RFC §9.1", "PRD §10"},
	},
	"RS-CONSTRAINT-010": {
		Code:        "RS-CONSTRAINT-010",
		Title:       "CHECK constraint conflicts with existing data",
		Summary:     "The column's profiled range violates the CHECK predicate, so existing rows already fail it and adding the constraint (or validating it) fails.",
		Remediation: "Repair or exclude the rows that violate the predicate before adding the CHECK (or widen the predicate). Add the constraint NOT VALID, fix the data, then VALIDATE.",
		References:  []string{"RFC §6.1", "RFC §6.4", "PRD §10"},
	},
	// RS-APPLY is a SEVENTH namespace beyond the six INV-VERDICT-STABLE names, and
	// it is deliberately not one of them. The other six classify HAZARDS found in a
	// migration that ran; this one says the migration did not run. Folding it into
	// RS-DATA would claim the data rejected a statement that may never have parsed.
	// Recorded as a contract extension in docs/DECISIONS.md D-023.
	"RS-APPLY-001": {
		Code:        "RS-APPLY-001",
		Title:       "Migration did not apply",
		Summary:     "A statement in the migration was rejected by the database, so nothing downstream was evaluated. The verdict carries the engine's own SQLSTATE and message, and the file and line the statement came from.",
		Remediation: "Read the SQLSTATE and message in the finding's evidence: they are the database's own words about what it refused. Fix the statement at the reported file and line, then re-run validate. A class-23 code (23505 unique_violation, 23502 not_null_violation, 23514 check_violation) means production-shaped DATA rejected it — the migration is syntactically fine and the data does not permit it. A class-42 code (42P01 undefined_table, 42703 undefined_column, 42601 syntax_error) means the statement does not match the schema it was written against.",
		References:  []string{"PRD §10", "RFC §13"},
	},
	"RS-INDEX-001": {
		Code:        "RS-INDEX-001",
		Title:       "Non-concurrent CREATE INDEX blocks writes",
		Summary:     "A plain CREATE INDEX holds a lock that blocks writes for the whole O(n log n) build. On a large table that is a long write outage.",
		Remediation: "Use CREATE INDEX CONCURRENTLY: it builds in two passes without an exclusive lock, so writes continue. Run it outside a transaction block.",
		References:  []string{"RFC §6.5", "RFC §9.1", "PRD §10"},
	},
	"RS-INDEX-002": {
		Code:        "RS-INDEX-002",
		Title:       "ADD PRIMARY KEY or UNIQUE builds an index under ACCESS EXCLUSIVE",
		Summary:     "Adding a PRIMARY KEY or UNIQUE constraint over existing data builds a unique index while holding an ACCESS EXCLUSIVE lock — no reads or writes proceed for the whole O(n log n) build, and ADD PRIMARY KEY also scans the column for NULLs. On a large table that is a full outage, not just a write block. This is the lock cost of building the constraint, separate from whether the data lets it build at all (RS-DATA-014).",
		Remediation: "Build the index first without the exclusive lock, then adopt it: CREATE UNIQUE INDEX CONCURRENTLY on the column(s), then attach it with ALTER TABLE ... ADD PRIMARY KEY/UNIQUE USING INDEX <name>, which holds the exclusive lock only briefly. For a PRIMARY KEY, ensure the column is already NOT NULL first (add a validated CHECK (col IS NOT NULL) if needed).",
		References:  []string{"RFC §6.5", "RFC §9.1", "PRD §10"},
	},
	"RS-INDEX-010": {
		Code:        "RS-INDEX-010",
		Title:       "CREATE UNIQUE INDEX without proven uniqueness",
		Summary:     "A unique index can only build if the indexed set is actually unique. Uniqueness is never inferred from a sample (INV-UNIQUENESS): unproven uniqueness cannot certify PASS, and proven duplicates make the build fail. A PARTIAL index (WHERE ...) or an EXPRESSION index (lower(email)) is a special case: the fixture records uniqueness for the whole column, which describes neither the predicate-selected subset nor the expression, so rowshape declines to decide in either direction and warns instead — duplicates in soft-deleted rows do not stop a `WHERE deleted_at IS NULL` index from building.",
		Remediation: "Prove uniqueness before creating the unique index. If duplicates already exist, de-duplicate the column first (remove or merge the duplicate rows).",
		References:  []string{"RFC §6.5", "RFC §7.2", "PRD §10"},
	},
	"RS-INDEX-020": {
		Code:        "RS-INDEX-020",
		Title:       "Non-concurrent REINDEX rebuilds under lock",
		Summary:     "A non-concurrent REINDEX rewrites the whole index while holding a lock that blocks writes. Its cost is driven by the index's on-disk size and bloat.",
		Remediation: "Use REINDEX INDEX CONCURRENTLY (PG 12+) so the rebuild does not block writes.",
		References:  []string{"RFC §6.5", "PRD §10"},
	},
	"RS-PERF-001": {
		Code:        "RS-PERF-001",
		Title:       "DELETE cascades through a long-tailed fan-out",
		Summary:     "Deleting from a parent table referenced ON DELETE CASCADE cascades to its children. When the fan-out is long-tailed (the max dwarfs the mean), deleting the wrong parents cascades to a huge, slow, lock-holding delete — an outage a uniform mean hides.",
		Remediation: "Delete in bounded batches (by primary-key range), and check the fan-out tail before deleting parents with many cascaded children. Consider detaching or soft-deleting children first so the cascade is bounded.",
		References:  []string{"RFC §6.6", "PRD §10"},
	},
	"RS-PERF-002": {
		Code:        "RS-PERF-002",
		Title:       "Unqualified UPDATE/DELETE touches every row",
		Summary:     "An UPDATE or DELETE with no WHERE clause rewrites or removes every row of a large table — a slow, lock-holding, bloat-inducing full scan that is almost never intended.",
		Remediation: "Add a WHERE clause to scope the change. For a genuine full-table update, run it in bounded batches (by primary-key range) and VACUUM afterward to reclaim the bloat.",
		References:  []string{"PRD §10"},
	},
	"RS-DEPLOY-001": {
		Code:        "RS-DEPLOY-001",
		Title:       "Renaming a column or table breaks running code that still uses the old name",
		Summary:     "The migration is safe and fast — this is not a lock or a data problem. The hazard is ordering: from the instant it commits, every application instance still running the previous release references a name that no longer exists, and fails. Rolling deploys and blue/green make that a certainty rather than a race, because old and new code run simultaneously by design. Nothing about applying the migration to a database reveals it, because the database is fine.",
		Remediation: "Use expand/contract across separate deploys rather than renaming in place. EXPAND: add the new column, and have the application write BOTH names. BACKFILL: copy the existing values across. SWITCH: ship a release that reads the new name. CONTRACT: once no running code references the old name, drop it in a later migration. For a table, the same shape works with a view carrying the old name over the new table while the switch lands.",
		References:  []string{"PRD §10", "PRD §12"},
	},
	"RS-TX-001": {
		Code:        "RS-TX-001",
		Title:       "This statement cannot run inside a transaction block",
		Summary:     "Postgres refuses a handful of statements inside a transaction block with SQLSTATE 25001: CREATE/DROP INDEX CONCURRENTLY, REINDEX CONCURRENTLY, VACUUM, CLUSTER, CREATE/DROP DATABASE and TABLESPACE, ALTER SYSTEM, and (before PostgreSQL 12) ALTER TYPE ... ADD VALUE. Most migration runners — Alembic, Django, Rails, Flyway — wrap each migration file in a transaction by default, so a file that looks fine standalone fails under the runner. rowshape's own sandbox does not execute your BEGIN/COMMIT, applying each statement in its own transaction so locks can be inspected, so a clean apply is NOT evidence that this works.",
		Remediation: "Move the statement into its own migration that runs outside a transaction, and tell your runner. Alembic: set transaction_per_migration and use an autocommit block. Django: set atomic = False on the Migration class. Rails: disable_ddl_transaction!. Flyway: use a script-based migration or set executeInTransaction to false. golang-migrate: it does not wrap by default, so no change is needed. If you opened the transaction yourself with BEGIN, split the statement out of that block.",
		References:  []string{"PRD §10", "PRD §13"},
	},
	"RS-CONSTRAINT-020": {
		Code:        "RS-CONSTRAINT-020",
		Title:       "ADD CONSTRAINT without NOT VALID scans the whole table under ACCESS EXCLUSIVE",
		Summary:     "Adding a CHECK or FOREIGN KEY constraint without NOT VALID validates every existing row before the statement returns, holding ACCESS EXCLUSIVE for the whole scan — so reads and writes on the table block for its duration. This SUCCEEDS when the data is valid; the cost is the lock, not the outcome, which is why applying it to a small or freshly-hydrated table reveals nothing about what it does in production.",
		Remediation: "Split it in two. First ADD CONSTRAINT ... NOT VALID, which takes only a brief lock and applies to new and updated rows immediately. Then, in a separate statement (and ideally a separate migration), VALIDATE CONSTRAINT, which scans the table under a SHARE UPDATE EXCLUSIVE lock that does not block reads or writes. Keep the two apart: doing both in one transaction holds the exclusive lock across the scan anyway and gains nothing (RS-CONSTRAINT-001).",
		References:  []string{"PRD §10", "RFC §9.1"},
	},
	"RS-INDEX-003": {
		Code:        "RS-INDEX-003",
		Title:       "DROP INDEX without CONCURRENTLY takes ACCESS EXCLUSIVE on the table",
		Summary:     "A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the TABLE, not merely on the index — so every read and write on the table queues behind it, and behind anything already holding a conflicting lock. The drop itself is fast, which is exactly why it looks harmless in a sandbox: the risk is the lock queue on a busy table, not the work.",
		Remediation: "Use DROP INDEX CONCURRENTLY, which takes only SHARE UPDATE EXCLUSIVE and does not block reads or writes. It cannot run inside a transaction block (see RS-TX-001), so it needs its own migration with the runner's transaction wrapping disabled. Set a short lock_timeout either way, so a drop that cannot get its lock fails fast instead of queueing every query behind it.",
		References:  []string{"PRD §10"},
	},
	"RS-PERF-010": {
		Code:        "RS-PERF-010",
		Title:       "A qualified UPDATE/DELETE on a large table rewrites many rows in one statement",
		Summary:     "RS-PERF-002 only fires when a statement has NO WHERE clause, but the real-world hazard always has one: UPDATE users SET normalized = lower(email) WHERE normalized IS NULL is the canonical unbatched backfill — one statement, tens of millions of rows, one transaction, row locks held for the whole duration, table bloat, and a replication lag spike. Where the fixture can size the predicate it does (null_fraction is exactly the selectivity of an IS NULL test); where it cannot, it reports the table's row count as an upper bound rather than falling silent.",
		Remediation: "Batch it. Loop over a bounded key range — UPDATE ... WHERE id BETWEEN :lo AND :hi AND <predicate> — committing each batch, so no single transaction holds locks over the whole table and autovacuum can keep up between batches. Ten to fifty thousand rows per batch is a common starting point. Run the backfill OUTSIDE the migration if your runner wraps migrations in one transaction, since batching inside a single transaction defeats the purpose. If the statement genuinely matches only a handful of rows, the upper bound reported here is conservative and you can disregard it.",
		References:  []string{"PRD §10"},
	},
	"RS-DEPLOY-002": {
		Code:        "RS-DEPLOY-002",
		Title:       "Changing REPLICA IDENTITY changes what downstream consumers can identify",
		Summary:     "REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY. Changing it raises no error and does not affect queries, so nothing about applying the migration reveals a problem — but logical replication subscribers, CDC pipelines and analytics mirrors can silently start receiving UPDATE and DELETE events they cannot match to a row. NOTHING is the extreme case: those events carry no old-row identity at all. FULL is the safe-but-costly direction, writing the entire old row into WAL on every UPDATE and DELETE.",
		Remediation: "Check what consumes this table's changes before changing its identity: SELECT * FROM pg_publication_tables WHERE tablename = '<table>'; and check for active replication slots with SELECT * FROM pg_replication_slots. If you are dropping the index that backs REPLICA IDENTITY USING INDEX, set a new identity BEFORE dropping it, not after. If you need FULL, size the WAL increase first — it is proportional to update volume, not table size.",
		References:  []string{"PRD §10"},
	},
	"RS-DEPLOY-003": {
		Code:        "RS-DEPLOY-003",
		Title:       "Dropping NOT NULL withdraws a guarantee running code relies on",
		Summary:     "The migration is instant and safe for the database. The hazard is the contract: application code, ORM models, serializers and downstream schemas were written against a column that could never be null, and none of them are re-checked when that guarantee is withdrawn. Nothing fails at migration time — the first null arrives later, at runtime, somewhere else. This is the reverse of RS-DATA-001, which covers adding the constraint.",
		Remediation: "Ship the code that TOLERATES a null first, then relax the constraint in a later migration. That is the same expand/contract ordering a rename needs, in the other direction: make the readers safe before the writers are allowed to produce the new shape. If the column is exposed through an API or a downstream schema, check those contracts too — a nullable column is a breaking change to a consumer that declared it required.",
		References:  []string{"PRD §10"},
	},
	"RS-LOCK-003": {
		Code:        "RS-LOCK-003",
		Title:       "ATTACH PARTITION validates the incoming rows under a lock on the parent",
		Summary:     "ATTACH PARTITION takes ACCESS EXCLUSIVE on the PARENT table — blocking every query against every partition — and then scans the incoming table to prove every row satisfies the partition bound, unless a matching CHECK constraint already exists. On a large incoming partition that scan is the outage, and it happens while the whole partitioned table is locked. Non-concurrent DETACH takes the same lock on the parent.",
		Remediation: "Add a CHECK constraint matching the partition bound to the incoming table BEFORE attaching, and validate it separately: ALTER TABLE incoming ADD CONSTRAINT c CHECK (<bound>) NOT VALID; then VALIDATE CONSTRAINT c; then ATTACH. With the constraint already proven, ATTACH skips the scan and the lock is brief. On PostgreSQL 14+, prefer DETACH PARTITION CONCURRENTLY for the reverse direction.",
		References:  []string{"PRD §10", "RFC §14.2"},
	},
	"RS-LOCK-010": {
		Code:        "RS-LOCK-010",
		Title:       "The migration takes ACCESS EXCLUSIVE without setting lock_timeout",
		Summary:     "With no lock_timeout, a DDL statement that cannot acquire its lock immediately WAITS — and while it waits, every new query on that table queues behind it, because a pending ACCESS EXCLUSIVE request blocks incoming readers too. A migration that is instant in isolation can therefore stall an entire table behind one long-running transaction it happened to collide with. This is the mechanism behind most 'one quick migration took the site down' incidents, and it is invisible to any per-statement rule because no single statement is wrong.",
		Remediation: "Set a short timeout at the head of the migration: SET lock_timeout = '3s'; so a statement that cannot get its lock fails fast instead of queueing every reader behind it. Pair it with a retry: a failed migration you can re-run is strictly better than a stalled table. For operations that must eventually succeed on a busy table, retry in a loop with backoff rather than raising the timeout. Note that SET LOCAL scopes it to the transaction, which is usually what you want inside a migration.",
		References:  []string{"PRD §10"},
	},
	"RS-LOCK-011": {
		Code:        "RS-LOCK-011",
		Title:       "Several statements take ACCESS EXCLUSIVE on the same table",
		Summary:     "Each lock acquisition queues independently, so the table is unavailable across the whole sequence rather than for the duration of the longest single statement — and between them, traffic that built up during one lock competes for the next. The individual statements can each look perfectly reasonable; the cost is in the repetition.",
		Remediation: "Combine the changes into a single ALTER TABLE with comma-separated actions — ALTER TABLE t ADD COLUMN a int, ADD COLUMN b int, ALTER COLUMN c SET NOT NULL — which takes the lock once. Where the operations genuinely cannot be combined, split them into separate migrations so each takes its lock independently and a failure does not leave the table half-changed.",
		References:  []string{"PRD §10"},
	},
	"RS-REVERSE-001": {
		Code:        "RS-REVERSE-001",
		Title:       "DROP COLUMN loses its data irreversibly",
		Summary:     "Dropping a column permanently removes its values across every row. A down-migration can recreate the column, but not what it held — the rollback is lossy.",
		Remediation: "Drop in two phases across separate deploys: first stop writing and reading the column and ship that, then drop it in a later migration once you are sure it is unused. Take a backup (or snapshot the column into an archive table) before the drop so the data is recoverable.",
		References:  []string{"PRD §10", "PRD §12"},
	},
	"RS-REVERSE-002": {
		Code:        "RS-REVERSE-002",
		Title:       "DROP TABLE loses every row irreversibly",
		Summary:     "Dropping a table permanently removes every row. A down-migration can recreate the table structure, but not its data — the rollback cannot restore what was there.",
		Remediation: "Drop in two phases across separate deploys: first stop using the table and ship that, then drop it in a later migration once you are sure it is unreferenced. Take a backup (or rename it aside — ALTER TABLE ... RENAME TO — rather than dropping) so the data is recoverable.",
		References:  []string{"PRD §10", "PRD §12"},
	},
	"RS-REVERSE-004": {
		Code:        "RS-REVERSE-004",
		Title:       "TRUNCATE removes every row irreversibly and locks the table",
		Summary:     "TRUNCATE deletes every row in the table and cannot be rolled back once committed. It takes an ACCESS EXCLUSIVE lock, so every read and write on the table blocks for its duration, and it does not fire per-row DELETE triggers — so audit or soft-delete logic built on them is silently skipped. Because TRUNCATE succeeds instantly against a small or freshly-hydrated table, nothing about running it reveals how much production data it would destroy.",
		Remediation: "If you mean to discard the data, take a backup first and say so in the migration. If you mean to remove SOME rows, use DELETE with a WHERE clause. If you are resetting a table between deploys, consider renaming it aside (ALTER TABLE ... RENAME TO) so the rows remain recoverable. TRUNCATE ... CASCADE additionally empties every table with a foreign key into this one — check what that reaches before running it.",
		References:  []string{"PRD §10", "PRD §12"},
	},
	"RS-REVERSE-003": {
		Code:        "RS-REVERSE-003",
		Title:       "Narrowing a column type can truncate data irreversibly",
		Summary:     "Narrowing a column's type (a wider integer to a smaller one, an unbounded string to a length-limited one, or a fractional number to an integer) can truncate or round values. Widening back cannot restore the lost precision.",
		Remediation: "Keep the wider type, or migrate without loss: add a new column of the target type, backfill it while checking every value fits, swap reads and writes over, then drop the old column in a later migration. Take a backup before any in-place narrowing.",
		References:  []string{"PRD §10", "PRD §12"},
	},
}

// Explain returns the documentation for a finding code.
func Explain(code string) (Explanation, bool) {
	e, ok := catalog[code]
	return e, ok
}

// Codes returns every documented finding code, sorted.
func Codes() []string {
	codes := make([]string, 0, len(catalog))
	for c := range catalog {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

// remediation returns the canonical remediation for a code — the text analyzers
// attach to their findings, identical to what `rowshape explain` prints.
func remediation(code string) string { return catalog[code].Remediation }
