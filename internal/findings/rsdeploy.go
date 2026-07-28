package findings

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/validate"
	"github.com/rowshape/rowshape/internal/verdict"
)

func init() { validate.Register(rsDeploy{}) }

// rsDeploy covers hazards where the DATABASE is fine and the running APPLICATION
// is not.
//
// Every other analyzer in the catalog asks a question about the data or the lock:
// will this constraint build, how long will this rewrite hold ACCESS EXCLUSIVE,
// is this irreversible. A rename is none of those. It is instant, it takes a
// brief lock, it applies cleanly, and it leaves the database in a perfectly good
// state — while every application instance still running the previous release
// starts raising `column "email" does not exist` the moment it commits.
//
// That is why this rule has to be STATIC. No amount of executing the migration
// against a hydrated database can surface it, because nothing about the database
// is wrong. It is also, in practice, the most common migration outage there is,
// and the catalog was silent on it: both `ALTER TABLE ... RENAME COLUMN` and
// `ALTER TABLE ... RENAME TO` returned PASS with zero findings.
type rsDeploy struct{}

func (rsDeploy) Analyze(f *fixture.Fixture, c *validate.Capture) []verdict.Finding {
	var out []verdict.Finding
	for _, st := range c.Statements {
		clean := collapseSpaces(stripSQLComments(st.SQL))
		upper := strings.ToUpper(clean)
		if !strings.HasPrefix(upper, "ALTER TABLE") {
			continue
		}
		switch {
		case strings.Contains(upper, " RENAME"):
			if fnd, ok := renameFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}
		case strings.Contains(upper, "REPLICA IDENTITY"):
			out = append(out, replicaIdentityFinding(f, clean, upper))
		case strings.Contains(upper, "DROP NOT NULL"):
			if fnd, ok := dropNotNullFinding(f, clean, upper); ok {
				out = append(out, fnd)
			}
		}
	}
	return out
}

// renameFinding reports the deploy-ordering hazard of a rename.
func renameFinding(f *fixture.Fixture, clean, upper string) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(clean))

	// RENAME COLUMN <old> TO <new> vs RENAME [TO] <new> (the table form).
	var what, oldName, newName string
	switch {
	case strings.Contains(upper, "RENAME COLUMN"):
		what = "column"
		oldName = identAfter(clean, upper, "RENAME COLUMN")
		newName = identAfter(clean, upper, " TO ")
	case strings.Contains(upper, "RENAME CONSTRAINT"):
		// A constraint rename breaks nothing an application references by name in
		// normal use; it is called out only so the family's coverage is explicit.
		return verdict.Finding{}, false
	default:
		what = "table"
		oldName = shortTable(table)
		newName = identAfter(clean, upper, " TO ")
	}
	if newName == "" {
		return verdict.Finding{}, false
	}
	if oldName == "" {
		oldName = "the " + what
	}

	subject := oldName
	if what == "column" {
		subject = shortTable(table) + "." + oldName
	}

	return verdict.Finding{
		Code:     "RS-DEPLOY-001",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("Renaming %s %s breaks running code that still uses the old name", what, subject),
		Detail: fmt.Sprintf(
			"The migration itself is safe and fast — this is not a lock or a data problem. The hazard is ORDERING: "+
				"from the instant this commits, every application instance still running the previous release "+
				"references %q, which no longer exists, and fails. Rolling deploys and blue/green make this a "+
				"certainty rather than a race, because old and new code run at the same time by design. Nothing "+
				"about applying this migration to a database reveals the problem, because the database is fine.",
			oldName),
		Evidence: map[string]any{"kind": what, "from": oldName, "to": newName},
		// Deliberately no DependsOn: this conclusion rests on no fixture fact. It
		// is true of a rename on an empty table and on a billion-row one alike,
		// and citing a fact that does not support it would put a false provenance
		// trail into a signed document (the same reason indexUniqueFinding
		// declines to cite the whole-column `unique` fact).
		DependsOn:   nil,
		Remediation: remediation("RS-DEPLOY-001"),
		Explain:     "rowshape explain RS-DEPLOY-001",
	}, true
}

