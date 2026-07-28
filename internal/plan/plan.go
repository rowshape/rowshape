// Package plan computes a dry-run diff of a migration against a live target's
// current schema — the shared core behind `rowshape plan --against` (CLI, P2-T15)
// and the plan_against MCP tool (P3-T6), so both produce the same diff with no
// reimplementation. Reading the target is strictly read-only
// (profile.ReadStructure runs in a read-only transaction, INV-BLAST-RADIUS-ZERO);
// nothing is ever applied.
package plan

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/rowshape/rowshape/internal/dsn"
	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/profile"
	"github.com/rowshape/rowshape/internal/sqlkind"
	"github.com/rowshape/rowshape/internal/validate"
)

// Item is one statement's planned effect on the live schema.
type Item struct {
	Statement string `json:"statement"`
	Change    string `json:"change"` // human description of the operation
	Status    string `json:"status"` // ok | conflict | missing-target
	Note      string `json:"note,omitempty"`
}

// ReadLiveSchema reads a live target's structure read-only and upgrades the facts
// to `exact`: they come from a real target, not a sample (PRD §15). The read runs
// inside a read-only transaction, so plan/verify can never write
// (INV-BLAST-RADIUS-ZERO).
func ReadLiveSchema(ctx context.Context, url string) (*fixture.Fixture, error) {
	if url == "" {
		return nil, fmt.Errorf("no target given")
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		// Deliberately does not echo the URL: it may carry a password.
		return nil, fmt.Errorf("could not parse the connection settings")
	}
	if w := dsn.InsecureWarning(cfg); w != "" {
		fmt.Fprintf(os.Stderr, "rowshape: warning: %s\n", w)
	}
	// plan/verify read a LIVE target, frequently the production one, so the lock
	// and idle-transaction limits matter most here. The statement limit stays
	// unset (ReadDefaults): a catalog read on a very large schema is slow but
	// legitimate, and failing it halfway serves nobody.
	dsn.Apply(cfg, dsn.ReadDefaults)
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		// Classified, never the driver's text: pgx embeds the host, port, user
		// and database in its dial errors.
		class, hint := dsn.ClassifyConnect(err)
		return nil, fmt.Errorf("connect to target failed: %s (%s)", class, hint)
	}
	defer func() { _ = conn.Close(ctx) }()
	f, err := profile.ReadStructure(ctx, conn, profile.Options{})
	if err != nil {
		return nil, fmt.Errorf("reading target schema failed: %w", err)
	}
	validate.MarkExact(f)
	return f, nil
}

// Items classifies each migration statement against the current schema, skipping
// transaction control. It applies nothing.
func Items(current *fixture.Fixture, stmts []string) []Item {
	var items []Item
	for _, raw := range stmts {
		s := collapse(raw)
		if s == "" || sqlkind.IsTxControl(s) {
			continue
		}
		items = append(items, classify(current, s))
	}
	return items
}

