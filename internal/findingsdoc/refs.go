package findingsdoc

import "strings"

// specSections names the fixture-spec (RFC-0001) sections the catalog cites, so
// a reference renders as something a reader can follow — "Fixture spec §9.1:
// Extrapolation is model-based, not linear", linked to the on-site spec page —
// instead of a bare section number. refs_test.go checks every title against the
// headings of RFC-0001-rowshape-fixture-spec.md, so a renumbered spec fails the
// test rather than publishing a wrong pointer.
var specSections = map[string]string{
	"6.1":  "Universal fields",
	"6.4":  "Constraints",
	"6.5":  "Indexes",
	"6.6":  "References and fan-out",
	"7.2":  "Uniqueness is never estimated",
	"7.4":  "Verdict capping (the payoff)",
	"9.1":  "Extrapolation is model-based, not linear",
	"13":   "Conformance",
	"14.2": "Partitioned tables", // open question 2, resolved normatively in §6.9
}

// specRoute is the on-site page for the fixture spec. It has no per-section
// anchors, so every reference links the page itself.
const specRoute = "/spec/"

// publicReferences turns the catalog's References into lines a site reader can
// use. The catalog cites two kinds of document: the fixture spec (RFC-0001),
// which is published and has a page on this site, and the internal product
// plan ("PRD §10"), which is not published anywhere a reader could open it. A
// citation the reader cannot follow is noise, so plan references are dropped
// here and spec references become links. The catalog itself keeps them because
// it is also the maintainers' trace from a finding back to the commitment it
// implements.
func publicReferences(refs []string) []string {
	var out []string
	for _, r := range refs {
		sec, ok := strings.CutPrefix(r, "RFC §")
		if !ok {
			continue
		}
		label := "Fixture spec §" + sec
		if t := specSections[sec]; t != "" {
			label += ": " + t
		}
		out = append(out, "["+label+"]("+specRoute+")")
	}
	return out
}
