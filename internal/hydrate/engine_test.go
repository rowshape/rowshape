package hydrate

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// oneTable builds a single-table fixture with the given columns and declared row
// count, for engine tests.
func oneTable(name string, rows int64, cols map[string]fixture.Column) *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			name: {Rows: fixture.Fact[int64]{Value: rows, Confidence: fixture.Exact}, Columns: cols},
		},
	}
}

func nullableText(nullFrac float64, distinct int64, format string) fixture.Column {
	return fixture.Column{
		Type:         "text",
		Nullable:     true,
		NullFraction: &fixture.Fact[float64]{Value: nullFrac, Confidence: fixture.Estimated},
		Distinct:     &fixture.Fact[int64]{Value: distinct, Confidence: fixture.Estimated},
		Format:       format,
	}
}

func column(rows int64) *fixture.Fixture {
	return oneTable("public.users", rows, map[string]fixture.Column{
		"id":    {Type: "bigint", Nullable: false, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"}, Generated: "identity"},
		"email": nullableText(0.1, 900, "email"),
	})
}

// TestDeterminism: the same seed reproduces identical output; a different seed
// produces different output (RFC §10).
func TestDeterminism(t *testing.T) {
	f := column(500)
	a, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("same seed produced different output")
	}
	c, err := Generate(f, Options{Seed: 7})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if reflect.DeepEqual(a, c) {
		t.Errorf("different seeds produced identical output")
	}
}

// TestScalePrefixStability: increasing --scale only appends rows; the retained
// prefix of an independent column's values is byte-stable (RFC §10).
func TestScalePrefixStability(t *testing.T) {
	f := column(1000)
	small, err := Generate(f, Options{Seed: 42, Scale: 0.1}) // 100 rows
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	big, err := Generate(f, Options{Seed: 42, Scale: 0.5}) // 500 rows
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	st := small.Tables[0]
	bt := big.Tables[0]
	if len(bt.Rows) <= len(st.Rows) {
		t.Fatalf("expected more rows at larger scale: %d vs %d", len(bt.Rows), len(st.Rows))
	}
	for r := range st.Rows {
		if !reflect.DeepEqual(st.Rows[r], bt.Rows[r]) {
			t.Fatalf("prefix row %d changed with scale: %v vs %v", r, st.Rows[r], bt.Rows[r])
		}
	}
}

// TestNullFractionTolerance: the realized null fraction is within ±0.5% of the
// declared value (RFC §13).
func TestNullFractionTolerance(t *testing.T) {
	for _, p := range []float64{0.0, 0.03, 0.1, 0.5} {
		f := oneTable("public.t", 5000, map[string]fixture.Column{
			"c": nullableText(p, 1000, "free_text"),
		})
		res, err := Generate(f, Options{Seed: 42})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		gt := res.Tables[0]
		nulls := 0
		for _, row := range gt.Rows {
			if row[0] == nil {
				nulls++
			}
		}
		got := float64(nulls) / float64(len(gt.Rows))
		if diff := got - p; diff > 0.005 || diff < -0.005 {
			t.Errorf("null_fraction %.3f: realized %.4f, outside ±0.5%%", p, got)
		}
	}
}

// TestUniqueHonored: a column marked unique has all-distinct values (RFC §13).
func TestUniqueHonored(t *testing.T) {
	f := oneTable("public.t", 2000, map[string]fixture.Column{
		"email": {
			Type:     "text",
			Nullable: false,
			Unique:   &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"},
			Format:   "email",
		},
	})
	res, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	gt := res.Tables[0]
	seen := map[any]bool{}
	for _, row := range gt.Rows {
		if seen[row[0]] {
			t.Fatalf("duplicate value in unique column: %v", row[0])
		}
		seen[row[0]] = true
	}
	if len(seen) != len(gt.Rows) {
		t.Errorf("unique column has %d distinct of %d rows", len(seen), len(gt.Rows))
	}
}

// TestUniqueNumericOutlivesItsRange: a unique NUMERIC column stays unique even
// when its declared range is too narrow to hold one value per row.
//
// This is not a hypothetical fixture. `pull` derives `range` from pg_stats
// histogram bounds, which are a SAMPLE — for a 150k-row bigserial primary key it
// reported {min: 1633, max: 149328} where the true extent is 1..150000. The
// column therefore declares an EXACT unique constraint (via: constraint) and a
// range spanning 147,696 values, and 150,000 rows cannot fit in 147,696 slots.
//
// numericInRange wrapped with `min + (n % span)`, so ordinals past the span
// restarted at min and collided. Hydrating a fixture that `pull` had just emitted
// died with `duplicate key value violates unique constraint "order_items_pkey"`
// — the round trip that is the tool's core promise, broken on an ordinary table.
//
// The precedence is what fixes it: `unique` is an EXACT fact read off a
// constraint, while `range` is an approximation from a sample. When the two
// cannot both hold, the exact one wins and the approximate bound is overshot.
func TestUniqueNumericOutlivesItsRange(t *testing.T) {
	const rows = 150000
	f := oneTable("public.order_items", rows, map[string]fixture.Column{
		"id": {
			Type:     "bigint",
			Nullable: false,
			Unique:   &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"},
			Distinct: &fixture.Fact[int64]{Value: rows, Confidence: fixture.Exact},
			// Narrower than the row count — exactly what pull emitted.
			Range: &fixture.Range{Min: int64(1633), Max: int64(149328)},
		},
	})
	res, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	gt := res.Tables[0]
	if len(gt.Rows) != rows {
		t.Fatalf("generated %d rows, want %d", len(gt.Rows), rows)
	}
	seen := make(map[int64]bool, rows)
	for i, row := range gt.Rows {
		v, ok := row[0].(int64)
		if !ok {
			t.Fatalf("row %d: unique bigint cell is %T, want int64", i, row[0])
		}
		if seen[v] {
			t.Fatalf("row %d: duplicate value %d in unique column — the fixture's "+
				"exact unique constraint was violated to honor an approximate range", i, v)
		}
		seen[v] = true
	}
}

