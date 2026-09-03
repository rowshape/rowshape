package findings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestRSLockNoRewriteStringWidening: a binary-coercible string widening
// (varchar(n)->varchar(m>=n), ->text) is a catalog-only change on every supported
// Postgres — no rewrite, no scan — so it must NOT produce an RS-LOCK rewrite
// finding. rsLock previously reported EVERY ALTER COLUMN TYPE as a full rewrite,
// fabricating an extrapolated outage for a metadata-only change.
func TestRSLockNoRewriteStringWidening(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 50000000, confidence: exact}
    columns:
      name: {type: 'varchar(100)', nullable: false}
      code: {type: 'varchar(20)', nullable: false}
      note: {type: text, nullable: true}
      qty:  {type: integer, nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	cap := func(sql string) *validate.Capture {
		return &validate.Capture{Success: true, Statements: []validate.Statement{{SQL: sql, DurationMs: 40}}, TableRows: map[string]int64{"public.users": 2000}}
	}
	fires := func(sql string) bool {
		for _, fnd := range (rsLock{}).Analyze(f, cap(sql)) {
			if fnd.Code == "RS-LOCK-001" {
				return true
			}
		}
		return false
	}

	// No-rewrite widenings: must NOT fire.
	for _, sql := range []string{
		"ALTER TABLE public.users ALTER COLUMN name TYPE varchar(200)", // widen cap
		"ALTER TABLE public.users ALTER COLUMN name TYPE text",         // drop cap
		"ALTER TABLE public.users ALTER COLUMN code TYPE varchar(20)",  // same cap
	} {
		if fires(sql) {
			t.Errorf("no-rewrite string widening must not fire RS-LOCK-001: %q", sql)
		}
	}

	// Genuine rewrites / verify scans: must still fire.
	for _, sql := range []string{
		"ALTER TABLE public.users ALTER COLUMN name TYPE varchar(50)",  // narrowing cap
		"ALTER TABLE public.users ALTER COLUMN note TYPE varchar(100)", // text -> capped: verify scan
		"ALTER TABLE public.users ALTER COLUMN qty TYPE bigint",        // representation change
	} {
		if !fires(sql) {
			t.Errorf("a rewrite/verify-scan type change must still fire RS-LOCK-001: %q", sql)
		}
	}
}

// loadCorpus reads a corpus case's fixture and migration.
func loadCorpus(t *testing.T, name string) (*fixture.Fixture, string) {
	t.Helper()
	dir := filepath.Join("..", "..", "corpus", "cases", name)
	fx, err := os.ReadFile(filepath.Join(dir, "fixture.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f, err := fixture.Parse(fx)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	mig, err := os.ReadFile(filepath.Join(dir, "migration.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return f, string(mig)
}

// captureOf builds a synthetic capture of applying a migration: the rewrite
// statement is recorded with the ACCESS EXCLUSIVE lock it takes and a small
// hydrated basis, standing in for a real run.
func captureOf(migration, table string, hydrated int64) *validate.Capture {
	var stmts []validate.Statement
	for _, s := range validate.SplitStatements(migration) {
		stmts = append(stmts, validate.Statement{SQL: s, LockMode: "AccessExclusiveLock", DurationMs: 90})
	}
	return &validate.Capture{Success: true, Statements: stmts, TableRows: map[string]int64{table: hydrated}}
}

// TestRSLockVolatileDefault: RS-LOCK-001 fires on the volatile-default rewrite
// corpus case with the right lock mode, rows_rewritten evidence, a bucketed
// duration with basis, and the expand/backfill/contract remediation (PRD §10).
func TestRSLockVolatileDefault(t *testing.T) {
	f, mig := loadCorpus(t, "rslock-volatile-default")
	c := captureOf(mig, "public.orders", 12_000)

	got := rsLock{}.Analyze(f, c)
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-LOCK finding, got %d", len(got))
	}
	fnd := got[0]
	if fnd.Code != "RS-LOCK-001" {
		t.Errorf("code = %s, want RS-LOCK-001", fnd.Code)
	}
	ev, _ := fnd.Evidence.(map[string]any)
	if ev["lock_mode"] != "ACCESS EXCLUSIVE" {
		t.Errorf("lock_mode evidence = %v, want ACCESS EXCLUSIVE", ev["lock_mode"])
	}
	if ev["rows_rewritten"] != int64(5_000_000) {
		t.Errorf("rows_rewritten evidence = %v, want 5000000", ev["rows_rewritten"])
	}
	if fnd.Estimate == nil || fnd.Estimate.Bucket == "" || fnd.Estimate.BasisRows != 12_000 || fnd.Estimate.DeclaredRows != 5_000_000 {
		t.Errorf("estimate must be a bucket with basis, got %+v", fnd.Estimate)
	}
	for _, want := range []string{"backfill", "batches"} {
		if !strings.Contains(strings.ToLower(fnd.Remediation), want) {
			t.Errorf("remediation must be the expand/backfill/contract recipe, missing %q: %q", want, fnd.Remediation)
		}
	}
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.orders.rows" {
		t.Errorf("depends_on = %v, want [public.orders.rows]", fnd.DependsOn)
	}
}

