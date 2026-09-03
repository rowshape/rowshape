package findings

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

// TestRSReverseDropColumn: DROP COLUMN fires RS-REVERSE-001 (WARN) declaring the
// table's rows as what is lost, with mandatory remediation.
func TestRSReverseDropColumn(t *testing.T) {
	f, mig := loadCorpus(t, "reverse-drop-column")
	got := rsReverse{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 {
		t.Fatalf("expected 1 RS-REVERSE finding, got %d: %+v", len(got), got)
	}
	fnd := got[0]
	if fnd.Code != "RS-REVERSE-001" || fnd.Severity != verdict.SeverityWarn {
		t.Errorf("want RS-REVERSE-001 warn, got %s/%s", fnd.Code, fnd.Severity)
	}
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.users.rows" {
		t.Errorf("depends_on = %v, want [public.users.rows]", fnd.DependsOn)
	}
	if strings.TrimSpace(fnd.Remediation) == "" {
		t.Error("RS-REVERSE-001 must carry mandatory remediation")
	}
}

// TestRSReverseDropTable: DROP TABLE fires RS-REVERSE-002 (WARN) with the row
// count as evidence and mandatory remediation.
func TestRSReverseDropTable(t *testing.T) {
	f, mig := loadCorpus(t, "reverse-drop-table")
	got := rsReverse{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 || got[0].Code != "RS-REVERSE-002" {
		t.Fatalf("expected RS-REVERSE-002, got %+v", got)
	}
	fnd := got[0]
	if fnd.Severity != verdict.SeverityWarn {
		t.Errorf("severity = %s, want warn", fnd.Severity)
	}
	ev, _ := fnd.Evidence.(map[string]any)
	if ev["rows"] == nil {
		t.Errorf("evidence must carry the lost row count, got %v", ev)
	}
	if strings.TrimSpace(fnd.Remediation) == "" {
		t.Error("RS-REVERSE-002 must carry mandatory remediation")
	}
}

// TestRSReverseNarrowType: a narrowing type change fires RS-REVERSE-003; a
// widening change does not.
func TestRSReverseNarrowType(t *testing.T) {
	f, mig := loadCorpus(t, "reverse-narrow-type")
	got := rsReverse{}.Analyze(f, plainCapture(mig))
	if len(got) != 1 || got[0].Code != "RS-REVERSE-003" {
		t.Fatalf("expected RS-REVERSE-003, got %+v", got)
	}
	if got[0].Severity != verdict.SeverityWarn {
		t.Errorf("severity = %s, want warn", got[0].Severity)
	}

	// A widening change (integer -> bigint) restores no loss — not flagged.
	wide, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.ledger:
    rows: {value: 2500000, confidence: exact}
    columns:
      amount: {type: integer, nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	widen := "ALTER TABLE public.ledger ALTER COLUMN amount TYPE bigint;"
	if got := (rsReverse{}).Analyze(wide, plainCapture(widen)); len(got) != 0 {
		t.Errorf("a widening type change must not be flagged, got %+v", got)
	}
}

// TestIsNarrowingStringLengths pins the length comparison for character types.
// isNarrowing used to flag ANY string change whose new type carried a length
// modifier — so widening varchar(100) -> varchar(200) misfired as an
// irreversible truncation, a wrong WARN on a safe, common migration. It now
// compares the actual caps.
func TestIsNarrowingStringLengths(t *testing.T) {
	cases := []struct {
		old, new string
		want     bool
	}{
		{"varchar(100)", "varchar(200)", false},         // widen: not narrowing
		{"varchar(200)", "varchar(100)", true},          // shrink: narrowing
		{"varchar(100)", "varchar(100)", false},         // same cap
		{"varchar(100)", "text", false},                 // drop the cap: widen
		{"text", "varchar(255)", true},                  // gain a cap: can truncate
		{"character varying(50)", "varchar(80)", false}, // widen across spellings
		{"char(10)", "char(4)", true},                   // shrink char
		{"integer", "bigint", false},                    // widen numeric (unaffected)
		{"bigint", "integer", true},                     // narrow numeric (unaffected)
	}
	for _, tc := range cases {
		if got := isNarrowing(tc.old, tc.new); got != tc.want {
			t.Errorf("isNarrowing(%q, %q) = %v, want %v", tc.old, tc.new, got, tc.want)
		}
	}
}

// TestRSReverseWideningStringNotFlagged is the analyzer-level guard: a
// varchar-widening migration must produce no RS-REVERSE-003.
func TestRSReverseWideningStringNotFlagged(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 1000000, confidence: exact}
    columns:
      name: {type: 'varchar(100)', nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	widen := "ALTER TABLE public.users ALTER COLUMN name TYPE varchar(200);"
	if got := (rsReverse{}).Analyze(f, plainCapture(widen)); len(got) != 0 {
		t.Errorf("widening varchar(100)->varchar(200) must not be flagged, got %+v", got)
	}
}

// TestRSReverseCorpusVerdicts runs the RS-REVERSE analyzer against the dedicated
// reverse-* corpus cases and asserts each expected verdict — the corpus triples
// land with the finding (the ordering discipline, PRD §14).
func TestRSReverseCorpusVerdicts(t *testing.T) {
	for _, name := range []string{"reverse-drop-column", "reverse-drop-table", "reverse-narrow-type"} {
		t.Run(name, func(t *testing.T) {
			f, mig := loadCorpus(t, name)
			res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsReverse{}}, false)
			if res.Verdict != verdict.VerdictWarn {
				t.Errorf("verdict = %s, want WARN", res.Verdict)
			}
		})
	}
}

