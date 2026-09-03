package hydrate

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/sqlkind"
)

// Options controls a hydration run.
type Options struct {
	// Seed drives all generation; the same seed reproduces the same output
	// (RFC §10).
	Seed int64
	// Scale is the fraction of declared rows to synthesize (RFC §9). 1.0 means
	// the full declared count; 0.01 means 1%. Values <= 0 default to 1.0.
	Scale float64
	// MaxRows caps the synthesized rows per table (0 = no cap), a safety valve so
	// a huge declared count can't try to generate billions of rows.
	MaxRows int64
	// MaxCells bounds the total materialized cells (rows x columns) across all
	// tables. Zero uses DefaultMaxCells; negative means explicitly unbounded.
	// This is the backstop MaxRows was not: MaxRows defaults to no cap, so
	// nothing prevented a billion-row fixture from being materialized in memory.
	MaxCells int64
}

// Result is the generated data for a whole fixture.
type Result struct {
	Tables []GeneratedTable
}

// GeneratedTable is one table's synthesized rows in column order.
type GeneratedTable struct {
	Name         string
	Columns      []string
	Rows         [][]any
	DeclaredRows int64 // production rows the fixture declares (RFC §9, for P1-T8)
}

// Generate synthesizes rows for every table in the fixture (RFC §13). Tables and
// columns are processed in sorted order so generation never depends on Go map
// iteration order (RFC §10).
func Generate(f *fixture.Fixture, opts Options) (*Result, error) {
	if f == nil {
		return nil, fmt.Errorf("hydrate: nil fixture")
	}
	scale := opts.Scale
	if scale <= 0 {
		scale = 1.0
	}

	tableNames := sortedKeys(f.Tables)
	res := &Result{}
	// A map of table -> hydrated row count, needed so a foreign key knows how many
	// parent rows exist.
	rowCounts := map[string]int64{}
	for _, name := range tableNames {
		rowCounts[name] = hydratedRowCount(f.Tables[name].Rows.Value, scale, opts.MaxRows)
	}
	if err := checkBudget(f, tableNames, rowCounts, opts.MaxCells); err != nil {
		return nil, err
	}

	for _, name := range tableNames {
		tbl := f.Tables[name]
		gt, err := generateTable(f, name, tbl, opts.Seed, rowCounts)
		if err != nil {
			return nil, fmt.Errorf("hydrate %s: %w", name, err)
		}
		res.Tables = append(res.Tables, gt)
	}
	return res, nil
}

// DefaultMaxCells bounds how much hydrate will materialize, in cells (a cell is
// one column of one row).
//
// Generate builds the ENTIRE result in memory — a [][]any per table, all tables
// retained in Result — and target.Load then makes another full copy before COPY.
// Meanwhile validate and hydrate both default --max-rows to 0, meaning NO CAP.
// So `rowshape validate` against a fixture that legitimately declares
// rows: 1000000000 attempted a billion-row in-memory materialization by default
// and died with an OOM that named nothing useful.
//
// Each cell costs at least a 16-byte interface header plus its value, so 50M
// cells is roughly 2-3 GB — generous enough that no realistic fixture trips it
// by accident, and bounded enough to fail before the OOM killer does.
const DefaultMaxCells = 50_000_000

// checkBudget refuses a hydrate that would not fit in memory, rather than
// truncating it.
//
// Refusing is deliberate. Silently capping the row count would change the
// duration estimates and therefore the VERDICT, without telling anyone — the
// same class of quiet wrongness as reporting `instant` from a 1ms basis. An
// error that names the projected size and the two flags that fix it is both
// honest and actionable; an OOM is neither.
func checkBudget(f *fixture.Fixture, tableNames []string, rowCounts map[string]int64, maxCells int64) error {
	if maxCells == 0 {
		maxCells = DefaultMaxCells
	}
	if maxCells < 0 {
		return nil // explicitly unbounded: the caller has accepted the risk
	}

	var total int64
	var biggest string
	var biggestCells int64
	for _, name := range tableNames {
		cols := int64(len(f.Tables[name].Columns))
		if cols == 0 {
			cols = 1
		}
		rows := rowCounts[name]
		cells := rows * cols
		// Detect the multiplication overflowing rather than testing for a
		// negative result: 1<<62 * 8 wraps to exactly 0, which would have slipped
		// past a sign check and let an absurd fixture through as "no cells".
		if rows != 0 && cells/cols != rows {
			return fmt.Errorf(
				"hydrate: table %s declares %d rows, which overflows when multiplied by its column count; "+
					"use --max-rows or --scale", name, rows)
		}
		total += cells
		if total < 0 { // the running sum overflowed
			return fmt.Errorf("hydrate: this fixture's total row count overflows; use --max-rows or --scale")
		}
		if cells > biggestCells {
			biggest, biggestCells = name, cells
		}
	}
	if total <= maxCells {
		return nil
	}
	return fmt.Errorf(
		"hydrate: this fixture would materialize about %s cells in memory (limit %s); "+
			"the largest table is %s at %s cells. Use --max-rows to cap rows per table, or --scale to "+
			"hydrate a fraction of the declared counts. Note that scaling down changes the measured basis "+
			"for duration estimates, which rowshape reports as the basis alongside each estimate",
		humanInt(total), humanInt(maxCells), biggest, humanInt(biggestCells))
}

