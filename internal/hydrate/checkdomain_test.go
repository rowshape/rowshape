package hydrate

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func ptrBool(b bool) *bool { return &b }

func checkTable(cons ...fixture.Constraint) fixture.Table {
	return fixture.Table{
		Rows:        fixture.Fact[int64]{Value: 50},
		Columns:     map[string]fixture.Column{"status": {Type: "text"}, "qty": {Type: "integer"}},
		Constraints: cons,
	}
}

// TestCheckDomainsReadsMembership: `CHECK (status IN (...))`, which Postgres
// renders as `= ANY (ARRAY[...])`, is the shape most production CHECKs have. A
// column under one is an enum in all but name, and reading it is what lets the
// disposable database enforce the constraint instead of skipping it.
func TestCheckDomainsReadsMembership(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{"rendered ANY", `(status = ANY (ARRAY['pending'::text, 'paid'::text, 'shipped'::text]))`},
		{"no casts", `status = ANY (ARRAY['pending', 'paid', 'shipped'])`},
		{"cast column", `((status)::text = ANY (ARRAY['pending'::text, 'paid'::text, 'shipped'::text]))`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doms := checkDomains(checkTable(fixture.Constraint{
				Name: "c", Kind: "check", Expression: tc.expr,
			}))
			got := doms["status"].Values
			if strings.Join(got, ",") != "pending,paid,shipped" {
				t.Errorf("values = %v, want [pending paid shipped]", got)
			}
		})
	}
}

// TestCheckDomainsReadsBounds: a numeric comparison narrows the column's range.
// Strict comparisons are nudged inside the inclusive bound.
func TestCheckDomainsReadsBounds(t *testing.T) {
	doms := checkDomains(checkTable(
		fixture.Constraint{Name: "lo", Kind: "check", Expression: `(qty > 0)`},
		fixture.Constraint{Name: "hi", Kind: "check", Expression: `(qty <= 100)`},
	))
	d := doms["qty"]
	if d.Min == nil || *d.Min != 1 {
		t.Errorf("min = %v, want 1 (qty > 0 nudged inside an inclusive bound)", d.Min)
	}
	if d.Max == nil || *d.Max != 100 {
		t.Errorf("max = %v, want 100", d.Max)
	}
}

// TestCheckDomainsSplitsConjunction: one CHECK can carry both bounds.
func TestCheckDomainsSplitsConjunction(t *testing.T) {
	doms := checkDomains(checkTable(fixture.Constraint{
		Name: "c", Kind: "check", Expression: `((qty >= 5) AND (qty <= 9))`,
	}))
	d := doms["qty"]
	if d.Min == nil || *d.Min != 5 || d.Max == nil || *d.Max != 9 {
		t.Errorf("bounds = [%v, %v], want [5, 9]", d.Min, d.Max)
	}
}