// TestRSLockCappedVerdict: run through the pipeline, the finding produces a WARN
// (matching the corpus) and carries the capped confidence of its dependency —
// public.orders.rows is exact, so the finding is exact.
func TestRSLockCappedVerdict(t *testing.T) {
	f, mig := loadCorpus(t, "rslock-volatile-default")
	c := captureOf(mig, "public.orders", 12_000)

	res := validate.BuildResult(f, c, []validate.Analyzer{rsLock{}}, false)
	if res.Verdict != verdict.VerdictWarn {
		t.Errorf("verdict = %s, want WARN (matches corpus expected.json)", res.Verdict)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected the RS-LOCK finding to surface, got %d", len(res.Findings))
	}
	if res.Findings[0].Confidence != string(fixture.Exact) {
		t.Errorf("finding confidence = %q, want exact (capped by public.orders.rows)", res.Findings[0].Confidence)
	}
}

// TestRSLockTypeChange: an in-place column type change rewrites the table and
// fires RS-LOCK with the online-swap remediation.
func TestRSLockTypeChange(t *testing.T) {
	f, mig := loadCorpus(t, "rslock-type-change")
	c := captureOf(mig, "public.events", 9_000)

	got := rsLock{}.Analyze(f, c)
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-LOCK finding for the type change, got %d", len(got))
	}
	if !strings.Contains(strings.ToLower(got[0].Remediation), "type") {
		t.Errorf("type-change remediation should describe the online type swap: %q", got[0].Remediation)
	}
}

// TestRSLockVersionConditional: the SAME non-volatile-default ADD COLUMN fires on
// PG 10 (rewrite) but not on PG 11 or PG 16 (catalog fast-path) — RS-LOCK is
// version-conditional (RFC §9.1, PRD §12). A volatile default fires on all.
func TestRSLockVersionConditional(t *testing.T) {
	base := `rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "%s"}}
tables:
  public.t:
    rows: {value: 4000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
`
	fixtureAt := func(t *testing.T, ver string) *fixture.Fixture {
		f, err := fixture.Parse([]byte(strings.Replace(base, "%s", ver, 1)))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}

	nonVolatile := "ALTER TABLE public.t ADD COLUMN flag boolean NOT NULL DEFAULT false;"
	volatile := "ALTER TABLE public.t ADD COLUMN token uuid NOT NULL DEFAULT gen_random_uuid();"

	count := func(t *testing.T, ver, mig string) int {
		f := fixtureAt(t, ver)
		c := captureOf(mig, "public.t", 10_000)
		return len(rsLock{}.Analyze(f, c))
	}

	// Non-volatile default: rewrite pre-11, catalog fast-path on 11 and 16.
	if got := count(t, "10", nonVolatile); got != 1 {
		t.Errorf("PG 10 non-volatile default must fire RS-LOCK (rewrite), got %d findings", got)
	}
	if got := count(t, "11", nonVolatile); got != 0 {
		t.Errorf("PG 11 non-volatile default must NOT fire (catalog fast-path), got %d", got)
	}
	if got := count(t, "16", nonVolatile); got != 0 {
		t.Errorf("PG 16 non-volatile default must NOT fire (catalog fast-path), got %d", got)
	}
	// Volatile default: rewrites on every version.
	for _, ver := range []string{"10", "11", "16"} {
		if got := count(t, ver, volatile); got != 1 {
			t.Errorf("PG %s volatile default must fire RS-LOCK on every version, got %d", ver, got)
		}
	}
}

