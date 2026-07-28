package findings

import "testing"

// TestIsNarrowing pins the direction of a string-type change.
//
// The original test was `old is a string type && new contains "("`, which never
// compared the two lengths. Every widening of a length-limited string type —
// and even a no-op change to the identical type — was reported as
// RS-REVERSE-003 "narrowing a column type can truncate data irreversibly".
// A false FAIL on a safe migration is worse than a missed finding: it blocks
// correct work and teaches people to ignore the tool.
func TestIsNarrowing(t *testing.T) {
	cases := []struct {
		old, new string
		want     bool
		why      string
	}{
		// The regressions.
		{"varchar(50)", "varchar(255)", false, "widening a varchar loses nothing"},
		{"varchar(50)", "varchar(50)", false, "the identical type is not a change at all"},
		{"char(10)", "char(20)", false, "widening a char loses nothing"},
		{"varchar(255)", "varchar(255)", false, "identical"},

		// Genuine narrowings that must keep firing.
		{"varchar(255)", "varchar(50)", true, "shortening a varchar can truncate"},
		{"text", "varchar(100)", true, "an unbounded string gaining a limit can truncate"},
		{"varchar", "varchar(100)", true, "unbounded varchar gaining a limit can truncate"},
		{"char(20)", "char(10)", true, "shortening a char can truncate"},

		// Widening to unbounded.
		{"varchar(50)", "text", false, "text is strictly wider"},

		// Integer rules must be untouched by this change.
		{"bigint", "integer", true, "int8 -> int4 narrows"},
		{"integer", "bigint", false, "int4 -> int8 widens"},
		{"integer", "smallint", true, "int4 -> int2 narrows"},
		{"smallint", "integer", false, "int2 -> int4 widens"},

		// Numeric -> integer must be untouched.
		{"numeric", "integer", true, "numeric -> integer drops the fraction"},
		{"double precision", "bigint", true, "float -> integer drops the fraction"},
	}
	for _, c := range cases {
		t.Run(c.old+"_to_"+c.new, func(t *testing.T) {
			if got := isNarrowing(c.old, c.new); got != c.want {
				t.Errorf("isNarrowing(%q, %q) = %v, want %v — %s", c.old, c.new, got, c.want, c.why)
			}
		})
	}
}

func TestTypeLength(t *testing.T) {
	cases := []struct {
		in      string
		n       int
		bounded bool
	}{
		{"varchar(255)", 255, true},
		{"varchar", 0, false},
		{"text", 0, false},
		{"character varying(10)", 10, true},
		{"numeric(10,2)", 10, true}, // only the first component is the length
		{"varchar(", 0, false},      // malformed: unbounded, never length 0
		{"varchar()", 0, false},
		{"varchar(abc)", 0, false},
	}
	for _, c := range cases {
		n, bounded := typeLength(c.in)
		if n != c.n || bounded != c.bounded {
			t.Errorf("typeLength(%q) = (%d, %v), want (%d, %v)", c.in, n, bounded, c.n, c.bounded)
		}
	}
}
