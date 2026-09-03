package target

import (
	"context"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/hydrate"
)

// TestLoadRoundTripsUserAndExoticTypes drives the DDL and the synthesis engine
// against a REAL Postgres, over the type zoo that broke them.
//
// Every bug this covers was invisible to unit tests and to the hand-written corpus,
// and surfaced only when a fixture from an actual `pull` met an actual server:
//
//   - a column typed with an enum or domain: `type "…" does not exist`
//   - a domain over integer: `unable to encode "val_14" into binary format for int4`
//   - an inet column: `unable to encode "val_141" into binary format for inet`
//   - a char(3) enum_like column: `value too long for type character(3)`
//   - a unique key whose sampled range is narrower than its row count:
//     `duplicate key value violates unique constraint`
//
// Asserting the load SUCCEEDS is the whole point: each failure above aborted it.
func TestLoadRoundTripsUserAndExoticTypes(t *testing.T) {
	dsn := adminDSN(t)
	ctx := context.Background()

	const rows = 2000
	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Types: map[string]fixture.UserType{
			"zoo.mood":         {Kind: "enum", Labels: []string{"sad", "ok", "happy"}, LabelCount: 3},
			"zoo.redacted":     {Kind: "enum", LabelCount: 2}, // privacy:strict shape
			"zoo.positive_int": {Kind: "domain", Base: "integer", Check: "(VALUE > 0)"},
		},
		Tables: map[string]fixture.Table{
			"zoo.wide": {
				Rows: fixture.Fact[int64]{Value: rows, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					// A unique key whose declared range is too narrow for the row
					// count — exactly what pull emits from pg_stats histogram bounds.
					"id": {
						Type:     "bigint",
						Nullable: false,
						Unique:   &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"},
						Range:    &fixture.Range{Min: int64(31), Max: int64(1900)},
					},
					"m":        {Type: "zoo.mood", Nullable: false},
					"secret":   {Type: "zoo.redacted", Nullable: false},
					"pos":      {Type: "zoo.positive_int", Nullable: false, Range: &fixture.Range{Min: int64(1), Max: int64(50)}},
					"currency": {Type: "character(3)", Nullable: false, Format: "enum_like"},
					"country":  {Type: "character(2)", Nullable: false, Format: "enum_like"},
					"ip":       {Type: "inet", Nullable: true},
					"net":      {Type: "cidr", Nullable: true},
					"mac":      {Type: "macaddr", Nullable: true},
					"mac8":     {Type: "macaddr8", Nullable: true},
					"span":     {Type: "interval", Nullable: true},
					"tags":     {Type: "text[]", Nullable: false},
					"nums":     {Type: "integer[]", Nullable: false},
					"doc":      {Type: "xml", Nullable: true},
					"bits":     {Type: "bit(4)", Nullable: true},
					"loc":      {Type: "point", Nullable: true},
					"payload":  {Type: "jsonb", Nullable: false},
					"cost":     {Type: "money", Nullable: false, Range: &fixture.Range{Min: int64(0), Max: int64(9999)}},
					"uid":      {Type: "uuid", Nullable: false},
				},
				Constraints: []fixture.Constraint{
					{Name: "wide_pkey", Kind: "primary_key", Columns: []string{"id"}},
				},
			},
		},
	}

	eph, err := NewEphemeral(ctx, dsn)
	if err != nil {
		t.Fatalf("create disposable database: %v", err)
	}
	defer func() { _ = eph.Close(ctx) }()

	report, err := Load(ctx, eph, f, hydrate.Options{Seed: 42})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := report.Tables["zoo.wide"]; got != rows {
		t.Errorf("loaded %d rows, want %d", got, rows)
	}

	conn, err := eph.Connect(ctx)
	if err != nil {
		t.Fatalf("connect to disposable database: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// The enum column holds real labels, and the server enforced membership on the
	// way in — so this also proves the DDL created the type with these labels.
	var distinctMoods int
	if err := conn.QueryRow(ctx, `SELECT count(DISTINCT m) FROM zoo.wide`).Scan(&distinctMoods); err != nil {
		t.Fatalf("count moods: %v", err)
	}
	if distinctMoods != 3 {
		t.Errorf("enum column has %d distinct labels, want 3", distinctMoods)
	}

	// The domain's CHECK was enforced by the server for every row. Generating from
	// the range production reported is what keeps it satisfied.
	var minPos int
	if err := conn.QueryRow(ctx, `SELECT min(pos) FROM zoo.wide`).Scan(&minPos); err != nil {
		t.Fatalf("min pos: %v", err)
	}
	if minPos < 1 {
		t.Errorf("min(pos) = %d, which CHECK (VALUE > 0) should have rejected", minPos)
	}

	// The unique key really is unique despite the too-narrow declared range.
	var ids, distinctIDs int
	if err := conn.QueryRow(ctx, `SELECT count(*), count(DISTINCT id) FROM zoo.wide`).Scan(&ids, &distinctIDs); err != nil {
		t.Fatalf("count ids: %v", err)
	}
	if ids != distinctIDs {
		t.Errorf("%d rows but only %d distinct ids", ids, distinctIDs)
	}
}

// TestLoadRefusesTypeItCannotSynthesize: a type with no synthesis rule is refused
// before anything is written, naming the column. The failure it replaces was a pgx
// encode error from deep inside COPY that named neither table nor column.
func TestLoadRefusesTypeItCannotSynthesize(t *testing.T) {
	dsn := adminDSN(t)
	ctx := context.Background()

	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.docs": {
				Rows:    fixture.Fact[int64]{Value: 5, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"body": {Type: "tsvector"}},
			},
		},
	}

	eph, err := NewEphemeral(ctx, dsn)
	if err != nil {
		t.Fatalf("create disposable database: %v", err)
	}
	defer func() { _ = eph.Close(ctx) }()

	_, err = Load(ctx, eph, f, hydrate.Options{Seed: 1})
	if err == nil {
		t.Fatal("Load succeeded for a tsvector column; want a named refusal")
	}
	for _, want := range []string{"public.docs", "body", "tsvector"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