// TestUnqualifiedTableGetsTheSameEstimate is the defect resolution exists to
// prevent, pinned end to end rather than at the resolver.
//
// `ALTER TABLE orders` and `ALTER TABLE public.orders` are the same statement.
// Before resolution the unqualified form missed the fixture key (RFC §5 keys
// tables by qualified name), the analyzer read the zero value, and a 50M-row
// rewrite was reported as `instant` instead of `outage` — a confident, materially
// wrong answer to the exact question rowshape is for. Both verdicts were WARN, so
// only the estimate gave it away.
//
// The capture below carries what a real validate run has and a bare fixture does
// not: a hydrated row count and a measured duration. Without those, estimateFor
// extrapolates from 1ms at declared==hydrated and every bucket is `instant` — an
// earlier version of this test omitted them and passed against the bug.
func TestUnqualifiedTableGetsTheSameEstimate(t *testing.T) {
	f := &fixture.Fixture{
		Meta: fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.orders": {
				Rows:    fixture.Fact[int64]{Value: 50_000_000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"id": {Type: "bigint"}},
			},
		},
	}
	const stmt = `ALTER TABLE %s ADD COLUMN note text DEFAULT clock_timestamp()::text NOT NULL`

	bucketFor := func(t *testing.T, table string) string {
		t.Helper()
		c := &validate.Capture{
			Success:    true,
			Statements: []validate.Statement{{SQL: fmt.Sprintf(stmt, table), DurationMs: 40}},
			// 2,000 rows were hydrated and the rewrite took 40ms; production
			// declares 50M. That is a 25,000x extrapolation — an outage.
			TableRows: map[string]int64{"public.orders": 2000},
		}
		for _, fnd := range (rsLock{}).Analyze(f, c) {
			if fnd.Estimate != nil {
				return fnd.Estimate.Bucket
			}
		}
		return ""
	}

	qualified := bucketFor(t, "public.orders")
	unqualified := bucketFor(t, "orders")
	t.Logf("qualified=%q unqualified=%q", qualified, unqualified)

	// Guard the guard: if the qualified form is not itself alarming, the
	// comparison below proves nothing.
	if qualified == "" || qualified == "instant" {
		t.Fatalf("qualified estimate = %q; a 50M-row rewrite extrapolated from 40ms/2k rows must be "+
			"alarming or this test cannot detect the bug", qualified)
	}
	if unqualified != qualified {
		t.Errorf("estimate for `ALTER TABLE orders` = %q, but `ALTER TABLE public.orders` = %q — "+
			"the same statement on the same 50M-row table. The unqualified form read rows=0 and would "+
			"tell a user an outage is %q.", unqualified, qualified, unqualified)
	}
}

// TestUnknownTableRefusesToExtrapolate: a table the fixture has no facts for must
// not get a duration bucket.
//
// The arithmetic is happy to answer: rows=0 rewrites in no time, so an unknown
// table reports `instant`. That is indistinguishable from a genuinely empty table
// and is the wrong answer for the likeliest cause — a typo, a table never pulled,
// or a name that exists in two schemas. Telling someone an unknown migration is
// instant is worse than telling them nothing, so this mirrors RFC §9.1's refusal
// to extrapolate without engine.version: omit the estimate and say why.
func TestUnknownTableRefusesToExtrapolate(t *testing.T) {
	f := &fixture.Fixture{
		Meta: fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.orders": {Rows: fixture.Fact[int64]{Value: 50_000_000, Confidence: fixture.Exact}},
		},
	}
	c := &validate.Capture{
		Success: true,
		Statements: []validate.Statement{
			{SQL: `ALTER TABLE public.widgets ADD COLUMN note text DEFAULT clock_timestamp()::text NOT NULL`, DurationMs: 40},
		},
		TableRows: map[string]int64{"public.orders": 2000},
	}

	found := (rsLock{}).Analyze(f, c)
	if len(found) == 0 {
		t.Fatal("the rewrite is visible in the SQL, so the finding must still be reported")
	}
	fnd := found[0]

	if fnd.Estimate != nil {
		t.Errorf("estimate = %q for a table the fixture has no facts for; rows=0 makes any rewrite look "+
			"`instant`, which is the wrong answer told confidently (RFC §9.1: refuse to extrapolate "+
			"without a basis)", fnd.Estimate.Bucket)
	}
	// A refusal that does not say why is a dead end.
	if !strings.Contains(fnd.Title, "not extrapolated") {
		t.Errorf("the title should say the duration was not extrapolated, got: %s", fnd.Title)
	}
	if !strings.Contains(fnd.Detail, "rowshape pull") {
		t.Errorf("the detail should say how to fix it, got: %s", fnd.Detail)
	}

	// The known table still gets its estimate — the refusal must not silence the
	// tool generally.
	c.Statements[0].SQL = `ALTER TABLE public.orders ADD COLUMN note text DEFAULT clock_timestamp()::text NOT NULL`
	known := (rsLock{}).Analyze(f, c)
	if len(known) == 0 || known[0].Estimate == nil {
		t.Fatal("a known table must still be estimated")
	}
	if known[0].Estimate.Bucket == "instant" {
		t.Errorf("control: a 50M-row rewrite should not be instant, got %q", known[0].Estimate.Bucket)
	}
}

