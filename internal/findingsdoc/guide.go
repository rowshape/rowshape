package findingsdoc

// pageGuide is the part of a finding page written for a reader deciding whether
// the finding matters to them: what goes wrong in production if they ship the
// migration anyway, and which other codes travel with this one.
//
// Like pageMetas it is docs-only. The catalog's Summary says what the finding
// IS; this says what it COSTS, in the terms of whoever gets paged.
type pageGuide struct {
	Why     string
	Related []string
}

var pageGuides = map[string]pageGuide{
	"RS-APPLY-001": {
		Why:     "When a statement is rejected nothing after it was checked, because Postgres stops there too. If the SQLSTATE is a class-23 code, the data rejected it: production has the same rows, so the deploy will fail at the same statement.",
		Related: []string{"RS-DATA-001", "RS-DATA-014", "RS-CONSTRAINT-010", "RS-TX-001"},
	},
	"RS-CONSTRAINT-001": {
		Why:     "NOT VALID exists to keep the validating scan out from under the ACCESS EXCLUSIVE lock. Validated in the same transaction, the scan runs while that lock is still held, so the table is blocked for the whole scan: the outage the split was written to avoid, in a migration that looks like it follows best practice.",
		Related: []string{"RS-CONSTRAINT-020", "RS-DATA-020", "RS-LOCK-010"},
	},
	"RS-CONSTRAINT-010": {
		Why:     "Postgres checks every existing row when a CHECK is added or validated and fails on the first one that violates it. The migration fails in production, after holding its lock for as long as the scan took to get there, and a test database built from clean fixtures never has the bad rows.",
		Related: []string{"RS-CONSTRAINT-020", "RS-CONSTRAINT-001", "RS-APPLY-001"},
	},
	"RS-CONSTRAINT-020": {
		Why:     "The statement succeeds, which is why it passes review and every test. The cost is the lock: while it scans, every read and write on the table waits, and on a large table the scan takes minutes.",
		Related: []string{"RS-CONSTRAINT-001", "RS-CONSTRAINT-010", "RS-DATA-020", "RS-LOCK-010"},
	},
	"RS-DATA-001": {
		Why:     "SET NOT NULL scans the whole table under ACCESS EXCLUSIVE and fails at the first NULL, so the migration both blocks the table and fails. Test databases rarely have NULLs where production does, which is why it passes everywhere else first.",
		Related: []string{"RS-DEPLOY-003", "RS-APPLY-001", "RS-PERF-010"},
	},
	"RS-DATA-014": {
		Why:     "If the column has duplicates the constraint fails to build, and the migration fails after building most of a unique index under ACCESS EXCLUSIVE. rowshape treats \"probably unique\" as unknown, because a sample cannot see the two rows that collide.",
		Related: []string{"RS-INDEX-002", "RS-INDEX-010", "RS-TX-001"},
	},
	"RS-DATA-020": {
		Why:     "Orphans build up wherever the key was never enforced: deletes from another service, a bulk import, a partial restore. Validation scans every row and fails on the first orphan, and a test database never has them.",
		Related: []string{"RS-CONSTRAINT-020", "RS-CONSTRAINT-001", "RS-PERF-001"},
	},
	"RS-DEPLOY-001": {
		Why:     "During a rolling or blue/green deploy the old and new releases run side by side. From the moment the rename commits, every request the old release serves that touches the old name fails, until the last old instance is gone.",
		Related: []string{"RS-DEPLOY-003", "RS-REVERSE-001", "RS-REVERSE-002"},
	},
	"RS-DEPLOY-002": {
		Why:     "Nothing fails where you can see it. A logical replication subscriber or CDC pipeline can start rejecting or skipping updates and deletes it can no longer match to a row, and the first symptom is data drift somewhere downstream.",
		Related: []string{"RS-INDEX-003", "RS-DEPLOY-001", "RS-DEPLOY-003"},
	},
	"RS-DEPLOY-003": {
		Why:     "Code written against a NOT NULL column does not check for NULL. Once the constraint is gone, the first NULL written, by a new code path, an import or a backfill, surfaces as an error or a wrong answer somewhere else, long after the migration that allowed it.",
		Related: []string{"RS-DATA-001", "RS-DEPLOY-001"},
	},
	"RS-INDEX-001": {
		Why:     "A plain CREATE INDEX takes a SHARE lock: reads continue, but every INSERT, UPDATE and DELETE on the table waits until the build finishes. On a large table that is minutes of queued or failed writes.",
		Related: []string{"RS-TX-001", "RS-INDEX-002", "RS-INDEX-020"},
	},
	"RS-INDEX-002": {
		Why:     "Unlike CREATE INDEX, adding the constraint blocks reads as well as writes, for the whole build. The data can be perfectly unique and the migration still takes the table offline for as long as the index takes to build.",
		Related: []string{"RS-DATA-014", "RS-INDEX-010", "RS-INDEX-001", "RS-TX-001"},
	},
	"RS-INDEX-003": {
		Why:     "The drop is instant once it has the lock. The danger is getting it: it waits behind any long-running query on the table, and every query that arrives after it waits behind the drop.",
		Related: []string{"RS-LOCK-010", "RS-TX-001", "RS-DEPLOY-002"},
	},
	"RS-INDEX-010": {
		Why:     "A unique index build that meets a duplicate fails after doing most of the work. Built CONCURRENTLY, it also leaves an INVALID index behind that has to be dropped by hand before the migration can be retried.",
		Related: []string{"RS-DATA-014", "RS-INDEX-002", "RS-TX-001"},
	},
	"RS-INDEX-020": {
		Why:     "A plain REINDEX blocks writes to the table, and reads that would use the index, for the whole rebuild. A bloated index is the one you want to reindex and also the slowest one to rebuild.",
		Related: []string{"RS-INDEX-001", "RS-TX-001", "RS-LOCK-010"},
	},
	"RS-LOCK-001": {
		Why:     "Every row is rewritten while the table is locked against reads and writes. The time scales with the table, so a change that takes a second in staging takes minutes in production, and all of it is downtime for that table.",
		Related: []string{"RS-LOCK-010", "RS-LOCK-011", "RS-REVERSE-003", "RS-PERF-010"},
	},
	"RS-LOCK-003": {
		Why:     "The lock is on the parent, so queries against every partition wait, not only queries on the new one, and the scan that checks the bound runs while they wait.",
		Related: []string{"RS-CONSTRAINT-020", "RS-LOCK-010"},
	},
	"RS-LOCK-010": {
		Why:     "A waiting ACCESS EXCLUSIVE request blocks every query that arrives after it. One long report or idle transaction holding a lock on the table is enough to turn a quick migration into a stalled site; lock_timeout makes the migration fail and retry instead. rowshape reports it where a statement also holds the lock for a scan, rewrite or build, where the stall is worst, but the advice applies to all DDL.",
		Related: []string{"RS-LOCK-001", "RS-LOCK-011", "RS-INDEX-003"},
	},
	"RS-LOCK-011": {
		Why:     "Each statement queues for the lock separately, so the table is unavailable across the whole sequence, and the traffic that built up behind one lock hits the next one.",
		Related: []string{"RS-LOCK-001", "RS-LOCK-010", "RS-CONSTRAINT-020"},
	},
	"RS-PERF-001": {
		Why:     "The mean says a parent has a handful of children; the tail says some have thousands. Deleting one of those parents deletes all of its children in the same statement, holding locks until it finishes.",
		Related: []string{"RS-PERF-010", "RS-PERF-002", "RS-REVERSE-004"},
	},
	"RS-PERF-002": {
		Why:     "One statement changes every row in one transaction: row locks on the whole table until it commits, a table's worth of dead tuples for vacuum, and a burst of WAL that replicas have to replay.",
		Related: []string{"RS-PERF-010", "RS-PERF-001"},
	},
	"RS-PERF-010": {
		Why:     "A WHERE clause makes it look targeted. When the predicate matches most of a large table it is the same single huge transaction: row locks held throughout, bloat, and replication lag.",
		Related: []string{"RS-PERF-002", "RS-LOCK-001", "RS-DATA-001"},
	},
	"RS-REVERSE-001": {
		Why:     "A down-migration that recreates the column brings it back empty. If the drop was a mistake, or the release has to be rolled back, the values are gone unless you kept a copy.",
		Related: []string{"RS-REVERSE-002", "RS-DEPLOY-001", "RS-REVERSE-003"},
	},
	"RS-REVERSE-002": {
		Why:     "There is no down-migration for the data. If anything still read the table, or the release has to be rolled back, the only way back is a restore.",
		Related: []string{"RS-REVERSE-001", "RS-REVERSE-004", "RS-DEPLOY-001"},
	},
	"RS-REVERSE-003": {
		Why:     "Where Postgres can convert a value, it does so silently: 19.99 becomes 20. Where it cannot, such as a bigint too large for integer, the migration fails instead. Either way, widening the column back does not restore what was lost.",
		Related: []string{"RS-REVERSE-001", "RS-LOCK-001", "RS-APPLY-001"},
	},
	"RS-REVERSE-004": {
		Why:     "TRUNCATE is instant, so it looks harmless against a test database. In production it empties the table, cannot be undone once committed, locks the table while it runs, and skips per-row DELETE triggers.",
		Related: []string{"RS-REVERSE-002", "RS-PERF-001", "RS-PERF-002"},
	},
	"RS-TX-001": {
		Why:     "The file works when you run it by hand and fails under the migration runner, which wraps it in a transaction, so it usually fails for the first time in CI or in production.",
		Related: []string{"RS-INDEX-001", "RS-INDEX-003", "RS-INDEX-020", "RS-DATA-014"},
	},
}
