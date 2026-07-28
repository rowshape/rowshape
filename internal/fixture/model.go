// Package fixture defines the rowshape fixture data model, canonical form, and
// digest.
//
// This is one of the two package boundaries — together with internal/verdict —
// reserved so the phase-5 cloud API can import it UNCHANGED. Canonical form and
// digesting MUST have exactly ONE implementation, in Go, shared by CLI and API
// (INV-ONE-CANONICAL-FORM, PRD §9, RFC §11).
//
// The types here model the whole RFC-0001 §5/§6 document. Design notes:
//
//   - Scalar facts are {value, confidence, via} objects, not bare scalars
//     (RFC §6.1). A bare scalar is accepted on read as shorthand for
//     confidence:estimated — the weakest reading, never the strongest.
//   - `tables` and `columns` are maps keyed by qualified/column name, not lists,
//     so they diff cleanly (RFC §5).
//   - Unknown fields are ignored rather than rejected; `x_`-prefixed vendor
//     extensions are preserved (RFC §12).
//   - An unknown major `rowshape_fixture` version is refused by Parse (RFC §12).
package fixture

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// FormatVersion is the declared major version of the Rowshape Fixture Spec
// (RFC-0001). A fixture whose rowshape_fixture major differs from this is
// refused (RFC §12).
const FormatVersion = "1"

// Fixture is a whole rowshape.yaml document (RFC §5).
type Fixture struct {
	RowshapeFixture string           `yaml:"rowshape_fixture"`
	Meta            Meta             `yaml:"meta"`
	Tables          map[string]Table `yaml:"tables"`

	// X holds preserved x_-prefixed vendor extensions (RFC §12).
	X map[string]any `yaml:",inline"`
}

// UnmarshalYAML ignores unknown fields but preserves x_ vendor extensions.
func (f *Fixture) UnmarshalYAML(node *yaml.Node) error {
	type alias Fixture
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*f = Fixture(a)
	f.X = pruneExtensions(f.X)
	return nil
}

// Meta is the document header (RFC §5).
type Meta struct {
	ID string `yaml:"id"`
	// GeneratedAt is kept as its verbatim string; it is excluded from the digest
	// (RFC §11) so its exact representation never affects identity.
	GeneratedAt string  `yaml:"generated_at"`
	Generator   string  `yaml:"generator"`
	Engine      Engine  `yaml:"engine"`
	Privacy     string  `yaml:"privacy,omitempty"` // strict | standard | permissive (§8)
	Source      string  `yaml:"source,omitempty"`  // salted hash of the host (§8.4)
	Profile     Profile `yaml:"profile"`
	// Digest is SHA-256 over the canonical form, excluding this field (RFC §11).
	Digest string `yaml:"digest,omitempty"`
}

// Engine names the source database engine and version. The version is mandatory
// because cost models are engine-version-conditional (RFC §9.1).
type Engine struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// Profile records how the fixture was produced (RFC §7.3).
type Profile struct {
	Mode      string `yaml:"mode"` // fast | exact | targeted
	ScannedAt string `yaml:"scanned_at,omitempty"`
	// Escalated always emits (even as an empty list) so the profile block is
	// complete and self-describing (RFC §7.3).
	Escalated []string `yaml:"escalated"`
}

// Table is one relation's structure and shape (RFC §5, §6).
type Table struct {
	Rows        Fact[int64]       `yaml:"rows"`
	Bytes       int64             `yaml:"bytes,omitempty"`
	Columns     map[string]Column `yaml:"columns,omitempty"`
	Constraints []Constraint      `yaml:"constraints,omitempty"`
	Indexes     []Index           `yaml:"indexes,omitempty"`
	References  []Reference       `yaml:"references,omitempty"`
	// Partitions describes a partitioned table's shape (RFC §14.2): the parent
	// declares count/strategy/skew, with no per-partition entries.
	Partitions *Partitions `yaml:"partitions,omitempty"`

	X map[string]any `yaml:",inline"`
}

// Partitions is a partitioned table's shape (RFC §14.2). Partition count and
// per-partition skew change lock behavior under a partitioning migration
// materially, and no other field captures it.
type Partitions struct {
	Count    int    `yaml:"count"`
	Strategy string `yaml:"strategy"` // range | list | hash
	// Skew is the fraction of rows in the largest partition (1/count is uniform;
	// approaching 1 means one partition dominates).
	Skew float64 `yaml:"skew,omitempty"`
}

// UnmarshalYAML ignores unknown fields but preserves x_ vendor extensions.
func (t *Table) UnmarshalYAML(node *yaml.Node) error {
	type alias Table
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*t = Table(a)
	t.X = pruneExtensions(t.X)
	return nil
}

