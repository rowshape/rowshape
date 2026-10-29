package findings

import "strings"

// Example is a minimal, runnable illustration of one finding: a migration that
// produces it, the production shape (a fixture) that makes it produce it, and
// the migration that does the same job without the hazard.
//
// It is data rather than prose so it can be RUN. corpus/harness's
// TestFindingExamples applies every Migration to its Fixture through the same
// pipeline `rowshape validate` uses and asserts the code is reported, then
// applies Fixed and asserts it is not. An example in the docs that does not
// trigger the finding it illustrates is worse than no example, and SQL written
// from memory gets this wrong more often than it looks.
//
// It is kept OUT of Explanation on purpose: `rowshape explain` and the
// explain_finding MCP tool return Explanation as-is, and a fixture plus two
// migrations would bury the remediation an agent needs. The docs generator
// (internal/findingsdoc) is the only reader.
type Example struct {
	// Shape is one sentence describing the production data the example is
	// checked against, in the reader's terms ("public.users has 400,000 rows
	// and 3.2% of email is NULL").
	Shape string
	// Fixture is the rowshape fixture (YAML) that Shape describes.
	Fixture string
	// Migration is SQL that produces the finding against Fixture.
	Migration string
	// FixedNote says what to do before or around Fixed, when the fix is more
	// than a different statement — repair the data and re-pull, split into two
	// migrations, change the runner's settings.
	FixedNote string
	// Fixed is SQL that does the same job without the hazard.
	Fixed string
	// FixedFixture, when set, is the fixture Fixed is checked against instead of
	// Fixture: the production shape AFTER the data repair FixedNote describes has
	// shipped and the fixture has been re-pulled. A data finding is resolved by
	// changing the data, so checking the fix against the old data would prove
	// nothing.
	FixedFixture string
	// StillReported is set when rowshape cannot see the fix — it lives in your
	// deploy order, your runner's settings, or a consumer outside the database —
	// and so still reports the code on Fixed (never at error severity). It says
	// why, so the page does not pretend the warning goes away.
	StillReported string
	// NeedsTarget is set when `rowshape validate --ephemeral` refuses the
	// example, and says what to run instead. (TestFindingExamples drives the
	// pipeline directly, below that CLI check, so the example is still run.)
	NeedsTarget string
}

// migrationMarker starts a new migration file inside an example's SQL.
const migrationMarker = "-- Migration "

// MigrationFiles splits example SQL into the migration files it describes. A
// line beginning "-- Migration " (as in "-- Migration 2, after the orphans are
// gone") starts a new file; SQL with no marker is one file.
//
// Many fixes are "do this, ship it, then do that" — and the distinction is not
// cosmetic: a runner wraps each FILE in a transaction, so the same two
// statements in one file and in two files are different migrations (see
// RS-CONSTRAINT-001). The examples are run file by file, as written.
func MigrationFiles(sql string) []string {
	var files []string
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			files = append(files, s)
		}
		cur = nil
	}
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(line, migrationMarker) {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return files
}

// ExampleFor returns the worked example for a code.
func ExampleFor(code string) (Example, bool) {
	e, ok := examples[code]
	return e, ok
}

// Fixtures shared by several examples. Each is small on purpose: only the facts
// the finding reads, so the reader can see which fact drives it.
const (
	fxOrders5M = `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.orders:
    rows: {value: 5000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false}
      amount_cents: {type: integer, nullable: false, range: {min: 1, max: 2500000}}
      created_at: {type: timestamp with time zone, nullable: false}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
`
	fxEvents20M = `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.events:
    rows: {value: 20000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: probe}}
      note: {type: text, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false}
    indexes: [{name: events_note_idx, columns: [note], bytes: 400000000}]
`
)

var examples = map[string]Example{
	"RS-APPLY-001": {
		Shape:   "public.users has 400,000 rows.",
		Fixture: fxUsers(""),
		Migration: `-- Fails on any table that already has rows: every existing row would
-- get NULL in a NOT NULL column (SQLSTATE 23502).
ALTER TABLE public.users ADD COLUMN signup_source text NOT NULL;`,
		FixedNote: "Give existing rows a value in the same statement. A constant DEFAULT is a catalog-only change on PostgreSQL 11 and later, so it does not rewrite the table.",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.users ADD COLUMN signup_source text NOT NULL DEFAULT 'unknown';`,
	},

	"RS-CONSTRAINT-001": {
		Shape:   "public.orders has 5,000,000 rows.",
		Fixture: fxOrders5M,
		Migration: `BEGIN;
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0) NOT VALID;
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_amount_positive;
COMMIT;`,
		FixedNote: "Commit between the two steps. Migration runners wrap each file in its own transaction, so put the VALIDATE in the next migration file.",
		Fixed: `-- Migration 1
SET lock_timeout = '3s';
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0) NOT VALID;