// TestFindingCarriesLocation: `location` is in the verdict contract (PRD §10),
// the human renderer prints it, and P4-T2 turns it into a PR annotation at the
// offending line. locationFor was a stub returning nil, so it was never set — and
// no test noticed, because none asserted it.
func TestFindingCarriesLocation(t *testing.T) {
	f := &fixture.Fixture{
		Meta: fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.orders": {Rows: fixture.Fact[int64]{Value: 50_000_000, Confidence: fixture.Exact}},
		},
	}
	const stmt = `ALTER TABLE public.orders ADD COLUMN note text DEFAULT clock_timestamp()::text NOT NULL`

	c := &validate.Capture{Success: true, Statements: []validate.Statement{
		{SQL: stmt, File: "migrations/0042_add_note.sql", Line: 3, DurationMs: 40},
	}}
	found := (rsLock{}).Analyze(f, c)
	if len(found) == 0 {
		t.Fatal("expected a rewrite finding")
	}
	loc := found[0].Location
	if loc == nil {
		t.Fatal("finding carries no location — a PR annotation has nothing to point at (PRD §10, P4-T2)")
	}
	if loc.File != "migrations/0042_add_note.sql" || loc.Line != 3 {
		t.Errorf("location = %+v, want migrations/0042_add_note.sql:3", loc)
	}

	// A path built the way the CLI builds one must reach the contract with forward
	// slashes on EVERY platform: the verdict is DSSE-signable (INV-DSSE-SHAPE), so
	// the same migration must produce the same document on Windows and Linux, and
	// annotations want repo-style paths.
	//
	// Asserted through filepath.Join rather than a hardcoded `migrations\0042.sql`.
	// An earlier version did the latter and CI failed it on Linux — correctly:
	// filepath.ToSlash is platform-dependent, and on Linux a backslash is a legal
	// FILENAME character, not a separator, so rewriting it would corrupt the path.
	// The claim worth pinning is about the platform's own paths, not about
	// backslashes.
	c.Statements[0].File = filepath.Join("migrations", "0042_add_note.sql")
	if got := (rsLock{}).Analyze(f, c)[0].Location; got == nil || got.File != "migrations/0042_add_note.sql" {
		t.Errorf("a native path must reach the verdict with forward slashes, got %+v", got)
	}

	// Inline SQL (an agent handing over what it just wrote, unsaved) has no file;
	// nil is honest, an invented path is not.
	c.Statements[0].File, c.Statements[0].Line = "", 0
	if got := (rsLock{}).Analyze(f, c)[0].Location; got != nil {
		t.Errorf("inline SQL must carry no location, got %+v", got)
	}
}

// TestVersionGateHasOneEnforcementPoint: CR-T21. The RFC §9.1 refusal to
// extrapolate without an engine version used to be checked by each analyzer
// separately (`if hasVersion { ... }` in rslock, rsindex and rsconstraint),
// while estimate.ForFixture implemented the same gate and was never called. The
// gate now lives in estimateFor, so a fourth analyzer cannot forget it.
//
// This drives all three analyzers against a fixture with NO engine version and
// asserts none of them attaches an estimate — the property the duplication was
// supposed to guarantee, now asserted once for all of them.
func TestVersionGateHasOneEnforcementPoint(t *testing.T) {
	noVersion, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres}}
tables:
  public.orders:
    rows: {value: 50000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
      email: {type: text, nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}

	// Split into statements the way the capture does: rsconstraint detects
	// add-then-validate ACROSS statements, so handing it one blob asserts nothing.
	migrations := map[string][]string{
		"rslock":  {"ALTER TABLE public.orders ALTER COLUMN id TYPE text"},
		"rsindex": {"CREATE INDEX ix ON public.orders (id)"},
		"rsconstraint": {
			"BEGIN",
			"ALTER TABLE public.orders ADD CONSTRAINT c CHECK (id > 0) NOT VALID",
			"ALTER TABLE public.orders VALIDATE CONSTRAINT c",
			"COMMIT",
		},
	}
	analyzers := map[string]validate.Analyzer{
		"rslock":       rsLock{},
		"rsindex":      rsIndex{},
		"rsconstraint": rsConstraint{},
	}

	for name, a := range analyzers {
		t.Run(name, func(t *testing.T) {
			var stmts []validate.Statement
			for _, sql := range migrations[name] {
				stmts = append(stmts, validate.Statement{SQL: sql, DurationMs: 40})
			}
			c := &validate.Capture{
				Success:    true,
				Statements: stmts,
				TableRows:  map[string]int64{"public.orders": 2000},
			}
			found := false
			for _, fnd := range a.Analyze(noVersion, c) {
				found = true
				if fnd.Estimate != nil {
					t.Errorf("%s attached an estimate (%+v) with no engine.version — RFC §9.1 refuses "+
						"to extrapolate without one", fnd.Code, fnd.Estimate)
				}
			}
			// Guard the guard: a skip here would mean the analyzer never fired and
			// the assertion above ran over nothing.
			if !found {
				t.Fatal("analyzer produced no finding; this subtest would assert nothing")
			}
		})
	}
}
