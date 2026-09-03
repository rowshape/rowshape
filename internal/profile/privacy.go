package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/rowshape/rowshape/internal/fixture"
)

// Privacy is a fixture privacy level (RFC §8.2).
type Privacy string

const (
	// PrivacyStrict emits no numeric/temporal range, no histograms, no
	// values/frequencies, and no verbatim CHECK expressions.
	PrivacyStrict Privacy = "strict"
	// PrivacyStandard is the default: ranges, histograms, and CHECK expressions
	// are emitted, but never value sets.
	PrivacyStandard Privacy = "standard"
	// PrivacyPermissive additionally materializes small, safe value sets.
	PrivacyPermissive Privacy = "permissive"
)

// DefaultK is the minimum per-value occurrence count for a value to appear under
// permissive privacy (RFC §8.2). A value seen fewer than k times could identify
// an individual and is withheld.
const DefaultK = 20

// permissiveMaxDistinct caps the cardinality at which a value set may be
// materialized under permissive privacy (RFC §8.2).
const permissiveMaxDistinct = 50

// sourceSalt is a fixed application salt for meta.source (RFC §8.4). §8.4 wants
// a per-fixture salt, but meta.source is part of the canonical form (§11), so a
// random salt would make the digest unstable across runs. A fixed salt keeps the
// digest stable while still never publishing the hostname — which is all §8.4
// claims to defend ("casual disclosure, not a determined attacker").
const sourceSalt = "rowshape-fixture-source-v1"

// ParsePrivacy validates a privacy level. An empty string is standard — the
// default MUST NOT be permissive (RFC §8.2).
func ParsePrivacy(s string) (Privacy, error) {
	switch Privacy(s) {
	case "", PrivacyStandard:
		return PrivacyStandard, nil
	case PrivacyStrict:
		return PrivacyStrict, nil
	case PrivacyPermissive:
		return PrivacyPermissive, nil
	default:
		return "", fmt.Errorf("unknown privacy level %q (want strict | standard | permissive)", s)
	}
}

// HashSource returns meta.source: a salted SHA-256 hash of the source host,
// never the hostname itself (RFC §8.4). An empty host yields an empty source.
//
// The host is normalized first so that one machine hashes to one value. Without
// it, `DB.Internal` and `db.internal` — the same host, since DNS is
// case-insensitive — produce different hashes, and validate's host-match refusal
// (INV-BLAST-RADIUS-ZERO) fails to fire between them. Normalizing here rather
// than only at comparison time is what makes the two directions symmetric: a hash
// cannot be inverted, so if the odd spelling is the one recorded in meta.source,
// no amount of work at check time can recover it.
func HashSource(host string) string {
	if host == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceSalt + "\x00" + NormalizeHost(host)))
	return fixture.DigestPrefix + hex.EncodeToString(sum[:])
}

// NormalizeHost reduces the spellings of one host to a single form: lowercase
// (DNS is case-insensitive, RFC 4343), no trailing FQDN dot, no IPv6 brackets.
//
// It deliberately does NOT resolve DNS. A hostname and its IP are the same
// machine, but learning that means a network call from inside a safety check, and
// an answer that can change between the check and the connection.
func NormalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return h
}

// ApplyPrivacy enforces a privacy level over a fixture in place (RFC §8.2). It is
// the single emit-time gate: per-column `redact` overrides are applied first and
// always win, then the level's field matrix. If k <= 0 the default is used.
// It MUTATES f IN PLACE. SINGLE-OWNER CONTRACT: the caller must own f
// exclusively. See validate.MarkExact for why this matters to the planned
// phase-5 cloud API — a cached, shared fixture cannot be passed here.
func ApplyPrivacy(f *fixture.Fixture, level Privacy, k int) {
	if k <= 0 {
		k = DefaultK
	}
	for tname, tbl := range f.Tables {
		for cname, col := range tbl.Columns {
			applyColumnPrivacy(&col, level, k)
			tbl.Columns[cname] = col
		}
		if level == PrivacyStrict {
			// Under strict, verbatim CHECK expressions become opaque (RFC §6.4).
			for i := range tbl.Constraints {
				if tbl.Constraints[i].Kind == "check" && tbl.Constraints[i].Expression != "" {
					tbl.Constraints[i].Expression = "opaque"
				}
			}
			// A STORED generated column's expression is the same class of information
			// as a CHECK — DDL that can name business logic — so it gets the same
			// treatment. `generated: stored` survives, so a consumer still knows the
			// column is computed and can report that it cannot reproduce it.
			for cname, col := range tbl.Columns {
				changed := false
				if col.GeneratedExpression != "" {
					col.GeneratedExpression = "opaque"
					changed = true
				}
				// A DEFAULT is DDL of the same class and can embed a literal from the
				// business domain, so it gets the same treatment.
				if col.Default != "" {
					col.Default = "opaque"
					changed = true
				}
				if changed {
					tbl.Columns[cname] = col
				}
			}
		}
		f.Tables[tname] = tbl
	}

	if level == PrivacyStrict {
		applyTypePrivacy(f)
	}
}

