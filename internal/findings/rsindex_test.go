package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestRSIndexCorpusVerdicts runs the RS-INDEX analyzer against the corpus cases
// it owns and asserts each verdict matches expected.json.
func TestRSIndexCorpusVerdicts(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"unique_index_cant_build", verdict.VerdictFail},
		{"rsindex-non-concurrent", verdict.VerdictWarn},
		{"rsindex-reindex-bloat", verdict.VerdictWarn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, mig := loadCorpus(t, c.name)
			res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsIndex{}}, false)
			if res.Verdict != c.want {
				t.Errorf("verdict = %s, want %s (corpus expected.json)", res.Verdict, c.want)
			}
		})
	}
}

// TestRSIndexNonConcurrent: a plain CREATE INDEX fires RS-INDEX-001 (WARN),
// recommends CONCURRENTLY, and buckets the build via the O(n log n) model.
func TestRSIndexNonConcurrent(t *testing.T) {
	f, mig := loadCorpus(t, "rsindex-non-concurrent")
	got := rsIndex{}.Analyze(f, captureOf(mig, "public.orders", 10_000))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-INDEX finding, got %d", len(got))
	}
	fnd := got[0]
	if fnd.Code != "RS-INDEX-001" || fnd.Severity != verdict.SeverityWarn {
		t.Errorf("want RS-INDEX-001 warn, got %s/%s", fnd.Code, fnd.Severity)
	}
	if !strings.Contains(fnd.Remediation, "CONCURRENTLY") {
		t.Errorf("remediation must recommend CONCURRENTLY: %q", fnd.Remediation)
	}
	if fnd.Estimate == nil || fnd.Estimate.Model != "n_log_n" {
		t.Errorf("build must be bucketed via the n_log_n model, got %+v", fnd.Estimate)
	}
}

// TestRSIndexUniqueCantBuild: CREATE UNIQUE INDEX on a proven non-unique column
// is an error/FAIL — the index cannot build (RFC §6.5).
func TestRSIndexUniqueCantBuild(t *testing.T) {
	f, mig := loadCorpus(t, "unique_index_cant_build")
	got := rsIndex{}.Analyze(f, plainCapture(mig))
	// CONCURRENTLY, so no non-concurrent lock finding — only the uniqueness one.
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-INDEX finding, got %d: %+v", len(got), got)
	}
	if got[0].Code != "RS-INDEX-010" || got[0].Severity != verdict.SeverityError {
		t.Errorf("want RS-INDEX-010 error, got %s/%s", got[0].Code, got[0].Severity)
	}
	if got[0].DependsOn[0] != "public.users.email.unique" {
		t.Errorf("depends_on = %v, want [public.users.email.unique]", got[0].DependsOn)
	}
}

// TestRSIndexUniqueUnprovenWarns: CREATE UNIQUE INDEX on a column whose
// uniqueness is unproven is capped to a resolving WARN, never PASS
// (INV-UNIQUENESS).
func TestRSIndexUniqueUnprovenWarns(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 1000000, confidence: exact}
    columns:
      email: {type: text, nullable: false, distinct: {value: 999000, confidence: estimated}}
`))
	if err != nil {
		t.Fatal(err)
	}
	mig := "CREATE UNIQUE INDEX CONCURRENTLY users_email_uniq ON public.users (email);"
	res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsIndex{}}, false)
	if res.Verdict != verdict.VerdictWarn {
		t.Errorf("verdict = %s, want WARN (uniqueness unproven)", res.Verdict)
	}
	if len(res.Findings) == 0 || !strings.Contains(res.Findings[0].Remediation, "pull --exact") {
		t.Errorf("capped WARN must name the resolving command, got %+v", res.Findings)
	}
}

// TestRSIndexUniqueProvenPasses: CREATE UNIQUE INDEX on proven-unique data
// certifies PASS.
func TestRSIndexUniqueProvenPasses(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 1000000, confidence: exact}
    columns:
      email: {type: text, nullable: false, unique: {value: true, confidence: exact, via: probe}}
`))
	if err != nil {
		t.Fatal(err)
	}
	mig := "CREATE UNIQUE INDEX CONCURRENTLY users_email_uniq ON public.users (email);"
	res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsIndex{}}, false)
	if res.Verdict != verdict.VerdictPass {
		t.Errorf("verdict = %s, want PASS (uniqueness proven exact)", res.Verdict)
	}
}