// TestUniqueNumericStaysInRangeWhenItFits: the fix above must not move any value
// that was already correct. Whenever the declared span is wide enough for the row
// count, a unique numeric column is generated exactly as before — min + ordinal,
// entirely inside [min, max]. This pins that the collision fix is confined to the
// unsatisfiable case and does not perturb existing fixtures (INV-DETERMINISM).
func TestUniqueNumericStaysInRangeWhenItFits(t *testing.T) {
	const rows = 5000
	const lo, hi = int64(1), int64(20000)
	f := oneTable("public.users", rows, map[string]fixture.Column{
		"id": {
			Type:     "bigint",
			Nullable: false,
			Unique:   &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"},
			Range:    &fixture.Range{Min: lo, Max: hi},
		},
	})
	res, err := Generate(f, Options{Seed: 7})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, row := range res.Tables[0].Rows {
		v := row[0].(int64)
		if v != lo+int64(i) {
			t.Fatalf("row %d: got %d, want %d (min + ordinal)", i, v, lo+int64(i))
		}
		if v < lo || v > hi {
			t.Fatalf("row %d: value %d escaped the declared range [%d, %d] even "+
				"though the span holds %d rows", i, v, lo, hi, rows)
		}
	}
}

// TestGeneratedStringsFitTheirColumn: no synthesized string exceeds its column's
// character limit.
//
// A format hint renders at its own natural length: `enum_like` with no declared
// values produces "value_0" (7 chars). `pull` describes a Postgres char(3)
// currency column exactly that way, and COPYing "value_0" into it aborted
// hydration with `value too long for type character(3)` — so a plain two-value
// currency column broke the pull -> hydrate round trip.
//
// Every bounded character type is covered here, including bare `char` (which is
// character(1), the tightest type in Postgres despite having no modifier) and the
// unbounded types, which must be left alone.
func TestGeneratedStringsFitTheirColumn(t *testing.T) {
	cases := []struct {
		name    string
		colType string
		format  string
		width   int // 0 = unbounded, no clamp expected
	}{
		{"char(3) enum_like", "character(3)", "enum_like", 3},
		{"char(2) enum_like", "character(2)", "enum_like", 2},
		{"bare char is char(1)", "char", "enum_like", 1},
		{"varchar(4) free_text", "character varying(4)", "free_text", 4},
		{"varchar(8) email", "varchar(8)", "email", 8},
		{"varchar(5) slug", "varchar(5)", "slug", 5},
		{"varchar(6) url", "varchar(6)", "url", 6},
		{"text is unbounded", "text", "free_text", 0},
		{"bare varchar is unbounded", "character varying", "free_text", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := oneTable("public.t", 200, map[string]fixture.Column{
				"c": {
					Type:     tc.colType,
					Nullable: false,
					Distinct: &fixture.Fact[int64]{Value: 5, Confidence: fixture.Estimated},
					Format:   tc.format,
				},
			})
			res, err := Generate(f, Options{Seed: 3})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			for i, row := range res.Tables[0].Rows {
				s, ok := row[0].(string)
				if !ok {
					t.Fatalf("row %d: got %T, want string", i, row[0])
				}
				if tc.width > 0 && len(s) > tc.width {
					t.Fatalf("row %d: %q is %d chars, exceeds %s — Postgres rejects "+
						"this with `value too long`", i, s, len(s), tc.colType)
				}
				if tc.width == 0 && len(s) == 0 {
					t.Fatalf("row %d: unbounded %s should keep its rendered value, got empty",
						i, tc.colType)
				}
			}
		})
	}
}

// TestNarrowUniqueColumnStaysUnique: clamping to the column width must not be
// allowed to collapse a unique column into duplicates. Truncating the rendered
// string would do exactly that ("value_0" and "value_1" both become "val"), which
// is why the clamp re-encodes the ordinal in base 36 instead — injective while
// 36^width values remain.
func TestNarrowUniqueColumnStaysUnique(t *testing.T) {
	const rows = 1000 // < 36^3, so char(3) can hold them all
	f := oneTable("public.t", rows, map[string]fixture.Column{
		"code": {
			Type:     "character(3)",
			Nullable: false,
			Unique:   &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"},
			Format:   "enum_like",
		},
	})
	res, err := Generate(f, Options{Seed: 11})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seen := make(map[string]bool, rows)
	for i, row := range res.Tables[0].Rows {
		s := row[0].(string)
		if len(s) > 3 {
			t.Fatalf("row %d: %q exceeds character(3)", i, s)
		}
		if seen[s] {
			t.Fatalf("row %d: duplicate %q — the width clamp collapsed a unique column", i, s)
		}
		seen[s] = true
	}
}

// TestObviouslyFake: generated values are obviously synthetic — content realism
// is explicitly not attempted (RFC §13).
func TestObviouslyFake(t *testing.T) {
	f := oneTable("public.t", 50, map[string]fixture.Column{
		"email": {Type: "text", Nullable: false, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Format: "email"},
	})
	res, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, row := range res.Tables[0].Rows {
		s, _ := row[0].(string)
		if !strings.HasSuffix(s, "@example.invalid") || !strings.HasPrefix(s, "user_") {
			t.Errorf("email %q is not obviously fake (want user_N@example.invalid)", s)
		}
	}
}