// humanInt renders a large count readably for an error message.
func humanInt(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// hydratedRowCount converts a declared count and scale into a row count, capped
// by MaxRows.
//
// A NON-EMPTY table floors at 1, so `--scale 0.001` against a small table still
// exercises it rather than rounding away to nothing. An EMPTY one does not.
// Emptiness is a FACT the fixture recorded, and overriding it invents a row that
// does not exist — which made the most common safe migration there is come back
// as a failure: `ALTER TABLE <empty> ADD COLUMN ... NOT NULL` succeeds in
// production precisely because there are no rows to fail, and returned FAIL here.
// A false FAIL is worse than noise, because an agent trained by the rule to trust
// FAIL will rewrite a migration that was already correct.
func hydratedRowCount(declared int64, scale float64, maxRows int64) int64 {
	if declared <= 0 {
		return 0
	}
	n := int64(float64(declared) * scale)
	if n < 1 {
		n = 1
	}
	if maxRows > 0 && n > maxRows {
		n = maxRows
	}
	return n
}

// generateTable synthesizes one table.
func generateTable(f *fixture.Fixture, name string, tbl fixture.Table, seed int64, rowCounts map[string]int64) (GeneratedTable, error) {
	n := rowCounts[name]
	colNames := sortedKeys(tbl.Columns)

	// Refuse a type there is no synthesis rule for BEFORE generating anything, and
	// name the column. The alternative is what used to happen: generation produced a
	// generic "val_141", and the failure surfaced hundreds of thousands of rows
	// later as a pgx internals message from inside COPY —
	//
	//	unable to encode "val_141" into binary format for inet (OID 869)
	//
	// which names neither the table nor the column and reads as a rowshape defect
	// rather than a decision the operator has to make. An enum or domain the fixture
	// DEFINES is not affected: it is resolved before this check.
	for _, col := range colNames {
		c := tbl.Columns[col]
		if _, isEnum := f.EnumLabels(c.Type); isEnum {
			continue
		}
		// A column the database computes needs no synthesis rule, so refusing on its
		// type would reject a fixture rowshape can hydrate perfectly well.
		if generatedByDatabase(c) {
			continue
		}
		if unsupportedType(f.ResolveType(c.Type)) {
			return GeneratedTable{}, fmt.Errorf(
				"%s.%s has type %q, which rowshape cannot synthesize values for: "+
					"exclude the column from the fixture, or hydrate against a "+
					"--target that already holds data of this shape", name, col, c.Type)
		}
	}

	// A STORED generated column is computed BY the database from the other columns,
	// so supplying a value for it is an error — `cannot insert a non-DEFAULT value
	// into column "total"` — and would take the whole COPY down now that the target
	// actually declares the column as generated. Dropping it from the insert list is
	// not a loss: the value the database computes IS the production shape, more
	// faithfully than anything synthesis could invent.
	//
	// Identity columns are NOT dropped. GENERATED BY DEFAULT accepts an explicit
	// value, and supplying one is what keeps a foreign key's parent ids matching the
	// ids the parent table was actually loaded with.
	insertable := make([]string, 0, len(colNames))
	for _, col := range colNames {
		if generatedByDatabase(tbl.Columns[col]) {
			continue
		}
		insertable = append(insertable, col)
	}

	gt := GeneratedTable{
		Name:         name,
		Columns:      insertable,
		Rows:         make([][]any, n),
		DeclaredRows: tbl.Rows.Value,
	}
	colNames = insertable
	for i := range gt.Rows {
		gt.Rows[i] = make([]any, len(colNames))
	}

	// Precompute foreign-key assignments per column so fan-out shape is honored.
	fkAssign := map[string][]int64{}
	fkRefs := map[string]fixture.Reference{}
	for _, ref := range tbl.References {
		parentRows := rowCounts[parentTable(ref.To)]
		fkAssign[ref.Column] = assignForeignKeys(seed, name, ref, n, parentRows, tbl.Rows.Value)
		fkRefs[ref.Column] = ref
	}

	// What the table's CHECK constraints say about each column's legal values. The
	// disposable database now enforces those constraints (target.DeferredConstraints),
	// so synthesis has to satisfy them or they get skipped — and a CHECK the target
	// does not enforce is one a migration can violate and still be certified for.
	domains := checkDomains(tbl)

	for ci, col := range colNames {
		c := tbl.Columns[col]
		fk, isFK := fkAssign[col]
		dom, constrained := domains[col]
		for ord := int64(0); ord < n; ord++ {
			var v any
			switch {
			case isFK:
				// fk[ord] is the assigned parent ordinal; map it to the id value
				// the parent's identity column actually generated for that ordinal.
				v = parentIDValue(seed, f, fkRefs[col], fk[ord], rowCounts[parentTable(fkRefs[col].To)])
			default:
				v = generateValue(f, seed, name, col, c, ord)
				if constrained {
					v = applyCheckDomain(v, dom, cellRNG(seed, name, col, ord))
				}
			}
			gt.Rows[ord][ci] = v
		}
	}
	return gt, nil
}

// generatedByDatabase reports whether the target computes this column itself, so
// hydration must not supply a value for it.
//
// Only a STORED generated column qualifies, and only when the fixture describes it
// well enough that the target actually declared it generated. Where the expression
// is missing or `opaque` (privacy:strict), the target created an ordinary column —
// which then requires a value like any other, so dropping it would leave it NULL
// or violate NOT NULL. The two decisions have to agree, so both read the same
// field rather than each deciding for itself.
//
// An identity column is deliberately excluded. GENERATED BY DEFAULT accepts an
// explicit value, and supplying one is what keeps a child table's foreign keys
// pointing at the ids the parent was actually loaded with.
func generatedByDatabase(c fixture.Column) bool {
	// "virtual" is NOT included: a PostgreSQL 18 virtual generated column is not
	// reproduced as generated (the syntax does not exist on earlier targets), so the
	// target holds an ordinary column that needs a value like any other.
	return c.Generated == "stored" && c.GeneratedExpression != "" && c.GeneratedExpression != "opaque"
}

// applyCheckDomain brings a generated value inside what the column's CHECK
// constraints allow, leaving it alone when they say nothing about it.
//
// A NULL is never touched: a CHECK is satisfied by NULL (it evaluates to unknown,
// which does not fail the constraint), and the null fraction is a recorded fact
// this must not disturb.
func applyCheckDomain(v any, dom checkDomain, r *rng) any {
	if v == nil {
		return nil
	}
	// A literal set replaces the value outright, the same way an enum's labels do:
	// the constraint says these are the only legal values, so nothing else will do.
	if len(dom.Values) > 0 {
		return dom.Values[r.intn(int64(len(dom.Values)))]
	}
	// Numeric bounds CLAMP rather than replace, so the column keeps the distribution
	// the fixture recorded wherever that distribution already satisfies the bound.
	return clampNumeric(v, dom.Min, dom.Max)
}

// clampNumeric brings a numeric value within inclusive bounds, preserving its Go
// type so COPY still encodes it for the column. A non-numeric value is returned
// unchanged — bounds cannot apply to it, and converting would be a guess.
func clampNumeric(v any, min, max *float64) any {
	switch n := v.(type) {
	case int64:
		f := clampFloat(float64(n), min, max)
		return int64(f)
	case int:
		return int(clampFloat(float64(n), min, max))
	case float64:
		return clampFloat(n, min, max)
	default:
		return v
	}
}

func clampFloat(f float64, min, max *float64) float64 {
	if min != nil && f < *min {
		f = *min
	}
	if max != nil && f > *max {
		f = *max
	}
	return f
}

// generateValue synthesizes one cell. Nulls are placed by a deterministic
// low-discrepancy quota so the null fraction is honored within a row or two
// (RFC §13, ±0.5%) and a cell's null-ness never changes as --scale grows.
func generateValue(f *fixture.Fixture, seed int64, table, column string, c fixture.Column, ord int64) any {
	if c.Nullable && c.NullFraction != nil && isNullAt(ord, c.NullFraction.Value) {
		return nil
	}
	r := cellRNG(seed, table, column, ord)

	// An enum column accepts ONLY its own labels, so its value has to come from the
	// type definition rather than from a format hint or the type-name fallback: a
	// generic string is not a member of the type and Postgres rejects it outright.
	// The fixture's `distinct` for such a column is at most the label count, so
	// drawing a label reproduces the cardinality as well.
	if labels, isEnum := f.EnumLabels(c.Type); isEnum && len(labels) > 0 {
		return labels[r.intn(int64(len(labels)))]
	}

	// An ARRAY of an enum has the same constraint on its elements, and the array
	// path below builds elements from the type name alone — with no way to reach the
	// type definitions — so it would fill an `app.status[]` with generic strings that
	// are not members of the element type. Resolved here, where the definitions are
	// in hand.
	if elem, isArray := strings.CutSuffix(strings.TrimSpace(c.Type), "[]"); isArray {
		if labels, isEnum := f.EnumLabels(elem); isEnum && len(labels) > 0 {
			return `{"` + labels[r.intn(int64(len(labels)))] + `"}`
		}
	}

	// A domain is a base type plus constraints, so generate for the base. Rewriting
	// c.Type here (rather than at each use) also gets the character-width clamp and
	// the numeric range right for a domain over varchar(n) or over integer.
	c.Type = f.ResolveType(c.Type)

	// A unique column derives its value from the ordinal so values never collide
	// (RFC §13 honor `unique`). A non-unique column draws a bucket in [0, distinct)
	// so only about `distinct` distinct values appear.
	unique := c.Unique != nil && c.Unique.Value

	// A skewed numeric column carries a histogram; sample from it so hydrate
	// reproduces the skew, not just the mean/range (RFC §6.2). Each equi-depth
	// bucket holds equal rows, so picking a bucket uniformly and a value within it
	// recreates the original density.
	if !unique && c.Histogram != nil && categorize(c.Type) == "numeric" {
		if v, ok := sampleHistogram(c.Histogram, r); ok {
			return v
		}
	}

	var n int64
	if unique {
		n = ord
	} else if d := distinctOf(c); d > 0 {
		n = r.intn(d)
	} else {
		n = r.intn(1 << 20)
	}
	return fakeValue(c, n, r, unique)
}

// sampleHistogram draws an integer from an equi-depth histogram: a uniformly
// chosen bucket, then a uniform value within that bucket's bounds. This
// reproduces the column's skew — dense value regions have many narrow buckets
// and so receive proportionally many rows.
func sampleHistogram(h *fixture.Histogram, r *rng) (any, bool) {
	if h == nil || len(h.Bounds) < 2 {
		return nil, false
	}
	b := int(r.intn(int64(len(h.Bounds) - 1)))
	lo, lok := toFloat(h.Bounds[b])
	hi, hok := toFloat(h.Bounds[b+1])
	if !lok || !hok {
		return nil, false
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	span := hi - lo
	// The float64() conversion is LOAD-BEARING, not decoration. The Go spec lets
	// an implementation fuse a multiply and an add into a single FMA with only
	// one rounding, and the compiler DOES fuse this shape on arm64, ppc64 and
	// s390x while amd64 does not — so the same fixture and seed would synthesize
	// different values on an Apple Silicon laptop than in amd64 CI, breaking
	// INV-DETERMINISM's "byte-identical on ANY platform".
	//
	// The spec also gives the remedy: "An explicit floating-point type conversion
	// rounds to the precision of the target type, preventing fusion that would
	// discard that rounding." Measured over 2M randomly drawn (lo, r, span) at
	// realistic fixture magnitudes, fusing changes 14.15% of the float results
	// and 0.07% of the int64 values that actually reach the SQL — roughly one row
	// in 1,380. Do not "simplify" this back.
	v := lo + float64(r.float64()*span)
	return toI64(v), true
}

// toFloat best-effort converts a histogram bound to a float64.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

// fakeValue produces an obviously-fake value of the right shape (RFC §13): the
// hydrator reproduces shape, never content. n selects which value (the ordinal
// for unique columns, a bucket otherwise).
// unique tells the numeric fallback that n is a row ordinal which must map
// injectively, so it must not wrap into the declared range (see numericInRange).
// The format-hinted branches above are already injective in n.
//
// Every result passes through fitCharWidth, so a value can never be wider than
// the column it is about to be written into. The clamp lives here rather than in
// generateValue because parentUniqueValue also calls fakeValue directly to
// reproduce a parent's key for a foreign key: clamping in only one of the two
// paths would make a child cell differ from the parent id it references and
// manufacture an orphan.
func fakeValue(c fixture.Column, n int64, r *rng, unique bool) any {
	return fitCharWidth(c, n, rawFakeValue(c, n, r, unique), unique)
}

// fitCharWidth keeps a synthesized string within the width the fixture and the
// column type between them allow.
//
// TWO limits apply, and the tighter one wins:
//
//   - the TYPE's character limit. A format hint renders content of its own natural
//     length with no reference to the column: `enum_like` with no declared values
//     renders "value_0" (7 characters), and `pull` reports a Postgres char(3)
//     currency column as exactly that, so the COPY aborted with `value too long for
//     type character(3)`.
//   - the fixture's `length.max`. For a text column this is the ONLY shape §6.1
//     permits, and ignoring it produces a WRONG VERDICT rather than merely odd
//     content: a varchar(12) column whose production values are 2-5 characters
//     hydrated as 9-character values, so `ALTER COLUMN ... TYPE varchar(8)` — which
//     production data satisfies comfortably — came back FAIL with `value too long`.
//     A verdict the migration would not have produced is the failure this format
//     exists to prevent, in the direction that destroys trust rather than safety.
//
// An over-wide value becomes a PREFIX of the rendered string followed by n in base
// 36, sized to fill the width exactly. That keeps all three properties that matter:
//
//   - injective in n while 36^k values remain, so a unique column stays unique;
//   - cardinality preserved, which plain truncation destroys whenever the varying
//     part sits at the end ("value_0" and "value_1" both truncate to "val");
//   - still recognizable, which a bare ordinal destroys — an email clamped to 19
//     characters stays "user_00000@examp3o" rather than becoming "3o".
//
// Only character types are touched: a non-string value has no character width to
// violate.
func fitCharWidth(c fixture.Column, n int64, v any, unique bool) any {
	s, isString := v.(string)
	if !isString {
		return v
	}
	width, bounded := effectiveWidth(c)
	if !bounded || len(s) <= width {
		return s
	}
	tail := base36Tail(n, width)
	if unique {
		// The ordinal ALONE, filling the width. base36Tail is injective in n on its
		// own, but combining it with a rendered prefix is not: the tail's length
		// varies with n, so the prefix length varies too, and two different ordinals
		// can assemble the same string — n=0 gives "va"+"0" while n=360 gives
		// "v"+"a0", both "va0". Recognizability is worth less than the uniqueness the
		// fixture states as an exact fact.
		return tail
	}
	if len(tail) >= width {
		return tail
	}
	// Not unique: keep as much of the rendered value as the ordinal leaves room for,
	// so an email clamped to 19 characters still reads as one. Distinct ordinals can
	// now collide, which is acceptable where only the cardinality is being
	// approximated — and the varying tail is what keeps that cardinality from
	// collapsing the way plain truncation does.
	return s[:width-len(tail)] + tail
}

// effectiveWidth is the tightest character limit that applies to a column: the
// type's own limit, the fixture's declared length.max, or whichever is smaller when
// both are present.
//
// length.max is a fact about production values and the type limit is a fact about
// the column, so honoring only one of them is wrong in a different direction each
// time — exceeding the type aborts the load, and exceeding the declared length
// fabricates data wider than production ever held.
func effectiveWidth(c fixture.Column) (int, bool) {
	width, bounded := sqlkind.CharMaxLength(c.Type)

	if c.Length != nil && c.Length.Max != nil {
		if m := *c.Length.Max; m > 0 && m <= int64(maxInt) {
			declared := int(m)
			if !bounded || declared < width {
				width, bounded = declared, true
			}
		}
	}
	return width, bounded
}

// maxInt guards the int64 -> int narrowing of a declared length on 32-bit builds.
const maxInt = int64(^uint(0) >> 1)

// arrayLiteral renders a one-element array literal whose element is generated from
// the element type, so an `integer[]` gets a number and a `text[]` a string.
//
// One element, not the real length: the fixture records nothing about array length
// (RFC §6 has no such statistic), so any other count would be invented. One is the
// honest minimum that still exercises the column as a non-empty array.
func arrayLiteral(c fixture.Column, n int64, r *rng, unique bool) string {
	elem := fixture.Column{
		Type: strings.TrimSuffix(strings.TrimSpace(c.Type), "[]"),
		// The element carries no format hint: a format describes the COLUMN's
		// content, and applying it to the element would put an email inside an
		// integer[] as readily as inside a text[].
	}
	v := rawFakeValue(elem, n, r, unique)
	s := fmt.Sprintf("%v", v)
	// Quote the element and escape the characters that are structural inside an
	// array literal, so a generated value can never change the literal's shape.
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `{"` + s + `"}`
}

// bitLiteral renders a bit string of exactly the column's declared width, which
// bit(n) requires — a shorter or longer string is rejected outright. An unadorned
// `bit` is bit(1), and `bit varying` accepts anything up to its width, so the
// declared width is a safe length for both.
func bitLiteral(c fixture.Column, n int64) string {
	width, ok := sqlkind.TypeLength(c.Type)
	if !ok || width <= 0 {
		width = 1
	}
	if width > 64 {
		width = 64
	}
	out := make([]byte, width)
	for i := range width {
		// Fill from the low-order bit up, so consecutive n differ.
		if (n>>(width-1-i))&1 == 1 {
			out[i] = '1'
		} else {
			out[i] = '0'
		}
	}
	return string(out)
}

// base36Tail renders n in base 36, keeping at most the last `width` digits — that
// is, n mod 36^width. It is injective for n < 36^width and always at most `width`
// characters, so it fits by construction.
func base36Tail(n int64, width int) string {
	if width <= 0 {
		return ""
	}
	if n < 0 {
		n = -n
	}
	s := strconv.FormatInt(n, 36)
	if len(s) > width {
		s = s[len(s)-width:]
	}
	return s
}

// rawFakeValue is fakeValue before the character-width clamp.
func rawFakeValue(c fixture.Column, n int64, r *rng, unique bool) any {
	switch c.Format {
	case "email":
		return fmt.Sprintf("user_%05d@example.invalid", n)
	case "uuid":
		return fakeUUID(n)
	case "url":
		return fmt.Sprintf("https://example.invalid/%d", n)
	case "hostname":
		return fmt.Sprintf("host-%d.example.invalid", n)
	case "slug":
		return fmt.Sprintf("slug-%d", n)
	case "ipv4":
		return fmt.Sprintf("192.0.2.%d", n%256)
	case "numeric_string":
		return fmt.Sprintf("%d", n)
	case "enum_like":
		if len(c.Values) > 0 {
			return c.Values[n%int64(len(c.Values))]
		}
		return fmt.Sprintf("value_%d", n)
	case "free_text":
		return fmt.Sprintf("sample text %d", n)
	case "jsonb_shape", "json":
		return "{}"
	}

	// No format hint: fall back to the type category.
	switch categorize(c.Type) {
	case "array":
		return arrayLiteral(c, n, r, unique)
	case "inet":
		// Valid for both inet and cidr: a bare host address is accepted by each
		// (cidr reads it as /32). 192.0.2.0/24 is the RFC 5737 documentation range,
		// so the address is as obviously non-real as the rest of the content.
		return fmt.Sprintf("192.0.2.%d", n%256)
	case "macaddr":
		// 08:00:2b is a reserved OUI prefix, keeping the address obviously synthetic.
		return fmt.Sprintf("08:00:2b:%02x:%02x:%02x", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
	case "macaddr8":
		return fmt.Sprintf("08:00:2b:%02x:%02x:%02x:%02x:%02x",
			(n>>32)&0xff, (n>>24)&0xff, (n>>16)&0xff, (n>>8)&0xff, n&0xff)
	case "interval":
		return fmt.Sprintf("%d seconds", n)
	case "xml":
		return fmt.Sprintf("<value>%d</value>", n)
	case "bit":
		return bitLiteral(c, n)
	case "point":
		return fmt.Sprintf("(%d,%d)", n, n)
	case "json":
		// A jsonb/json column with no format hint still needs parseable JSON; the
		// bare fallback ("val_7") is not, and the server rejects it.
		return "{}"
	case "numeric":
		return numericInRange(c, n, unique)
	case "temporal":
		return temporalInRange(c, n)
	case "bool":
		return n%2 == 0
	case "bytea":
		return []byte(fmt.Sprintf("\\x%08x", n))
	case "uuid":
		return fakeUUID(n)
	default:
		return fmt.Sprintf("val_%d", n)
	}
}

// isNullAt reports whether ordinal ord is null for a target fraction p, using a
// deterministic low-discrepancy sequence: the count of nulls up to ord tracks
// p*ord to within one, so the realized fraction is within 1/N of p and ord's
// null-ness is independent of the total row count (scale-stable).
func isNullAt(ord int64, p float64) bool {
	if p <= 0 {
		return false
	}
	if p >= 1 {
		return true
	}
	prev := int64(float64(ord) * p)
	cur := int64(float64(ord+1) * p)
	return cur > prev
}

// distinctOf returns the column's distinct estimate, or 0 if unknown.
func distinctOf(c fixture.Column) int64 {
	if c.Distinct == nil {
		return 0
	}
	return c.Distinct.Value
}

// numericInRange returns an integer within the column's range (or a plain fake
// number if no range is known).
//
// `unique` selects which of two facts wins when they conflict. A non-unique
// column WRAPS (min + n mod span) so every value lands inside the declared range.
// A unique column must not wrap: n is the row ordinal, so wrapping restarts at
// min once the ordinal passes the span and duplicates every value from there on.
//
// That conflict is the ordinary case, not a corner: `pull` derives `range` from
// pg_stats histogram bounds, which are a SAMPLE, so a 150k-row bigserial key came
// back as {min: 1633, max: 149328} — 147,696 slots for 150,000 rows. Wrapping
// honored that approximate range by breaking the column's EXACT unique constraint
// (via: constraint), and hydrating a fixture pull had just written died on
// `duplicate key value violates unique constraint`.
//
// So the exact fact wins and the approximate bound is overshot: min + n stays
// inside [min, max] whenever the span is wide enough for the row count (where it
// is byte-identical to the wrapping form, since n < span makes n mod span == n),
// and runs past max only when no injective assignment could have stayed inside.
func numericInRange(c fixture.Column, n int64, unique bool) any {
	lo, hi, ok := numericBounds(c)
	if !ok {
		return n
	}
	if unique {
		return lo + n
	}
	span := hi - lo + 1
	if span <= 0 {
		return lo
	}
	return lo + (n % span)
}

// numericBounds extracts integer min/max from a numeric range, if present.
func numericBounds(c fixture.Column) (lo, hi int64, ok bool) {
	if c.Range == nil {
		return 0, 0, false
	}
	l, lok := toInt64(c.Range.Min)
	h, hok := toInt64(c.Range.Max)
	if !lok || !hok || h < l {
		return 0, 0, false
	}
	return l, h, true
}

// temporalInRange returns a timestamp within the column's range, or a fixed fake
// epoch-based time if no range is known. It returns a time.Time so it encodes
// cleanly for both SQL literals and the binary COPY protocol.
func temporalInRange(c fixture.Column, n int64) any {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if c.Range != nil {
		if lo, ok := toTime(c.Range.Min); ok {
			if hi, ok := toTime(c.Range.Max); ok && !hi.Before(lo) {
				span := hi.Sub(lo)
				if span <= 0 {
					return lo.UTC()
				}
				off := time.Duration(n) % span
				return lo.Add(off).UTC()
			}
			return lo.UTC()
		}
	}
	return base.Add(time.Duration(n) * time.Hour).UTC()
}

// fakeUUID renders a deterministic, obviously-synthetic UUID encoding n.
func fakeUUID(n int64) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
}

// parentIDValue maps a parent ordinal to the id value the parent table actually
// generated for it, by running the same generator over the same ordinal.
//
// It used to just return the ordinal, on the reasoning that "a unique numeric id
// with no range is generated as its ordinal". The caveat was the bug: a real
// `pull` emits a range for every numeric column (RFC §6.2), so a real fixture's
// id column has one, and numericInRange produces `min + (ordinal % span)` — not
// the ordinal. For `users.id` with range {min: 1, max: 5000}, ordinal 0 is id 1.
//
// Returning the ordinal therefore pointed every child one parent short: user_id
// ran 0..parentN-1 while users.id ran 1..parentN, so user_id=0 referenced a
// parent that does not exist. That is an ORPHAN, in a fixture whose
// orphan_fraction is {value: 0, confidence: exact, via: constraint} — hydrate
// inventing the exact condition the fixture proves absent. A migration adding
// `FOREIGN KEY (user_id) REFERENCES users(id)` then fails on hydrated data, and
// rowshape reports a FAIL it manufactured itself.
//
// Deriving the value from the parent column instead of assuming its shape keeps
// the two in step whatever the range is — and if the fixture carries no facts for
// the parent column, generation falls back to the ordinal, which is what the id
// would be in that case anyway.
//
// The value must also carry the parent column's TYPE, not just its numeric shape.
// A `uuid`/`text`/`cuid` primary key — the modern default (Prisma, Drizzle,
// Rails-uuid) — generates a string id (fakeUUID / fakeValue), so a FK to it must
// be that same string. The earlier form returned int64 unconditionally, which
// closed the numeric-orphan case but fed the ordinal `0` into a uuid FK column;
// the binary COPY then failed to encode an int into a uuid and hydration aborted
// with a tool error for every non-integer-keyed schema that has a foreign key.
// Reproducing fakeValue over the same ordinal keeps the child cell type-identical
// to the parent id it references.
func parentIDValue(seed int64, f *fixture.Fixture, ref fixture.Reference, parentOrdinal, parentN int64) any {
	col, ok := parentIDColumn(f, ref)
	if !ok {
		return parentOrdinal
	}
	ptable := parentTable(ref.To)
	pcol := parentColumnName(ref.To)

	// Non-orphan: reproduce exactly what the parent's unique id column generated
	// for this ordinal (generateValue's unique path — no null, no histogram —
	// which is fakeValue over the ordinal), so the reference resolves whatever the
	// parent's type is.
	if parentOrdinal < parentN || parentN <= 0 {
		return parentUniqueValue(seed, ptable, pcol, col, parentOrdinal)
	}

	// A deliberate orphan (assignForeignKeys hands out ordinals >= parentN to
	// honour orphan_fraction): it must be an id NO parent has.
	val := parentUniqueValue(seed, ptable, pcol, col, parentOrdinal)
	if _, isInt := val.(int64); isInt {
		// Step above the largest id the parent generated. Stated this way the orphan
		// is unused no matter how the unique path assigns ids: it held when
		// numericInRange wrapped (min + ordinal % span, where an unused ordinal was
		// NOT enough because the wrap could land on a real parent), and it still
		// holds now that a unique column does not wrap — min + ordinal is strictly
		// increasing, so this resolves to exactly that same value.
		return maxParentID(col, parentN) + 1 + (parentOrdinal - parentN)
	}
	// Injective string/uuid formats never wrap, so a value generated from an
	// ordinal the parent never used (parentOrdinal >= parentN) is already unused.
	return val
}

// parentUniqueValue reproduces the value generateValue would produce for a unique,
// non-null column at ordinal ord: the unique branch takes n = ord and returns
// fakeValue(c, ord, rng), skipping the null and histogram branches (a unique id
// column hits neither). Reconstructing the same rng keeps it identical even if
// fakeValue starts consuming it.
func parentUniqueValue(seed int64, table, column string, c fixture.Column, ord int64) any {
	return fakeValue(c, ord, cellRNG(seed, table, column, ord), true)
}

// maxParentID is the largest id the parent table generated across ordinals
// [0, parentN).
//
// A unique numeric id is min + ordinal (numericInRange's unique path), which is
// strictly increasing, so the largest is simply the value at the last ordinal —
// no scan, and no cycle to reason about. The previous form walked the ordinals
// and broke out after "one lap" on the assumption that values repeat with period
// span; now that a unique column does not wrap, that break would stop at the span
// boundary and UNDERSTATE the maximum, handing deliberate orphans an id a real
// parent holds. Computing it directly removes the assumption instead of
// re-tuning it.
func maxParentID(col fixture.Column, parentN int64) int64 {
	if parentN <= 0 {
		return 0
	}
	v, ok := numericInRange(col, parentN-1, true).(int64)
	if !ok {
		return parentN // no numeric range: ids are the ordinals themselves
	}
	return v
}

// parentIDColumn resolves ref.To ("public.users.id") to the referenced column's
// profile.
func parentIDColumn(f *fixture.Fixture, ref fixture.Reference) (fixture.Column, bool) {
	if f == nil {
		return fixture.Column{}, false
	}
	i := strings.LastIndex(ref.To, ".")
	if i < 0 {
		return fixture.Column{}, false
	}
	tbl, ok := f.Tables[ref.To[:i]]
	if !ok {
		return fixture.Column{}, false
	}
	col, ok := tbl.Columns[ref.To[i+1:]]
	return col, ok
}

// parentColumnName extracts the column from a reference target schema.table.column.
func parentColumnName(to string) string {
	i := strings.LastIndex(to, ".")
	if i < 0 {
		return to
	}
	return to[i+1:]
}

// parentTable extracts schema.table from a reference target schema.table.column.
func parentTable(to string) string {
	i := strings.LastIndex(to, ".")
	if i < 0 {
		return to
	}
	return to[:i]
}

// sortedKeys returns a map's keys in lexicographic order — the canonicalization
// that makes generation independent of map iteration order (RFC §10).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// toInt64 best-effort converts a range bound to int64.
//
// The float64 case goes through toI64: a range bound is read straight out of the
// fixture, so a document carrying a value beyond int64 range would otherwise
// convert differently on amd64 and arm64 and break INV-DETERMINISM.
func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		if math.IsNaN(x) {
			// A NaN bound is not a bound. Reporting "no bound" is honest and
			// keeps it out of span arithmetic, where it would poison everything
			// downstream.
			return 0, false
		}
		return toI64(x), true
	default:
		return 0, false
	}
}

