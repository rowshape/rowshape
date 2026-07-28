package verdict

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// prdExample is the verdict contract example from PRD §10 verbatim (the digest
// abbreviated placeholder replaced with a real-shaped value so the DSSE subject
// split is exercised). The JSON marshaler MUST reproduce this field-for-field.
const prdExample = `{
  "rowshape": "1",
  "verdict": "FAIL",
  "fixture": { "id": "prod@2026-07-14", "digest": "sha256:abc123" },
  "duration_ms": 8420,
  "findings": [
    {
      "code": "RS-LOCK-001",
      "severity": "error",
      "title": "ACCESS EXCLUSIVE lock on users, estimated 10-60s",
      "location": { "file": "migrations/0042_add_email.sql", "line": 3 },
      "detail": "ALTER TABLE users ADD COLUMN email text NOT NULL DEFAULT '' rewrites 1.2M rows.",
      "evidence": {
        "lock_mode": "ACCESS EXCLUSIVE",
        "rows_rewritten": 1200000
      },
      "estimate": {
        "bucket": "slow",
        "model": "linear",
        "basis_rows": 12000,
        "basis_ms": 91,
        "declared_rows": 1200000
      },
      "depends_on": ["public.users.rows"],
      "confidence": "exact",
      "remediation": "Split into: ADD COLUMN nullable; backfill in batches; SET NOT NULL via a validated CHECK constraint.",
      "explain": "rowshape explain RS-LOCK-001"
    }
  ]
}`

// exampleResult is the Result whose JSON marshaling must equal prdExample. It is
// also the single value both marshalers consume in TestTwoMarshalersOneStruct.
func exampleResult() Result {
	return Result{
		Rowshape:   Rowshape,
		Verdict:    VerdictFail,
		Fixture:    FixtureRef{ID: "prod@2026-07-14", Digest: "sha256:abc123"},
		DurationMs: 8420,
		Findings: []Finding{{
			Code:     "RS-LOCK-001",
			Severity: SeverityError,
			Title:    "ACCESS EXCLUSIVE lock on users, estimated 10-60s",
			Location: &Location{File: "migrations/0042_add_email.sql", Line: 3},
			Detail:   "ALTER TABLE users ADD COLUMN email text NOT NULL DEFAULT '' rewrites 1.2M rows.",
			Evidence: map[string]any{
				"lock_mode":      "ACCESS EXCLUSIVE",
				"rows_rewritten": 1200000,
			},
			Estimate: &Estimate{
				Bucket:       BucketSlow,
				Model:        "linear",
				BasisRows:    12000,
				BasisMs:      91,
				DeclaredRows: 1200000,
			},
			DependsOn:   []string{"public.users.rows"},
			Confidence:  "exact",
			Remediation: "Split into: ADD COLUMN nullable; backfill in batches; SET NOT NULL via a validated CHECK constraint.",
			Explain:     "rowshape explain RS-LOCK-001",
		}},
	}
}

// TestJSONMatchesPRDExample: the JSON marshaler reproduces the PRD §10 example
// field-for-field (compared as normalized JSON so key order and whitespace don't
// matter — the contract is the field set and values, not formatting).
func TestJSONMatchesPRDExample(t *testing.T) {
	got, err := json.Marshal(exampleResult())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(prdExample), &wantAny); err != nil {
		t.Fatalf("unmarshal PRD example: %v", err)
	}
	if !reflect.DeepEqual(gotAny, wantAny) {
		t.Errorf("JSON does not match PRD §10 example.\n got: %s\nwant: %s", got, prdExample)
	}
}

// TestTwoMarshalersOneStruct proves human output is a rendering of the SAME
// struct the JSON marshaler serializes (INV-VERDICT-SHAPE): one value in, both
// marshalers consume it, and the human text surfaces the struct's key fields.
func TestTwoMarshalersOneStruct(t *testing.T) {
	r := exampleResult()

	jsonBytes, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	human := r.Human()

	// Both are derived from r: round-trip the JSON back and confirm it still
	// equals r, and confirm the human rendering surfaces fields taken from r.
	var back Result
	if err := json.Unmarshal(jsonBytes, &back); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if back.Verdict != r.Verdict || back.Fixture != r.Fixture || len(back.Findings) != len(r.Findings) {
		t.Errorf("JSON round-trip diverged from the struct: %+v", back)
	}
	for _, want := range []string{r.Verdict, r.Fixture.ID, r.Findings[0].Code, r.Findings[0].Title, r.Findings[0].Remediation} {
		if !strings.Contains(human, want) {
			t.Errorf("human rendering missing %q from the struct:\n%s", want, human)
		}
	}
}

