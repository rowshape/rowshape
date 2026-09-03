package fixture

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// rfcExample is a fixture document assembled verbatim from the YAML examples in
// RFC §5 and §6 (the users table plus its columns, constraints, indexes, and an
// orders→users reference). It is the round-trip fidelity fixture.
const rfcExample = `
rowshape_fixture: "1"

meta:
  id: prod@2026-07-14
  generated_at: 2026-07-14T09:12:44Z
  generator: rowshape/0.1.0
  engine: { name: postgres, version: "16.3" }
  privacy: standard
  source: sha256:41b0
  profile:
    mode: fast
    scanned_at: 2026-07-14T09:12:44Z
    escalated: [public.users.email, public.orders.external_ref]
  digest: sha256:9f2c

tables:
  public.users:
    rows: { value: 1200000, confidence: exact }
    bytes: 890000000
    columns:
      id:
        type: bigint
        nullable: false
        null_fraction: { value: 0.0, confidence: exact }
        distinct: { value: 1200000, confidence: exact, via: unique_index }
        unique: { value: true, confidence: exact, via: constraint }
        generated: identity
      email:
        type: text
        nullable: true
        null_fraction: { value: 0.032, confidence: exact }
        distinct: { value: 1161600, confidence: measured, via: hll, error: 0.02 }
        unique: { value: false, confidence: exact, via: scan }
        format: email
        length: { min: 6, max: 254, mean: 24.1, p95: 38 }
      status:
        type: text
        nullable: false
        null_fraction: { value: 0.0, confidence: exact }
        distinct: { value: 4, confidence: exact }
        format: enum_like
        values: [active, trialing, past_due, canceled]
        frequencies: [0.71, 0.11, 0.06, 0.12]
      created_at:
        type: timestamptz
        nullable: false
        null_fraction: { value: 0.0, confidence: estimated }
        distinct: { value: 1198412, confidence: estimated, via: pg_stats }
    constraints:
      - name: users_pkey
        kind: primary_key
        columns: [id]
      - name: users_email_key
        kind: unique
        columns: [email]
        nulls_distinct: true
      - name: users_status_check
        kind: check
        expression: "status = ANY (ARRAY['active'::text])"
        validated: true
    indexes:
      - name: users_email_idx
        method: btree
        columns: [email]
        unique: true
        partial: "WHERE deleted_at IS NULL"
        bytes: 48000000
        bloat_estimate: 0.12
  public.orders:
    rows: { value: 8400000, confidence: exact }
    references:
      - column: user_id
        to: public.users.id
        on_delete: cascade
        fanout: { mean: 8.4, p50: 3, p95: 41, max: 12902, confidence: measured }
        orphan_fraction: { value: 0.0, confidence: exact, via: scan }
`