// toTime best-effort converts a range bound to a time.Time.
func toTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// categorize maps a Postgres type name onto a coarse generation category.
func categorize(typ string) string {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch {
	case strings.HasSuffix(t, "[]"):
		return "array"
	case t == "bytea":
		return "bytea"
	case t == "json" || t == "jsonb":
		return "json"
	case t == "uuid":
		return "uuid"
	case t == "boolean":
		return "bool"
	case t == "inet" || t == "cidr":
		return "inet"
	case t == "macaddr":
		return "macaddr"
	case t == "macaddr8":
		return "macaddr8"
	case t == "interval":
		return "interval"
	case t == "xml":
		return "xml"
	case strings.HasPrefix(t, "bit"): // bit(n) and bit varying(n)
		return "bit"
	case t == "point":
		return "point"
	case strings.HasPrefix(t, "timestamp") || t == "date" || strings.HasPrefix(t, "time"):
		return "temporal"
	case t == "text" || strings.Contains(t, "char") || strings.Contains(t, "varying") || t == "citext" || t == "name":
		return "text"
	case t == "smallint" || t == "integer" || t == "bigint" || t == "real" ||
		t == "double precision" || strings.HasPrefix(t, "numeric") ||
		strings.HasPrefix(t, "decimal") || t == "money" || strings.HasSuffix(t, "serial"):
		return "numeric"
	case t == "inet" || t == "cidr":
		return "inet"
	case t == "macaddr" || t == "macaddr8":
		return "macaddr"
	case t == "interval":
		return "interval"
	case t == "xml":
		return "xml"
	default:
		// NOT "text". Falling back to text meant every unmodelled type — inet,
		// cidr, macaddr, interval, point, xml, tsvector, enums, ranges, arrays,
		// domains, composite types — was hydrated as a string literal like
		// 'val_206071', which Postgres refuses on INSERT. The load then failed,
		// and validate reported a tool error or a manufactured FAIL for a
		// migration that was fine. Same class as the uuid foreign-key bug
		// (CR3-T6): a plausible-looking value of the wrong type.
		return "unmodelled"
	}
}

// unsupportedType reports whether a type has no synthesis rule AND is not
// string-compatible, so generating for it would produce a value Postgres refuses.
//
// The distinction matters because the generic fallback is a text value like
// "val_141", and pgx will happily hand that to the server for anything whose text
// representation is free-form. It is only rejected where the type has a real
// grammar — which is what made `inet` fail with
//
//	unable to encode "val_141" into binary format for inet (OID 869)
//
// The types listed here are the ones with a grammar that no plausible generic
// value satisfies and for which the fixture carries nothing to build one from:
// range and multirange types, tsvector/tsquery, and composite/geometric types
// beyond point. They get a NAMED error at generation time rather than a pgx
// internals message from deep inside COPY, because the fix is a decision the
// operator has to make (exclude the column, or extend the spec), not something the
// engine can guess.
func unsupportedType(typ string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	switch t {
	case "tsvector", "tsquery", "line", "lseg", "box", "path", "polygon", "circle":
		return true
	}
	// Built-in range and multirange types, plus any user-defined type the fixture
	// declared no definition for, share the same problem: a strict literal grammar.
	if strings.HasSuffix(t, "range") {
		return true
	}
	return false
}
