package hydrate

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func typeFixture(typ string, nullable bool) *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.t": {
				Rows:    fixture.Fact[int64]{Value: 3, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"c": {Type: typ, Nullable: nullable}},
			},
		},
	}
}

func sqlFor(t *testing.T, f *fixture.Fixture) string {
	t.Helper()
	res, err := Generate(f, Options{Seed: 1, Scale: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var b strings.Builder
	if err := WriteSQL(&b, res); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestUnmodelledTypesAreNotFakedAsText is the regression for a bug in the same
// family as the uuid foreign-key one: a plausible-looking value of the WRONG
// type, which Postgres rejects on INSERT.
//
// Every type not in the classifier fell to "text", so an inet column hydrated as
// 'val_206071'. The load then failed with a Postgres error naming neither
// rowshape nor the column, and validate reported a tool error or a manufactured
// FAIL for a migration that was fine.
func TestUnmodelledTypesAreNotFakedAsText(t *testing.T) {
	// These now have real generators and must produce type-valid literals.
	cases := []struct{ typ, want string }{
		{"inet", "192.0.2."},
		{"cidr", "192.0.2."},
		{"macaddr", "08:00:2b:"},
		{"interval", "seconds"},
		{"xml", "<value>"},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			got := sqlFor(t, typeFixture(c.typ, false))
			if !strings.Contains(got, c.want) {
				t.Errorf("%s must hydrate to a type-valid literal containing %q, got:\n%s", c.typ, c.want, got)
			}
			if strings.Contains(got, "val_") {
				t.Errorf("%s is still being faked as text:\n%s", c.typ, got)
			}
		})
	}
}

// A column whose type has a strict literal grammar and no synthesis rule is
// REFUSED at generation time — nullable or not — naming the column, the type,
// and the ways forward, rather than emitting SQL that fails hundreds of thousands
// of rows later inside COPY with a pgx message naming neither.
//
// Nullability makes no difference here. Hydrating such a column as all-NULL would
// load, but it would hand validate a table whose column holds nothing, and a
// migration touching that column would be certified against data that does not
// resemble production — a silent wrong PASS in place of a loud refusal.
func TestUnsynthesizableTypeIsRefused(t *testing.T) {
	for _, nullable := range []bool{true, false} {
		_, err := Generate(typeFixture("tsvector", nullable), Options{Seed: 1, Scale: 1})
		if err == nil {
			t.Fatalf("nullable=%v: an unsynthesizable type must be refused, not faked", nullable)
		}
		for _, want := range []string{"t.c", "tsvector", "--target"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("nullable=%v: the refusal must mention %q, got: %v", nullable, want, err)
			}
		}
	}
}

// The modelled types must be unaffected.
func TestKnownTypesStillHydrate(t *testing.T) {
	for _, typ := range []string{"text", "bigint", "boolean", "timestamptz", "uuid", "jsonb", "numeric"} {
		if _, err := Generate(typeFixture(typ, false), Options{Seed: 1, Scale: 1}); err != nil {
			t.Errorf("%s must still hydrate, got %v", typ, err)
		}
	}
}