// TestExitCodeTable: verdict -> exit code, including the WARN-only "configurable
// to fail" knob (PRD §10).
func TestExitCodeTable(t *testing.T) {
	cases := []struct {
		verdict   string
		warnFails bool
		want      int
	}{
		{VerdictPass, false, ExitPass},
		{VerdictPass, true, ExitPass},
		{VerdictFail, false, ExitFail},
		{VerdictFail, true, ExitFail},
		{VerdictWarn, false, ExitWarnOnly},
		{VerdictWarn, true, ExitFail}, // WARN-only configured to fail
	}
	for _, c := range cases {
		got := Result{Verdict: c.verdict}.ExitCode(c.warnFails)
		if got != c.want {
			t.Errorf("ExitCode(%s, warnFails=%v) = %d, want %d", c.verdict, c.warnFails, got, c.want)
		}
	}
}

// TestExitCodesArePermanent pins the public exit-code constants (INV-VERDICT-STABLE).
func TestExitCodesArePermanent(t *testing.T) {
	if ExitPass != 0 || ExitFail != 1 || ExitWarnOnly != 2 || ExitToolError != 3 {
		t.Errorf("exit codes changed: %d/%d/%d/%d, want 0/1/2/3", ExitPass, ExitFail, ExitWarnOnly, ExitToolError)
	}
}