func classify(current *fixture.Fixture, s string) Item {
	up := strings.ToUpper(s)
	table := planTable(s, up)
	item := Item{Statement: truncate(s, 90), Status: "ok"}

	tableExists := false
	var tbl fixture.Table
	if table != "" {
		tbl, tableExists = current.Tables[table]
	}

	switch {
	case strings.Contains(up, "ADD COLUMN") || (strings.HasPrefix(up, "ALTER TABLE") && addsBareColumn(up)):
		col := addColumnName(s, up)
		item.Change = fmt.Sprintf("add column %s.%s", short(table), col)
		if !tableExists {
			item.Status, item.Note = "missing-target", "target table not present on the live schema"
		} else if _, ok := tbl.Columns[col]; ok {
			item.Status, item.Note = "conflict", "column already exists"
		} else {
			item.Note = "column will be added"
		}
	case strings.Contains(up, "SET NOT NULL"):
		item.Change = fmt.Sprintf("set NOT NULL on %s", short(table))
		item.Note = existsNote(tableExists)
		if !tableExists {
			item.Status = "missing-target"
		}
	case strings.Contains(up, "ADD CONSTRAINT"):
		item.Change = fmt.Sprintf("add constraint on %s", short(table))
		item.Note = existsNote(tableExists)
		if !tableExists {
			item.Status = "missing-target"
		}
	case strings.HasPrefix(up, "CREATE INDEX") || strings.HasPrefix(up, "CREATE UNIQUE INDEX"):
		item.Change = fmt.Sprintf("create index on %s", short(table))
		item.Note = existsNote(tableExists)
		if !tableExists {
			item.Status = "missing-target"
		}
	case strings.HasPrefix(up, "DROP TABLE"):
		item.Change = fmt.Sprintf("drop table %s", short(table))
		if !tableExists {
			item.Status, item.Note = "conflict", "table is already absent"
		} else {
			item.Note = "table will be dropped"
		}
	default:
		item.Change = "schema change"
		item.Note = existsNote(tableExists)
	}
	return item
}

func existsNote(exists bool) string {
	if exists {
		return "target present"
	}
	return "target table not present on the live schema"
}

// RedactURL strips credentials from a connection URL for display (PRD §5: the
// connection URL and any credentials are never logged, persisted, or written into
// a fixture).
//
// It cuts at the LAST "@" inside the authority, not the first. A password may
// legally contain an unencoded "@" — and people write them — so splitting on the
// first one leaves the tail of the password in the output:
//
//	postgres://admin:p@ss@host/db  ->  postgres://…@ss@host/db   (leaks "ss")
//
// The search is bounded to the authority so an "@" in a path or query string is
// not mistaken for a credential separator, which would corrupt the URL it is
// supposed to be merely displaying.
// pgx accepts two DSN spellings and so must this: the URL form handled below,
// and libpq's keyword/value form ("host=h user=u password=p"), which has no
// "://" and whose password would otherwise be returned verbatim.
func RedactURL(url string) string {
	// Dispatch on the SCHEME PREFIX, not on "://" appearing anywhere.
	//
	// The first cut of this tested `strings.Index(url, "://") < 0` to pick the
	// keyword/value branch, which meant any keyword/value DSN carrying "://"
	// inside a value took the URL branch instead — found no "@" in what it
	// treated as the authority, and returned the input VERBATIM:
	//
	//	host=db.prod password=hunter2 options='-c foo=bar://baz'   (unchanged)
	//	host=db password=ab://cd                                   (unchanged)
	//
	// i.e. the exact leak this function exists to prevent, on a realistic input.
	// libpq URIs must BEGIN with one of these two schemes, so anchoring the test
	// is both correct and unambiguous.
	if !strings.HasPrefix(url, "postgres://") && !strings.HasPrefix(url, "postgresql://") {
		return redactKeywordValue(url)
	}
	s := strings.Index(url, "://")
	start := s + 3

	// The authority runs to the first "/", "?", or "#" after the scheme.
	end := len(url)
	for _, sep := range []string{"/", "?", "#"} {
		if i := strings.Index(url[start:], sep); i >= 0 && start+i < end {
			end = start + i
		}
	}

	// A host cannot contain "@", so the last one in the authority ends the
	// userinfo — everything before it is credentials.
	at := strings.LastIndex(url[start:end], "@")
	if at < 0 {
		return url
	}
	return url[:start] + "…@" + url[start+at+1:]
}

