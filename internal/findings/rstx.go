package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/estimate"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsTx{}) }

// rsTx catches statements Postgres refuses to run inside a transaction block.
//
// This is the one place where the sandbox does not merely fail to notice a
// hazard — it ACTIVELY DIVERGES from production and manufactures confidence.
// validate deliberately does not execute the migration's own BEGIN/COMMIT (it
// records them and runs each statement in its own transaction so locks can be
// inspected), and it hoists CREATE INDEX CONCURRENTLY out of any transaction
// entirely. So:
//
//	BEGIN;
//	CREATE INDEX CONCURRENTLY idx ON orders (user_id);
//	COMMIT;
//
// applied cleanly and returned PASS, while in production it raises
// `25001 CREATE INDEX CONCURRENTLY cannot run inside a transaction block`. The
// sandbox's own convenience erased a guaranteed production failure.
//
// The analyzer must therefore reason about the migration TEXT, not about what
// happened when it ran — running it is precisely what cannot see this.
type rsTx struct{}

// nonTransactional are statements Postgres refuses inside a transaction block.
// Each entry is matched as a prefix on the upper-cased, comment-stripped,
// whitespace-collapsed statement.
//
// The list is conservative: only statements that ALWAYS fail are here.
// `ALTER TYPE ... ADD VALUE` is version-conditional (it became transactional in
// PG 12) and is handled separately so the version gate can apply.
var nonTransactional = []struct{ prefix, what string }{
	{"CREATE INDEX CONCURRENTLY", "CREATE INDEX CONCURRENTLY"},
	{"CREATE UNIQUE INDEX CONCURRENTLY", "CREATE UNIQUE INDEX CONCURRENTLY"},
	{"DROP INDEX CONCURRENTLY", "DROP INDEX CONCURRENTLY"},
	{"REINDEX CONCURRENTLY", "REINDEX CONCURRENTLY"},
	{"REINDEX INDEX CONCURRENTLY", "REINDEX INDEX CONCURRENTLY"},
	{"REINDEX TABLE CONCURRENTLY", "REINDEX TABLE CONCURRENTLY"},
	{"VACUUM", "VACUUM"},
	{"CREATE DATABASE", "CREATE DATABASE"},
	{"DROP DATABASE", "DROP DATABASE"},
	{"CREATE TABLESPACE", "CREATE TABLESPACE"},
	{"DROP TABLESPACE", "DROP TABLESPACE"},
	{"ALTER SYSTEM", "ALTER SYSTEM"},
	{"CLUSTER", "CLUSTER"},
}

func (rsTx) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	major, hasVersion := estimate.Major(f.Meta.Engine.Version)

	var out []verdict.Finding
	depth := 0
	for _, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)
		if upper == "" {
			continue
		}

		// Track explicit transaction structure from the migration TEXT. These
		// statements are recorded by the capture but never executed, so the text
		// is the only evidence of the author's intent.
		if strings.HasPrefix(upper, "BEGIN") || strings.HasPrefix(upper, "START TRANSACTION") {
			depth++
			continue
		}
		if isTxEnd(upper) {
			if depth > 0 {
				depth--
			}
			continue
		}

		what, ok := nonTransactionalStatement(upper)
		if !ok {
			// ALTER TYPE ... ADD VALUE became transactional in PG 12. Without a
			// declared engine version rowshape must not guess (RFC §9.1), so it
			// says so rather than assuming the safe or the unsafe reading.
			if !isAlterTypeAddValue(upper) {
				continue
			}
			if hasVersion && major >= 12 {
				continue
			}
			what = "ALTER TYPE ... ADD VALUE"
			if !hasVersion {
				out = append(out, txFinding(what, depth, false, true))
				continue
			}
		}
		out = append(out, txFinding(what, depth, true, false))
	}
	return out
}

func nonTransactionalStatement(upper string) (string, bool) {
	// Longest prefix first, so "CREATE UNIQUE INDEX CONCURRENTLY" is not reported
	// as the shorter "CREATE INDEX CONCURRENTLY".
	best, bestLen := "", 0
	for _, nt := range nonTransactional {
		if strings.HasPrefix(upper, nt.prefix) && len(nt.prefix) > bestLen {
			best, bestLen = nt.what, len(nt.prefix)
		}
	}
	return best, bestLen > 0
}