-- Migration 2: takes only SHARE UPDATE EXCLUSIVE, so reads and writes continue
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_amount_positive;`,
	},

	"RS-CONSTRAINT-010": {
		Shape: "public.accounts has 250,000 rows and balance_cents ranges from -4,500 to 980,000, so some balances are already negative.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.accounts:
    rows: {value: 250000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      balance_cents: {type: integer, nullable: false, range: {min: -4500, max: 980000}}
    constraints: [{name: accounts_pkey, kind: primary_key, columns: [id]}]
`,
		Migration: `ALTER TABLE public.accounts ADD CONSTRAINT accounts_balance_nonneg CHECK (balance_cents >= 0);`,
		FixedNote: "Repair the negative balances first — what \"repair\" means is a business decision — ship that, and re-pull the fixture. Once its range starts at zero, add the constraint in two steps so neither holds a long lock.",
		Fixed: `-- Migration 1
SET lock_timeout = '3s';
ALTER TABLE public.accounts ADD CONSTRAINT accounts_balance_nonneg CHECK (balance_cents >= 0) NOT VALID;

-- Migration 2
ALTER TABLE public.accounts VALIDATE CONSTRAINT accounts_balance_nonneg;`,
		FixedFixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.accounts:
    rows: {value: 250000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      balance_cents: {type: integer, nullable: false, range: {min: 0, max: 980000}}
    constraints: [{name: accounts_pkey, kind: primary_key, columns: [id]}]
`,
	},

	"RS-CONSTRAINT-020": {
		Shape:     "public.orders has 5,000,000 rows, all with a positive amount.",
		Fixture:   fxOrders5M,
		Migration: `ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0);`,
		FixedNote: "Add the constraint NOT VALID — new and updated rows are checked immediately — and validate the existing rows in a separate migration.",
		Fixed: `-- Migration 1: a brief lock, no scan
SET lock_timeout = '3s';
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0) NOT VALID;

