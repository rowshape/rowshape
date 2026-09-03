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
	"strconv"
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
	// Types describes the user-defined types the columns reference — enums and
	// domains (RFC §6.7). Keyed by the type's schema-qualified name, exactly as it
	// appears in a column's `type`, so a column and its definition join by string
	// equality.
	//
	// Without this section a column typed `app.status` names something the fixture
	// never defines, so the DDL that receives hydrated rows cannot be built:
	// hydration died on `type "app.status" does not exist`, meaning no schema using
	// a Postgres enum could be hydrated at all. Optional and omitted when empty, so
	// every fixture written before it stays valid (RFC §12).
	Types map[string]UserType `yaml:"types,omitempty"`

	// Extensions names the engine extensions the read schema depends on, keyed by
	// extension name (RFC §6.8).
	//
	// Types is not enough. A column typed `citext` names a type that only exists once
	// the citext EXTENSION is installed, and a freshly created disposable database has
	// none installed beyond plpgsql — so the DDL failed with
	//
	//	ERROR: type "citext" does not exist (SQLSTATE 42704)
	//
	// aborting the whole load and leaving `validate` unable to run at all on the
	// schema. The same shape covers hstore, ltree, postgis geometry and pgvector's
	// `vector`, and it covers operator classes too: an index declared
	// `gin (email gin_trgm_ops)` needs pg_trgm even though no column type mentions it.
	//
	// Only the extensions the read schema actually references are recorded, resolved
	// through pg_depend from the types and operator classes the scan saw — an
	// extension merely installed on the source contributes nothing. Optional and
	// omitted when empty, so every fixture written before it stays valid (RFC §12).
	Extensions map[string]Extension `yaml:"extensions,omitempty"`

	// X holds preserved x_-prefixed vendor extensions (RFC §12).
	X map[string]any `yaml:",inline"`
}

// Extension is one engine extension the schema depends on (RFC §6.8).
//
// It deliberately records no VERSION. The fixture's claim is "this schema needs
// citext", not "it needs citext 1.6": pinning a version would make a fixture
// refuse to hydrate on a server that ships a different one, for no gain — the
// disposable database only has to provide the type, and `CREATE EXTENSION IF NOT
// EXISTS` gets whatever that server has.
type Extension struct {
	// Schema is where the extension's objects live. It matters because a type name
	// can be schema-qualified — `ext.citext` and `public.citext` are different
	// strings in a column's `type`, and only one of them resolves.
	Schema string `yaml:"schema,omitempty"`
}

// UserType defines a user-defined column type (RFC §6.7).
//
// It carries only what is needed to RECREATE the type in a disposable database,
// which is the whole reason it is recorded: an enum's labels are its full domain
// of legal values, and a domain's base type plus constraint are its definition.
// Both are DDL — the same class of information as a column name or a CHECK
// expression (§6.4), not row content.
type UserType struct {
	// Kind is "enum" or "domain".
	Kind string `yaml:"kind"`

	// Labels are an enum's values in declaration order, which is significant:
	// Postgres orders an enum by it, so a migration comparing or sorting the column
	// depends on it. Omitted under privacy:strict, where a label is treated like a
	// verbatim CHECK expression (§8.2) and only LabelCount survives.
	Labels []string `yaml:"labels,omitempty"`
	// LabelCount is the number of enum labels. It is always present for an enum, so
	// cardinality is known even when the vocabulary is withheld — the same trade
	// strict already makes for ranges, keeping `distinct` while dropping values.
	LabelCount int `yaml:"label_count,omitempty"`

	// Base is a domain's underlying type ("integer", "text", "numeric(12,2)").
	Base string `yaml:"base,omitempty"`
	// NotNull records a domain declared NOT NULL, which constrains every column
	// using it regardless of that column's own nullability.
	NotNull bool `yaml:"not_null,omitempty"`
	// Check is a domain's constraint expression, verbatim, over the keyword VALUE
	// ("(VALUE > 0)"). Opaque under privacy:strict for the same reason a table
	// CHECK is (§6.4): it can embed literals from the domain.
	Check string `yaml:"check,omitempty"`

	X map[string]any `yaml:",inline"`
}

// UnmarshalYAML ignores unknown fields but preserves x_ vendor extensions.
func (u *UserType) UnmarshalYAML(node *yaml.Node) error {
	type alias UserType
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*u = UserType(a)
	u.X = pruneExtensions(u.X)
	return nil
}

