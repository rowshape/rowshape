// Package sqlkind classifies raw SQL statements by kind, without a parser.
//
// It exists to hold logic that more than one package needs to agree on. The
// transaction-control test lived, verbatim, in both internal/plan and
// internal/validate — two copies with no guard tying them together, so a fix to
// one could silently diverge from the other. Neither package can import the
// other (internal/plan already imports internal/validate), so the shared home is
// a third, lower package that depends on neither.
package sqlkind

import "strings"

// txKeywords are the leading verbs of a transaction-control statement.
var txKeywords = []string{
	"BEGIN", "COMMIT", "ROLLBACK", "START TRANSACTION", "END", "SAVEPOINT", "RELEASE",
}

// IsTxControl reports whether sql is transaction control (BEGIN, COMMIT,
// ROLLBACK, START TRANSACTION, END, SAVEPOINT, RELEASE) rather than DDL/DML.
//
// It matches on the leading verb only, so an identifier that merely starts with
// a keyword ("BEGINNING", "COMMITTED", a table named "beginner") is not mistaken
// for transaction control. Surrounding whitespace is ignored.
func IsTxControl(sql string) bool {
	u := strings.ToUpper(TrimLeadingComments(sql))
	for _, kw := range txKeywords {
		if u == kw || strings.HasPrefix(u, kw+" ") || strings.HasPrefix(u, kw+";") {
			return true
		}
	}
	return false
}

// TrimLeadingComments drops whitespace and any `-- line` or `/* block */`
// comments in front of a statement, so its leading verb can be read.
//
// The statement splitter keeps a comment with the statement that follows it,
// which is right for error locations and wrong for classification: a migration
// that wrote `-- Migration 2` above its BEGIN, or above a CREATE INDEX
// CONCURRENTLY, had that statement misclassified by every prefix test here —
// the BEGIN was executed, and the CONCURRENTLY build was run inside a
// transaction and failed with 25001.
func TrimLeadingComments(sql string) string {
	s := strings.TrimSpace(sql)
	for {
		switch {
		case strings.HasPrefix(s, "--"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return ""
			}
			s = strings.TrimSpace(s[i+1:])
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return ""
			}
			s = strings.TrimSpace(s[i+2:])
		default:
			return s
		}
	}
}

// RunsOutsideTransaction reports whether Postgres refuses sql inside a
// transaction block, among the statements a migration plausibly carries: the
// CONCURRENTLY forms of CREATE INDEX, DROP INDEX and REINDEX. (VACUUM and the
// database and tablespace commands are refused too, but are not migration DDL.)
func RunsOutsideTransaction(sql string) bool {
	u := strings.ToUpper(TrimLeadingComments(sql))
	if !strings.Contains(u, "CONCURRENTLY") {
		return false
	}
	for _, p := range []string{"CREATE INDEX ", "CREATE UNIQUE INDEX ", "DROP INDEX ", "REINDEX "} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}