-- Migration 2: scans under SHARE UPDATE EXCLUSIVE, which does not block reads or writes
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_amount_positive;`,
	},

	"RS-DATA-001": {
		Shape:     "public.users has 400,000 rows and 3.2% of email is NULL (counted exactly).",
		Fixture:   fxUsers(`null_fraction: {value: 0.032, confidence: exact}`),
		Migration: `ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;`,
		FixedNote: "Backfill or delete the NULL rows first (in batches — see RS-PERF-010), ship that, and re-pull the fixture so it records zero NULLs. Then the constraint is safe to add.",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.users ALTER COLUMN email SET NOT NULL;`,
		FixedFixture: fxUsers(`null_fraction: {value: 0, confidence: exact}`),
	},

	"RS-DATA-014": {
		Shape:     "public.users has 400,000 rows and nothing proves email is unique: there is no unique index on it, and uniqueness is never guessed from a sample.",
		Fixture:   fxUsers(""),
		Migration: `ALTER TABLE public.users ADD CONSTRAINT users_email_key UNIQUE (email);`,
		FixedNote: "Prove uniqueness with `rowshape pull --exact public.users.email` (de-duplicate first if it finds duplicates). With uniqueness proven, build the index without the exclusive lock and attach it.",
		Fixed: `-- Migration 1: its own file, outside a transaction (see RS-TX-001)
CREATE UNIQUE INDEX CONCURRENTLY users_email_key ON public.users (email);

-- Migration 2
SET lock_timeout = '3s';
ALTER TABLE public.users ADD CONSTRAINT users_email_key UNIQUE USING INDEX users_email_key;`,
		FixedFixture: fxUsers(`unique: {value: true, confidence: exact, via: probe}`),
	},

	"RS-DATA-020": {
		Shape:   "public.orders has 800,000 rows and 0.4% of them point at a user_id that no longer exists in public.users.",
		Fixture: fxOrdersFK(`orphan_fraction: {value: 0.004, confidence: exact, via: scan}`),
		Migration: `ALTER TABLE public.orders
  ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES public.users (id);`,
		FixedNote: "Add the key NOT VALID so new rows are checked from now on, then delete or repair the orphaned rows and ship that. Once a re-pulled fixture records no orphans, validate.",
		Fixed: `-- Migration 1
SET lock_timeout = '3s';
ALTER TABLE public.orders
  ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES public.users (id) NOT VALID;

-- Migration 2, after the orphans are gone
ALTER TABLE public.orders VALIDATE CONSTRAINT orders_user_fk;`,
		FixedFixture: fxOrdersFK(`orphan_fraction: {value: 0, confidence: exact, via: scan}`),
	},

	"RS-DEPLOY-001": {
		Shape:     "public.users has 400,000 rows and the running application reads users.email.",
		Fixture:   fxUsers(""),
		Migration: `ALTER TABLE public.users RENAME COLUMN email TO email_address;`,
		FixedNote: "Expand, then contract, across separate deploys. This migration is the expand step; the application then writes both columns, a batched job copies the old values across, a release switches reads to email_address, and a later migration drops email.",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.users ADD COLUMN email_address text;`,
	},

	"RS-DEPLOY-002": {
		Shape:     "public.events has 20,000,000 rows; whether anything consumes its changes is not something a fixture records.",
		Fixture:   fxEvents20M,
		Migration: `ALTER TABLE public.events REPLICA IDENTITY NOTHING;`,
		FixedNote: "Check what consumes the table's changes before changing its identity, on the production database.",
		Fixed: `-- Run these first, against production:
--   SELECT pubname FROM pg_publication_tables WHERE schemaname = 'public' AND tablename = 'events';
--   SELECT slot_name, plugin, active FROM pg_replication_slots;
-- Only if nothing consumes the table (or every consumer is ready):
ALTER TABLE public.events REPLICA IDENTITY NOTHING;`,
		StillReported: "rowshape cannot see your subscribers or CDC pipelines, so it reports every REPLICA IDENTITY change. The check above is the fix, not a different statement.",
	},

	"RS-DEPLOY-003": {
		Shape:     "public.events has 20,000,000 rows and note has never been NULL.",
		Fixture:   fxEvents20M,
		Migration: `ALTER TABLE public.events ALTER COLUMN note DROP NOT NULL;`,
		FixedNote: "Ship the application code that tolerates a NULL note first, including any API schema or downstream consumer that declared the field required. Then run the same migration.",
		Fixed: `-- Only after every reader of events.note handles NULL:
SET lock_timeout = '3s';
ALTER TABLE public.events ALTER COLUMN note DROP NOT NULL;`,
		StillReported: "The fix is the deploy order, which a migration cannot show, so rowshape still reports the code as a reminder.",
	},

	"RS-INDEX-001": {
		Shape:     "public.orders has 5,000,000 rows.",
		Fixture:   fxOrders5M,
		Migration: `CREATE INDEX orders_created_at_idx ON public.orders (created_at);`,
		FixedNote: "Build it CONCURRENTLY, in its own migration with the runner's transaction wrapping turned off (see RS-TX-001, which rowshape reports on this as a reminder).",
		Fixed:     `CREATE INDEX CONCURRENTLY orders_created_at_idx ON public.orders (created_at);`,
	},

	"RS-INDEX-002": {
		Shape:     "public.events has 20,000,000 rows, its id is proven unique, and it has no primary key yet.",
		Fixture:   fxEvents20M,
		Migration: `ALTER TABLE public.events ADD CONSTRAINT events_pkey PRIMARY KEY (id);`,
		FixedNote: "Build the unique index first without the exclusive lock, then adopt it. Adopting holds ACCESS EXCLUSIVE only briefly.",
		Fixed: `-- Migration 1: its own file, outside a transaction (see RS-TX-001)
CREATE UNIQUE INDEX CONCURRENTLY events_pkey_idx ON public.events (id);

-- Migration 2
SET lock_timeout = '3s';
ALTER TABLE public.events ADD CONSTRAINT events_pkey PRIMARY KEY USING INDEX events_pkey_idx;`,
	},

	"RS-INDEX-003": {
		Shape:     "public.events has 20,000,000 rows and a 400 MB index, events_note_idx.",
		Fixture:   fxEvents20M,
		Migration: `DROP INDEX events_note_idx;`,
		FixedNote: "Drop it CONCURRENTLY, in its own migration with the runner's transaction wrapping turned off.",
		Fixed: `SET lock_timeout = '3s';
DROP INDEX CONCURRENTLY events_note_idx;`,
	},

	"RS-INDEX-010": {
		Shape:     "public.users has 400,000 rows and a probe proved email is NOT unique: some addresses appear more than once.",
		Fixture:   fxUsers(`unique: {value: false, confidence: exact, via: probe}`),
		Migration: `CREATE UNIQUE INDEX CONCURRENTLY users_email_key ON public.users (email);`,
		FixedNote: "Find the duplicates, merge or delete them, ship that, and re-pull with `rowshape pull --exact public.users.email` so the fixture proves uniqueness. Then the same statement builds.",
		Fixed: `-- Find them first:
--   SELECT email, count(*) FROM public.users GROUP BY email HAVING count(*) > 1;
CREATE UNIQUE INDEX CONCURRENTLY users_email_key ON public.users (email);`,
		FixedFixture: fxUsers(`unique: {value: true, confidence: exact, via: probe}`),
	},

	"RS-INDEX-020": {
		Shape: "public.orders has 9,000,000 rows and a 4.2 GB index on customer_id that is 45% bloat.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.orders:
    rows: {value: 9000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      customer_id: {type: bigint, nullable: false}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    indexes:
      - {name: orders_customer_idx, method: btree, columns: [customer_id], bytes: 4200000000, bloat_estimate: 0.45}
`,
		Migration: `REINDEX INDEX orders_customer_idx;`,
		FixedNote: "Rebuild it CONCURRENTLY (PostgreSQL 12 and later), outside a transaction block.",
		Fixed:     `REINDEX INDEX CONCURRENTLY orders_customer_idx;`,
	},

	"RS-LOCK-001": {
		Shape:     "public.orders has 5,000,000 rows.",
		Fixture:   fxOrders5M,
		Migration: `ALTER TABLE public.orders ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();`,
		FixedNote: "Add the column with no default, so no row is rewritten; give new rows a default; backfill existing rows in batches outside the migration (see RS-PERF-010); then enforce NOT NULL in a later migration (see RS-DATA-001).",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.orders ADD COLUMN token uuid;
ALTER TABLE public.orders ALTER COLUMN token SET DEFAULT gen_random_uuid();`,
	},

	"RS-LOCK-003": {
		Shape: "public.events is partitioned by month on created_at, and public.events_2026_10 is an ordinary 20,000,000-row table about to become October's partition.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.events:
    rows: {value: 200000000, confidence: exact}
    partitions: {count: 12, strategy: range, key: created_at}
    columns:
      id: {type: bigint, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false}
  public.events_2026_10:
    rows: {value: 20000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
      created_at: {type: timestamp with time zone, nullable: false, range: {min: "2026-10-01T00:00:00Z", max: "2026-10-31T23:59:59Z"}}
`,
		Migration: `ALTER TABLE public.events ATTACH PARTITION public.events_2026_10
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');`,
		FixedNote: "Prove the bound on the incoming table first with a CHECK that matches it, validated without the exclusive lock. ATTACH then skips its scan, so the lock on the parent is brief.",
		Fixed: `-- Migration 1
SET lock_timeout = '3s';
ALTER TABLE public.events_2026_10 ADD CONSTRAINT events_2026_10_bound
  CHECK (created_at >= '2026-10-01' AND created_at < '2026-11-01') NOT VALID;

-- Migration 2
ALTER TABLE public.events_2026_10 VALIDATE CONSTRAINT events_2026_10_bound;

-- Migration 3
SET lock_timeout = '3s';
ALTER TABLE public.events ATTACH PARTITION public.events_2026_10
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');`,
		StillReported: "rowshape reports every ATTACH PARTITION, because the lock on the parent is taken regardless; what the CHECK removes is the scan under it.",
		NeedsTarget:   "`rowshape validate --ephemeral` refuses a migration that attaches or detaches a partition, because the disposable database it hydrates does not reproduce partitioning. Validate it with `--target` against a database that has the real schema.",
	},

	"RS-LOCK-010": {
		Shape:     "public.orders has 5,000,000 rows and is queried constantly.",
		Fixture:   fxOrders5M,
		Migration: `ALTER TABLE public.orders ALTER COLUMN amount_cents TYPE bigint;`,
		FixedNote: "Set a short lock_timeout at the head of the migration, and retry the migration if it times out. This fixes the queueing only: the rewrite itself is still RS-LOCK-001.",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.orders ALTER COLUMN amount_cents TYPE bigint;`,
	},

	"RS-LOCK-011": {
		Shape:   "public.orders has 5,000,000 rows.",
		Fixture: fxOrders5M,
		Migration: `SET lock_timeout = '3s';
ALTER TABLE public.orders ALTER COLUMN amount_cents TYPE bigint;
ALTER TABLE public.orders ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0);`,
		FixedNote: "Combine the actions into one ALTER TABLE. It takes the lock once, and Postgres checks the new constraint during the same pass that rewrites the table.",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.orders
  ALTER COLUMN amount_cents TYPE bigint,
  ADD CONSTRAINT orders_amount_positive CHECK (amount_cents > 0);`,
	},

	"RS-PERF-001": {
		Shape: "public.users has 1,200,000 rows; public.orders references it ON DELETE CASCADE with a mean of 8 orders per user but a maximum of 12,902.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 1200000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      last_seen_at: {type: timestamp with time zone, nullable: true, null_fraction: {value: 0.01, confidence: exact}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
  public.orders:
    rows: {value: 10000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false, distinct: 1198000}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    references:
      - column: user_id
        to: public.users.id
        on_delete: cascade
        fanout: {mean: 8.4, p50: 3, p95: 41, max: 12902, confidence: measured}
        orphan_fraction: {value: 0, confidence: exact, via: constraint}
`,
		Migration: `DELETE FROM public.users WHERE last_seen_at < now() - interval '2 years';`,
		FixedNote: "Move the delete out of the migration into a job that removes the children first, in bounded batches, so no single statement cascades through a long tail. Run each statement until it deletes nothing.",
		Fixed: `-- 1. Children first, 10,000 at a time:
DELETE FROM public.orders WHERE id IN (
  SELECT o.id FROM public.orders o JOIN public.users u ON u.id = o.user_id
  WHERE u.last_seen_at < now() - interval '2 years'
  LIMIT 10000);

-- 2. Then the parents, which now cascade to nothing:
DELETE FROM public.users WHERE id IN (
  SELECT id FROM public.users
  WHERE last_seen_at < now() - interval '2 years'
  LIMIT 10000);`,
		StillReported: "rowshape sizes a cascade from the fixture's fan-out profile, not from which children an earlier statement removed, so the parent delete is still reported. What changed is that every statement is now bounded.",
	},

	"RS-PERF-002": {
		Shape:     "public.users has 400,000 rows.",
		Fixture:   fxUsers(""),
		Migration: `UPDATE public.users SET email = lower(email);`,
		FixedNote: "Scope the statement to the rows that need it, and run a genuine full-table change in bounded batches by primary-key range — from a job, not the migration — then VACUUM.",
		Fixed: `-- Repeat for each id range until the table is covered:
UPDATE public.users SET email = lower(email)
WHERE id BETWEEN 1 AND 50000 AND email <> lower(email);`,
	},

	"RS-PERF-010": {
		Shape: "public.users has 50,000,000 rows and about 90% of normalized is NULL.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 50000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      email: {type: text, nullable: false}
      normalized: {type: text, nullable: true, null_fraction: {value: 0.9, confidence: estimated, via: pg_stats}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
`,
		Migration: `UPDATE public.users SET normalized = lower(email) WHERE normalized IS NULL;`,
		FixedNote: "Batch it by key range, committing each batch, from a job outside the migration. 10,000 to 50,000 rows per batch is a common starting point.",
		Fixed: `-- Repeat with the next id range until the table is covered:
UPDATE public.users SET normalized = lower(email)
WHERE id BETWEEN 1 AND 50000 AND normalized IS NULL;`,
	},

	"RS-REVERSE-001": {
		Shape:     "public.users has 400,000 rows, most with a legacy_notes value.",
		Fixture:   fxUsersLegacy,
		Migration: `ALTER TABLE public.users DROP COLUMN legacy_notes;`,
		FixedNote: "Stop reading and writing the column and ship that first. Keep a copy of the values, then drop the column in a later migration.",
		Fixed: `-- Migration 1: keep the values recoverable
CREATE TABLE public.users_legacy_notes_archive AS
  SELECT id, legacy_notes FROM public.users WHERE legacy_notes IS NOT NULL;

-- Migration 2, a later deploy, once nothing references the column:
SET lock_timeout = '3s';
ALTER TABLE public.users DROP COLUMN legacy_notes;`,
		StillReported: "A DROP COLUMN always loses data, so it is always reported. What the archive changes is that a rollback can restore it.",
	},

	"RS-REVERSE-002": {
		Shape: "public.audit_log has 88,000,000 rows.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.audit_log:
    rows: {value: 88000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      action: {type: text, nullable: false}
    constraints: [{name: audit_log_pkey, kind: primary_key, columns: [id]}]
`,
		Migration: `DROP TABLE public.audit_log;`,
		FixedNote: "Stop using the table and ship that. Then rename it aside rather than dropping it, and drop it for good only once you are sure nothing needs it. (rowshape reports the rename as RS-DEPLOY-001, which is expected: by then nothing should reference the old name.)",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.audit_log RENAME TO audit_log_retired;`,
	},

	"RS-REVERSE-003": {
		Shape: "public.products has 2,000,000 rows and price is numeric(12,2), e.g. 19.99.",
		Fixture: `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.products:
    rows: {value: 2000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      price: {type: 'numeric(12,2)', nullable: false}
    constraints: [{name: products_pkey, kind: primary_key, columns: [id]}]
`,
		Migration: `-- 19.99 silently becomes 20: the cast rounds, and nothing raises.
ALTER TABLE public.products ALTER COLUMN price TYPE integer;`,
		FixedNote: "Keep the precision: add a column whose type holds every value exactly, backfill it in batches, switch reads and writes over, and drop the old column in a later migration.",
		Fixed: `SET lock_timeout = '3s';
ALTER TABLE public.products ADD COLUMN price_cents bigint;
-- then, in batches outside the migration:
--   UPDATE public.products SET price_cents = round(price * 100) WHERE id BETWEEN ...;`,
	},

	"RS-REVERSE-004": {
		Shape:     "public.events has 20,000,000 rows.",
		Fixture:   fxEvents20M,
		Migration: `TRUNCATE public.events;`,
		FixedNote: "If the intent is to remove old rows, not all of them, say so with a WHERE clause and delete in bounded batches from a job.",
		Fixed: `-- Repeat with the next id range until the old rows are gone:
DELETE FROM public.events
WHERE id BETWEEN 1 AND 50000 AND created_at < now() - interval '90 days';`,
	},

	"RS-TX-001": {
		Shape:   "public.orders has 5,000,000 rows.",
		Fixture: fxOrders5M,
		Migration: `BEGIN;
CREATE INDEX CONCURRENTLY orders_user_id_idx ON public.orders (user_id);
COMMIT;`,
		FixedNote:     "Put the statement in a migration of its own with no BEGIN/COMMIT, and turn off your runner's per-migration transaction for that file: Django `atomic = False`, Rails `disable_ddl_transaction!`, Alembic an autocommit block, Flyway `executeInTransaction=false`.",
		Fixed:         `CREATE INDEX CONCURRENTLY orders_user_id_idx ON public.orders (user_id);`,
		StillReported: "rowshape cannot see your runner's settings, so a file that does not open its own transaction is still reported, as a warning rather than an error, until you confirm the runner will not wrap it.",
	},
}