// Column is a single column profile (RFC §6).
type Column struct {
	Type     string `yaml:"type"`
	Nullable bool   `yaml:"nullable"` // structural (the DDL), always exact (§6.1)

	NullFraction *Fact[float64] `yaml:"null_fraction,omitempty"`
	Distinct     *Fact[int64]   `yaml:"distinct,omitempty"`
	// Unique MUST be exact or absent (RFC §7.2). A pointer so absence — the
	// common, honest case — is distinct from a value.
	Unique *Fact[bool] `yaml:"unique,omitempty"`

	Generated string `yaml:"generated,omitempty"` // e.g. identity
	Format    string `yaml:"format,omitempty"`    // a §6.3 format class

	Length      *Length   `yaml:"length,omitempty"`
	Values      []string  `yaml:"values,omitempty"`      // privacy: permissive only (§8.2)
	Frequencies []float64 `yaml:"frequencies,omitempty"` // parallels Values
	// SampleN is how many sampled values Values/Frequencies were derived from.
	// Frequencies are sample proportions, so the k-anonymity gate (§8.2) needs
	// the denominator to recover an OBSERVED occurrence count; without it the
	// gate can only multiply a proportion by an estimated row count, which
	// cannot resolve below 1/SampleN and so never rejects on a large table.
	// Deliberately not serialized: it is gate scaffolding, not a published
	// fact, and keeping it out of the YAML keeps the canonical digest (§11)
	// unchanged. A fixture read back from disk therefore has SampleN == 0,
	// which the gate treats as "unverifiable" and withholds.
	SampleN   int        `yaml:"-"`
	Range     *Range     `yaml:"range,omitempty"`     // MUST NOT appear on text/bytea (§6.1)
	Histogram *Histogram `yaml:"histogram,omitempty"` // privacy: standard+ (§8.2)
	// Shape carries a JSON key skeleton (key names, depth, leaf types) for a
	// jsonb_shape column — never leaf values (RFC §6.3).
	Shape any `yaml:"shape,omitempty"`

	Redact Redact `yaml:"redact,omitempty"` // per-column privacy override (§8.2)

	X map[string]any `yaml:",inline"`
}

// UnmarshalYAML ignores unknown fields but preserves x_ vendor extensions.
func (c *Column) UnmarshalYAML(node *yaml.Node) error {
	type alias Column
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*c = Column(a)
	c.X = pruneExtensions(c.X)
	return nil
}

// Length holds string-length statistics (RFC §6.1). Pointers distinguish an
// absent statistic from a legitimate zero (an empty string has length 0).
type Length struct {
	Min  *int64   `yaml:"min,omitempty"`
	Max  *int64   `yaml:"max,omitempty"`
	Mean *float64 `yaml:"mean,omitempty"`
	P95  *int64   `yaml:"p95,omitempty"`
}

// Range holds numeric or temporal min/max/mean (RFC §6.1, §6.2). Min and Max are
// untyped because they may be numbers or timestamps depending on the column
// type; text and bytea columns MUST NOT emit a range at all (RFC §6.1).
type Range struct {
	Min  any      `yaml:"min,omitempty"`
	Max  any      `yaml:"max,omitempty"`
	Mean *float64 `yaml:"mean,omitempty"`
}

// Histogram captures skew — the thing no summary statistic captures (RFC §6.2).
// Bounds may be numeric or temporal, so they are untyped.
type Histogram struct {
	Buckets int   `yaml:"buckets"`
	Bounds  []any `yaml:"bounds"`
}

// Constraint is a table constraint (RFC §6.4).
type Constraint struct {
	Name    string   `yaml:"name"`
	Kind    string   `yaml:"kind"` // primary_key | unique | check | foreign_key | exclusion
	Columns []string `yaml:"columns,omitempty"`
	// NullsDistinct mirrors NULLS [NOT] DISTINCT on a unique constraint.
	NullsDistinct *bool `yaml:"nulls_distinct,omitempty"`
	// Expression is a CHECK body, emitted verbatim (RFC §6.4) — opaque under
	// privacy:strict.
	Expression string `yaml:"expression,omitempty"`
	// Validated distinguishes a NOT VALID constraint. A pointer because
	// validated:false MUST be preserved and differs from an absent field (§6.4).
	Validated *bool `yaml:"validated,omitempty"`

	X map[string]any `yaml:",inline"`
}

// Index is a table index (RFC §6.5).
type Index struct {
	Name          string   `yaml:"name"`
	Method        string   `yaml:"method"` // btree | hash | gin | gist | ...
	Columns       []string `yaml:"columns"`
	Unique        bool     `yaml:"unique,omitempty"`
	Partial       string   `yaml:"partial,omitempty"` // a partial-index predicate
	Bytes         int64    `yaml:"bytes,omitempty"`
	BloatEstimate *float64 `yaml:"bloat_estimate,omitempty"`

	X map[string]any `yaml:",inline"`
}

// Reference is a foreign-key relationship and its fan-out (RFC §6.6).
type Reference struct {
	Column   string `yaml:"column"`
	To       string `yaml:"to"` // public.users.id
	OnDelete string `yaml:"on_delete,omitempty"`
	// Fanout is the most important field in the format (RFC §6.6): a hydrator
	// MUST reproduce the distribution shape, not merely the mean.
	Fanout *Fanout `yaml:"fanout,omitempty"`
	// OrphanFraction records FK violations already present in production (§6.6).
	OrphanFraction *Fact[float64] `yaml:"orphan_fraction,omitempty"`

	X map[string]any `yaml:",inline"`
}