// EffectiveLabels returns the enum labels to materialize: the real vocabulary when
// the fixture carries it, and placeholders derived from LabelCount when it does not.
//
// It is the ONE place that decision is made, because two callers must agree on it
// exactly. The DDL emitter creates the type from these labels and the synthesis
// engine draws column values from them; if they disagreed by even one label, every
// insert of the missing value would be rejected as not a member of the type. A
// privacy:strict fixture withholds the vocabulary but keeps the count (§8.2),
// which is precisely the case that needs both sides to invent the same names.
//
// Returns nil for a non-enum or an enum with neither labels nor a count.
func (u UserType) EffectiveLabels() []string {
	if u.Kind != "enum" {
		return nil
	}
	if len(u.Labels) > 0 {
		return u.Labels
	}
	if u.LabelCount <= 0 {
		return nil
	}
	out := make([]string, u.LabelCount)
	for i := range out {
		// Obviously synthetic, like every other value the hydrator invents (§13).
		out[i] = "label_" + strconv.Itoa(i)
	}
	return out
}

// ResolveType reduces a type name to the underlying type that governs how a value
// is built: a domain becomes its base type, anything else is returned unchanged.
//
// A domain is a base type plus constraints, so `app.positive_int` over `integer`
// must be generated AS an integer. Reading it as an opaque name produced the
// generic text fallback and the server rejected it:
//
//	unable to encode "val_14" into binary format for int4 (OID 23)
//
// Domains over domains are followed to the bottom, with a hard step limit so a
// malformed fixture describing a cycle cannot hang the generator.
func (f *Fixture) ResolveType(typeName string) string {
	if f == nil {
		return typeName
	}
	name := typeName
	for range 16 {
		t, ok := f.Types[name]
		if !ok || t.Kind != "domain" || t.Base == "" {
			return name
		}
		name = t.Base
	}
	return name
}

