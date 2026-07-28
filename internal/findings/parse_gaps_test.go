package findings

import "testing"

// TestHasWhereClause: the old test was for the literal " WHERE ", which missed
// `DELETE FROM t WHERE(id=1)` — valid SQL, since collapseSpaces normalizes
// whitespace runs but does not insert a space before "(". A well-qualified
// DELETE was therefore reported as unqualified full-table DML, which is a FALSE
// finding on safe SQL: the expensive kind, because it blocks a correct migration.
func TestHasWhereClause(t *testing.T) {
	yes := []string{
		"DELETE FROM T WHERE ID = 1",
		"DELETE FROM T WHERE(ID=1)",
		"UPDATE T SET A=1 WHERE(B)",
		"DELETE FROM T WHERE\tID = 1",
		"SELECT 1 WHERE",
	}
	for _, s := range yes {
		if !hasWhereClause(s) {
			t.Errorf("hasWhereClause(%q) = false, want true", s)
		}
	}
	no := []string{
		"DELETE FROM T",
		"UPDATE T SET A = 1",
		// An identifier that merely contains the keyword must not count.
		"DELETE FROM NOWHERE_LOG",
		"UPDATE T SET WHEREABOUTS = 1",
		"DELETE FROM T_WHERE",
	}
	for _, s := range no {
		if hasWhereClause(s) {
			t.Errorf("hasWhereClause(%q) = true, want false", s)
		}
	}
}

func TestUnqualifiedDMLRespectsParenthesizedWhere(t *testing.T) {
	cases := []struct {
		sql  string
		want bool // want an unqualified-DML finding
	}{
		{"DELETE FROM users", true},
		{"DELETE FROM users WHERE id = 1", false},
		{"DELETE FROM users WHERE(id=1)", false},
		{"UPDATE users SET a = 1", true},
		{"UPDATE users SET a = 1 WHERE(b)", false},
	}
	for _, c := range cases {
		_, _, got := unqualifiedDML(upperOf(c.sql), c.sql)
		if got != c.want {
			t.Errorf("unqualifiedDML(%q) flagged = %v, want %v", c.sql, got, c.want)
		}
	}
}

func upperOf(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}