// Fanout is a fan-out distribution summary (RFC §6.6). It carries its own
// confidence like a scalar fact, but describes several quantiles at once.
type Fanout struct {
	Mean       float64    `yaml:"mean"`
	P50        float64    `yaml:"p50,omitempty"`
	P95        float64    `yaml:"p95,omitempty"`
	Max        float64    `yaml:"max,omitempty"`
	Confidence Confidence `yaml:"confidence,omitempty"`
}

// Redact is a per-column privacy override (RFC §8.2). It reads either a list of
// field names (`redact: [range, histogram]`) or the scalar `all`.
type Redact []string

// UnmarshalYAML accepts either a scalar (`all`) or a sequence of field names.
func (r *Redact) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*r = Redact{node.Value}
		return nil
	}
	var s []string
	if err := node.Decode(&s); err != nil {
		return err
	}
	*r = Redact(s)
	return nil
}

// MarshalYAML re-emits the scalar shorthand for `redact: all`.
func (r Redact) MarshalYAML() (any, error) {
	if len(r) == 1 && r[0] == "all" {
		return "all", nil
	}
	return []string(r), nil
}

// pruneExtensions drops unknown non-x_ keys captured by an inline map (they are
// ignored per RFC §12) and keeps only x_ vendor extensions (preserved per §12).
func pruneExtensions(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	for k := range m {
		if !strings.HasPrefix(k, "x_") {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// Parse decodes a fixture document and refuses an unknown major version.
//
// A reader encountering an unknown major `rowshape_fixture` version MUST refuse
// rather than best-effort (RFC §12): silent partial understanding is how you get
// a PASS that means nothing.
func Parse(data []byte) (*Fixture, error) {
	var f Fixture
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if err := f.checkVersion(); err != nil {
		return nil, err
	}
	if err := f.checkEngine(); err != nil {
		return nil, err
	}
	return &f, nil
}

// EngineError is returned when a fixture declares an engine this build cannot
// reason about. It is a distinct type so a caller can map it to the right
// tool-error category rather than reporting a generic parse failure.
type EngineError struct{ Got string }

func (e *EngineError) Error() string {
	return "unsupported engine " + e.Got + ": this build understands postgres only. " +
		"Every cost model, lock rule and finding in the catalog is Postgres-specific, so a verdict " +
		"computed for another engine would be wrong rather than merely incomplete"
}

// checkEngine refuses a fixture whose engine this build does not model.
//
// Nothing branched on meta.engine.name. It was written by pull (hardcoded to
// "postgres") and then never read — while estimate.Major parses engine.version as
// a POSTGRES major unconditionally. So a MySQL 8.0 fixture would have been read
// as "PG 8", routed through Postgres cost models, and produced a confident
// verdict about lock behaviour that does not exist in InnoDB. That is a WRONG
// ANSWER, not a missing feature, and it is the worst kind: fluent and specific.
//
// An EMPTY name is permitted. Hand-authored and older fixtures legitimately omit
// meta.engine, and the version gate (RFC §9.1) already declines to extrapolate
// without a version — so absence is handled honestly elsewhere. This refuses only
// a name that is present and is NOT one this build models.
func (f *Fixture) checkEngine() error {
	name := strings.ToLower(strings.TrimSpace(f.Meta.Engine.Name))
	if name == "" || name == "postgres" || name == "postgresql" {
		return nil
	}
	return &EngineError{Got: f.Meta.Engine.Name}
}

// Marshal encodes a fixture to YAML. This is a straightforward serialization;
// the canonical form used for digesting (RFC §11) lands in P1-T2.
func Marshal(f *Fixture) ([]byte, error) {
	return yaml.Marshal(f)
}

// VersionError is returned when a fixture's format major is missing or unknown.
// It is a distinct type so a caller can tell "refused an unknown major version"
// (RFC §12) apart from an ordinary parse error and map it to the right tool-error
// category (never a partial-understanding verdict).
type VersionError struct {
	Got  string // the major found ("" if missing)
	Want string // the major this build understands
}

func (e *VersionError) Error() string {
	if e.Got == "" {
		return "fixture: missing rowshape_fixture version; refusing to read (RFC §12)"
	}
	return fmt.Sprintf("fixture: unknown major rowshape_fixture version %q (this build understands %q); refusing rather than best-effort (RFC §12)", e.Got, e.Want)
}

// checkVersion enforces the major-version compatibility rule (RFC §12).
func (f *Fixture) checkVersion() error {
	got := majorOf(f.RowshapeFixture)
	if got != FormatVersion {
		return &VersionError{Got: got, Want: FormatVersion}
	}
	return nil
}

// majorOf extracts the major component of a version string ("1", "1.2" -> "1").
func majorOf(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}