// TestFanoutShape: a foreign key reproduces the fan-out DISTRIBUTION shape
// (p50/p95/max children per parent), not just the mean (RFC §6.6).
func TestFanoutShape(t *testing.T) {
	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id": {Type: "bigint", Nullable: false, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Generated: "identity"},
				},
			},
			"public.orders": {
				Rows: fixture.Fact[int64]{Value: 8000, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id":      {Type: "bigint", Nullable: false, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Generated: "identity"},
					"user_id": {Type: "bigint", Nullable: false},
				},
				References: []fixture.Reference{
					{Column: "user_id", To: "public.users.id", OnDelete: "cascade",
						Fanout: &fixture.Fanout{Mean: 8, P50: 3, P95: 41, Max: 200, Confidence: fixture.Measured}},
				},
			},
		},
	}
	res, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Find orders and its user_id column.
	var orders GeneratedTable
	for _, t := range res.Tables {
		if t.Name == "public.orders" {
			orders = t
		}
	}
	fkCol := indexOf(orders.Columns, "user_id")
	perParent := map[int64]int64{}
	for _, row := range orders.Rows {
		pid := row[fkCol].(int64)
		perParent[pid]++
	}
	// Include parents with zero children (1000 parents, ids 1..1000).
	counts := make([]int64, 0, 1000)
	for pid := int64(1); pid <= 1000; pid++ {
		counts = append(counts, perParent[pid])
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })

	p50 := counts[len(counts)*50/100]
	p95 := counts[len(counts)*95/100]
	max := counts[len(counts)-1]
	t.Logf("fan-out: p50=%d p95=%d max=%d (target 3/41/200)", p50, p95, max)

	// The distribution must be heavy-tailed, not uniform: p95 far above p50 and a
	// long max. Exact quantiles need not match, but the SHAPE must.
	if !(p95 > p50*3) {
		t.Errorf("fan-out not heavy-tailed: p95=%d should be >> p50=%d", p95, p50)
	}
	if !(max > p95*2) {
		t.Errorf("fan-out lacks a long tail: max=%d should be >> p95=%d", max, p95)
	}
	// And the quantiles should be in the right ballpark of the target.
	if p95 < 20 || p95 > 80 {
		t.Errorf("p95=%d far from target 41", p95)
	}
	if max < 100 {
		t.Errorf("max=%d far below target 200", max)
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// TestForeignKeysPointAtRealParents: hydrate must not invent orphans.
//
// A real `pull` emits a range for every numeric column (RFC §6.2), so a real
// fixture's id column has one and numericInRange generates `min + (ordinal %
// span)` — for range {1, N} that is 1..N, not 0..N-1. parentIDValue used to
// return the ordinal, so children referenced 0..N-1 while parents were 1..N, and
// user_id=0 pointed at nothing.
//
// Both fixtures declare orphan_fraction {0, exact, via: constraint} — the FK is
// PROVEN to have no orphans. Hydrate inventing one means a migration adding
// `FOREIGN KEY (user_id) REFERENCES users(id)` fails on hydrated data and
// rowshape reports a FAIL it manufactured itself.
//
// The no-fan-out case is the one that pins it. With a fan-out fact the rescale in
// scaledTargetCounts can zero out the low quantiles, so parent ordinal 0 is never
// handed out and the off-by-one hides; the uniform spread (out[i] = i % parentN)
// always uses ordinal 0. An earlier version of this test only had the fan-out
// case and passed against the bug.
func TestForeignKeysPointAtRealParents(t *testing.T) {
	build := func(fo *fixture.Fanout) *fixture.Fixture {
		return &fixture.Fixture{
			Tables: map[string]fixture.Table{
				"public.users": {
					Rows: fixture.Fact[int64]{Value: 100},
					Columns: map[string]fixture.Column{
						// The shape a real pull emits: unique, and WITH a range.
						"id": {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact},
							Range: &fixture.Range{Min: 1, Max: 100}},
					},
				},
				"public.orders": {
					Rows: fixture.Fact[int64]{Value: 400},
					Columns: map[string]fixture.Column{
						"id":      {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Range: &fixture.Range{Min: 1, Max: 400}},
						"user_id": {Type: "bigint"},
					},
					References: []fixture.Reference{{
						Column: "user_id", To: "public.users.id",
						Fanout:         fo,
						OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: fixture.Exact},
					}},
				},
			},
		}
	}

	cases := []struct {
		name string
		fo   *fixture.Fanout
	}{
		{"uniform spread (no fan-out fact)", nil},
		{"with a fan-out distribution", &fixture.Fanout{Mean: 4, P50: 2, P95: 8, Max: 40, Confidence: fixture.Measured}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Generate(build(tc.fo), Options{Seed: 42, Scale: 1})
			if err != nil {
				t.Fatal(err)
			}

			parentIDs := map[int64]bool{}
			var childFKs []int64
			for _, tb := range res.Tables {
				idx := map[string]int{}
				for i, c := range tb.Columns {
					idx[c] = i
				}
				for _, row := range tb.Rows {
					switch tb.Name {
					case "public.users":
						parentIDs[row[idx["id"]].(int64)] = true
					case "public.orders":
						childFKs = append(childFKs, row[idx["user_id"]].(int64))
					}
				}
			}
			if len(parentIDs) == 0 || len(childFKs) == 0 {
				t.Fatal("nothing generated; this case proves nothing")
			}

			var orphans []int64
			for _, fk := range childFKs {
				if !parentIDs[fk] {
					orphans = append(orphans, fk)
				}
			}
			if len(orphans) > 0 {
				t.Errorf("%d of %d foreign keys reference a parent that does not exist (e.g. %v) — the "+
					"fixture declares orphan_fraction 0 (exact), so hydrate invented the exact condition it "+
					"proves absent. `ADD FOREIGN KEY` would fail on this data and rowshape would report a "+
					"FAIL it manufactured.", len(orphans), len(childFKs), orphans[:minInt(3, len(orphans))])
			}
		})
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestForeignKeysMatchNonIntegerParentKeys: a FK to a uuid/text primary key — the
// modern default (Prisma, Drizzle, Rails-uuid) — must hydrate as the SAME string
// the parent generated, not an int64 ordinal.
//
// parentIDValue used to return int64 unconditionally (it called numericInRange,
// which returns the bare ordinal when a column has no numeric range — every uuid
// column). The child cell then carried an int where the column is a uuid, and the
// binary COPY in target.Load could not encode it: hydration aborted with a tool
// error for EVERY non-integer-keyed schema that has a foreign key. Generate itself
// does not COPY, so the type mismatch surfaces here as an int64 cell in a uuid
// column — which is exactly what would fail to encode.
func TestForeignKeysMatchNonIntegerParentKeys(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"public.accounts": {
				Rows: fixture.Fact[int64]{Value: 50},
				Columns: map[string]fixture.Column{
					"id": {Type: "uuid", Nullable: false, Format: "uuid",
						Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"}},
				},
			},
			"public.sessions": {
				Rows: fixture.Fact[int64]{Value: 200},
				Columns: map[string]fixture.Column{
					"id": {Type: "uuid", Nullable: false, Format: "uuid",
						Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"}},
					"account_id": {Type: "uuid", Nullable: false, Format: "uuid"},
				},
				References: []fixture.Reference{{
					Column: "account_id", To: "public.accounts.id",
					OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: fixture.Exact},
				}},
			},
		},
	}

	res, err := Generate(f, Options{Seed: 42, Scale: 1})
	if err != nil {
		t.Fatal(err)
	}

	parentIDs := map[string]bool{}
	var childFKs []any
	for _, tb := range res.Tables {
		idx := map[string]int{}
		for i, c := range tb.Columns {
			idx[c] = i
		}
		for _, row := range tb.Rows {
			switch tb.Name {
			case "public.accounts":
				id, ok := row[idx["id"]].(string)
				if !ok {
					t.Fatalf("parent accounts.id is %T, want string (uuid)", row[idx["id"]])
				}
				parentIDs[id] = true
			case "public.sessions":
				childFKs = append(childFKs, row[idx["account_id"]])
			}
		}
	}
	if len(parentIDs) == 0 || len(childFKs) == 0 {
		t.Fatal("nothing generated; this case proves nothing")
	}

	for i, fk := range childFKs {
		s, ok := fk.(string)
		if !ok {
			t.Fatalf("sessions.account_id[%d] is %T (%v), want string matching a uuid parent id — "+
				"an int here is what fails to COPY-encode into a uuid column and aborts hydration", i, fk, fk)
		}
		if !parentIDs[s] {
			t.Errorf("sessions.account_id[%d] = %q references no account (orphan_fraction is 0/exact)", i, s)
		}
	}
}

