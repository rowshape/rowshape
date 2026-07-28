package findings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rowshape/rowshape/internal/estimate"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsSequence{}) }

// rsSequence reasons about the migration as a WHOLE, not statement by statement.
//
// A note on the architecture, because the gap was easy to misread: the analyzer
// interface already receives the entire Capture, so cross-statement rules were
// always expressible — rsConstraint has tracked NOT VALID/VALIDATE pairs across
// statements from the start. Nothing needed changing. What was missing was
// analyzers that USE that, so every hazard whose cause is the SEQUENCE rather
// than any single statement went unseen.
//
// Two of those are worth catching on their own:
//
//   - A migration that takes ACCESS EXCLUSIVE without first setting lock_timeout.
//     The DDL may be instant, but if it cannot get its lock immediately it waits
//     — and while it waits, every subsequent query on that table queues BEHIND
//     it, because a pending ACCESS EXCLUSIVE request blocks new readers. That is
//     the mechanism behind most "one quick migration took the site down" stories,
//     and it is invisible to any per-statement rule because the statement itself
//     is fine.
//   - Several exclusive-lock operations on the same table in one migration, each
//     re-acquiring and re-queueing.
type rsSequence struct{}

func (rsSequence) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	var out []verdict.Finding

	sawLockTimeout := false
	firstLocking := ""
	byTable := map[string][]string{}
	var order []string

	for _, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)
		if upper == "" {
			continue
		}
		if setsLockTimeout(upper) {
			sawLockTimeout = true
			continue
		}
		table, what, locks := takesAccessExclusive(f, clean, upper)
		if !locks {
			continue
		}
		if firstLocking == "" {
			firstLocking = what
		}
		if _, seen := byTable[table]; !seen {
			order = append(order, table)
		}
		byTable[table] = append(byTable[table], what)
	}

	if firstLocking != "" && !sawLockTimeout {
		out = append(out, lockTimeoutFinding(firstLocking))
	}

	// Sorted, not map order: this walks a map and the verdict is meant to be
	// attestable (see internal/findings/order.go).
	sort.Strings(order)
	for _, table := range order {
		if ops := byTable[table]; len(ops) > 1 {
			out = append(out, repeatedLockFinding(table, ops))
		}
	}
	return out
}

// setsLockTimeout recognizes a statement that bounds how long DDL will wait.
func setsLockTimeout(upper string) bool {
	return strings.HasPrefix(upper, "SET LOCK_TIMEOUT") ||
		strings.HasPrefix(upper, "SET LOCAL LOCK_TIMEOUT") ||
		strings.HasPrefix(upper, "SET SESSION LOCK_TIMEOUT")
}

// takesAccessExclusive reports whether a statement acquires ACCESS EXCLUSIVE and
// HOLDS it for a non-trivial time, and on which table.
//
// The "holds it" qualifier is the whole design of this rule, and getting it
// wrong is what makes a lock advisory useless. Strictly, the lock_timeout hazard
// applies to ANY DDL: even an instant catalog-only change queues readers behind
// it if it cannot acquire its lock. But firing on every migration containing any
// ALTER TABLE means firing on essentially every migration ever written, which is
// noise that trains people to ignore findings — and it damaged a real signal:
// the version-matrix case whose whole point is that ADD COLUMN ... DEFAULT is a
// rewrite WARN on PG 10 and a catalog-only PASS on PG 11+ started reporting WARN
// on every major.
//
// So the rule fires only where the lock is actually HELD: a rewrite, a
// validating scan, an index rebuild. There the advice is materially valuable and
// the noise is bounded. Catalog-only DDL is deliberately silent.
//
// Also deliberately excluded: CREATE INDEX (non-concurrent) takes SHARE — it
// blocks writes but not reads, a materially different operational risk that is
// already RS-INDEX-001's subject.
func takesAccessExclusive(f *fixture.Fixture, clean, upper string) (table, what string, ok bool) {
	switch {
	case strings.HasPrefix(upper, "ALTER TABLE"):
		// A rename is instant and its hazard is elsewhere (RS-DEPLOY-001).
		if strings.Contains(upper, " RENAME") {
			return "", "", false
		}
		target := resolveTable(f, alterTableTarget(clean))
		// A constraint added without NOT VALID scans the table under the lock.
		if strings.Contains(upper, "ADD CONSTRAINT") && !strings.Contains(upper, "NOT VALID") {
			return target, "ADD CONSTRAINT (validating scan)", true
		}
		if strings.Contains(upper, "ADD PRIMARY KEY") {
			return target, "ADD PRIMARY KEY (index build)", true
		}
		if strings.Contains(upper, "ATTACH PARTITION") {
			return target, "ATTACH PARTITION", true
		}
		// Otherwise defer to the rewrite classifier, so this rule agrees with
		// RS-LOCK-001 about what actually rewrites — including its
		// version-conditional handling of ADD COLUMN ... DEFAULT.
		major, hasVersion := majorOf(f)
		if _, _, kind, rewrites := classifyRewrite(clean, major, hasVersion); rewrites {
			return target, kind, true
		}
		return "", "", false
	case strings.HasPrefix(upper, "VACUUM FULL"):
		return "", "VACUUM FULL", true
	case strings.HasPrefix(upper, "CLUSTER"):
		return "", "CLUSTER", true
	case strings.HasPrefix(upper, "REINDEX") && !strings.Contains(upper, "CONCURRENTLY"):
		return "", "REINDEX", true
	}
	return "", "", false
}

// majorOf reads the fixture's engine major, mirroring what the other analyzers do.
func majorOf(f *fixture.Fixture) (int, bool) {
	return estimate.Major(f.Meta.Engine.Version)
}

func lockTimeoutFinding(first string) verdict.Finding {
	return verdict.Finding{
		Code:     "RS-LOCK-010",
		Severity: verdict.SeverityWarn,
		Title:    "This migration takes ACCESS EXCLUSIVE without setting lock_timeout",
		Detail: fmt.Sprintf(
			"The first locking statement is a %s. With no lock_timeout, a DDL statement that cannot acquire "+
				"its lock immediately WAITS — and while it waits, every new query on that table queues behind "+
				"it, because a pending ACCESS EXCLUSIVE request blocks incoming readers too. A migration that "+
				"is instant in isolation can therefore stall an entire table behind one long-running "+
				"transaction it happened to collide with. This is a property of the migration as a whole, so "+
				"no single statement looks wrong.",
			first),
		Evidence: map[string]any{"first_locking_statement": first},
		// A property of the SQL and the server, not of any fixture fact.
		DependsOn:   nil,
		Remediation: remediation("RS-LOCK-010"),
		Explain:     "rowshape explain RS-LOCK-010",
	}
}

func repeatedLockFinding(table string, ops []string) verdict.Finding {
	subject := "the same table"
	if table != "" {
		subject = shortTable(table)
	}
	return verdict.Finding{
		Code:     "RS-LOCK-011",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("%d separate statements take ACCESS EXCLUSIVE on %s", len(ops), subject),
		Detail: fmt.Sprintf(
			"This migration locks %s %d times (%s). Each acquisition queues independently, so the table is "+
				"unavailable across the whole sequence rather than for the duration of the longest single "+
				"statement — and between them, traffic that built up during one lock is competing for the next. "+
				"Combining the changes into one ALTER TABLE takes the lock once.",
			subject, len(ops), strings.Join(ops, ", ")),
		Evidence:    map[string]any{"table": table, "operations": ops, "count": len(ops)},
		DependsOn:   nil,
		Remediation: remediation("RS-LOCK-011"),
		Explain:     "rowshape explain RS-LOCK-011",
	}
}
