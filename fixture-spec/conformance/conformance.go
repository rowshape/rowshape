// Package conformance is the executable conformance suite for the Rowshape
// Fixture Spec (RFC-0001 §13). It encodes the emitter, hydrator, and validator
// MUSTs so that rowshape's own CLI can be held to them, and so that a third
// party can hold their own EMITTER to them — which is what makes the spec a
// position rather than an aspiration (PRD §3, §16: the strategic value is that
// anyone can emit the format).
//
// CheckEmitterYAML is the third-party entry point, and it takes bytes on
// purpose. The rest of this package is typed in rowshape's internal fixture
// model, which no outside module can name — Go forbids importing
// .../internal/... across module boundaries, so a signature like
// CheckEmitter(*fixture.Fixture) is uncallable from anywhere but this repo. An
// emitter has bytes; bytes are the honest interface.
//
// Scope, stated plainly: CheckHydrator and CheckValidator exercise ROWSHAPE's
// hydrator and verdict engine. They are regression tests that the reference
// implementation obeys its own spec, not a harness a third party can plug their
// hydrator into — doing that needs an agreed wire format for hydrated rows,
// which RFC-0001 does not define.
//
// Lives under fixture-spec/ in this monorepo; in the published layout it is the
// rowshape/fixture-spec repository alongside schema/rowshape.schema.json. Note
// that the Go suite cannot move there as-is: it imports rowshape's internal
// packages, which stop compiling the moment it becomes a separate module.
package conformance

import (
	"fmt"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
)

// Violation is one failed conformance MUST, naming the rule and where it broke.
type Violation struct {
	Rule    string // the RFC clause, e.g. "§6.1 no range on text"
	Where   string // the offending path, e.g. "public.users.email"
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("[%s] %s: %s", v.Rule, v.Where, v.Message)
}

// textTypes are the type spellings that MUST NOT carry a range (RFC §6.1): the
// min/max of a text or bytea column would be real production values.
var textTypes = map[string]bool{
	"text": true, "bytea": true, "varchar": true, "character varying": true,
	"char": true, "character": true, "citext": true, "json": true, "jsonb": true,
}

// CheckEmitterYAML runs the emitter MUSTs against fixture bytes. This is the
// entry point for a third-party emitter: hand it what you emitted.
//
// It exists because CheckEmitter takes *fixture.Fixture — a type in
// rowshape's internal tree, which no other module may import. The suite claimed
// anyone could run it while its whole surface was, in fact, uncallable from
// outside this repository.
//
// A parse failure is returned as an error rather than a Violation: unreadable
// bytes are not a conformance verdict, they are the absence of one.
func CheckEmitterYAML(data []byte) ([]Violation, error) {
	f, err := fixture.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse fixture: %w", err)
	}
	return CheckEmitter(f), nil
}

// CheckEmitter runs the statically-checkable emitter MUSTs (RFC §13) against a
// parsed fixture: a known format version; never `range` on text/bytea (§6.1);
// `unique` is exact or absent, never inferred from a sample (§7.2); every fact
// carries a valid confidence (§6.1); and the canonical digest is stable across
// repeated computation over the unchanged fixture (§11). Returns every violation
// found (empty means conformant).
func CheckEmitter(f *fixture.Fixture) []Violation {
	var vs []Violation

	if major := majorOf(f.RowshapeFixture); major != fixture.FormatVersion {
		vs = append(vs, Violation{"§12 version", "rowshape_fixture", fmt.Sprintf("unknown or missing format version %q (expected %q)", f.RowshapeFixture, fixture.FormatVersion)})
	}

	for tname := range f.Tables {
		tbl := f.Tables[tname]
		vs = append(vs, checkConfidence(tname+".rows", "rows", tbl.Rows.Confidence)...)

		for cname := range tbl.Columns {
			col := tbl.Columns[cname]
			where := tname + "." + cname

			if col.Range != nil && textTypes[strings.ToLower(strings.TrimSpace(baseType(col.Type)))] {
				vs = append(vs, Violation{"§6.1 no range on text", where, "a text/bytea column emitted a range; its min/max are real production values"})
			}
			if col.Unique != nil && col.Unique.Confidence != fixture.Exact {
				vs = append(vs, Violation{"§7.2 uniqueness never from a sample", where, fmt.Sprintf("unique carries confidence %q; unique MUST be exact or absent", col.Unique.Confidence)})
			}
			if col.NullFraction != nil {
				vs = append(vs, checkConfidence(where+".null_fraction", "null_fraction", col.NullFraction.Confidence)...)
			}
			if col.Distinct != nil {
				vs = append(vs, checkConfidence(where+".distinct", "distinct", col.Distinct.Confidence)...)
			}
		}
		for _, ref := range tbl.References {
			if ref.OrphanFraction != nil {
				vs = append(vs, checkConfidence(tname+"."+ref.Column+".orphan_fraction", "orphan_fraction", ref.OrphanFraction.Confidence)...)
			}
			if ref.Fanout != nil {
				vs = append(vs, checkConfidence(tname+"."+ref.Column+".fanout", "fanout", ref.Fanout.Confidence)...)
			}
		}
	}

	vs = append(vs, checkUserTypes(f)...)
	vs = append(vs, checkIndexKeys(f)...)
	vs = append(vs, checkIndexInclude(f)...)
	vs = append(vs, checkExtensionTypes(f)...)

	// The digest MUST be stable across runs against an unchanged fixture (§11).
	d1, e1 := fixture.Digest(f)
	d2, e2 := fixture.Digest(f)
	if e1 != nil || e2 != nil || d1 != d2 || d1 == "" {
		vs = append(vs, Violation{"§11 stable digest", "meta.digest", "canonical digest is not stable across repeated computation"})
	}

	return vs
}