// TestRSIndexReindexBytesBasis: a non-concurrent REINDEX buckets its duration
// from the index's on-disk bytes and reports the bloat (RFC §6.5).
func TestRSIndexReindexBytesBasis(t *testing.T) {
	f, mig := loadCorpus(t, "rsindex-reindex-bloat")
	got := rsIndex{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-INDEX finding, got %d", len(got))
	}
	fnd := got[0]
	if fnd.Code != "RS-INDEX-020" || fnd.Severity != verdict.SeverityWarn {
		t.Errorf("want RS-INDEX-020 warn, got %s/%s", fnd.Code, fnd.Severity)
	}
	ev, _ := fnd.Evidence.(map[string]any)
	if ev["index_bytes"] != int64(4200000000) {
		t.Errorf("index_bytes evidence = %v, want 4200000000", ev["index_bytes"])
	}
	// 4.2 GB / ~50 MB/s is well over a minute → an outage bucket.
	if fnd.Estimate == nil || fnd.Estimate.Bucket != verdict.BucketOutage {
		t.Errorf("4.2 GB reindex should bucket as outage, got %+v", fnd.Estimate)
	}
	// The estimate rests on the index's bytes, NOT the row count. Declaring
	// <table>.rows was false provenance (a fact this finding never reads) and
	// borrowed that fact's confidence for a byte-based claim (CR-CONSTRAINT-010
	// bug class). depends_on must not cite rows.
	for _, dep := range fnd.DependsOn {
		if strings.HasSuffix(dep, ".rows") {
			t.Errorf("depends_on = %v cites a row-count fact the byte-based estimate never reads", fnd.DependsOn)
		}
	}
	// The byte→duration projection is a model, so the estimate is `estimated`,
	// not left blank.
	if fnd.Estimate.Confidence != string(fixture.Estimated) {
		t.Errorf("estimate confidence = %q, want %q (a bucket modelled from bytes)", fnd.Estimate.Confidence, fixture.Estimated)
	}
}