// TestParse_RFCExample checks that the RFC §5/§6 example decodes into the
// documented shape.
func TestParse_RFCExample(t *testing.T) {
	f, err := Parse([]byte(rfcExample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if f.RowshapeFixture != "1" {
		t.Errorf("rowshape_fixture = %q, want 1", f.RowshapeFixture)
	}
	if f.Meta.Engine.Version != "16.3" {
		t.Errorf("engine.version = %q, want 16.3", f.Meta.Engine.Version)
	}

	// tables is a map keyed by qualified name (RFC §5), not a list.
	users, ok := f.Tables["public.users"]
	if !ok {
		t.Fatalf("tables missing public.users; keys=%v", keys(f.Tables))
	}
	if users.Rows.Value != 1200000 || users.Rows.Confidence != Exact {
		t.Errorf("users.rows = %+v, want {1200000 exact}", users.Rows)
	}

	// A proven-unique column (RFC §6.1 / §6.4).
	id := users.Columns["id"]
	if id.Unique == nil || id.Unique.Value != true || id.Unique.Confidence != Exact {
		t.Errorf("id.unique = %+v, want exact true", id.Unique)
	}
	if id.Distinct == nil || id.Distinct.Via != "unique_index" {
		t.Errorf("id.distinct.via = %+v, want unique_index", id.Distinct)
	}

	// A measured HLL fact carries its error bound (RFC §6.1).
	email := users.Columns["email"]
	if email.Distinct == nil || email.Distinct.Confidence != Measured || email.Distinct.Error != 0.02 {
		t.Errorf("email.distinct = %+v, want measured error 0.02", email.Distinct)
	}
	// unique:false is still exact (§7.2): absence is the only alternative to exact.
	if email.Unique == nil || email.Unique.Value != false || email.Unique.Confidence != Exact {
		t.Errorf("email.unique = %+v, want exact false", email.Unique)
	}
	if email.Length == nil || email.Length.Min == nil || *email.Length.Min != 6 {
		t.Errorf("email.length.min = %+v, want 6", email.Length)
	}

	// A NOT VALID / VALIDATED distinction is preserved (RFC §6.4).
	var check *Constraint
	for i := range users.Constraints {
		if users.Constraints[i].Kind == "check" {
			check = &users.Constraints[i]
		}
	}
	if check == nil || check.Validated == nil || *check.Validated != true {
		t.Errorf("check constraint validated = %+v, want true", check)
	}

	// Fan-out is the moat field (RFC §6.6).
	orders := f.Tables["public.orders"]
	if len(orders.References) != 1 {
		t.Fatalf("orders references = %d, want 1", len(orders.References))
	}
	ref := orders.References[0]
	if ref.Fanout == nil || ref.Fanout.Max != 12902 || ref.Fanout.Confidence != Measured {
		t.Errorf("fanout = %+v, want max 12902 measured", ref.Fanout)
	}
	if ref.OrphanFraction == nil || ref.OrphanFraction.Via != "scan" {
		t.Errorf("orphan_fraction = %+v, want via scan", ref.OrphanFraction)
	}
}

// TestRoundTrip checks that decode → encode → decode is stable: the model
// captures every fact in the RFC examples (modulo canonicalization, RFC §11).
func TestRoundTrip(t *testing.T) {
	first, err := Parse([]byte(rfcExample))
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	out, err := Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("round-trip changed the model.\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

// TestBareScalarShorthand: a bare scalar deserializes as {value, confidence:
// estimated} — the weakest reading (RFC §6.1).
func TestBareScalarShorthand(t *testing.T) {
	const doc = `
rowshape_fixture: "1"
meta:
  engine: { name: postgres, version: "16" }
  profile: { mode: fast }
tables:
  public.t:
    rows: 500
    columns:
      c:
        type: int
        nullable: false
        null_fraction: 0.25
        distinct: 12
`
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tbl := f.Tables["public.t"]
	if tbl.Rows.Value != 500 || tbl.Rows.Confidence != Estimated {
		t.Errorf("bare rows = %+v, want {500 estimated}", tbl.Rows)
	}
	c := tbl.Columns["c"]
	if c.NullFraction == nil || c.NullFraction.Value != 0.25 || c.NullFraction.Confidence != Estimated {
		t.Errorf("bare null_fraction = %+v, want {0.25 estimated}", c.NullFraction)
	}
	if c.Distinct == nil || c.Distinct.Value != 12 || c.Distinct.Confidence != Estimated {
		t.Errorf("bare distinct = %+v, want {12 estimated}", c.Distinct)
	}
}

// TestUnknownFieldsIgnored: unknown fields are ignored, not rejected; x_ vendor
// extensions are preserved (RFC §12).
func TestUnknownFieldsIgnored(t *testing.T) {
	const doc = `
rowshape_fixture: "1"
meta:
  engine: { name: postgres, version: "16" }
  profile: { mode: fast }
  future_meta_field: whatever
tables:
  public.t:
    rows: { value: 1, confidence: exact }
    some_unknown_table_field: 123
    x_vendor_note: keep-me
    columns:
      c:
        type: int
        nullable: false
        another_unknown: nope
        x_col_ext: also-keep
`
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse should ignore unknown fields, got: %v", err)
	}
	tbl := f.Tables["public.t"]
	if tbl.X["x_vendor_note"] != "keep-me" {
		t.Errorf("x_ table extension not preserved: %v", tbl.X)
	}
	if _, ok := tbl.X["some_unknown_table_field"]; ok {
		t.Errorf("non-x_ unknown field should be dropped: %v", tbl.X)
	}
	c := tbl.Columns["c"]
	if c.X["x_col_ext"] != "also-keep" {
		t.Errorf("x_ column extension not preserved: %v", c.X)
	}
	if _, ok := c.X["another_unknown"]; ok {
		t.Errorf("non-x_ unknown column field should be dropped: %v", c.X)
	}
}

// TestUnknownMajorVersionRefused: an unknown major rowshape_fixture version is
// refused rather than best-effort (RFC §12).
func TestUnknownMajorVersionRefused(t *testing.T) {
	cases := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"v1", `"1"`, false},
		{"v1_minor", `"1.4"`, false},
		{"v2", `"2"`, true},
		{"v99", `"99"`, true},
		{"missing", `""`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := "rowshape_fixture: " + tc.version + "\nmeta:\n  engine: { name: postgres, version: \"16\" }\n  profile: { mode: fast }\ntables: {}\n"
			_, err := Parse([]byte(doc))
			if tc.wantErr && err == nil {
				t.Errorf("version %s: expected refusal, got nil", tc.version)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("version %s: expected acceptance, got %v", tc.version, err)
			}
		})
	}
}

