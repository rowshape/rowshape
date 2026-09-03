package fixture

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Emit assembles a profiled fixture into human-readable, diffable rowshape.yaml
// bytes (RFC §5). It validates the mandatory meta fields, completes the profile
// block, computes meta.digest over the canonical form (§11), and writes
// two-space-indented YAML with map keys sorted — small enough to commit and
// clean to diff (RFC §3.3).
//
// Emit and Canonical are different by design: Emit is the readable artifact
// (includes meta.digest and meta.generated_at, preserves the document's field
// order); Canonical is the sorted, volatile-field-free byte string that the
// digest is computed over.
func Emit(f *Fixture) ([]byte, error) {
	if err := f.prepareForEmit(); err != nil {
		return nil, err
	}
	// The digest is computed last, over everything else, and stored for readers
	// (§11). Because the canonical form excludes meta.digest, storing it here
	// does not change the value.
	if err := f.SetDigest(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// prepareForEmit validates and fills the meta block so every emitted fixture is
// complete and self-describing.
func (f *Fixture) prepareForEmit() error {
	if f.RowshapeFixture == "" {
		f.RowshapeFixture = FormatVersion
	}
	// engine.version is mandatory: cost models are engine-version-conditional and
	// a validator MUST refuse to extrapolate without it (RFC §9.1).
	if f.Meta.Engine.Name == "" {
		return fmt.Errorf("emit: meta.engine.name is required")
	}
	if f.Meta.Engine.Version == "" {
		return fmt.Errorf("emit: meta.engine.version is mandatory (RFC §9.1)")
	}
	if f.Meta.Profile.Mode == "" {
		f.Meta.Profile.Mode = "fast"
	}
	// Always emit an explicit (possibly empty) escalation list so the profile
	// block is complete and self-describing.
	if f.Meta.Profile.Escalated == nil {
		f.Meta.Profile.Escalated = []string{}
	}
	return nil
}

// VerifyDigest reparses fixture bytes, recomputes the digest over the canonical
// form, and reports whether it matches the stored meta.digest (RFC §11). This is
// how a reader confirms a committed fixture has not been hand-edited out of sync
// with its identity.
func VerifyDigest(data []byte) (ok bool, stored, recomputed string, err error) {
	f, err := Parse(data)
	if err != nil {
		return false, "", "", err
	}
	stored = f.Meta.Digest
	recomputed, err = Digest(f)
	if err != nil {
		return false, stored, "", err
	}
	return stored == recomputed, stored, recomputed, nil
}

// ParseVerified parses fixture bytes and refuses a fixture whose stored digest
// does not match its content.
//
// meta.digest is the fixture's identity (RFC §11) and the subject of every
// attestation (INV-DSSE-SHAPE). rowshape computed it, stored it, and then never
// looked at it again: a fixture edited after `pull` was trusted verbatim, and its
// stale digest sat in the file saying otherwise.
//
// That is reachable without malice — a merge resolution, a hand-tweak, a tool
// that rewrites YAML — and it is not cosmetic. A fixture edited to read
// null_fraction: {value: 0.0, confidence: exact} makes `validate` return PASS,
// exit 0, for a SET NOT NULL against a column that is 2.9% null in production.
// That is the wrong PASS INV-CONFIDENCE-CAPPING calls unrecoverable, produced by
// editing a text file, while the mechanism built to detect it was present and
// unenforced.
//
// A fixture with NO digest is accepted: hand-authored fixtures (every one in this
// repo's corpus and test suites) legitimately have none, and demanding one would
// mean rowshape only reads its own output.
func ParseVerified(data []byte) (*Fixture, error) {
	f, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if f.Meta.Digest == "" {
		return f, nil // hand-authored: nothing was claimed, nothing to check
	}
	got, err := Digest(f)
	if err != nil {
		return nil, fmt.Errorf("recomputing the fixture digest: %w", err)
	}
	if got != f.Meta.Digest {
		// Distinguish a TAMPER from a FORWARD-COMPATIBILITY drop before accusing
		// anyone of editing the file.
		//
		// ParseVerified recomputes the digest from the PARSED STRUCT, and parsing
		// silently discards keys this build does not know (pruneExtensions). So a
		// fixture written by a newer rowshape — carrying a field added since —
		// digests over that field at emit, loses it here, and hashes differently.
		// The old message called that "the file was modified after `rowshape
		// pull`", which is simply false: nobody edited anything, and its advice
		// (re-run pull, or delete meta.digest) is wrong for that case.
		if dropped := droppedKeys(data); len(dropped) > 0 {
			return nil, fmt.Errorf(
				"fixture digest mismatch, and this build did not understand %d field(s) in the file: %s\n"+
					"  meta.digest claims: %s\n"+
					"  content hashes to:  %s\n"+
					"This looks like a fixture written by a NEWER rowshape rather than an edited one — the "+
					"unknown fields were part of what it digested, and this build dropped them. Upgrade "+
					"rowshape, or re-run `rowshape pull` with this build to regenerate the fixture",
				len(dropped), strings.Join(dropped, ", "), f.Meta.Digest, got)
		}
		return nil, fmt.Errorf(
			"fixture digest mismatch: the file was modified after `rowshape pull`\n"+
				"  meta.digest claims: %s\n"+
				"  content hashes to:  %s\n"+
				"Re-run `rowshape pull` to regenerate it, or delete meta.digest if you are hand-authoring this fixture",
			f.Meta.Digest, got)
	}
	return f, nil
}

// droppedKeys reports YAML keys present in the raw document that this build's
// model does not carry, sorted and de-duplicated.
//
// It exists to tell "someone edited this fixture" apart from "this fixture came
// from a newer rowshape". Both produce the same symptom — a digest mismatch —
// and they want opposite advice.
//
// It walks the raw document and the re-marshalled parsed struct in parallel, so
// it reports what parsing ACTUALLY lost rather than inferring from the version
// string: a newer build might add a field without bumping a minor, and the
// fixture would still fail here.
//
// x_-prefixed vendor extensions are excluded, since those are preserved by
// design (RFC §12) and their presence is not evidence of anything.
func droppedKeys(raw []byte) []string {
	var before, after map[string]any
	if err := yaml.Unmarshal(raw, &before); err != nil {
		return nil
	}
	var f Fixture
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil
	}
	out, err := yaml.Marshal(&f)
	if err != nil {
		return nil
	}
	if err := yaml.Unmarshal(out, &after); err != nil {
		return nil
	}

	seen := map[string]bool{}
	var missing []string
	var walk func(a, b map[string]any, path string)
	walk = func(a, b map[string]any, path string) {
		for k, av := range a {
			if strings.HasPrefix(k, "x_") {
				continue
			}
			full := k
			if path != "" {
				full = path + "." + k
			}
			bv, present := b[k]
			if !present {
				if !seen[full] {
					seen[full] = true
					missing = append(missing, full)
				}
				continue
			}
			am, aok := av.(map[string]any)
			bm, bok := bv.(map[string]any)
			if aok && bok {
				walk(am, bm, full)
			}
		}
	}
	walk(before, after, "")
	sort.Strings(missing)
	return missing
}

// The committable-size budget (RFC §3.3): a fixture has to fit in a git diff, or
// nobody commits it and the whole premise fails.
//
// The numbers are what the emitter ACTUALLY costs, measured from a real
// `rowshape pull`, not an aspiration. §3.3 originally said "under 100KB for a
// 200-table schema"; a real 200-table pull is ~246KB, and the only test guarding
// the claim built its fixture by hand with deliberately sparse facts and passed
// with under 1% headroom. Nothing was wrong with the emitter — the weight is
// spread evenly across `range`, `distinct`, `unique` and their confidences, all
// load-bearing — so the CLAIM was corrected rather than facts dropped to meet it
// (docs/DECISIONS.md D-021).
const (
	// BudgetTables is the schema size the budget is quoted for.
	BudgetTables = 200
	// BudgetBytes is the size a BudgetTables-table fixture must stay under.
	BudgetBytes = 320 * 1024
)

// OverBudget reports whether a fixture of this size and table count exceeds the
// §3.3 budget, scaled to its actual table count. Zero tables is never over: there
// is nothing to be over about.
func OverBudget(size, tables int) bool {
	if tables <= 0 {
		return false
	}
	return size/tables*BudgetTables > BudgetBytes
}
