package findingsdoc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/findings"
)

// TestSpecSectionTitlesMatchRFC: a rendered reference names the section it
// points at, so the name must be the spec's own heading for that number.
func TestSpecSectionTitlesMatchRFC(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "RFC-0001-rowshape-fixture-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	rfc := string(raw)
	for sec, title := range specSections {
		if sec == "14.2" {
			// An open question, not a heading; it was resolved into §6.9.
			if !strings.Contains(rfc, "### 6.9 "+title+"\n") {
				t.Errorf("§14.2 is labelled %q but §6.9 is not titled that", title)
			}
			continue
		}
		re := regexp.MustCompile(`(?m)^#{2,3} ` + regexp.QuoteMeta(sec) + `\.? ` + regexp.QuoteMeta(title) + `$`)
		if !re.MatchString(rfc) {
			t.Errorf("RFC-0001 has no heading %q titled %q", sec, title)
		}
	}
}

// TestEveryCitedSpecSectionIsNamed: a new RFC citation in the catalog must get
// a title here, or its link would read as a bare number.
func TestEveryCitedSpecSectionIsNamed(t *testing.T) {
	for _, code := range findings.Codes() {
		e, _ := findings.Explain(code)
		for _, r := range e.References {
			if sec, ok := strings.CutPrefix(r, "RFC §"); ok && specSections[sec] == "" {
				t.Errorf("%s cites RFC §%s, which has no entry in specSections", code, sec)
			}
		}
	}
}

// TestPublicReferencesDropPlanAndLinkSpec: the internal plan is never shown,
// the spec always is, as a link.
func TestPublicReferencesDropPlanAndLinkSpec(t *testing.T) {
	got := publicReferences([]string{"RFC §9.1", "PRD §10"})
	if len(got) != 1 || got[0] != "[Fixture spec §9.1: Extrapolation is model-based, not linear](/spec/)" {
		t.Errorf("publicReferences = %q", got)
	}
}