// TestRSIndexConcurrentNotFlagged: CREATE INDEX CONCURRENTLY takes no exclusive
// lock, so it is not flagged for locking.
func TestRSIndexConcurrentNotFlagged(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.orders:
    rows: {value: 1000000, confidence: exact}
    columns:
      created_at: {type: timestamptz, nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	mig := "CREATE INDEX CONCURRENTLY orders_created_at_idx ON public.orders (created_at);"
	if got := (rsIndex{}).Analyze(f, plainCapture(mig)); len(got) != 0 {
		t.Errorf("CREATE INDEX CONCURRENTLY must not be flagged for locking, got %+v", got)
	}
}

// --- CR-T2: unqualified table names -----------------------------------------
//
// rsindex skipped resolveTable, so `CREATE INDEX ON orders (...)` missed the
// fixture key `public.orders`, read the zero value, and reported `instant` for a
// build on a 50M-row table. Unlike rsperf (where the finding vanished), here the
// finding survives and states a confidently wrong duration, which is worse: the
// user gets an answer, and it is the reassuring one.

func indexFixture(t *testing.T) *fixture.Fixture {
	t.Helper()
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.orders:
    rows: {value: 50000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
      email: {type: text, nullable: false, unique: {value: false, confidence: exact, via: probe}}
    indexes:
      - {name: orders_email_idx, method: btree, columns: [email], bytes: 4200000000, bloat_estimate: 0.45}
`))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// indexCapture mirrors what a real validate run carries and a bare fixture does
// not: a hydrated row count and a measured duration. Without both, estimateFor
// extrapolates from declared==hydrated and every bucket is `instant` — which is
// how a version of this test could pass against the very bug it targets.
func indexCapture(sql string) *validate.Capture {
	return &validate.Capture{
		Success:    true,
		Statements: []validate.Statement{{SQL: sql, DurationMs: 40}},
		TableRows:  map[string]int64{"public.orders": 2000},
	}
}

func indexFindingByCode(f *fixture.Fixture, c *validate.Capture, code string) *verdict.Finding {
	for _, fnd := range (rsIndex{}).Analyze(f, c) {
		if fnd.Code == code {
			return &fnd
		}
	}
	return nil
}

// TestAddPrimaryKeyFlagged: ADD [CONSTRAINT] PRIMARY KEY builds a unique index
// under ACCESS EXCLUSIVE. Nothing flagged it before (rsLock excludes ADD PRIMARY,
// rsConstraint files it under OTHER, rsData keys on UNIQUE), so a PK added to a
// 50M-row table returned PASS with zero findings. It must now warn with a
// row-based build estimate.
func TestAddPrimaryKeyFlagged(t *testing.T) {
	f := indexFixture(t) // public.orders declared at 50M rows

	fnd := indexFindingByCode(f,
		indexCapture("ALTER TABLE public.orders ADD CONSTRAINT orders_pkey PRIMARY KEY (id)"),
		"RS-INDEX-002")
	if fnd == nil {
		t.Fatal("ADD PRIMARY KEY produced no RS-INDEX-002 — a large-table PK addition passes clean")
	}
	if fnd.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %q, want warn", fnd.Severity)
	}
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.orders.rows" {
		t.Errorf("depends_on = %v, want [public.orders.rows]", fnd.DependsOn)
	}
	// 50M-row build extrapolated → an outage bucket, not the hydrated-rows instant.
	if fnd.Estimate == nil || fnd.Estimate.Bucket == verdict.BucketInstant {
		t.Errorf("expected an extrapolated build bucket, got %+v", fnd.Estimate)
	}

	// The bare (no CONSTRAINT name) table-constraint form also fires.
	if indexFindingByCode(f, indexCapture("ALTER TABLE public.orders ADD PRIMARY KEY (id)"), "RS-INDEX-002") == nil {
		t.Error("`ADD PRIMARY KEY (id)` (no constraint name) must also be flagged")
	}
}

// TestAddUniqueConstraintFlagged: ADD CONSTRAINT ... UNIQUE builds a unique index
// under ACCESS EXCLUSIVE too — the lock hazard is distinct from RS-DATA-014's
// question of whether the data lets it build.
func TestAddUniqueConstraintFlagged(t *testing.T) {
	f := indexFixture(t)
	fnd := indexFindingByCode(f,
		indexCapture("ALTER TABLE public.orders ADD CONSTRAINT orders_email_key UNIQUE (email)"),
		"RS-INDEX-002")
	if fnd == nil {
		t.Fatal("ADD UNIQUE produced no RS-INDEX-002 lock finding")
	}
	if fnd.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %q, want warn", fnd.Severity)
	}
	// The constraint NAME `orders_email_key` contains no UNIQUE token; the keyword
	// is the constraint keyword, and the table must still resolve.
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.orders.rows" {
		t.Errorf("depends_on = %v, want [public.orders.rows]", fnd.DependsOn)
	}
	// Bare `ADD UNIQUE (col)` (no constraint name) also fires.
	if indexFindingByCode(f, indexCapture("ALTER TABLE public.orders ADD UNIQUE (email)"), "RS-INDEX-002") == nil {
		t.Error("`ADD UNIQUE (email)` (no constraint name) must also be flagged")
	}
}

// TestAddConstraintUsingIndexNotFlagged: the adopt form ADD PRIMARY KEY/UNIQUE
// USING INDEX attaches a prebuilt index and holds the exclusive lock only
// briefly — it is exactly what RS-INDEX-002's remediation recommends, so the
// finding must not fire on its own fix.
func TestAddConstraintUsingIndexNotFlagged(t *testing.T) {
	f := indexFixture(t)
	for _, sql := range []string{
		"ALTER TABLE public.orders ADD PRIMARY KEY USING INDEX orders_pkey_idx",
		"ALTER TABLE public.orders ADD CONSTRAINT orders_email_key UNIQUE USING INDEX orders_email_idx",
	} {
		if fnd := indexFindingByCode(f, indexCapture(sql), "RS-INDEX-002"); fnd != nil {
			t.Errorf("USING INDEX adopt form must not fire RS-INDEX-002 (it is the remediation): %q -> %+v", sql, fnd)
		}
	}
}

// TestAddColumnPrimaryKeyNotFlagged: the COLUMN form `ADD COLUMN c type PRIMARY
// KEY` adds a NEW column — a different operation from a table-constraint PK over
// existing data — and must NOT be flagged as an existing-data index build.
func TestAddColumnPrimaryKeyNotFlagged(t *testing.T) {
	f := indexFixture(t)
	if fnd := indexFindingByCode(f,
		indexCapture("ALTER TABLE public.orders ADD COLUMN pk bigint PRIMARY KEY"),
		"RS-INDEX-002"); fnd != nil {
		t.Errorf("ADD COLUMN ... PRIMARY KEY must not fire RS-INDEX-002 (it is a new column), got %+v", fnd)
	}
}

// TestAddPrimaryKeyUnqualifiedResolves: the table name is resolved like every
// other analyzer, so `ADD PRIMARY KEY` on an unqualified table still reaches the
// fixture's row count instead of reading zero and reporting instant.
func TestAddPrimaryKeyUnqualifiedResolves(t *testing.T) {
	f := indexFixture(t)
	qualified := indexFindingByCode(f, indexCapture("ALTER TABLE public.orders ADD PRIMARY KEY (id)"), "RS-INDEX-002")
	unqualified := indexFindingByCode(f, indexCapture("ALTER TABLE orders ADD PRIMARY KEY (id)"), "RS-INDEX-002")
	if qualified == nil || unqualified == nil {
		t.Fatalf("both forms must fire: qualified=%v unqualified=%v", qualified, unqualified)
	}
	if qualified.Estimate.Bucket != unqualified.Estimate.Bucket {
		t.Errorf("unqualified form got a different bucket (%s vs %s) — it did not resolve to public.orders",
			unqualified.Estimate.Bucket, qualified.Estimate.Bucket)
	}
}

// TestReindexScopeTotals: REINDEX rebuilds EVERY index in its scope
// sequentially, so the duration must bucket from the SUM of their bytes, not the
// single largest — and REINDEX SCHEMA / DATABASE (previously silent) must produce
// a finding at all.
func TestReindexScopeTotals(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.orders:
    rows: {value: 10000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
    indexes:
      - {name: orders_a_idx, method: btree, columns: [id], bytes: 30000000000}
      - {name: orders_b_idx, method: btree, columns: [id], bytes: 30000000000}
  analytics.events:
    rows: {value: 5000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
    indexes:
      - {name: events_idx, method: btree, columns: [id], bytes: 20000000000}
`))
	if err != nil {
		t.Fatal(err)
	}
	get := func(sql string) *verdict.Finding {
		return indexFindingByCode(f, indexCapture(sql), "RS-INDEX-020")
	}
	ev := func(fnd *verdict.Finding) (int64, int) {
		m := fnd.Evidence.(map[string]any)
		return m["index_bytes"].(int64), m["index_count"].(int)
	}

	// REINDEX TABLE sums BOTH of orders' indexes (60 GB), not just one (30 GB).
	tbl := get("REINDEX TABLE public.orders")
	if tbl == nil {
		t.Fatal("REINDEX TABLE produced no finding")
	}
	if b, n := ev(tbl); b != 60000000000 || n != 2 {
		t.Errorf("REINDEX TABLE totals = %d bytes / %d indexes, want 60000000000 / 2 (sum, not largest)", b, n)
	}

	// REINDEX SCHEMA public: only public.orders' indexes (60 GB), not analytics.
	sch := get("REINDEX SCHEMA public")
	if sch == nil {
		t.Fatal("REINDEX SCHEMA produced no finding (was silent before)")
	}
	if b, n := ev(sch); b != 60000000000 || n != 2 {
		t.Errorf("REINDEX SCHEMA public totals = %d / %d, want 60000000000 / 2", b, n)
	}

	// REINDEX DATABASE: every index across all schemas (80 GB / 3 indexes).
	db := get("REINDEX DATABASE app")
	if db == nil {
		t.Fatal("REINDEX DATABASE produced no finding (was silent before)")
	}
	if b, n := ev(db); b != 80000000000 || n != 3 {
		t.Errorf("REINDEX DATABASE totals = %d / %d, want 80000000000 / 3", b, n)
	}
}

// TestUnqualifiedCreateIndexGetsTheSameEstimate is the rsindex twin of
// rslock's TestUnqualifiedTableGetsTheSameEstimate.
func TestUnqualifiedCreateIndexGetsTheSameEstimate(t *testing.T) {
	f := indexFixture(t)

	bucketFor := func(t *testing.T, table string) string {
		t.Helper()
		fnd := indexFindingByCode(f, indexCapture("CREATE INDEX ix ON "+table+" (id)"), "RS-INDEX-001")
		if fnd == nil || fnd.Estimate == nil {
			return ""
		}
		return fnd.Estimate.Bucket
	}

	qualified := bucketFor(t, "public.orders")
	unqualified := bucketFor(t, "orders")
	t.Logf("qualified=%q unqualified=%q", qualified, unqualified)

	// Guard the guard.
	if qualified == "" || qualified == "instant" {
		t.Fatalf("qualified estimate = %q; a 50M-row index build extrapolated from 40ms/2k rows must "+
			"be alarming or this test cannot detect the bug", qualified)
	}
	if unqualified != qualified {
		t.Errorf("estimate for `CREATE INDEX ix ON orders (id)` = %q, but the qualified form = %q — "+
			"the same build on the same 50M-row table. The unqualified form read rows=0.",
			unqualified, qualified)
	}
}

// TestUnqualifiedUniqueIndexKeepsItsVerdict: uniqueness is looked up per table,
// so an unresolved name made a column with PROVEN duplicates look merely
// unproven — downgrading a definite failure (this index cannot build) into a
// soft "not confirmed".
func TestUnqualifiedUniqueIndexKeepsItsVerdict(t *testing.T) {
	f := indexFixture(t)

	qualified := indexFindingByCode(f, indexCapture("CREATE UNIQUE INDEX ix ON public.orders (email)"), "RS-INDEX-010")
	unqualified := indexFindingByCode(f, indexCapture("CREATE UNIQUE INDEX ix ON orders (email)"), "RS-INDEX-010")

	if qualified == nil || qualified.Severity != verdict.SeverityError {
		t.Fatalf("qualified form must be a proven-duplicate error, got %+v", qualified)
	}
	if unqualified == nil {
		t.Fatal("unqualified CREATE UNIQUE INDEX produced no RS-INDEX-010")
	}
	if unqualified.Severity != qualified.Severity {
		t.Errorf("severity for the unqualified form = %q, qualified = %q — the same index on the same "+
			"column with proven duplicates", unqualified.Severity, qualified.Severity)
	}
}

// TestUnqualifiedReindexTableResolves: REINDEX TABLE names a TABLE, so it needs
// resolution. REINDEX INDEX names an index, matched against index names rather
// than fixture table keys, and must NOT be resolved — the two cases share a
// parser and are deliberately treated differently.
func TestUnqualifiedReindexTableResolves(t *testing.T) {
	f := indexFixture(t)

	qualified := indexFindingByCode(f, indexCapture("REINDEX TABLE public.orders"), "RS-INDEX-020")
	unqualified := indexFindingByCode(f, indexCapture("REINDEX TABLE orders"), "RS-INDEX-020")

	if qualified == nil {
		t.Fatal("qualified REINDEX TABLE produced no RS-INDEX-020; cannot detect the bug")
	}
	if unqualified == nil {
		t.Fatal("`REINDEX TABLE orders` produced NO RS-INDEX-020, but the qualified form did")
	}
	// Both forms must resolve to the SAME table's largest index — proven by an
	// identical byte-based estimate. (depends_on is deliberately empty on this
	// finding: the estimate rests on index bytes, not the row count.)
	qb, _ := qualified.Evidence.(map[string]any)
	ub, _ := unqualified.Evidence.(map[string]any)
	if qb["index_bytes"] != ub["index_bytes"] {
		t.Errorf("resolved to different indexes: unqualified bytes=%v qualified bytes=%v", ub["index_bytes"], qb["index_bytes"])
	}
	if qualified.Estimate == nil || unqualified.Estimate == nil || qualified.Estimate.Bucket != unqualified.Estimate.Bucket {
		t.Errorf("estimate buckets differ: unqualified=%+v qualified=%+v", unqualified.Estimate, qualified.Estimate)
	}

	// REINDEX INDEX still resolves by index name, unaffected by table resolution.
	if fnd := indexFindingByCode(f, indexCapture("REINDEX INDEX orders_email_idx"), "RS-INDEX-020"); fnd == nil {
		t.Error("REINDEX INDEX <index-name> must still be found by index name")
	}
}

// --- CR-T5: partial and expression unique indexes ---------------------------
//
// parseCreateIndex parsed neither WHERE predicates nor expression targets, so a
// partial unique index was judged against the WHOLE column's uniqueness fact.
// The soft-delete pattern is the common case and it produced a confident, wrong
// FAIL: duplicates that live entirely in soft-deleted rows do not stop
// `CREATE UNIQUE INDEX ... WHERE deleted_at IS NULL` from building.
//
// A wrong FAIL is less dangerous than a wrong PASS, but it is still the tool
// being confidently incorrect, and it is the kind of false positive that gets a
// check deleted from a pipeline — after which the true positives are gone too.

// softDeleteFixture: `email` has PROVEN duplicates across the whole table, which
// is exactly the fact that used to force a FAIL.
func softDeleteFixture(t *testing.T) *fixture.Fixture {
	t.Helper()
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 2000000, confidence: exact}
    columns:
      email: {type: text, nullable: false, unique: {value: false, confidence: exact, via: probe}}
      deleted_at: {type: timestamptz, nullable: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPartialUniqueIndexIsNotAConfidentFail(t *testing.T) {
	f := softDeleteFixture(t)
	fnd := indexFindingByCode(f,
		indexCapture("CREATE UNIQUE INDEX ix ON public.users (email) WHERE deleted_at IS NULL"),
		"RS-INDEX-010")

	if fnd == nil {
		t.Fatal("expected an RS-INDEX-010 finding")
	}
	if fnd.Severity == verdict.SeverityError {
		t.Errorf("a partial unique index must NOT be a confident FAIL: the column's duplicates may "+
			"live entirely in the rows the predicate excludes. Got severity %q, title %q",
			fnd.Severity, fnd.Title)
	}
	if fnd.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %q, want warn (undecidable, so neither FAIL nor PASS)", fnd.Severity)
	}
	// It must not cite the whole-column fact it deliberately declines to use.
	if len(fnd.DependsOn) != 0 {
		t.Errorf("depends_on = %v, want none: the whole-column unique fact does not support this "+
			"conclusion and citing it would be false provenance", fnd.DependsOn)
	}
	// The remediation must tell the user how to settle it themselves.
	if !strings.Contains(fnd.Remediation, "GROUP BY") || !strings.Contains(fnd.Remediation, "deleted_at IS NULL") {
		t.Errorf("remediation must name a check over the INDEXED SET (predicate included), got %q",
			fnd.Remediation)
	}
}

// TestPartialUniqueIndexCannotPass: the other half. Declining to FAIL must not
// become declining to warn — a partial index over a column that IS provably
// unique still cannot be certified, because the fixture measured the column and
// not the subset.
func TestPartialUniqueIndexCannotPass(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 2000000, confidence: exact}
    columns:
      email: {type: text, nullable: false, unique: {value: true, confidence: exact, via: probe}}
      deleted_at: {type: timestamptz, nullable: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	fnd := indexFindingByCode(f,
		indexCapture("CREATE UNIQUE INDEX ix ON public.users (email) WHERE deleted_at IS NULL"),
		"RS-INDEX-010")
	if fnd == nil {
		t.Fatal("expected an RS-INDEX-010 finding")
	}
	if fnd.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %q, want warn: a proven-unique COLUMN does not certify a PARTIAL index",
			fnd.Severity)
	}
}

// TestExpressionUniqueIndexIsUndecidable: a fact about `email` says nothing
// about `lower(email)`.
func TestExpressionUniqueIndexIsUndecidable(t *testing.T) {
	f := softDeleteFixture(t)
	fnd := indexFindingByCode(f,
		indexCapture("CREATE UNIQUE INDEX ix ON public.users (lower(email))"),
		"RS-INDEX-010")
	if fnd == nil {
		t.Fatal("expected an RS-INDEX-010 finding")
	}
	if fnd.Severity == verdict.SeverityError {
		t.Errorf("an expression index must not be judged from the bare column's fact, got %q / %q",
			fnd.Severity, fnd.Title)
	}
}

// TestPlainUniqueIndexStillFails guards the true positive the fix must not eat:
// a plain unique index over proven duplicates is still a definite FAIL.
func TestPlainUniqueIndexStillFails(t *testing.T) {
	f := softDeleteFixture(t)
	fnd := indexFindingByCode(f,
		indexCapture("CREATE UNIQUE INDEX ix ON public.users (email)"),
		"RS-INDEX-010")
	if fnd == nil {
		t.Fatal("expected an RS-INDEX-010 finding")
	}
	if fnd.Severity != verdict.SeverityError {
		t.Errorf("a NON-partial unique index over proven duplicates must still be an error, got %q — "+
			"the CR-T5 fix must not weaken the true positive", fnd.Severity)
	}
}

// TestParseCreateIndexPredicateAndExpression pins the parser itself.
func TestParseCreateIndexPredicateAndExpression(t *testing.T) {
	cases := []struct {
		sql            string
		wantPredicate  string
		wantExpression bool
	}{
		{"CREATE UNIQUE INDEX ix ON public.users (email)", "", false},
		{"CREATE UNIQUE INDEX ix ON public.users (email) WHERE deleted_at IS NULL", "deleted_at IS NULL", false},
		{"CREATE UNIQUE INDEX ix ON public.users (email) WHERE deleted_at IS NULL;", "deleted_at IS NULL", false},
		{"CREATE UNIQUE INDEX ix ON public.users (lower(email))", "", true},
		{"CREATE INDEX ix ON public.users (email) WHERE active", "active", false},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			ix, ok := parseCreateIndex(tc.sql, strings.ToUpper(tc.sql))
			if !ok {
				t.Fatal("parse failed")
			}
			if ix.predicate != tc.wantPredicate {
				t.Errorf("predicate = %q, want %q", ix.predicate, tc.wantPredicate)
			}
			if ix.expression != tc.wantExpression {
				t.Errorf("expression = %v, want %v", ix.expression, tc.wantExpression)
			}
			if ix.undecidable() != (tc.wantPredicate != "" || tc.wantExpression) {
				t.Errorf("undecidable() = %v for %q", ix.undecidable(), tc.sql)
			}
		})
	}
}