func isAlterTypeAddValue(upper string) bool {
	return strings.HasPrefix(upper, "ALTER TYPE") && strings.Contains(upper, "ADD VALUE")
}

// txFinding reports a non-transactional statement.
//
// The severity turns on what rowshape can actually KNOW:
//
//   - Inside an explicit BEGIN/COMMIT in the migration itself, the failure is
//     certain. That is an ERROR.
//   - With no explicit transaction, it depends on the RUNNER. Alembic, Django,
//     Rails and Flyway wrap a migration file in a transaction by default; plain
//     `psql -f` does not. rowshape cannot see the runner's configuration, so it
//     says so and warns rather than guessing in either direction.
//
// Guessing "safe" here is what produced the original bug; guessing "unsafe"
// would make the tool cry wolf on every raw-SQL project that uses CONCURRENTLY
// correctly.
func txFinding(what string, depth int, versionKnown, versionUnknown bool) verdict.Finding {
	explicit := depth > 0

	// WARN, deliberately, and the framing matters.
	//
	// A false-positive sweep showed `CREATE INDEX CONCURRENTLY idx ON users (...)`
	// alone producing a WARN — which looked like the tool arguing with its own
	// advice, since CONCURRENTLY is exactly what RS-INDEX-001 and RS-INDEX-002
	// recommend. The obvious fix was to drop it to info severity. That turns out
	// to be WRONG for two reasons:
	//
	//  1. BuildResult DROPS an info finding whose verdict caps to PASS ("a clean
	//     certification is the silent default"), so info would make the caveat
	//     invisible rather than merely non-blocking.
	//  2. The hazard is real and common. Alembic, Django, Rails and Flyway wrap a
	//     migration file in a transaction BY DEFAULT, so a user who follows the
	//     CONCURRENTLY advice without also disabling that wrapping gets a
	//     guaranteed failure. That is the most frequent way this bites.
	//
	// So it stays a WARN — which does not block by default (warn-as-fail is
	// false) — and the wording is framed as the SECOND HALF OF THE REMEDIATION
	// rather than as a complaint. The tool is not contradicting its own advice; it
	// is finishing it.
	sev := verdict.SeverityWarn
	title := fmt.Sprintf("%s needs its runner's transaction wrapping disabled", what)
	detail := fmt.Sprintf(
		"Using %s is the right call — it is what avoids the exclusive lock. This is the other half of that "+
			"change: Postgres refuses it inside a transaction block (SQLSTATE 25001), and Alembic, Django, "+
			"Rails and Flyway all wrap each migration file in a transaction BY DEFAULT. If you are on one of "+
			"those and have not disabled it for this migration, it will fail every time. Plain `psql -f` and "+
			"golang-migrate do not wrap, so no change is needed there. rowshape cannot see your runner's "+
			"configuration, so it cannot decide this for you.",
		what)

	if explicit {
		sev = verdict.SeverityError
		title = fmt.Sprintf("%s is inside an explicit transaction and will fail", what)
		detail = fmt.Sprintf(
			"%s is refused by Postgres inside a transaction block (SQLSTATE 25001), and this migration opens "+
				"one explicitly with BEGIN. This will fail every time it is run. Note that rowshape's own "+
				"sandbox does NOT execute your BEGIN/COMMIT — each statement is applied in its own "+
				"transaction so locks can be inspected — so a clean apply here is not evidence that it works.",
			what)
	}
	if versionUnknown {
		detail += " (This statement is only non-transactional before PostgreSQL 12, and the fixture " +
			"declares no engine version, so rowshape cannot rule it out.)"
	}

	return verdict.Finding{
		Code:     "RS-TX-001",
		Severity: sev,
		Title:    title,
		Detail:   detail,
		Evidence: map[string]any{"statement": what, "explicit_transaction": explicit},
		// No fixture fact supports this: it is a property of the SQL and the
		// server, true of an empty database and a production one alike.
		DependsOn:   nil,
		Remediation: remediation("RS-TX-001"),
		Explain:     "rowshape explain RS-TX-001",
	}
}