// TestDSSEShape: the Result serializes as an in-toto Statement whose subject is
// the fixture digest plus one entry per migration file, with the owned
// predicateType and the verdict body as the predicate (PRD §9.1, INV-DSSE-SHAPE).
func TestDSSEShape(t *testing.T) {
	r := exampleResult()
	migrations := []MigrationFile{
		{Path: "migrations/0042_add_email.sql", Contents: []byte("ALTER TABLE users ADD COLUMN email text NOT NULL DEFAULT '';")},
	}
	stmt := r.Statement(migrations)

	if stmt.Type != StatementType {
		t.Errorf("_type = %q, want %q", stmt.Type, StatementType)
	}
	if stmt.PredicateType != PredicateType {
		t.Errorf("predicateType = %q, want %q", stmt.PredicateType, PredicateType)
	}
	if stmt.PredicateType != "https://rowshape.com/attestation/v1" {
		t.Errorf("predicateType must be the owned URI, got %q", stmt.PredicateType)
	}
	if !reflect.DeepEqual(stmt.Predicate, r) {
		t.Errorf("predicate must be the verdict body verbatim")
	}
	if len(stmt.Subject) != 2 {
		t.Fatalf("subject count = %d, want 2 (fixture + 1 migration)", len(stmt.Subject))
	}
	// Fixture subject: "sha256:abc123" split into a bare DigestSet.
	if got := stmt.Subject[0].Digest["sha256"]; got != "abc123" {
		t.Errorf("fixture subject digest = %q, want bare hex %q", got, "abc123")
	}
	// Migration subject carries a real sha256 over its bytes (no "sha256:" prefix).
	migHex := stmt.Subject[1].Digest["sha256"]
	if len(migHex) != 64 || strings.Contains(migHex, ":") {
		t.Errorf("migration subject digest = %q, want 64-hex-char bare sha256", migHex)
	}
	if stmt.Subject[1].Name != "migrations/0042_add_email.sql" {
		t.Errorf("migration subject name = %q", stmt.Subject[1].Name)
	}

	// The statement marshals to valid JSON with the in-toto field names.
	b, err := r.MarshalStatement(migrations)
	if err != nil {
		t.Fatalf("marshal statement: %v", err)
	}
	for _, want := range []string{`"_type"`, `"subject"`, `"predicateType"`, `"predicate"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("statement JSON missing %s:\n%s", want, b)
		}
	}
}

// TestRemediationRequiredOnError: Validate rejects a verdict whose error finding
// has no remediation — "a finding an agent can't act on is a bug" (PRD §10,
// INV-VERDICT-STABLE).
func TestRemediationRequiredOnError(t *testing.T) {
	// The PRD example is valid.
	if err := exampleResult().Validate(); err != nil {
		t.Errorf("PRD example must validate, got %v", err)
	}

	// An error finding without remediation is rejected.
	bad := Result{
		Verdict: VerdictFail,
		Findings: []Finding{{
			Code:     "RS-LOCK-001",
			Severity: SeverityError,
			Title:    "no remediation",
		}},
	}
	if err := bad.Validate(); err == nil {
		t.Error("Validate accepted an error finding with no remediation")
	}

	// A warn finding without remediation is allowed (remediation is mandatory
	// only on error).
	warn := Result{
		Verdict: VerdictWarn,
		Findings: []Finding{{
			Code:     "RS-PERF-002",
			Severity: SeverityWarn,
			Title:    "warn without remediation is fine",
		}},
	}
	if err := warn.Validate(); err != nil {
		t.Errorf("warn finding without remediation must validate, got %v", err)
	}

	// An unknown verdict is rejected.
	if err := (Result{Verdict: "MAYBE"}).Validate(); err == nil {
		t.Error("Validate accepted an unknown verdict")
	}
}

// TestStatementSubjectDigestsRawBytes pins that the subject digest is over the
// artifact's ACTUAL bytes.
//
// An earlier change folded CRLF to LF here, to stop a Windows checkout digesting
// the same commit differently from a Linux runner. The observation was right and
// the fix was wrong: an in-toto subject digest is DEFINED as the digest of the
// artifact, so normalizing means `sha256sum migrations/001.sql` never matches the
// attested value and every standard verifier fails against the very file the
// subject names. The portability problem belongs in .gitattributes, where it can
// be solved without breaking verification.
//
// This test exists to stop the plausible-sounding fix being reapplied.
func TestStatementSubjectDigestsRawBytes(t *testing.T) {
	// Built from bytes rather than a quoted literal: the point is the exact
	// sequence, and spelling it out removes any doubt about what is being hashed.
	crlf := []byte("ALTER TABLE t\r\nADD COLUMN c int;\r\n")
	contents := crlf

	var r Result
	got := r.Statement([]MigrationFile{{Path: "migrations/001.sql", Contents: contents}})
	if len(got.Subject) != 1 {
		t.Fatalf("want one subject, got %d", len(got.Subject))
	}

	sum := sha256.Sum256(contents) // the bytes as they are, not normalized
	want := hex.EncodeToString(sum[:])
	if g := got.Subject[0].Digest["sha256"]; g != want {
		t.Errorf("subject digest = %s, want the digest of the RAW bytes %s — a verifier running "+
			"sha256sum on the file must get the attested value", g, want)
	}

	// And the CRLF and LF forms must therefore DIFFER: they are different files.
	lfBytes := []byte("ALTER TABLE t\nADD COLUMN c int;\n")
	lf := r.Statement([]MigrationFile{{Path: "migrations/001.sql", Contents: lfBytes}})
	if lf.Subject[0].Digest["sha256"] == got.Subject[0].Digest["sha256"] {
		t.Error("CRLF and LF contents are different bytes and must digest differently; " +
			"equal digests mean normalization crept back in")
	}
}

// Subject names must be slash-separated so one migration names one subject on
// every OS; a Windows path would otherwise read `migrations\001.sql`.
func TestStatementSubjectNameIsSlashed(t *testing.T) {
	var r Result
	s := r.Statement([]MigrationFile{{
		Path:     filepath.Join("migrations", "001.sql"),
		Contents: []byte("SELECT 1;"),
	}})
	if len(s.Subject) != 1 {
		t.Fatalf("want one subject, got %d", len(s.Subject))
	}
	if got := s.Subject[0].Name; got != "migrations/001.sql" {
		t.Errorf("subject name = %q, want %q", got, "migrations/001.sql")
	}
}