// replicaIdentityFinding reports a change that silently breaks downstream
// consumers.
//
// Same shape as a rename, and the same reason it belongs in this family: the
// database is left perfectly healthy. What breaks is OUTSIDE it — logical
// replication slots, CDC pipelines, read replicas built on Debezium and friends
// — and it breaks quietly, by emitting UPDATE and DELETE events with no
// identifying key rather than by raising an error anyone would notice.
func replicaIdentityFinding(f *fixture.Fixture, clean, upper string) verdict.Finding {
	table := resolveTable(f, alterTableTarget(clean))

	mode := "the configured identity"
	switch {
	case strings.Contains(upper, "REPLICA IDENTITY NOTHING"):
		mode = "NOTHING"
	case strings.Contains(upper, "REPLICA IDENTITY DEFAULT"):
		mode = "DEFAULT"
	case strings.Contains(upper, "REPLICA IDENTITY FULL"):
		mode = "FULL"
	case strings.Contains(upper, "REPLICA IDENTITY USING INDEX"):
		mode = "USING INDEX"
	}

	detail := "REPLICA IDENTITY controls what a logical decoding stream can identify a changed row BY. " +
		"Changing it does not error and does not affect queries, so nothing about applying this migration " +
		"reveals a problem — but downstream consumers (logical replication subscribers, CDC pipelines, " +
		"analytics mirrors) can silently start receiving UPDATE and DELETE events they cannot match to a row."
	if mode == "NOTHING" {
		detail = "REPLICA IDENTITY NOTHING means UPDATE and DELETE events carry NO old-row identity at all. " +
			"Logical replication subscribers cannot apply them, and CDC consumers see changes they cannot " +
			"attribute to any row. The database itself is unaffected, so nothing about applying this " +
			"migration surfaces the breakage — it appears downstream, later, as silent divergence."
	}
	if mode == "FULL" {
		detail += " FULL is the safe-but-costly direction: every UPDATE and DELETE writes the entire old row " +
			"into WAL, which can substantially increase WAL volume on a busy table."
	}

	return verdict.Finding{
		Code:        "RS-DEPLOY-002",
		Severity:    verdict.SeverityWarn,
		Title:       fmt.Sprintf("REPLICA IDENTITY %s on %s changes what downstream consumers can identify", mode, shortTable(table)),
		Detail:      detail,
		Evidence:    map[string]any{"mode": mode},
		DependsOn:   nil,
		Remediation: remediation("RS-DEPLOY-002"),
		Explain:     "rowshape explain RS-DEPLOY-002",
	}
}

// dropNotNullFinding reports relaxing a constraint that running code relies on.
//
// The migration is instant and safe FOR THE DATABASE. The hazard is that
// application code, ORM models and downstream schemas were written against a
// column that could never be null, and none of them are re-checked when the
// guarantee is withdrawn — the first null arrives later, at runtime, somewhere
// else.
func dropNotNullFinding(f *fixture.Fixture, clean, upper string) (verdict.Finding, bool) {
	table := resolveTable(f, alterTableTarget(clean))
	col := identAfter(clean, upper, "ALTER COLUMN")
	if col == "" {
		return verdict.Finding{}, false
	}
	subject := shortTable(table) + "." + col

	return verdict.Finding{
		Code:     "RS-DEPLOY-003",
		Severity: verdict.SeverityWarn,
		Title:    fmt.Sprintf("Dropping NOT NULL on %s withdraws a guarantee running code relies on", subject),
		Detail: "The migration is instant and safe for the database. The hazard is the CONTRACT: application " +
			"code, ORM models, serializers and downstream schemas were written against a column that could " +
			"never be null, and none of them are re-checked when that guarantee is withdrawn. Nothing fails " +
			"at migration time; the first null arrives later, at runtime, somewhere else. Note this is the " +
			"reverse of RS-DATA-001, which covers ADDING the constraint.",
		Evidence:    map[string]any{"column": col},
		DependsOn:   nil,
		Remediation: remediation("RS-DEPLOY-003"),
		Explain:     "rowshape explain RS-DEPLOY-003",
	}, true
}