// checkIndexKeys enforces that every index says what it is ON (§6.5).
//
// An index with neither `columns` nor `keys` is not merely under-described, it is
// unbuildable — and the shape is specific enough to catch the real mistake: an
// emitter that reads only pg_index.indkey records an EXPRESSION index as having no
// keys at all, because an expression key is stored as attribute 0. The reference
// implementation did exactly that, so a unique index on lower(email) was dropped
// when the disposable database was built and a constraint production enforces went
// missing — the direction that turns a real FAIL into a PASS.
func checkIndexKeys(f *fixture.Fixture) []Violation {
	var vs []Violation
	for tname := range f.Tables {
		for _, ix := range f.Tables[tname].Indexes {
			if len(ix.Columns) == 0 && len(ix.Keys) == 0 {
				vs = append(vs, Violation{"§6.5 an index declares its keys", tname + "." + ix.Name,
					"index has neither columns nor keys, so no consumer can rebuild it; an expression index MUST record `keys`"})
			}
		}
	}
	return vs
}

// checkIndexInclude enforces that a covering index's INCLUDE payload stays out of
// its key (§6.5).
//
// The key is what a UNIQUE index ENFORCES, so folding the payload in describes a
// strictly WEAKER constraint than the one production has: recording
// `UNIQUE (customer_id, created_at) INCLUDE (total)` as unique on all three let a
// migration violating the real two-column uniqueness reach PASS. A column that
// appears in both lists is that mistake, made visible.
func checkIndexInclude(f *fixture.Fixture) []Violation {
	var vs []Violation
	for tname := range f.Tables {
		for _, ix := range f.Tables[tname].Indexes {
			if len(ix.Include) == 0 {
				continue
			}
			key := make(map[string]bool, len(ix.Columns))
			for _, c := range ix.Columns {
				key[c] = true
			}
			for _, c := range ix.Include {
				if key[c] {
					vs = append(vs, Violation{"§6.5 INCLUDE payload is not part of the key",
						tname + "." + ix.Name + "." + c,
						"column appears in both `columns` and `include`; the key is what a UNIQUE index enforces, so counting the payload as key describes a weaker constraint than production has"})
				}
			}
		}
	}
	return vs
}

// checkExtensionTypes enforces the §6.8 emitter MUST that a schema depending on an
// extension says so.
//
// A `citext` column names a type a freshly created disposable database has never
// heard of, so the DDL failed with `type "citext" does not exist` and validation
// could not run on the schema AT ALL. The check is deliberately narrow — it fires
// only on type names that unambiguously come from a well-known extension — because
// a general "is this type built in?" test would need the engine's catalog, which a
// conformance suite reading a file does not have.
func checkExtensionTypes(f *fixture.Fixture) []Violation {
	// Types that exist only once an extension is installed, mapped to the extension
	// that provides them. Extending this list makes the check stricter, never wrong.
	provider := map[string]string{
		"citext":    "citext",
		"hstore":    "hstore",
		"ltree":     "ltree",
		"geometry":  "postgis",
		"geography": "postgis",
		"vector":    "vector",
	}
	var vs []Violation
	for tname := range f.Tables {
		for cname, col := range f.Tables[tname].Columns {
			base := strings.TrimSuffix(strings.TrimSpace(col.Type), "[]")
			if i := strings.LastIndex(base, "."); i >= 0 {
				base = base[i+1:]
			}
			ext, needs := provider[strings.ToLower(base)]
			if !needs {
				continue
			}
			if _, declared := f.Extensions[ext]; !declared {
				vs = append(vs, Violation{"§6.8 a schema depending on an extension declares it",
					tname + "." + cname,
					"column is typed `" + col.Type + "`, which requires the `" + ext +
						"` extension, but the fixture declares no such extension; a consumer cannot create the type"})
			}
		}
	}
	return vs
}