// TestRSReverseDependsOnAndCapped: RS-REVERSE findings declare depends_on and
// carry the capped confidence of that dependency (RFC §7.4).
func TestRSReverseDependsOnAndCapped(t *testing.T) {
	f, mig := loadCorpus(t, "reverse-drop-table")
	res := validate.BuildResult(f, plainCapture(mig), []validate.Analyzer{rsReverse{}}, false)
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(res.Findings))
	}
	fnd := res.Findings[0]
	if len(fnd.DependsOn) != 1 || fnd.DependsOn[0] != "public.audit_log.rows" {
		t.Errorf("depends_on = %v, want [public.audit_log.rows]", fnd.DependsOn)
	}
	// audit_log.rows is exact → the finding's capped confidence is exact.
	if fnd.Confidence != string(fixture.Exact) {
		t.Errorf("confidence = %q, want exact (capped by audit_log.rows)", fnd.Confidence)
	}
}

// TestRSReverseRemediationInCatalog: every RS-REVERSE code the analyzer can emit
// has a catalog entry, so `rowshape explain` and the finding's remediation are
// the same text (no drift).
func TestRSReverseRemediationInCatalog(t *testing.T) {
	for _, code := range []string{"RS-REVERSE-001", "RS-REVERSE-002", "RS-REVERSE-003"} {
		e, ok := Explain(code)
		if !ok {
			t.Errorf("%s has no catalog entry", code)
			continue
		}
		if strings.TrimSpace(e.Remediation) == "" {
			t.Errorf("%s catalog entry has empty remediation", code)
		}
	}
}

// --- CR-T10: unqualified DROP TABLE -----------------------------------------
//
// dropTableFinding was the one finding in this file that skipped resolveTable,
// while dropColumnFinding and narrowTypeFinding both used it. Unresolved, the
// row count read as the zero value, so `DROP TABLE users` announced "all 0 rows
// are lost" for a table holding millions — and cited a depends_on path that
// resolves to nothing in a DSSE-signed document.
//
// Narrower blast radius than CR-T2 (the finding still fires, and it is still a
// WARN), but the evidence and the provenance were both wrong.
func TestUnqualifiedDropTableResolves(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 4000000, confidence: exact}
    columns:
      id: {type: bigint, nullable: false}