// TestCheckDomainsIgnoresWhatItCannotRead is the load-bearing negative. This is
// not a SQL evaluator: anything it does not recognize with certainty must yield
// NOTHING, so generation is unchanged and the constraint fails inside its
// savepoint and is reported. A guess would constrain hydrated data in a way
// production does not.
func TestCheckDomainsIgnoresWhatItCannotRead(t *testing.T) {
	cases := []struct {
		name string
		con  fixture.Constraint
	}{
		{"opaque under strict", fixture.Constraint{Name: "c", Kind: "check", Expression: "opaque"}},
		{"multi-column", fixture.Constraint{Name: "c", Kind: "check", Expression: `(qty < other_col)`}},
		{"function call", fixture.Constraint{Name: "c", Kind: "check", Expression: `(char_length(status) > 2)`}},
		{"disjunction", fixture.Constraint{Name: "c", Kind: "check", Expression: `((qty > 5) OR (qty < 1))`}},
		{"equality pins cardinality", fixture.Constraint{Name: "c", Kind: "check", Expression: `(status = 'only')`}},
		{"not a check", fixture.Constraint{Name: "c", Kind: "unique", Columns: []string{"status"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if doms := checkDomains(checkTable(tc.con)); len(doms) != 0 {
				t.Errorf("expected no domain, got %+v", doms)
			}
		})
	}
}

// TestCheckDomainsIgnoresNotValid: a NOT VALID constraint is not enforced against
// existing rows, so production itself may hold values that violate it. Obeying it
// would make the target STRICTER than production — a wrong verdict of its own.
func TestCheckDomainsIgnoresNotValid(t *testing.T) {
	doms := checkDomains(checkTable(fixture.Constraint{
		Name: "c", Kind: "check", Expression: `(qty > 0)`, Validated: ptrBool(false),
	}))
	if len(doms) != 0 {
		t.Errorf("NOT VALID constraint constrained synthesis: %+v", doms)
	}
}

// TestGenerateSatisfiesCheck: end to end through the engine — the values it
// produces for a constrained column must be drawn from the constraint's set, or
// the ADD CONSTRAINT is skipped and the target stops enforcing what production does.
func TestGenerateSatisfiesCheck(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.orders": {
				Rows: fixture.Fact[int64]{Value: 200},
				Columns: map[string]fixture.Column{
					"status": {Type: "text", Distinct: &fixture.Fact[int64]{Value: 3}, Format: "enum_like"},
					"qty":    {Type: "integer", Distinct: &fixture.Fact[int64]{Value: 40}},
				},
				Constraints: []fixture.Constraint{
					{Name: "s", Kind: "check", Expression: `(status = ANY (ARRAY['pending'::text, 'paid'::text, 'shipped'::text]))`},
					{Name: "q", Kind: "check", Expression: `((qty >= 1) AND (qty <= 10))`},
				},
			},
		},
	}
	res, err := Generate(f, Options{Seed: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	gt := res.Tables[0]
	statusAt, qtyAt := columnIndex(t, gt.Columns, "status"), columnIndex(t, gt.Columns, "qty")
	allowed := map[string]bool{"pending": true, "paid": true, "shipped": true}

	seen := map[string]bool{}
	for _, row := range gt.Rows {
		s, ok := row[statusAt].(string)
		if !ok || !allowed[s] {
			t.Fatalf("synthesized status %v violates the CHECK; the target would not enforce it", row[statusAt])
		}
		seen[s] = true
		q, ok := row[qtyAt].(int64)
		if !ok || q < 1 || q > 10 {
			t.Fatalf("synthesized qty %v is outside CHECK bounds [1, 10]", row[qtyAt])
		}
	}
	// Drawing from the set must not collapse the column to one value: the recorded
	// cardinality is a fact hydration is supposed to reproduce.
	if len(seen) < 2 {
		t.Errorf("constrained column collapsed to %d distinct value(s): %v", len(seen), seen)
	}
}

// TestCheckDomainKeepsNullFraction: a CHECK is satisfied by NULL (it evaluates to
// unknown, which does not fail), and the null fraction is a recorded fact that
// constraining values must not disturb.
func TestCheckDomainKeepsNullFraction(t *testing.T) {
	nf := 0.5
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"app.t": {
				Rows: fixture.Fact[int64]{Value: 200},
				Columns: map[string]fixture.Column{
					"status": {Type: "text", Nullable: true,
						NullFraction: &fixture.Fact[float64]{Value: nf},
						Distinct:     &fixture.Fact[int64]{Value: 2}},
				},
				Constraints: []fixture.Constraint{
					{Name: "s", Kind: "check", Expression: `(status = ANY (ARRAY['a'::text, 'b'::text]))`},
				},
			},
		},
	}
	res, err := Generate(f, Options{Seed: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	gt := res.Tables[0]
	at := columnIndex(t, gt.Columns, "status")
	nulls := 0
	for _, row := range gt.Rows {
		if row[at] == nil {
			nulls++
		}
	}
	got := float64(nulls) / float64(len(gt.Rows))
	if got < nf-0.05 || got > nf+0.05 {
		t.Errorf("null fraction = %.3f, want ~%.2f — constraining values disturbed it", got, nf)
	}
}

func columnIndex(t *testing.T, cols []string, name string) int {
	t.Helper()
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	t.Fatalf("column %q not generated (have %v)", name, cols)
	return -1
}