// EnumLabels resolves a column's type name to the enum labels to use, and reports
// whether the named type is an enum at all.
//
// The boolean is separate from the label list on purpose: an enum is still an enum
// when its vocabulary was withheld, and the caller must draw from EffectiveLabels
// rather than fall through to a generic string — no arbitrary string is a member of
// the type, so falling through produces a value Postgres refuses.
func (f *Fixture) EnumLabels(typeName string) ([]string, bool) {
	if f == nil {
		return nil, false
	}
	t, ok := f.Types[typeName]
	if !ok || t.Kind != "enum" {
		return nil, false
	}
	return t.EffectiveLabels(), true
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
	// Key is the partition key — the column list or expression inside
	// `PARTITION BY <strategy> (...)`, without the strategy word.
	//
	// Count and strategy DESCRIBE a partitioned table; the key is what lets one be
	// REBUILT. Without it a consumer had nothing to declare `PARTITION BY` with, so
	// the parent was recreated as an ordinary table — and the target then accepted
	// `CREATE INDEX CONCURRENTLY`, which Postgres refuses outright on a partitioned
	// table (`cannot create index on partitioned table "events" concurrently`).
	Key string `yaml:"key,omitempty"`
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

	Generated string `yaml:"generated,omitempty"` // identity | stored (§6.1)
	// Identity distinguishes GENERATED ALWAYS AS IDENTITY ("always") from
	// GENERATED BY DEFAULT AS IDENTITY ("by_default"). The difference is not
	// cosmetic: an ALWAYS column REJECTS an explicit value in an INSERT unless the
	// statement says OVERRIDING SYSTEM VALUE, so a migration that supplies one
	// behaves differently on each. Empty when the column is not an identity column.
	Identity string `yaml:"identity,omitempty"`
	// GeneratedExpression is a STORED generated column's expression, verbatim.
	//
	// `generated: stored` alone says the column is computed but not from what, and a
	// consumer cannot recreate it — so the column was rebuilt as an ordinary one and
	// the target accepted an UPDATE that production rejects with `column "total" can
	// only be updated to DEFAULT`. It is DDL, the same class of information as a
	// CHECK expression (§6.4), and gets the same privacy treatment: verbatim at
	// standard, the literal "opaque" under strict.
	GeneratedExpression string `yaml:"generated_expression,omitempty"`
	// Default is the column's DEFAULT expression, verbatim.
	//
	// It is what makes a NOT NULL column insertable without naming it, and without
	// it every migration that inserts or backfills without listing every NOT NULL
	// column failed in the target and succeeded in production — a wrong FAIL, on the
	// most ordinary statement there is.
	//
	// It runs the other way too. `ADD COLUMN ... NOT NULL DEFAULT <expr>` is THE
	// canonical migration-safety question, and whether it rewrites the table turns on
	// the default being non-volatile. The finding rules reason about the default in
	// the MIGRATION; without this field nothing knows what defaults the table already
	// has, so a fixture cannot answer "does this column already have one".
	//
	// DDL, like a CHECK (§6.4) and like GeneratedExpression: verbatim at standard,
	// the literal "opaque" under strict. An identity or generated column's implicit
	// default is NOT recorded here — `generated` already carries it, and emitting
	// `nextval(...)` would name a sequence the target does not have.
	Default string `yaml:"default,omitempty"`
	Format  string `yaml:"format,omitempty"` // a §6.3 format class

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
	// Confidence is how well the EXTREMES are known: `exact` when min/max were read
	// over the whole column, `estimated` when they came from a sample.
	//
	// It exists because a sampled range UNDERSTATES the extremes, and a finding that
	// keys off them then fails to fire at all. `ALTER TABLE ADD CONSTRAINT CHECK
	// (customer_id <= 59900)` against a true max of 60,000 is refused by the source
	// database; the fixture recorded max 59,773 from a TABLESAMPLE, the conflict
	// detector found nothing, and the verdict was PASS.
	//
	// That failure runs the OPPOSITE way to the one confidence usually guards. Capping
	// caps findings that EXIST; here the weak fact makes the finding not exist, and a
	// missing finding is a PASS no cap can reach. So the confidence has to be
	// available to the ANALYZER, which can say "the sampled extremes cannot confirm
	// or deny this" and emit a WARN, not just to the capping engine.
	Confidence Confidence `yaml:"confidence,omitempty"`
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
	Name    string   `yaml:"name"`
	Method  string   `yaml:"method"` // btree | hash | gin | gist | ...
	Columns []string `yaml:"columns"`
	// Keys are the index's key expressions as SQL text, in order, present only when
	// at least one key is an EXPRESSION rather than a plain column (RFC §6.5).
	//
	// `columns` cannot describe such an index: a unique index on `lower(email)` has
	// no column key at all, so it was recorded with an empty column list and
	// silently dropped when the disposable database was built. The production
	// uniqueness constraint then did not exist there, and a migration that violates
	// it could PASS. When present, Keys is authoritative for reconstruction while
	// `columns` keeps listing the plain-column subset the analyzers read.
	Keys []string `yaml:"keys,omitempty"`
	// Include are a covering index's INCLUDE payload columns — stored in the index
	// but NOT part of its key (RFC §6.5).
	//
	// They have to be separate from `columns` because the key is what a UNIQUE index
	// ENFORCES. Folding the payload in recorded
	// `UNIQUE (customer_id, created_at) INCLUDE (total)` as unique on all three,
	// which is strictly weaker: the disposable database then accepted rows the
	// source database rejects, and a migration that violates the real two-column
	// uniqueness reached PASS.
	Include       []string `yaml:"include,omitempty"`
	Unique        bool     `yaml:"unique,omitempty"`
	Partial       string   `yaml:"partial,omitempty"` // a partial-index predicate
	Bytes         int64    `yaml:"bytes,omitempty"`
	BloatEstimate *float64 `yaml:"bloat_estimate,omitempty"`

	X map[string]any `yaml:",inline"`
}

// Reference is a foreign-key relationship and its fan-out (RFC §6.6).
type Reference struct {
	Column string `yaml:"column"`
	To     string `yaml:"to"` // public.users.id
	// Name is the foreign-key constraint's own name.
	//
	// It carries two things a per-column entry otherwise loses. First, identity: a
	// COMPOSITE foreign key is recorded as one entry per column pair (so fan-out
	// stays per-column), and without a shared name a consumer rebuilding the schema
	// cannot tell `FOREIGN KEY (a, b) REFERENCES t (x, y)` from two independent
	// single-column keys — which constrain different things. Second, a migration
	// that names the constraint (`DROP CONSTRAINT orders_customer_fkey`) has
	// something to match against.
	Name     string `yaml:"name,omitempty"`
	OnDelete string `yaml:"on_delete,omitempty"`
	// OnUpdate is the ON UPDATE action, recorded for the same reason OnDelete is:
	// a migration that rewrites parent keys behaves differently under CASCADE than
	// under RESTRICT, and the difference is the whole question being asked.
	OnUpdate string `yaml:"on_update,omitempty"`
	// Validated distinguishes a NOT VALID foreign key, which is not enforced against
	// existing rows. A pointer because validated:false MUST be preserved and differs
	// from an absent field, exactly as it does on a Constraint (§6.4).
	Validated *bool `yaml:"validated,omitempty"`
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