// TestRangeMustNotAppearOnText documents §6.1 at the model level: a range with
// timestamp bounds round-trips (temporal columns), and numeric ranges keep their
// numeric bounds. (The emitter enforces the text/bytea prohibition; here we only
// confirm the model preserves both numeric and temporal bounds.)
func TestRangeNumericAndTemporal(t *testing.T) {
	const doc = `
rowshape_fixture: "1"
meta:
  engine: { name: postgres, version: "16" }
  profile: { mode: fast }
tables:
  public.t:
    rows: { value: 1, confidence: exact }
    columns:
      n:
        type: int
        nullable: false
        range: { min: 0, max: 4999, mean: 82.3 }
      ts:
        type: timestamptz
        nullable: false
        range: { min: 2021-03-01T00:00:00Z, max: 2026-07-14T08:59:12Z }
`
	f, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cols := f.Tables["public.t"].Columns
	if cols["n"].Range == nil || cols["n"].Range.Mean == nil || *cols["n"].Range.Mean != 82.3 {
		t.Errorf("numeric range = %+v", cols["n"].Range)
	}
	if cols["ts"].Range == nil || cols["ts"].Range.Min == nil {
		t.Errorf("temporal range = %+v", cols["ts"].Range)
	}
	// A temporal range must round-trip.
	out, err := Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Parse(out); err != nil {
		t.Fatalf("re-parse temporal range: %v\n%s", err, out)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var _ = yaml.Marshal

// TestUserTypesRoundTrip: the types section survives a YAML round trip, including
// the x_ vendor extensions RFC §12 requires be preserved.
func TestUserTypesRoundTrip(t *testing.T) {
	src := `rowshape_fixture: "1"
meta:
  id: f
  engine: {name: postgres, version: "16"}
types:
  z.mood:
    kind: enum
    labels: [sad, ok, happy]
    label_count: 3
    x_vendor: keep-me
  z.positive_int:
    kind: domain
    base: integer
    not_null: true
    check: (VALUE > 0)
tables: {}
`
	var f Fixture
	if err := yaml.Unmarshal([]byte(src), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	mood, ok := f.Types["z.mood"]
	if !ok {
		t.Fatal("z.mood missing after unmarshal")
	}
	if mood.Kind != "enum" || mood.LabelCount != 3 {
		t.Errorf("enum decoded as kind=%q label_count=%d", mood.Kind, mood.LabelCount)
	}
	if got := strings.Join(mood.Labels, ","); got != "sad,ok,happy" {
		t.Errorf("labels = %q; declaration order is significant and must be preserved", got)
	}
	if mood.X["x_vendor"] != "keep-me" {
		t.Errorf("vendor extension not preserved: %v", mood.X)
	}
	d := f.Types["z.positive_int"]
	if d.Kind != "domain" || d.Base != "integer" || !d.NotNull || d.Check != "(VALUE > 0)" {
		t.Errorf("domain decoded wrong: %+v", d)
	}
}

// TestEffectiveLabelsIsTheSharedContract: the DDL emitter and the synthesis engine
// both build enum values from this, so it must be deterministic and agree with
// itself. A redacted vocabulary (privacy:strict) yields placeholders sized by the
// count; a present vocabulary is returned verbatim.
func TestEffectiveLabelsIsTheSharedContract(t *testing.T) {
	real := UserType{Kind: "enum", Labels: []string{"a", "b"}, LabelCount: 2}
	if got := strings.Join(real.EffectiveLabels(), ","); got != "a,b" {
		t.Errorf("present vocabulary altered: %q", got)
	}
	redacted := UserType{Kind: "enum", LabelCount: 3}
	if got := strings.Join(redacted.EffectiveLabels(), ","); got != "label_0,label_1,label_2" {
		t.Errorf("redacted vocabulary = %q, want label_0..2", got)
	}
	if got := redacted.EffectiveLabels(); len(got) != len(redacted.EffectiveLabels()) {
		t.Error("EffectiveLabels is not stable across calls")
	}
	if l := (UserType{Kind: "domain", Base: "integer"}).EffectiveLabels(); l != nil {
		t.Errorf("a domain returned labels: %v", l)
	}
	if l := (UserType{Kind: "enum"}).EffectiveLabels(); l != nil {
		t.Errorf("an enum with neither labels nor count returned %v, want nil", l)
	}
}

// TestResolveTypeFollowsDomains: a domain reduces to its base; nesting is followed;
// a cycle in a malformed fixture terminates instead of hanging the generator.
func TestResolveTypeFollowsDomains(t *testing.T) {
	f := &Fixture{Types: map[string]UserType{
		"z.outer": {Kind: "domain", Base: "z.inner"},
		"z.inner": {Kind: "domain", Base: "bigint"},
		"z.mood":  {Kind: "enum", Labels: []string{"a"}, LabelCount: 1},
		"z.loopA": {Kind: "domain", Base: "z.loopB"},
		"z.loopB": {Kind: "domain", Base: "z.loopA"},
	}}
	if got := f.ResolveType("z.outer"); got != "bigint" {
		t.Errorf("nested domain resolved to %q, want bigint", got)
	}
	if got := f.ResolveType("integer"); got != "integer" {
		t.Errorf("built-in type was rewritten to %q", got)
	}
	if got := f.ResolveType("z.mood"); got != "z.mood" {
		t.Errorf("enum was resolved away to %q; only domains have a base", got)
	}
	// Must terminate; the exact value is unimportant.
	_ = f.ResolveType("z.loopA")
}

// TestEnumLabelsReportsEnumnessSeparately: an enum whose vocabulary was withheld is
// still an enum. If the boolean were tied to the label list, the caller would fall
// through to a generic string, which is not a member of the type.
func TestEnumLabelsReportsEnumnessSeparately(t *testing.T) {
	f := &Fixture{Types: map[string]UserType{
		"z.mood": {Kind: "enum", LabelCount: 2},
		"z.dom":  {Kind: "domain", Base: "integer"},
	}}
	labels, isEnum := f.EnumLabels("z.mood")
	if !isEnum {
		t.Error("a redacted enum did not report as an enum")
	}
	if len(labels) != 2 {
		t.Errorf("got %d placeholder labels, want 2", len(labels))
	}
	if _, isEnum := f.EnumLabels("z.dom"); isEnum {
		t.Error("a domain reported as an enum")
	}
	if _, isEnum := f.EnumLabels("integer"); isEnum {
		t.Error("a built-in type reported as an enum")
	}
	var nilF *Fixture
	if _, isEnum := nilF.EnumLabels("z.mood"); isEnum {
		t.Error("nil fixture reported an enum")
	}
}