// TestFanoutHeavyTailIsReproduced pins the moat field at PRODUCTION skew.
//
// Nothing covered this. The Week-6 gate's fixture is a 40x tail (max 800 / mean
// 20) and the model coped; real production skew is 1942x — one whale owning 40%
// of the rows — and there the old model collapsed: declared p50 2 / p95 4 / max
// 7942 hydrated as p50 0 / p95 0 / max 1244, with 95% of parents childless. Only
// the mean survived, which is exactly what P1-T7 says is not enough, on the field
// PRD §6.6 calls the one no other tool captures.
//
// Heavy tails are the pathology rowshape exists to catch, so the model has to be
// right precisely where it was weakest.
func TestFanoutHeavyTailIsReproduced(t *testing.T) {
	// The shape of the real pulled fixture: 5k parents, 20k children, one whale.
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 5000},
				Columns: map[string]fixture.Column{
					"id": {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact},
						Range: &fixture.Range{Min: 1, Max: 5000}},
				},
			},
			"public.orders": {
				Rows: fixture.Fact[int64]{Value: 20000},
				Columns: map[string]fixture.Column{
					"id":      {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Range: &fixture.Range{Min: 1, Max: 20000}},
					"user_id": {Type: "bigint"},
				},
				References: []fixture.Reference{{
					Column: "user_id", To: "public.users.id",
					Fanout:         &fixture.Fanout{Mean: 4.09, P50: 2, P95: 4, Max: 7942, Confidence: fixture.Measured},
					OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: fixture.Exact},
				}},
			},
		},
	}

	res, err := Generate(f, Options{Seed: 42, Scale: 1})
	if err != nil {
		t.Fatal(err)
	}

	counts := map[int64]int64{}
	var children int64
	var parents int
	for _, tb := range res.Tables {
		idx := map[string]int{}
		for i, c := range tb.Columns {
			idx[c] = i
		}
		switch tb.Name {
		case "public.users":
			parents = len(tb.Rows)
		case "public.orders":
			for _, row := range tb.Rows {
				counts[row[idx["user_id"]].(int64)]++
				children++
			}
		}
	}

	// The population the fixture's facts describe is parents WITH children: pull
	// measures them with GROUP BY on the foreign key, so childless parents are not
	// in it. Measuring the wrong population is how the Week-6 gate stayed blind.
	var nonEmpty []int64
	for _, c := range counts {
		nonEmpty = append(nonEmpty, c)
	}
	sort.Slice(nonEmpty, func(i, j int) bool { return nonEmpty[i] < nonEmpty[j] })
	if len(nonEmpty) == 0 {
		t.Fatal("no children assigned")
	}
	pct := func(p float64) int64 { return nonEmpty[int(p*float64(len(nonEmpty)-1))] }
	mean := float64(children) / float64(len(nonEmpty))
	p50, p95, mx := pct(0.50), pct(0.95), nonEmpty[len(nonEmpty)-1]

	t.Logf("declared: mean 4.09 p50 2 p95 4 max 7942 over ~4894 non-empty of 5000")
	t.Logf("hydrated: mean %.2f p50 %d p95 %d max %d over %d non-empty of %d",
		mean, p50, p95, mx, len(nonEmpty), parents)

	// The mean alone is the trap: a hydration that dumps every child onto two
	// parents reproduces it exactly and reproduces nothing else.
	if p50 != 2 {
		t.Errorf("p50 = %d, want 2 — the median parent's fan-out is the shape claim a mean cannot make", p50)
	}
	if p95 != 4 {
		t.Errorf("p95 = %d, want 4", p95)
	}
	if mx != 7942 {
		t.Errorf("max = %d, want 7942 — the whale IS the cascade-delete pathology (PRD §6.6)", mx)
	}
	if math.Abs(mean-4.09) > 0.1 {
		t.Errorf("mean = %.2f, want ~4.09", mean)
	}
	// mean = children/nonEmpty, so the count of parents that get anything is
	// implied by the facts: 20000/4.09 ~= 4890. Collapsing onto a handful leaves
	// the rest childless and is the failure this test exists for.
	if len(nonEmpty) < 4800 || len(nonEmpty) > 4950 {
		t.Errorf("non-empty parents = %d, want ~4894 (childN/mean) — %d of %d parents got nothing",
			len(nonEmpty), parents-len(nonEmpty), parents)
	}
}

