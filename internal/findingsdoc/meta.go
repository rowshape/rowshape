package findingsdoc

// pageMeta is the search-facing frontmatter of a finding page: the <title> tag
// (seoTitle, at most 60 characters so a results page does not truncate it) and
// the meta description (sized to the snippet a results page renders).
//
// It lives HERE, not in internal/findings, because it is written for a search
// results page and nothing else: `rowshape explain` and the MCP tool return the
// catalog's Title and Summary, which are written for a reader who already has
// the finding in hand. Before this table existed the tuned values were edited
// into the generated pages by hand, which failed the staleness test and meant
// the next `go run ./tools/genfindings` would silently throw them away.
type pageMeta struct {
	SEOTitle    string
	Description string
}

var pageMetas = map[string]pageMeta{
	"RS-APPLY-001": {
		SEOTitle:    "RS-APPLY-001: the migration did not apply",
		Description: "A statement in the migration was rejected by the database, so nothing downstream was evaluated.",
	},
	"RS-CONSTRAINT-001": {
		SEOTitle:    "RS-CONSTRAINT-001: NOT VALID validated in the same tx",
		Description: "Adding a constraint NOT VALID and validating it in the same transaction still runs the full scan under that transaction's locks.",
	},
	"RS-CONSTRAINT-010": {
		SEOTitle:    "RS-CONSTRAINT-010: CHECK conflicts with existing data",
		Description: "The column's profiled range violates the CHECK predicate, so existing rows already fail it and adding the constraint (or validating it) fails.",
	},
	"RS-CONSTRAINT-020": {
		SEOTitle:    "RS-CONSTRAINT-020: ADD CONSTRAINT without NOT VALID",
		Description: "ADD CONSTRAINT without NOT VALID validates every existing row before the statement returns, holding ACCESS EXCLUSIVE for the whole scan.",
	},
	"RS-DATA-001": {
		SEOTitle:    "RS-DATA-001: SET NOT NULL against existing NULLs",
		Description: "SET NOT NULL scans the table and rejects rows that are NULL. If the column's null_fraction is above zero, the migration fails on contact.",
	},
	"RS-DATA-014": {
		SEOTitle:    "RS-DATA-014: ADD UNIQUE without proven uniqueness",
		Description: "ADD CONSTRAINT UNIQUE can only succeed if the column is actually unique.",
	},
	"RS-DATA-020": {
		SEOTitle:    "RS-DATA-020: FOREIGN KEY hits pre-existing orphan rows",
		Description: "Validating a foreign key scans every child row for a matching parent. If orphaned rows already exist, the VALIDATE fails when it reaches them.",
	},
	"RS-DEPLOY-001": {
		SEOTitle:    "RS-DEPLOY-001: renaming a column or table breaks code",
		Description: "The migration is safe and fast — this is not a lock or a data problem.",
	},
	"RS-DEPLOY-002": {
		SEOTitle:    "RS-DEPLOY-002: REPLICA IDENTITY change breaks consumers",
		Description: "REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY.",
	},
	"RS-DEPLOY-003": {
		SEOTitle:    "RS-DEPLOY-003: dropping NOT NULL breaks running code",
		Description: "Dropping NOT NULL is instant for the database, but it withdraws a guarantee that application code, ORMs and downstream schemas were written against.",
	},
	"RS-INDEX-001": {
		SEOTitle:    "RS-INDEX-001: non-concurrent CREATE INDEX blocks writes",
		Description: "A plain CREATE INDEX holds a lock that blocks writes for the whole O(n log n) build.",
	},
	"RS-INDEX-002": {
		SEOTitle:    "RS-INDEX-002: ADD PRIMARY KEY or UNIQUE locks the table",
		Description: "Adding a PRIMARY KEY or UNIQUE constraint builds a unique index under an ACCESS EXCLUSIVE lock — no reads or writes for the whole build.",
	},
	"RS-INDEX-003": {
		SEOTitle:    "RS-INDEX-003: DROP INDEX without CONCURRENTLY locks",
		Description: "A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the table, not just the index, so every read and write on it queues behind you.",
	},
	"RS-INDEX-010": {
		SEOTitle:    "RS-INDEX-010: CREATE UNIQUE INDEX, uniqueness unproven",
		Description: "A unique index only builds if the indexed set really is unique, and rowshape never infers uniqueness from a sample — unproven cannot be a PASS.",
	},
	"RS-INDEX-020": {
		SEOTitle:    "RS-INDEX-020: non-concurrent REINDEX rebuilds under lock",
		Description: "A non-concurrent REINDEX rewrites the whole index while holding a lock that blocks writes.",
	},
	"RS-LOCK-001": {
		SEOTitle:    "RS-LOCK-001: ACCESS EXCLUSIVE for a full table rewrite",
		Description: "Adding a column with a volatile default, or changing a column's type, rewrites every row while holding an ACCESS EXCLUSIVE lock.",
	},
	"RS-LOCK-003": {
		SEOTitle:    "RS-LOCK-003: ATTACH PARTITION locks the parent table",
		Description: "ATTACH PARTITION takes ACCESS EXCLUSIVE on the parent table, blocking every partition, then scans the incoming rows against the bound.",
	},
	"RS-LOCK-010": {
		SEOTitle:    "RS-LOCK-010: ACCESS EXCLUSIVE without a lock_timeout",
		Description: "Without a lock_timeout, a DDL statement that cannot take its lock waits — and every new query on that table queues behind it while it does.",
	},
	"RS-LOCK-011": {
		SEOTitle:    "RS-LOCK-011: repeated ACCESS EXCLUSIVE on one table",
		Description: "Each lock is acquired separately, so the table is unavailable across the whole sequence rather than for the longest single statement.",
	},
	"RS-PERF-001": {
		SEOTitle:    "RS-PERF-001: DELETE cascades through a long fan-out",
		Description: "Deleting from a parent table referenced ON DELETE CASCADE cascades to its children.",
	},
	"RS-PERF-002": {
		SEOTitle:    "RS-PERF-002: unqualified UPDATE or DELETE hits every row",
		Description: "An UPDATE or DELETE with no WHERE clause rewrites or removes every row of a large table — a slow, lock-holding, bloat-inducing full scan.",
	},
	"RS-PERF-010": {
		SEOTitle:    "RS-PERF-010: large single-statement UPDATE or DELETE",
		Description: "A qualified UPDATE or DELETE that touches tens of millions of rows in one statement: row locks held throughout, bloat, replication lag.",
	},
	"RS-REVERSE-001": {
		SEOTitle:    "RS-REVERSE-001: DROP COLUMN loses its data irreversibly",
		Description: "Dropping a column permanently removes its values across every row. A down-migration can recreate the column, but not what it held.",
	},
	"RS-REVERSE-002": {
		SEOTitle:    "RS-REVERSE-002: DROP TABLE loses every row irreversibly",
		Description: "Dropping a table permanently removes every row. A down-migration can recreate the table structure, but not the data it contained.",
	},
	"RS-REVERSE-003": {
		SEOTitle:    "RS-REVERSE-003: narrowing a column type truncates data",
		Description: "Narrowing a column's type — a wider integer to a smaller one, or a fractional number to an integer — can truncate or round values.",
	},
	"RS-REVERSE-004": {
		SEOTitle:    "RS-REVERSE-004: TRUNCATE is irreversible and locks",
		Description: "TRUNCATE deletes every row in the table and cannot be rolled back once committed.",
	},
	"RS-TX-001": {
		SEOTitle:    "RS-TX-001: statement cannot run in a transaction block",
		Description: "Postgres refuses some statements inside a transaction block with SQLSTATE 25001: CREATE INDEX CONCURRENTLY, VACUUM, ALTER SYSTEM and more.",
	},
}