// redactKeywordValue strips the password from a libpq keyword/value DSN, the
// form pgx.ParseConfig accepts alongside URLs:
//
//	host=db user=svc password=hunter2 sslmode=require  ->  host=db user=svc password=… sslmode=require
//
// Values may be single-quoted and may contain backslash escapes, so the scan
// follows libpq's own quoting rules rather than splitting on whitespace — a
// password of 'a b c' is one value, not three.
//
// It scans instead of regex-replacing so that a literal "password=" appearing
// INSIDE another value (an options= string, say) cannot be mistaken for a
// keyword. Anything unparseable is passed through as a keyword with no value,
// which cannot expose a password because a password only ever appears as the
// value of the password keyword.
func redactKeywordValue(raw string) string {
	var b strings.Builder
	i := 0
	for i < len(raw) {
		// Whitespace between pairs is preserved verbatim.
		if isSpace(raw[i]) {
			b.WriteByte(raw[i])
			i++
			continue
		}
		// Keyword: up to "=" or whitespace.
		ks := i
		for i < len(raw) && raw[i] != '=' && !isSpace(raw[i]) {
			i++
		}
		keyword := raw[ks:i]
		b.WriteString(keyword)
		// Optional space, "=", optional space.
		for i < len(raw) && isSpace(raw[i]) {
			b.WriteByte(raw[i])
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			continue // a bare token with no value; nothing to redact
		}
		b.WriteByte('=')
		i++
		for i < len(raw) && isSpace(raw[i]) {
			b.WriteByte(raw[i])
			i++
		}
		// Value: single-quoted (with backslash escapes) or bare.
		vs := i
		if i < len(raw) && raw[i] == '\'' {
			i++
			for i < len(raw) && raw[i] != '\'' {
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
				}
				i++
			}
			if i < len(raw) {
				i++ // closing quote
			}
		} else {
			for i < len(raw) && !isSpace(raw[i]) {
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
				}
				i++
			}
		}
		if strings.EqualFold(keyword, "password") {
			b.WriteString("…")
		} else {
			b.WriteString(raw[vs:i])
		}
	}
	return b.String()
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func short(table string) string {
	if i := strings.LastIndexByte(table, '.'); i >= 0 {
		return table[i+1:]
	}
	return table
}

// planTable extracts the target table of a schema statement.
func planTable(s, up string) string {
	fields := strings.Fields(s)
	switch {
	case strings.HasPrefix(up, "ALTER TABLE"):
		i := 2
		if i < len(fields) && strings.EqualFold(fields[i], "ONLY") {
			i++
		}
		if i < len(fields) {
			return strings.Trim(fields[i], `"`)
		}
	case strings.HasPrefix(up, "DROP TABLE"):
		i := 2
		if i < len(fields) && strings.EqualFold(fields[i], "IF") {
			i += 2 // IF EXISTS
		}
		if i < len(fields) {
			return strings.Trim(strings.TrimRight(fields[i], ";"), `"`)
		}
	case strings.Contains(up, " ON "):
		j := strings.Index(up, " ON ")
		rest := strings.Fields(s[j+4:])
		if len(rest) > 0 {
			// The column list may abut the table with no space
			// (CREATE INDEX i ON t(col)), so cut at the first "(" as well
			// as trimming a trailing one; otherwise the table name carries
			// "(col)" and never matches the live schema — a real index on an
			// existing table would misreport as missing-target.
			tok := rest[0]
			if k := strings.IndexByte(tok, '('); k >= 0 {
				tok = tok[:k]
			}
			return strings.Trim(tok, `"`)
		}
	}
	return ""
}

func addsBareColumn(up string) bool {
	return strings.Contains(up, " ADD ") && !strings.Contains(up, "ADD CONSTRAINT") &&
		!strings.Contains(up, "ADD PRIMARY") && !strings.Contains(up, "ADD UNIQUE") &&
		!strings.Contains(up, "ADD FOREIGN") && !strings.Contains(up, "ADD CHECK")
}

func addColumnName(s, up string) string {
	key := "ADD COLUMN "
	i := strings.Index(up, key)
	if i < 0 {
		if k := strings.Index(up, " ADD "); k >= 0 {
			i, key = k, " ADD "
		} else {
			return ""
		}
	}
	fields := strings.Fields(s[i+len(key):])
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"`)
}
