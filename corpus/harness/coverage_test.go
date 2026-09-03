package harness_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestCoverageTableMatchesTheCorpus keeps corpus/README.md's coverage table
// honest against the corpus it describes.
//
// The table is the credibility asset's self-description: which finding families
// have cases, which severities they reach, and whether the capping contract is
// asserted (CR-T15). It is hand-maintained, and it HAD DRIFTED — claiming 5
// RS-INDEX cases against 9, and 4 negative cases against 3 — which is the failure
// mode of any coverage record nothing verifies: it describes the corpus someone
// last remembered, not the one that exists. A stale gap list is worse than none,
// because it is read as current.
//
// Same discipline as TestFindingsDocsUpToDate, which keeps the generated finding
// pages from going stale against the registry.
func TestCoverageTableMatchesTheCorpus(t *testing.T) {
	type fam struct {
		cases   map[string]bool
		sevs    map[string]bool
		resolve int
	}
	families := map[string]*fam{}
	negatives := 0

	dirs, err := filepath.Glob(filepath.Join("..", "cases", "*", "expected.json"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no corpus cases found: %v", err)
	}
	for _, p := range dirs {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var e struct {
			Findings []struct {
				Code            string `json:"code"`
				Severity        string `json:"severity"`
				ResolveContains string `json:"resolve_contains"`
			} `json:"findings"`
		}
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		name := filepath.Base(filepath.Dir(p))
		if len(e.Findings) == 0 {
			negatives++
			continue
		}
		for _, f := range e.Findings {
			g := families[f.Code]
			if g == nil {
				g = &fam{cases: map[string]bool{}, sevs: map[string]bool{}}
				families[f.Code] = g
			}
			g.cases[name] = true
			g.sevs[f.Severity] = true
			if f.ResolveContains != "" {
				g.resolve++
			}
		}
	}

	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	text := string(readme)

	// One row per family: | `RS-DATA` | 6 | error, warn | yes (3) |
	row := regexp.MustCompile("(?m)^\\| `(RS-[A-Z]+)` \\| (\\d+) \\| ([^|]+) \\| ([^|]+) \\|$")
	documented := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(text, -1) {
		code, countStr := m[1], m[2]
		documented[code] = true
		g := families[code]
		if g == nil {
			t.Errorf("README documents family %s, which no corpus case names", code)
			continue
		}
		want := len(g.cases)
		got, _ := strconv.Atoi(countStr)
		if got != want {
			t.Errorf("README says %s has %d case(s); the corpus has %d", code, got, want)
		}
		var sevs []string
		for s := range g.sevs {
			sevs = append(sevs, s)
		}
		sort.Strings(sevs)
		if strings.TrimSpace(m[3]) != strings.Join(sevs, ", ") {
			t.Errorf("README says %s covers %q; the corpus covers %q", code, strings.TrimSpace(m[3]), strings.Join(sevs, ", "))
		}
		wantCap := "no"
		if g.resolve > 0 {
			wantCap = "yes (" + strconv.Itoa(g.resolve) + ")"
		}
		if strings.TrimSpace(m[4]) != wantCap {
			t.Errorf("README says %s capping contract %q; the corpus says %q", code, strings.TrimSpace(m[4]), wantCap)
		}
	}

	// A family with cases but no row is the drift that matters most: a new family
	// is invisible in the record that exists to make coverage visible.
	for code := range families {
		if !documented[code] {
			t.Errorf("corpus has %s cases but README's coverage table has no row for it", code)
		}
	}

	negRow := regexp.MustCompile(`\|\s*_\(negative cases: assert NO finding\)_\s*\|\s*(\d+)\s*\|`)
	m := negRow.FindStringSubmatch(text)
	if m == nil {
		t.Fatal("README has no negative-cases row")
	}
	if got, _ := strconv.Atoi(m[1]); got != negatives {
		t.Errorf("README says %d negative case(s); the corpus has %d", got, negatives)
	}
}
