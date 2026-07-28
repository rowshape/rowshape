package profile

import (
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// richColumn builds a column carrying every value-derived field, for matrix
// testing.
func richColumn() fixture.Column {
	mn, mx := int64(1), int64(9)
	mean := 5.0
	return fixture.Column{
		Type:         "text",
		Distinct:     &fixture.Fact[int64]{Value: 3, Confidence: fixture.Estimated},
		Length:       &fixture.Length{Min: &mn, Max: &mx, Mean: &mean},
		Range:        &fixture.Range{Min: 1, Max: 9},
		Histogram:    &fixture.Histogram{Buckets: 4, Bounds: []any{0, 1, 2, 3}},
		Values:       []string{"active", "trialing", "canceled"},
		Frequencies:  []float64{0.5, 0.3, 0.2},
		SampleN:      500, // the k-gate counts observations: 0.5/0.3/0.2 -> 250/150/100
		NullFraction: &fixture.Fact[float64]{Value: 0.0, Confidence: fixture.Estimated},
	}
}

func fixtureWith(col fixture.Column, rows int64) *fixture.Fixture {
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Tables: map[string]fixture.Table{
			"public.t": {
				Rows:    fixture.Fact[int64]{Value: rows, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{"c": col},
				Constraints: []fixture.Constraint{
					{Name: "t_chk", Kind: "check", Expression: "status <> 'secret'"},
				},
			},
		},
	}
}

// TestPrivacyFieldMatrix asserts the RFC §8.2 field matrix per level.
func TestPrivacyFieldMatrix(t *testing.T) {
	t.Run("strict", func(t *testing.T) {
		f := fixtureWith(richColumn(), 1000)
		ApplyPrivacy(f, PrivacyStrict, 0)
		c := f.Tables["public.t"].Columns["c"]
		if c.Range != nil || c.Histogram != nil || c.Values != nil || c.Frequencies != nil {
			t.Errorf("strict must drop range/histogram/values/frequencies: %+v", c)
		}
		if c.Length == nil {
			t.Errorf("strict must keep length stats")
		}
		if c.NullFraction == nil {
			t.Errorf("strict must keep null_fraction")
		}
		// CHECK expression becomes opaque (RFC §6.4).
		if got := f.Tables["public.t"].Constraints[0].Expression; got != "opaque" {
			t.Errorf("strict CHECK expression = %q, want opaque", got)
		}
	})

	t.Run("standard", func(t *testing.T) {
		f := fixtureWith(richColumn(), 1000)
		ApplyPrivacy(f, PrivacyStandard, 0)
		c := f.Tables["public.t"].Columns["c"]
		if c.Range == nil || c.Histogram == nil {
			t.Errorf("standard must keep range and histogram")
		}
		if c.Values != nil || c.Frequencies != nil {
			t.Errorf("standard must drop values/frequencies: %+v", c)
		}
		if got := f.Tables["public.t"].Constraints[0].Expression; got != "status <> 'secret'" {
			t.Errorf("standard must keep verbatim CHECK, got %q", got)
		}
	})

	t.Run("permissive_keeps_safe_values", func(t *testing.T) {
		f := fixtureWith(richColumn(), 1000) // observed 250/150/100 in a 500-row sample, all >= 20
		ApplyPrivacy(f, PrivacyPermissive, 0)
		c := f.Tables["public.t"].Columns["c"]
		if len(c.Values) != 3 || len(c.Frequencies) != 3 {
			t.Errorf("permissive should keep the safe value set: %+v", c)
		}
	})
}

// TestPermissiveKThreshold: values appear only when distinct<=50 AND every value
// occurs >= k times (RFC §8.2).
func TestPermissiveKThreshold(t *testing.T) {
	t.Run("rare_value_dropped", func(t *testing.T) {
		col := richColumn()
		// One value seen 0.001 * 500 = 0.5 -> 1 time in the sample, below k=20.
		col.Frequencies = []float64{0.5, 0.499, 0.001}
		f := fixtureWith(col, 1000)
		ApplyPrivacy(f, PrivacyPermissive, 20)
		if f.Tables["public.t"].Columns["c"].Values != nil {
			t.Errorf("a value below the k-threshold must suppress the whole set")
		}
	})

	t.Run("high_cardinality_dropped", func(t *testing.T) {
		col := richColumn()
		col.Distinct = &fixture.Fact[int64]{Value: 51, Confidence: fixture.Estimated} // > 50
		f := fixtureWith(col, 1_000_000)
		ApplyPrivacy(f, PrivacyPermissive, 20)
		if f.Tables["public.t"].Columns["c"].Values != nil {
			t.Errorf("distinct > 50 must suppress the value set")
		}
	})

	t.Run("custom_k", func(t *testing.T) {
		col := richColumn()
		col.Frequencies = []float64{0.5, 0.3, 0.2}
		col.SampleN = 100 // min observed = 0.2 * 100 = 20 occurrences
		f := fixtureWith(col, 100)
		ApplyPrivacy(f, PrivacyPermissive, 25) // require 25; 20 < 25 -> drop
		if f.Tables["public.t"].Columns["c"].Values != nil {
			t.Errorf("k=25 with a 20-count value must suppress the set")
		}
	})

	// Regression: the gate used to score a value as frequency × declared rows.
	// Because a frequency resolves no finer than 1/SampleN, that product grows
	// with the table and crossed k=20 at 10,000 rows — so a value seen exactly
	// ONCE in the sample was published verbatim from any larger table, and the
	// gate got weaker precisely as re-identification risk got worse.
	t.Run("singleton_suppressed_at_every_table_size", func(t *testing.T) {
		for _, rows := range []int64{1_000, 9_999, 10_001, 100_000_000} {
			col := richColumn()
			// "canceled" seen once in a 500-row sample; the others make up the rest.
			col.Frequencies = []float64{0.5, 0.498, 0.002}
			f := fixtureWith(col, rows)
			ApplyPrivacy(f, PrivacyPermissive, 20)
			if got := f.Tables["public.t"].Columns["c"].Values; got != nil {
				t.Errorf("rows=%d: a value seen once in the sample must never be published, got %v", rows, got)
			}
		}
	})

	// A fixture read back from disk has no SampleN (it is not serialized), so
	// the observed count cannot be recovered. The gate must fail closed.
	t.Run("missing_sample_size_fails_closed", func(t *testing.T) {
		col := richColumn()
		col.SampleN = 0
		f := fixtureWith(col, 1000)
		ApplyPrivacy(f, PrivacyPermissive, 20)
		if f.Tables["public.t"].Columns["c"].Values != nil {
			t.Errorf("without a sample size the gate must withhold, not guess")
		}
	})
}

// TestRedactOverrides: per-column redact always wins (RFC §8.2).
func TestRedactOverrides(t *testing.T) {
	t.Run("specific_fields", func(t *testing.T) {
		col := richColumn()
		col.Redact = fixture.Redact{"range", "histogram"}
		f := fixtureWith(col, 1000)
		// Even at permissive, the redacted fields are gone; others survive.
		ApplyPrivacy(f, PrivacyPermissive, 0)
		c := f.Tables["public.t"].Columns["c"]
		if c.Range != nil || c.Histogram != nil {
			t.Errorf("redact [range, histogram] must drop them: %+v", c)
		}
		if c.Length == nil {
			t.Errorf("redact must not drop unrelated length stats")
		}
	})

	t.Run("all", func(t *testing.T) {
		col := richColumn()
		col.Redact = fixture.Redact{"all"}
		f := fixtureWith(col, 1000)
		ApplyPrivacy(f, PrivacyStandard, 0)
		c := f.Tables["public.t"].Columns["c"]
		if c.Range != nil || c.Histogram != nil || c.Values != nil || c.Frequencies != nil || c.Length != nil || c.Shape != nil {
			t.Errorf("redact all must drop every value-derived field: %+v", c)
		}
		if c.Format != fmtOpaque {
			t.Errorf("redact all must set format opaque, got %q", c.Format)
		}
	})
}

// TestParsePrivacyDefault: the default is standard, never permissive (RFC §8.2).
func TestParsePrivacyDefault(t *testing.T) {
	got, err := ParsePrivacy("")
	if err != nil || got != PrivacyStandard {
		t.Errorf("ParsePrivacy(\"\") = (%q, %v), want (standard, nil)", got, err)
	}
	for _, lvl := range []string{"strict", "standard", "permissive"} {
		if _, err := ParsePrivacy(lvl); err != nil {
			t.Errorf("ParsePrivacy(%q) unexpected error: %v", lvl, err)
		}
	}
	if _, err := ParsePrivacy("wide-open"); err == nil {
		t.Errorf("ParsePrivacy(\"wide-open\") should error")
	}
}

// TestHashSource: meta.source is a salted hash, never the hostname (RFC §8.4).
func TestHashSource(t *testing.T) {
	const host = "db.internal.example.com"
	got := HashSource(host)
	if !strings.HasPrefix(got, fixture.DigestPrefix) {
		t.Errorf("source = %q, want a sha256: hash", got)
	}
	if strings.Contains(got, host) || strings.Contains(got, "internal") {
		t.Errorf("source must not contain the hostname: %q", got)
	}
	// Deterministic (stable digest, §11).
	if HashSource(host) != got {
		t.Errorf("HashSource is not deterministic")
	}
	// Distinct hosts hash differently.
	if HashSource("other.host") == got {
		t.Errorf("different hosts must hash differently")
	}
	// Empty host yields empty source.
	if HashSource("") != "" {
		t.Errorf("empty host should yield empty source")
	}
}