// checkUserTypes enforces the §6.7 emitter MUSTs for user-defined types:
// every enum/domain a column references is DEFINED, an enum carries a label_count
// even when its vocabulary is withheld, and a domain names a base type.
//
// The referenced-but-undefined case is the one with teeth. A column's `type` is
// only a name, so an undefined `app.status` leaves a consumer with nothing to
// create — the failure is not a missing statistic but a database that cannot be
// built at all. The rule exists because the reference implementation shipped
// exactly that fixture: pull recorded enum-typed columns and no definitions, and
// hydrate died on `type "app.status" does not exist`.
func checkUserTypes(f *fixture.Fixture) []Violation {
	var vs []Violation

	for name := range f.Types {
		t := f.Types[name]
		where := "types." + name
		switch t.Kind {
		case "enum":
			// label_count is what lets a strict fixture still be reconstructed, so it
			// is required even where the vocabulary is deliberately absent.
			if t.LabelCount <= 0 && len(t.Labels) == 0 {
				vs = append(vs, Violation{"§6.7 enum declares its size", where, "enum has neither labels nor label_count, so no consumer can create it"})
			}
			if t.LabelCount > 0 && len(t.Labels) > 0 && t.LabelCount != len(t.Labels) {
				vs = append(vs, Violation{"§6.7 enum declares its size", where, fmt.Sprintf("label_count is %d but %d labels are listed", t.LabelCount, len(t.Labels))})
			}
		case "domain":
			if t.Base == "" {
				vs = append(vs, Violation{"§6.7 domain names its base", where, "domain has no base type, so no consumer can create it"})
			}
		default:
			vs = append(vs, Violation{"§6.7 known type kind", where, fmt.Sprintf("unknown kind %q (expected enum or domain)", t.Kind)})
		}
	}

	// Every non-built-in column type MUST have a definition. Anything unqualified is
	// taken to be built-in: `integer` and `character varying(3)` carry no schema,
	// while a user-defined type is reported by pull as schema-qualified.
	for tname := range f.Tables {
		for cname := range f.Tables[tname].Columns {
			typ := f.Tables[tname].Columns[cname].Type
			if !looksUserDefined(typ) {
				continue
			}
			if _, ok := f.Types[typ]; !ok {
				vs = append(vs, Violation{"§6.7 referenced types are defined", tname + "." + cname,
					fmt.Sprintf("column type %q is not a built-in and has no entry in `types`; a consumer cannot create it", typ)})
			}
		}
	}
	return vs
}

// looksUserDefined reports whether a column type names something the engine does
// not already have.
//
// The test is that the name is schema-qualified, which is how a conformant emitter
// reports a user-defined type and never how it reports a built-in. Array and
// length modifiers are stripped first so `app.status[]` and `app.code(4)` resolve
// to the same name their definition is keyed by. A dot inside a modifier — as in
// `numeric(10,2)` — is therefore never mistaken for a qualification.
func looksUserDefined(typ string) bool {
	t := strings.TrimSpace(typ)
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSuffix(strings.TrimSpace(t), "[]")
	return strings.Contains(t, ".")
}

// checkConfidence enforces that a fact carries a valid confidence (RFC §6.1).
func checkConfidence(where, fact string, c fixture.Confidence) []Violation {
	if !c.Valid() {
		return []Violation{{"§6.1 confidence on every fact", where, fmt.Sprintf("%s fact has no valid confidence (got %q)", fact, c)}}
	}
	return nil
}

// baseType strips a type's length/precision modifier ("varchar(255)" -> "varchar").
func baseType(t string) string {
	if i := strings.IndexByte(t, '('); i >= 0 {
		return t[:i]
	}
	return t
}

// majorOf extracts the major component of a version string.
func majorOf(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}
