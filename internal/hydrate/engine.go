package hydrate

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rowshape/rowshape/internal/fixture"
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

// hydratedRowCount converts a declared count and scale into a row count of at
// least 1 (so every table gets exercised), capped by MaxRows.
func hydratedRowCount(declared int64, scale float64, maxRows int64) int64 {
	n := toI64(float64(declared) * scale)
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

	// Refuse a NOT NULL column of a type this build cannot generate, rather than
	// emitting a string literal that Postgres will reject on INSERT.
	//
	// The previous fallback classified every unmodelled type as "text", so an
	// `inet NOT NULL` column was hydrated as 'val_206071' and the load failed with
	// a Postgres syntax error naming neither rowshape nor the column. Refusing
	// here names both, and points at the two ways forward.
	for _, col := range colNames {
		c := tbl.Columns[col]
		if categorize(c.Type) == "unmodelled" && !c.Nullable {
			return GeneratedTable{}, fmt.Errorf(
				"column %s.%s is %s NOT NULL, and rowshape cannot synthesize a value of that type. "+
					"Hydrating it as text would emit a literal Postgres rejects on INSERT. "+
					"Use --target to validate against a database that already has real data, or add a "+
					"`format` hint to the column in the fixture if one of the known formats fits",
				shortName(name), col, c.Type)
		}
	}

	gt := GeneratedTable{
		Name:         name,
		Columns:      colNames,
		Rows:         make([][]any, n),
		DeclaredRows: tbl.Rows.Value,
	}
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

	for ci, col := range colNames {
		c := tbl.Columns[col]
		fk, isFK := fkAssign[col]
		for ord := int64(0); ord < n; ord++ {
			var v any
			switch {
			case isFK:
				// fk[ord] is the assigned parent ordinal; map it to the id value
				// the parent's identity column actually generated for that ordinal.
				v = parentIDValue(f, seed, fkRefs[col], fk[ord], rowCounts[parentTable(fkRefs[col].To)])
			default:
				v = generateValue(seed, name, col, c, ord)
			}
			gt.Rows[ord][ci] = v
		}
	}
	return gt, nil
}

// generateValue synthesizes one cell. Nulls are placed by a deterministic
// low-discrepancy quota so the null fraction is honored within a row or two
// (RFC §13, ±0.5%) and a cell's null-ness never changes as --scale grows.
func generateValue(seed int64, table, column string, c fixture.Column, ord int64) any {
	if c.Nullable && c.NullFraction != nil && isNullAt(ord, c.NullFraction.Value) {
		return nil
	}
	r := cellRNG(seed, table, column, ord)

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
	return fakeValue(c, n, r)
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
func fakeValue(c fixture.Column, n int64, r *rng) any {
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
	case "inet":
		// Documentation range (RFC 5737), so a hydrated value can never be
		// mistaken for a real address.
		return fmt.Sprintf("192.0.2.%d", n%256)
	case "macaddr":
		return fmt.Sprintf("00:00:5e:00:53:%02x", n%256)
	case "interval":
		return fmt.Sprintf("%d seconds", n%86400)
	case "xml":
		return fmt.Sprintf("<r id=\"%d\"/>", n)
	case "unmodelled":
		// A value cannot be invented for a type this build does not model. NULL
		// is the one literal every nullable column accepts; for a NOT NULL column
		// generateTable refuses rather than emitting something that will not load.
		return nil
	case "numeric":
		return numericInRange(c, n)
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
func numericInRange(c fixture.Column, n int64) any {
	lo, hi, ok := numericBounds(c)
	if !ok {
		return n
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
func parentIDValue(f *fixture.Fixture, seed int64, ref fixture.Reference, parentOrdinal, parentN int64) any {
	col, ok := parentIDColumn(f, ref)
	if !ok {
		return parentOrdinal
	}

	// A NON-NUMERIC parent key must be generated the way the parent generated it,
	// not as an integer.
	//
	// This function returned int64 unconditionally and derived the value through
	// numericInRange, which reads only col.Range and ignores col.Format and the
	// column's type. For a uuid primary key the PARENT column is produced by
	// generateValue -> fakeUUID (a string) while the child's FK column got
	// int64(0), int64(1), ... WriteSQL then emitted a bare 0 into a column that
	// DDL created as `uuid`, so the INSERT failed outright — or, under a looser
	// loader, produced an orphan for every child row. uuid primary keys with
	// foreign keys are an extremely common schema shape.
	//
	// This is the same class of bug as the ordinal-vs-range one described above:
	// that fix reasoned about the parent's RANGE but still assumed the parent's
	// TYPE. Running the parent's own generator over the parent's ordinal is what
	// keeps the two in step regardless of either.
	if categorize(col.Type) != "numeric" {
		// An ordinal at or beyond parentN is a deliberate orphan. No special
		// handling is needed here: unlike numericInRange, which wraps within the
		// column's span, these generators derive a unique column's value directly
		// from the ordinal, so an ordinal no parent used yields a value no parent
		// has.
		if v := generateValue(seed, parentTable(ref.To), parentColumnName(ref.To), col, parentOrdinal); v != nil {
			return v
		}
		return parentOrdinal
	}
	// An ordinal at or beyond parentN is a deliberate orphan (assignForeignKeys
	// hands these out to honour orphan_fraction). It must be an id NO parent has,
	// and "beyond the largest one generated" is the only choice that holds
	// whatever the column's range is: min + (ordinal % span) wraps, so picking an
	// unused ordinal is not enough when the span is narrower than the parent
	// count — the wrap would land on a real parent and the orphan would quietly
	// become valid.
	if parentOrdinal >= parentN && parentN > 0 {
		return maxParentID(col, parentN) + 1 + (parentOrdinal - parentN)
	}
	if v, ok := numericInRange(col, parentOrdinal).(int64); ok {
		return v
	}
	return parentOrdinal
}

// maxParentID is the largest id the parent table generated across ordinals
// [0, parentN).
//
// Closed form, not a scan. The previous implementation looped over every ordinal
// and relied on two escapes, BOTH of which failed for a numeric key with no
// range: the `!ok` branch was dead (numericInRange returns the ordinal itself
// when bounds are absent, so the int64 assertion always succeeds — and that
// branch would have returned parentN where the true answer is parentN-1), and
// the `break` required numericBounds to succeed, which is exactly what does not
// happen in that case. So a rangeless parent key cost a full parentN scan, per
// orphan cell. With a 1M-row parent and a 1% orphan fraction on a 1M-row child
// that is ~10^10 iterations.
//
// The values are lo + (ord % span), so the maximum over [0, parentN) is simply
// lo + min(parentN, span) - 1. This is behavior-preserving: hydrate_max_test.go
// checks it against the original scan across a grid of ranges and row counts.
func maxParentID(col fixture.Column, parentN int64) int64 {
	if parentN <= 0 {
		return 0
	}
	lo, hi, ok := numericBounds(col)
	if !ok {
		// No range: numericInRange hands back the ordinal, so the largest id
		// generated across [0, parentN) is parentN-1.
		return parentN - 1
	}
	span := hi - lo + 1
	if span <= 0 {
		return lo
	}
	if parentN < span {
		return lo + parentN - 1
	}
	return hi
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
	case t == "bytea":
		return "bytea"
	case t == "json" || t == "jsonb":
		return "json"
	case t == "uuid":
		return "uuid"
	case t == "boolean":
		return "bool"
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

// shortName trims the schema qualifier for a message, keeping it readable.
func shortName(qualified string) string {
	if i := strings.LastIndex(qualified, "."); i >= 0 {
		return qualified[i+1:]
	}
	return qualified
}