// fxUsers is a public.users fixture whose email column carries the given extra
// fact (a null_fraction, a uniqueness proof), or none.
func fxUsers(emailFact string) string {
	email := "{type: text, nullable: true}"
	if emailFact != "" {
		email = "{type: text, nullable: true, " + emailFact + "}"
	}
	return `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: exact, escalated: []}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      email: ` + email + `
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
`
}

const fxUsersLegacy = `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 400000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      legacy_notes: {type: text, nullable: true, null_fraction: {value: 0.2, confidence: estimated, via: pg_stats}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
`

// fxOrdersFK is public.users plus public.orders referencing it, with the given
// orphan_fraction fact on the reference.
func fxOrdersFK(orphans string) string {
	return `rowshape_fixture: "1"
meta: {id: example, engine: {name: postgres, version: "16"}, privacy: standard, profile: {mode: fast, escalated: []}}
tables:
  public.users:
    rows: {value: 100000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
    constraints: [{name: users_pkey, kind: primary_key, columns: [id]}]
  public.orders:
    rows: {value: 800000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false, unique: {value: true, confidence: exact, via: constraint}}
      user_id: {type: bigint, nullable: false, distinct: 99000}
    constraints: [{name: orders_pkey, kind: primary_key, columns: [id]}]
    references:
      - column: user_id
        to: public.users.id
        on_delete: cascade
        ` + orphans + `
`
}