// TestOrphanFractionIsReproduced: P1-T7's clause "satisfy declared constraints
// UNLESS orphan_fraction>0 demands otherwise" was unimplemented — hydrate never
// read the field. Measured on the corpus's own validate_orphans fixture: it
// declared orphan_fraction 0.004 (exact, via scan) and hydrated 800,000 children
// with ZERO orphans, so `ALTER TABLE ... VALIDATE CONSTRAINT` would have SUCCEEDED
// on the very case that exists for "the VALIDATE that trips on pre-existing
// orphans" (PRD §12). The FAIL was right, but read off the fixture rather than
// produced by running anything.
//
// This does not fight the fan-out, which is why it is possible at all: `pull`
// measures fan-out with GROUP BY on the foreign key, so its "parents" are FK
// VALUES — an orphan group is a value with no match. Group sizes are untouched;
// only how many real parents end up childless changes, which the facts imply.
func TestOrphanFractionIsReproduced(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 10000},
				Columns: map[string]fixture.Column{
					"id": {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact},
						Range: &fixture.Range{Min: 1, Max: 10000}},
				},
			},
			"public.orders": {
				Rows: fixture.Fact[int64]{Value: 80000},
				Columns: map[string]fixture.Column{
					"id":      {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Range: &fixture.Range{Min: 1, Max: 80000}},
					"user_id": {Type: "bigint"},
				},
				References: []fixture.Reference{{
					Column: "user_id", To: "public.users.id",
					Fanout:         &fixture.Fanout{Mean: 8, P50: 3, P95: 40, Max: 900, Confidence: fixture.Measured},
					OrphanFraction: &fixture.Fact[float64]{Value: 0.004, Confidence: fixture.Exact},
				}},
			},
		},
	}

	res, err := Generate(f, Options{Seed: 1, Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	parents := map[int64]bool{}
	var fks []int64
	for _, tb := range res.Tables {
		idx := map[string]int{}
		for i, c := range tb.Columns {
			idx[c] = i
		}
		for _, row := range tb.Rows {
			switch tb.Name {
			case "public.users":
				parents[row[idx["id"]].(int64)] = true
			case "public.orders":
				fks = append(fks, row[idx["user_id"]].(int64))
			}
		}
	}
	if len(fks) == 0 {
		t.Fatal("no children generated")
	}

	var orphans int
	for _, v := range fks {
		if !parents[v] {
			orphans++
		}
	}
	got := float64(orphans) / float64(len(fks))
	t.Logf("declared orphan_fraction 0.004 -> hydrated %.4f (%d of %d)", got, orphans, len(fks))

	if orphans == 0 {
		t.Fatal("no orphans hydrated: a fixture declaring orphan_fraction > 0 describes rows that " +
			"already violate the FK, and VALIDATE CONSTRAINT must fail on this data rather than succeed")
	}
	// Within half a percent of the declared fraction — the count is quantised by
	// whole groups, so it cannot be exact for every input.
	if diff := got - 0.004; diff > 0.005 || diff < -0.005 {
		t.Errorf("hydrated orphan_fraction = %.4f, want ~0.004", got)
	}
}

// TestNoOrphansWhenFractionIsZero: the other direction, and the one that matters
// more. A fixture proving orphan_fraction 0 (exact, via constraint) must hydrate
// clean — inventing an orphan there makes `ADD FOREIGN KEY` fail on hydrated data
// and rowshape report a FAIL it manufactured.
func TestNoOrphansWhenFractionIsZero(t *testing.T) {
	f := &fixture.Fixture{
		Tables: map[string]fixture.Table{
			"public.users": {
				Rows: fixture.Fact[int64]{Value: 500},
				Columns: map[string]fixture.Column{
					"id": {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact},
						Range: &fixture.Range{Min: 1, Max: 500}},
				},
			},
			"public.orders": {
				Rows: fixture.Fact[int64]{Value: 2000},
				Columns: map[string]fixture.Column{
					"id":      {Type: "bigint", Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}, Range: &fixture.Range{Min: 1, Max: 2000}},
					"user_id": {Type: "bigint"},
				},
				References: []fixture.Reference{{
					Column: "user_id", To: "public.users.id",
					Fanout:         &fixture.Fanout{Mean: 4, P50: 2, P95: 8, Max: 40, Confidence: fixture.Measured},
					OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: fixture.Exact},
				}},
			},
		},
	}
	res, err := Generate(f, Options{Seed: 1, Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	parents := map[int64]bool{}
	var orphans int
	for _, tb := range res.Tables {
		idx := map[string]int{}
		for i, c := range tb.Columns {
			idx[c] = i
		}
		if tb.Name == "public.users" {
			for _, row := range tb.Rows {
				parents[row[idx["id"]].(int64)] = true
			}
		}
	}
	for _, tb := range res.Tables {
		if tb.Name != "public.orders" {
			continue
		}
		idx := 0
		for i, c := range tb.Columns {
			if c == "user_id" {
				idx = i
			}
		}
		for _, row := range tb.Rows {
			if !parents[row[idx].(int64)] {
				orphans++
			}
		}
	}
	if orphans != 0 {
		t.Errorf("%d orphans hydrated against a fixture proving orphan_fraction 0 (exact)", orphans)
	}
}

// withTypes attaches user-defined type definitions to a fixture built by oneTable.
func withTypes(f *fixture.Fixture, types map[string]fixture.UserType) *fixture.Fixture {
	f.Types = types
	return f
}

// TestEnumColumnDrawsItsOwnLabels: an enum column accepts ONLY the type's labels,
// so its values must come from the type definition — not from a format hint and not
// from the generic type-name fallback, either of which produces a string that is
// not a member of the type and is rejected on the way in.
func TestEnumColumnDrawsItsOwnLabels(t *testing.T) {
	labels := []string{"sad", "ok", "happy"}
	f := withTypes(
		oneTable("z.wide", 300, map[string]fixture.Column{
			"m": {Type: "z.mood", Nullable: false, Distinct: &fixture.Fact[int64]{Value: 3, Confidence: fixture.Estimated}},
		}),
		map[string]fixture.UserType{"z.mood": {Kind: "enum", Labels: labels, LabelCount: 3}},
	)
	res, err := Generate(f, Options{Seed: 5})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	allowed := map[string]bool{}
	for _, l := range labels {
		allowed[l] = true
	}
	seen := map[string]bool{}
	for i, row := range res.Tables[0].Rows {
		s, ok := row[0].(string)
		if !ok {
			t.Fatalf("row %d: got %T, want string", i, row[0])
		}
		if !allowed[s] {
			t.Fatalf("row %d: %q is not a label of z.mood %v — Postgres rejects it", i, s, labels)
		}
		seen[s] = true
	}
	// Cardinality is shape: with 300 rows over 3 labels every label should appear.
	if len(seen) != len(labels) {
		t.Errorf("only %d of %d labels used; the column's cardinality was not reproduced", len(seen), len(labels))
	}
}

// TestStrictEnumUsesTheSamePlaceholdersAsTheDDL: under privacy:strict the labels
// are withheld and only the count survives. The engine and the DDL emitter both
// invent placeholders, and they MUST agree — a single disagreement means every
// insert of the missing label is rejected as not a member of the type. Both call
// EffectiveLabels, and this pins the contract.
func TestStrictEnumUsesTheSamePlaceholdersAsTheDDL(t *testing.T) {
	ut := fixture.UserType{Kind: "enum", LabelCount: 3} // vocabulary redacted
	f := withTypes(
		oneTable("z.t", 100, map[string]fixture.Column{"m": {Type: "z.mood", Nullable: false}}),
		map[string]fixture.UserType{"z.mood": ut},
	)
	res, err := Generate(f, Options{Seed: 2})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	allowed := map[string]bool{}
	for _, l := range ut.EffectiveLabels() {
		allowed[l] = true
	}
	if len(allowed) != 3 {
		t.Fatalf("EffectiveLabels returned %d labels, want 3", len(allowed))
	}
	for i, row := range res.Tables[0].Rows {
		if s := row[0].(string); !allowed[s] {
			t.Fatalf("row %d: %q is not one of the placeholder labels the DDL creates", i, s)
		}
	}
}

// TestDomainColumnGeneratesItsBaseType: a domain is a base type plus constraints,
// so a domain over integer must generate integers. Reading it as an opaque name fell
// through to the text fallback and the server refused the value:
//
//	unable to encode "val_14" into binary format for int4 (OID 23)
func TestDomainColumnGeneratesItsBaseType(t *testing.T) {
	f := withTypes(
		oneTable("z.wide", 50, map[string]fixture.Column{
			"pos": {
				Type:     "z.positive_int",
				Nullable: false,
				Distinct: &fixture.Fact[int64]{Value: 50, Confidence: fixture.Estimated},
				Range:    &fixture.Range{Min: int64(1), Max: int64(50)},
			},
		}),
		map[string]fixture.UserType{"z.positive_int": {Kind: "domain", Base: "integer", Check: "(VALUE > 0)"}},
	)
	res, err := Generate(f, Options{Seed: 9})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, row := range res.Tables[0].Rows {
		v, ok := row[0].(int64)
		if !ok {
			t.Fatalf("row %d: got %T (%v), want int64 for a domain over integer", i, row[0], row[0])
		}
		// The declared range comes from production, which satisfied the domain's
		// CHECK — so generating within it satisfies the CHECK too. This is what keeps
		// `value for domain violates check constraint` from firing.
		if v < 1 || v > 50 {
			t.Fatalf("row %d: %d escaped the declared range [1,50] and would violate CHECK (VALUE > 0)", i, v)
		}
	}
}

// TestDomainOverDomainResolvesToTheBottom: domains can nest, and only the ultimate
// base type says how to build a value.
func TestDomainOverDomainResolvesToTheBottom(t *testing.T) {
	f := withTypes(
		oneTable("z.t", 10, map[string]fixture.Column{
			"c": {Type: "z.outer", Nullable: false, Range: &fixture.Range{Min: int64(5), Max: int64(9)}},
		}),
		map[string]fixture.UserType{
			"z.outer": {Kind: "domain", Base: "z.inner"},
			"z.inner": {Kind: "domain", Base: "bigint"},
		},
	)
	res, err := Generate(f, Options{Seed: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := res.Tables[0].Rows[0][0].(int64); !ok {
		t.Fatalf("got %T, want int64 through two domain levels", res.Tables[0].Rows[0][0])
	}
}

// TestExoticTypesGetValidLiterals: pgx encodes a plain string for most Postgres
// types, so the bug was never "strings don't encode" — it was that the generic
// fallback "val_141" is not a valid literal for a type with a real grammar:
//
//	unable to encode "val_141" into binary format for inet (OID 869)
//
// Each case here asserts the shape a valid literal must have. The values are also
// checked against real Postgres in TestHydrateExoticTypesRoundTrip.
func TestExoticTypesGetValidLiterals(t *testing.T) {
	cases := []struct {
		colType string
		check   func(string) bool
		want    string
	}{
		{"inet", func(s string) bool { return strings.HasPrefix(s, "192.0.2.") }, "a 192.0.2.x address"},
		{"cidr", func(s string) bool { return strings.HasPrefix(s, "192.0.2.") }, "a 192.0.2.x address"},
		{"macaddr", func(s string) bool { return strings.Count(s, ":") == 5 }, "six colon-separated octets"},
		{"macaddr8", func(s string) bool { return strings.Count(s, ":") == 7 }, "eight colon-separated octets"},
		{"interval", func(s string) bool { return strings.HasSuffix(s, "seconds") }, "an interval literal"},
		{"xml", func(s string) bool { return strings.HasPrefix(s, "<value>") }, "an XML element"},
		{"point", func(s string) bool { return strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") }, "a point literal"},
		{"jsonb", func(s string) bool { return s == "{}" }, "parseable JSON"},
		{"json", func(s string) bool { return s == "{}" }, "parseable JSON"},
		{"text[]", func(s string) bool { return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") }, "an array literal"},
		{"bit(3)", func(s string) bool { return len(s) == 3 && strings.Trim(s, "01") == "" }, "exactly 3 bits"},
	}
	for _, tc := range cases {
		t.Run(tc.colType, func(t *testing.T) {
			f := oneTable("public.t", 20, map[string]fixture.Column{"c": {Type: tc.colType, Nullable: false}})
			res, err := Generate(f, Options{Seed: 4})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			for i, row := range res.Tables[0].Rows {
				s, ok := row[0].(string)
				if !ok {
					t.Fatalf("row %d: got %T, want a string literal", i, row[0])
				}
				if !tc.check(s) {
					t.Fatalf("row %d: %q is not %s for %s", i, s, tc.want, tc.colType)
				}
			}
		})
	}
}

// TestUnsupportedTypeIsNamed: a type with no synthesis rule must be refused BEFORE
// generation, naming the table and column. Previously it produced "val_141" and
// failed hundreds of thousands of rows later as a pgx internals message that named
// neither, reading as a rowshape defect rather than a decision for the operator.
func TestUnsupportedTypeIsNamed(t *testing.T) {
	for _, typ := range []string{"tsvector", "int4range", "polygon"} {
		t.Run(typ, func(t *testing.T) {
			f := oneTable("public.t", 5, map[string]fixture.Column{"weird": {Type: typ}})
			_, err := Generate(f, Options{Seed: 1})
			if err == nil {
				t.Fatalf("Generate succeeded for %s; want a named refusal", typ)
			}
			for _, want := range []string{"public.t", "weird", typ} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestEnumIsNotMistakenForAnUnsupportedType: the refusal above must not fire for a
// type the fixture actually DEFINES. An enum named e.g. "public.daterange" would
// otherwise be rejected by the name-suffix heuristic despite being fully described.
func TestEnumIsNotMistakenForAnUnsupportedType(t *testing.T) {
	f := withTypes(
		oneTable("public.t", 5, map[string]fixture.Column{"c": {Type: "public.daterange"}}),
		map[string]fixture.UserType{"public.daterange": {Kind: "enum", Labels: []string{"x", "y"}, LabelCount: 2}},
	)
	if _, err := Generate(f, Options{Seed: 1}); err != nil {
		t.Fatalf("a defined enum was refused as unsupported: %v", err)
	}
}

// TestEnumArrayElementsAreLabels: an array OF an enum constrains its elements just
// as the scalar type does. The array path builds elements from the type name alone
// and cannot reach the type definitions, so without special handling an
// `app.status[]` is filled with generic strings that are not members of the element
// type and the insert is rejected.
func TestEnumArrayElementsAreLabels(t *testing.T) {
	f := withTypes(
		oneTable("app.t", 60, map[string]fixture.Column{
			"tags": {Type: "app.status[]", Nullable: false},
		}),
		map[string]fixture.UserType{"app.status": {Kind: "enum", Labels: []string{"pending", "paid"}, LabelCount: 2}},
	)
	res, err := Generate(f, Options{Seed: 6})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, row := range res.Tables[0].Rows {
		s := row[0].(string)
		if s != `{"pending"}` && s != `{"paid"}` {
			t.Fatalf("row %d: %q is not an array of app.status labels", i, s)
		}
	}
}

// TestDeclaredLengthIsHonored: a text column's `length` is the ONLY shape §6.1
// permits it to carry, and ignoring it produces a WRONG VERDICT rather than merely
// odd content.
//
// The observed case: a varchar(12) column whose production values are 2-5
// characters (pull recorded length {min: 2, max: 5}) hydrated as 9-character
// "slug-8999" values, so validating `ALTER COLUMN vc TYPE varchar(8)` — which the
// real data satisfies comfortably — returned FAIL with `value too long for type
// character varying(8)`. A FAIL the migration would not have produced is the
// failure mode this format exists to prevent, in the direction that destroys trust.
func TestDeclaredLengthIsHonored(t *testing.T) {
	max := int64(5)
	min := int64(2)
	f := oneTable("z.wide", 9000, map[string]fixture.Column{
		"vc": {
			Type:     "character varying(12)", // the type allows 12...
			Nullable: false,
			Distinct: &fixture.Fact[int64]{Value: 9000, Confidence: fixture.Estimated},
			Format:   "slug",
			// ...but production never exceeded 5.
			Length: &fixture.Length{Min: &min, Max: &max},
		},
	})
	res, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, row := range res.Tables[0].Rows {
		s := row[0].(string)
		if int64(len(s)) > max {
			t.Fatalf("row %d: %q is %d chars but the fixture declares length.max=%d; "+
				"a migration narrowing this column would FAIL against data production never held",
				i, s, len(s), max)
		}
	}
}

// TestUniqueColumnStaysUniqueUnderDeclaredLength: honoring length.max must not cost
// uniqueness. 9000 distinct values must still fit in 5 characters (36^5 is ample),
// and the clamp must not be the thing that collapses them.
func TestUniqueColumnStaysUniqueUnderDeclaredLength(t *testing.T) {
	max := int64(5)
	const rows = 9000
	f := oneTable("z.wide", rows, map[string]fixture.Column{
		"vc": {
			Type:     "character varying(12)",
			Nullable: false,
			Unique:   &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact, Via: "constraint"},
			Format:   "slug",
			Length:   &fixture.Length{Max: &max},
		},
	})
	res, err := Generate(f, Options{Seed: 42})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seen := make(map[string]bool, rows)
	for i, row := range res.Tables[0].Rows {
		s := row[0].(string)
		if int64(len(s)) > max {
			t.Fatalf("row %d: %q exceeds declared length.max=%d", i, s, max)
		}
		if seen[s] {
			t.Fatalf("row %d: duplicate %q — honoring length.max collapsed a unique column", i, s)
		}
		seen[s] = true
	}
}

// TestTypeWidthStillWinsWhenTighter: the two limits are independent, and the
// TIGHTER one applies. A length.max wider than the column type must not be read as
// permission to exceed the type.
func TestTypeWidthStillWinsWhenTighter(t *testing.T) {
	max := int64(50) // production values were long...
	f := oneTable("public.t", 100, map[string]fixture.Column{
		"code": {
			Type:     "character(3)", // ...but the column only holds 3
			Nullable: false,
			Distinct: &fixture.Fact[int64]{Value: 5, Confidence: fixture.Estimated},
			Format:   "free_text",
			Length:   &fixture.Length{Max: &max},
		},
	})
	res, err := Generate(f, Options{Seed: 1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, row := range res.Tables[0].Rows {
		if s := row[0].(string); len(s) > 3 {
			t.Fatalf("row %d: %q exceeds character(3) despite a wider declared length", i, s)
		}
	}
}

// TestNonUniqueClampKeepsTheValueRecognizable: for a non-unique column the clamp
// keeps as much of the rendered value as the ordinal leaves room for, so an email
// narrowed to 19 characters still reads as an email instead of becoming a bare
// ordinal. This is the one place recognizability is preserved; a unique column
// trades it away for injectivity.
func TestNonUniqueClampKeepsTheValueRecognizable(t *testing.T) {
	max := int64(19)
	f := oneTable("public.users", 200, map[string]fixture.Column{
		"email": {
			Type:     "text",
			Nullable: false,
			Distinct: &fixture.Fact[int64]{Value: 150, Confidence: fixture.Estimated},
			Format:   "email",
			Length:   &fixture.Length{Max: &max},
		},
	})
	res, err := Generate(f, Options{Seed: 8})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, row := range res.Tables[0].Rows {
		s := row[0].(string)
		if int64(len(s)) > max {
			t.Fatalf("row %d: %q exceeds length.max=%d", i, s, max)
		}
		if !strings.HasPrefix(s, "user_") {
			t.Fatalf("row %d: %q lost the rendered prefix; a clamped email should still read as one", i, s)
		}
	}
}
