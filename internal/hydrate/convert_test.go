package hydrate

import (
	"math"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// TestToI64IsTotal pins a defined result for every input.
//
// Go leaves float-to-int conversion "implementation-specific" when the value is
// out of range, and implementations differ: amd64 yields INT64_MIN, arm64
// saturates to MaxInt64. INV-DETERMINISM promises byte-identical hydrate output
// on ANY platform, so a bare int64(f) breaks the promise the moment a fixture
// carries a value big enough to overflow — the same architecture-divergence
// family as the FMA-fusion bug the determinism-matrix job exists for.
func TestToI64IsTotal(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want int64
	}{
		{"zero", 0, 0},
		{"small positive", 42.9, 42},
		{"small negative", -42.9, -42},
		{"NaN", math.NaN(), 0},
		{"positive infinity", math.Inf(1), math.MaxInt64},
		{"negative infinity", math.Inf(-1), math.MinInt64},
		{"far above range", 1e30, math.MaxInt64},
		{"far below range", -1e30, math.MinInt64},
		{"just above MaxInt64", math.MaxInt64 * 1.5, math.MaxInt64},
		{"at the MaxInt64 boundary", math.MaxInt64, math.MaxInt64},
		{"at the MinInt64 boundary", math.MinInt64, math.MinInt64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toI64(c.in); got != c.want {
				t.Errorf("toI64(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// A bare conversion is genuinely undefined here — this documents what this
// platform does, so the divergence is visible rather than theoretical.
func TestBareConversionIsPlatformDependent(t *testing.T) {
	// A runtime variable, not a constant: Go rejects an overflowing CONSTANT
	// conversion at compile time, which is precisely why this class of bug only
	// ever shows up with values that arrive at runtime — such as from a fixture.
	huge := 1e30
	bare := int64(huge)
	t.Logf("this platform converts int64(1e30) to %d; toI64 gives %d", bare, toI64(huge))
	if bare == toI64(huge) {
		t.Log("this platform happens to saturate; on the other one it does not, which is the whole problem")
	}
}

// An unbounded Scale must not produce a nonsensical row count. The n<1 guard
// downstream MASKED this: a huge negative from amd64 became 1 row while arm64
// saturated to MaxRows — divergent by orders of magnitude, silently.
func TestScaleOverflowIsBounded(t *testing.T) {
	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.t": {
				Rows:    fixture.Fact[int64]{Value: math.MaxInt64 / 2, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"id": {Type: "bigint"}},
			},
		},
	}
	res, err := Generate(f, Options{Seed: 1, Scale: 1e12, MaxRows: 10})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, tb := range res.Tables {
		if int64(len(tb.Rows)) > 10 {
			t.Errorf("table %s generated %d rows despite MaxRows=10", tb.Name, len(tb.Rows))
		}
	}
}

// A NaN range bound is not a bound. Treating it as one would poison span
// arithmetic downstream.
func TestToInt64RejectsNaNBound(t *testing.T) {
	if _, ok := toInt64(math.NaN()); ok {
		t.Error("a NaN bound must report as absent, not convert to some integer")
	}
	if v, ok := toInt64(1e30); !ok || v != math.MaxInt64 {
		t.Errorf("an out-of-range bound must saturate deterministically, got (%d, %v)", v, ok)
	}
}