`))
	if err != nil {
		t.Fatal(err)
	}

	find := func(sql string) *verdict.Finding {
		for _, fnd := range (rsReverse{}).Analyze(f, plainCapture(sql)) {
			if fnd.Code == "RS-REVERSE-002" {
				return &fnd
			}
		}
		return nil
	}

	qualified := find("DROP TABLE public.users;")
	unqualified := find("DROP TABLE users;")

	if qualified == nil {
		t.Fatal("qualified DROP TABLE produced no RS-REVERSE-002")
	}
	if unqualified == nil {
		t.Fatal("unqualified DROP TABLE produced no RS-REVERSE-002")
	}

	qev, _ := qualified.Evidence.(map[string]any)
	uev, _ := unqualified.Evidence.(map[string]any)
	if uev["rows"] != qev["rows"] {
		t.Errorf("rows evidence = %v for `DROP TABLE users`, but %v for `DROP TABLE public.users` — "+
			"the same table. The unqualified form read the zero value.", uev["rows"], qev["rows"])
	}
	if unqualified.DependsOn[0] != "public.users.rows" {
		t.Errorf("depends_on = %v, want [public.users.rows] (the canonical key a signed attestation "+
			"should cite)", unqualified.DependsOn)
	}
	if unqualified.Title != qualified.Title {
		t.Errorf("same statement, different title:\n qualified:   %s\n unqualified: %s",
			qualified.Title, unqualified.Title)
	}
}

// --- CR-T22: identAfter must skip IF EXISTS ---------------------------------
//
// `ALTER TABLE t DROP COLUMN IF EXISTS c` returned "IF" as the column name, so
// the finding named a column that does not exist. The reversibility conclusion
// (dropping a column loses its data) happened to stay correct, which is why this
// survived — but the evidence was wrong on a document that gets signed.
func TestIdentAfterSkipsIfExists(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"ALTER TABLE public.users DROP COLUMN legacy_note", "legacy_note"},
		{"ALTER TABLE public.users DROP COLUMN IF EXISTS legacy_note", "legacy_note"},
		{"ALTER TABLE public.users DROP COLUMN if exists legacy_note", "legacy_note"},
		{"ALTER TABLE public.users DROP COLUMN IF  EXISTS legacy_note;", "legacy_note"},
		{`ALTER TABLE public.users DROP COLUMN IF EXISTS "legacy_note";`, "legacy_note"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			clean := collapseSpaces(stripSQLComments(tc.sql))
			if got := identAfter(clean, strings.ToUpper(clean), "DROP COLUMN"); got != tc.want {
				t.Errorf("identAfter = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDropColumnIfExistsNamesTheRealColumn drives it end to end, since the
// finding's title and evidence are what a reviewer actually reads.
func TestDropColumnIfExistsNamesTheRealColumn(t *testing.T) {
	f, err := fixture.Parse([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
tables:
  public.users:
    rows: {value: 900000, confidence: exact}
    columns:
      legacy_note: {type: text, nullable: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	find := func(sql string) *verdict.Finding {
		for _, fnd := range (rsReverse{}).Analyze(f, plainCapture(sql)) {
			if fnd.Code == "RS-REVERSE-001" {
				return &fnd
			}
		}
		return nil
	}

	plain := find("ALTER TABLE public.users DROP COLUMN legacy_note;")
	ifExists := find("ALTER TABLE public.users DROP COLUMN IF EXISTS legacy_note;")
	if plain == nil || ifExists == nil {
		t.Fatalf("both forms must produce RS-REVERSE-001 (plain=%v ifExists=%v)", plain, ifExists)
	}
	if strings.Contains(ifExists.Title, "IF") {
		t.Errorf("title names the SQL keyword instead of the column: %q", ifExists.Title)
	}
	if ifExists.Title != plain.Title {
		t.Errorf("IF EXISTS changed the finding:\n plain:     %s\n if exists: %s", plain.Title, ifExists.Title)
	}
}
