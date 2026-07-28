package fixture

import (
	"strings"
	"testing"
)

// TestForwardCompatMismatchIsNotCalledTampering pins that the two causes of a
// digest mismatch get different diagnoses.
//
// ParseVerified recomputes the digest from the PARSED STRUCT, and parsing
// silently discards keys this build does not know. So a fixture from a NEWER
// rowshape — carrying a field added since — digests over that field at emit,
// loses it here, and hashes differently. The message called that "the file was
// modified after `rowshape pull`", which is false, and offered advice (re-run
// pull, delete meta.digest) that does not fit.
func TestForwardCompatMismatchIsNotCalledTampering(t *testing.T) {
	// A fixture that is valid EXCEPT that it carries a field this build does not
	// model, and a digest that accounted for it.
	src := `rowshape_fixture: "1"
meta:
  id: from-the-future
  generated_at: "2026-01-01T00:00:00Z"
  generator: rowshape/99
  engine: {name: postgres, version: "16"}
  profile: {mode: fast, escalated: []}
  digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
tables:
  public.t:
    rows: {value: 10, confidence: exact}
    future_table_fact: {something: 1}
    columns:
      c: {type: text, nullable: true}
`
	_, err := ParseVerified([]byte(src))
	if err == nil {
		t.Fatal("a digest mismatch must still be refused")
	}
	msg := err.Error()
	if strings.Contains(msg, "was modified after") {
		t.Errorf("a forward-compat drop must not be reported as tampering:\n%s", msg)
	}
	if !strings.Contains(msg, "NEWER rowshape") {
		t.Errorf("the message must name the likely cause:\n%s", msg)
	}
	if !strings.Contains(msg, "future_table_fact") {
		t.Errorf("the message must name the field(s) it did not understand:\n%s", msg)
	}
}

// A genuine edit — no unknown fields, just changed values — must still be called
// what it is. The tamper message is the one that matters for INV-CONFIDENCE-CAPPING.
func TestGenuineTamperIsStillCalledTampering(t *testing.T) {
	src := `rowshape_fixture: "1"
meta:
  id: edited
  generated_at: "2026-01-01T00:00:00Z"
  generator: rowshape/1
  engine: {name: postgres, version: "16"}
  profile: {mode: fast, escalated: []}
  digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
tables:
  public.t:
    rows: {value: 10, confidence: exact}
    columns:
      c: {type: text, nullable: true}
`
	_, err := ParseVerified([]byte(src))
	if err == nil {
		t.Fatal("a digest mismatch must be refused")
	}
	if !strings.Contains(err.Error(), "was modified after") {
		t.Errorf("with no unknown fields, a mismatch IS an edit and must say so:\n%s", err)
	}
}

// x_ vendor extensions are preserved by design (RFC §12), so their presence must
// not be mistaken for evidence of a newer build.
func TestVendorExtensionsAreNotTreatedAsUnknownFields(t *testing.T) {
	if got := droppedKeys([]byte(`rowshape_fixture: "1"
meta: {id: t, engine: {name: postgres, version: "16"}}
x_vendor: {anything: 1}
tables:
  public.t:
    rows: {value: 10, confidence: exact}
    x_note: hello
    columns: {c: {type: text, nullable: true}}
`)); len(got) != 0 {
		t.Errorf("x_ extensions are preserved by design and must not count as dropped, got %v", got)
	}
}