// applyTypePrivacy redacts user-defined type definitions under privacy:strict
// (RFC §6.7, §8.2).
//
// Enum labels and a domain's CHECK are the same class of information as a table's
// verbatim CHECK expression, which strict already turns opaque: both are DDL text
// that can name things from the business domain (an enum of internal plan tiers, a
// bound that reveals a threshold). So strict withholds them.
//
// What strict does NOT drop is label_count. Cardinality is shape, and dropping it
// would make an enum column unhydratable rather than merely anonymous — the same
// trade strict already makes for a column, where `distinct` survives and the value
// set does not. The emitter synthesizes placeholder labels from the count, so a
// strict fixture still reconstructs a type of the right size.
func applyTypePrivacy(f *fixture.Fixture) {
	for name, t := range f.Types {
		switch t.Kind {
		case "enum":
			if t.LabelCount == 0 {
				t.LabelCount = len(t.Labels)
			}
			t.Labels = nil
		case "domain":
			if t.Check != "" {
				t.Check = "opaque"
			}
		}
		f.Types[name] = t
	}
}

// applyColumnPrivacy redacts one column: per-column overrides first, then the
// level's rules.
func applyColumnPrivacy(col *fixture.Column, level Privacy, k int) {
	redact := redactSet(col.Redact)
	switch {
	case redact["all"]:
		// "opaque free_text only" (RFC §8.2): drop every value-derived stat.
		col.Range = nil
		col.Histogram = nil
		col.Values = nil
		col.Frequencies = nil
		col.Length = nil
		col.Shape = nil
		col.Format = fmtOpaque
	default:
		if redact["range"] {
			col.Range = nil
		}
		if redact["histogram"] {
			col.Histogram = nil
		}
		if redact["values"] {
			col.Values = nil
			col.Frequencies = nil
		}
		if redact["frequencies"] {
			col.Frequencies = nil
		}
		if redact["length"] {
			col.Length = nil
		}
	}

	switch level {
	case PrivacyStrict:
		col.Range = nil
		col.Histogram = nil
		col.Values = nil
		col.Frequencies = nil
	case PrivacyStandard:
		col.Values = nil
		col.Frequencies = nil
	case PrivacyPermissive:
		if !permissiveValuesAllowed(col, k) {
			col.Values = nil
			col.Frequencies = nil
		}
	}
}

// permissiveValuesAllowed reports whether a value set is safe to publish under
// permissive privacy: distinct <= 50 AND every value was OBSERVED at least k
// times in the sample the value set came from (RFC §8.2).
//
// The count must come from the sample, not from frequency × declared rows. A
// frequency is a proportion over at most SampleN rows, so its smallest non-zero
// value is 1/SampleN; multiplying that by an estimated row count yields
// rows/SampleN, which exceeds k on any table past k×SampleN rows. With the
// defaults (k=20, SampleN=500) that gate stops rejecting anything at 10,000
// rows: a value seen exactly ONCE in the sample of a 100M-row table scored
// 200,000 and was published verbatim. The gate got weaker as the table — and
// so the re-identification risk — got larger, which inverts its purpose.
//
// Counting observations instead is both sound and conservative: k occurrences
// in the sample imply at least k in the table, so a published value is never
// rarer than it appears. Values rare enough to identify someone are simply too
// rare to survive sampling, which is the property §8.2 actually wants.
func permissiveValuesAllowed(col *fixture.Column, k int) bool {
	if len(col.Values) == 0 {
		return false
	}
	if col.Distinct == nil || col.Distinct.Value > permissiveMaxDistinct {
		return false
	}
	if len(col.Frequencies) != len(col.Values) {
		return false
	}
	// No denominator means the observed count cannot be recovered (e.g. a
	// fixture read back from disk, where SampleN is not serialized). Withhold
	// rather than guess: the gate fails closed.
	if col.SampleN <= 0 {
		return false
	}
	for _, fr := range col.Frequencies {
		// Round rather than truncate: frequencies are stored rounded to 6
		// places, so a value seen exactly k times can land a hair under k/n.
		if int(math.Round(fr*float64(col.SampleN))) < k {
			return false
		}
	}
	return true
}

// redactSet turns a column's redact list into a lookup set.
func redactSet(r fixture.Redact) map[string]bool {
	if len(r) == 0 {
		return nil
	}
	set := make(map[string]bool, len(r))
	for _, tok := range r {
		set[tok] = true
	}
	return set
}
