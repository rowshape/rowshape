package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPassingStoriesArtifactsExist audits prd.json: every artifact claimed by a
// story marked `passes: true` must actually be on disk.
//
// Why this is a real check and not bookkeeping: prd.json IS the loop's source of
// truth, and P0-T6's own note records a story that claimed passes:true with
// "golangci-lint 0 issues" when there were 25. A false green has happened here
// before. Running this audit for the first time found 13 stale paths — five from
// architecture that moved (internal/engine/postgres/* became internal/profile/*,
// internal/extrapolate became internal/estimate, findings/perf.go became
// rsperf.go), one from a decision that changed the implementation
// (testcontainers.go, dropped for the docker CLI per D-005), and four written in
// this session by a story that named a test file and then put the tests in an
// existing one.
//
// Skipped shapes are declared, not silently ignored: an artifact containing a
// glob or brace expansion, or ending in a parenthetical annotation like
// "cmd/pull.go (wiring)", is a description rather than a path.
func TestPassingStoriesArtifactsExist(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "prd.json"))
	if err != nil {
		t.Fatalf("read prd.json: %v", err)
	}
	var doc struct {
		Phases []struct {
			ID    string `json:"id"`
			Tasks []struct {
				ID        string   `json:"id"`
				Passes    bool     `json:"passes"`
				Artifacts []string `json:"artifacts"`
			} `json:"tasks"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("prd.json is not valid JSON: %v", err)
	}

	descriptive := func(a string) bool {
		return strings.ContainsAny(a, "*?[{") || strings.HasSuffix(a, ")")
	}

	var checked, skipped int
	for _, ph := range doc.Phases {
		for _, task := range ph.Tasks {
			if !task.Passes {
				continue
			}
			for _, a := range task.Artifacts {
				if descriptive(a) {
					skipped++
					continue
				}
				checked++
				if _, err := os.Stat(filepath.Join(root, a)); err != nil {
					t.Errorf("%s is marked passes:true but claims artifact %q, which does not exist. "+
						"Either the file moved (update the story) or the work is not actually done.",
						task.ID, a)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no artifacts were checked; this audit would pass over nothing")
	}
	t.Logf("verified %d artifacts of passing stories (%d descriptive entries skipped)", checked, skipped)
}

// TestPRDIsValidJSON is the cheapest possible guard on the loop's source of
// truth. It is here because this session corrupted prd.json once, by
// hand-escaping a note through a shell heredoc so a backslash-n reached the file
// as a real newline inside a JSON string — and the commit landed before anything
// noticed.
func TestPRDIsValidJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "prd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("prd.json is not valid JSON: %v", err)
	}
}

// TestEveryInvariantIsTraceable: each invariant in prd.json must be findable
// from the test tree — either cited by name in a test (that is the guard), or
// owned by a story that is not yet passing (the work is not done, so no guard is
// expected).
//
// Invariants are the promises every story is bound by, so one with no reachable
// enforcement is the most expensive kind of gap. Grepping the id is how that gets
// audited, which only works if guards name what they guard — so this enforces the
// convention rather than trusting it.
func TestEveryInvariantIsTraceable(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "prd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Invariants []struct {
			ID string `json:"id"`
		} `json:"invariants"`
		Phases []struct {
			Tasks []struct {
				ID       string   `json:"id"`
				Passes   bool     `json:"passes"`
				SpecRefs []string `json:"spec_refs"`
			} `json:"tasks"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Invariants) == 0 {
		t.Fatal("no invariants found; this audit would pass over nothing")
	}

	// Every _test.go file's contents, once.
	var testSrc strings.Builder
	err = filepath.WalkDir(root, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			if de != nil && de.IsDir() && (de.Name() == "node_modules" || de.Name() == ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			b, rerr := os.ReadFile(path)
			if rerr == nil {
				testSrc.Write(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := testSrc.String()

	// An invariant whose owning story is still open needs no guard yet.
	pending := map[string]bool{}
	for _, ph := range doc.Phases {
		for _, task := range ph.Tasks {
			if task.Passes {
				continue
			}
			for _, ref := range task.SpecRefs {
				if strings.HasPrefix(ref, "INV-") {
					pending[ref] = true
				}
			}
		}
	}

	for _, inv := range doc.Invariants {
		if strings.Contains(tests, inv.ID) {
			continue
		}
		if pending[inv.ID] {
			t.Logf("%s: no test cites it, but its owning story is not yet passing — expected", inv.ID)
			continue
		}
		t.Errorf("%s is cited by no test and no open story claims it. Either a guard exists but "+
			"does not name the invariant it enforces (cite it, so this is auditable), or the "+
			"promise is unenforced.", inv.ID)
	}
}

// knownOrderingViolations are passing stories whose dependency does NOT pass,
// each allowed with a stated reason. New ones must not appear silently.
//
// The loop's rule is "a story may only be selected when every id in its
// depends_on has passes: true". Both entries below break it, and neither is
// bookkeeping: each depends on an EXTERNAL act (reserving a namespace,
// publishing a repo) that no amount of local work can satisfy, so the code was
// written against an assumption the dependency exists to remove. See D-016.
// Empty, and that is the point: every ordering violation this map ever held has
// been RESOLVED rather than permanently exempted.
//
// Both entries were the same root — work accepted while P0-T1 (namespace
// reservation) was still blocked. P0-T3 fixed the module path on an unreserved
// name, and P0-T2 published the spec repo before the domain was confirmed. P0-T1
// now passes on all four criteria, checked against the live services, so neither
// violation exists to exempt.
//
// The audit refuses a stale entry precisely so this map cannot quietly become a
// list of things nobody re-examines. Add an entry only with a reason, and expect
// to be told when the reason expires.
var knownOrderingViolations = map[string]string{}

// TestNoNewOrderingViolations: a passing story whose dependency does not pass
// means work was accepted before its prerequisite. That is worth catching,
// because the prerequisite usually encodes a risk the work then inherits.
func TestNoNewOrderingViolations(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "prd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Phases []struct {
			Tasks []struct {
				ID        string   `json:"id"`
				Passes    bool     `json:"passes"`
				DependsOn []string `json:"depends_on"`
			} `json:"tasks"`
		} `json:"phases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	passes := map[string]bool{}
	var all []struct {
		ID        string
		Passes    bool
		DependsOn []string
	}
	for _, ph := range doc.Phases {
		for _, task := range ph.Tasks {
			passes[task.ID] = task.Passes
			all = append(all, struct {
				ID        string
				Passes    bool
				DependsOn []string
			}{task.ID, task.Passes, task.DependsOn})
		}
	}
	if len(all) == 0 {
		t.Fatal("no stories parsed; this audit would pass over nothing")
	}

	seen := map[string]bool{}
	for _, task := range all {
		if !task.Passes {
			continue
		}
		for _, dep := range task.DependsOn {
			known, isDep := passes[dep]
			if !isDep {
				t.Errorf("%s depends on %q, which is not a story", task.ID, dep)
				continue
			}
			if known {
				continue
			}
			key := task.ID + "->" + dep
			seen[key] = true
			if reason, ok := knownOrderingViolations[key]; ok {
				t.Logf("known ordering violation %s: %s", key, reason)
				continue
			}
			t.Errorf("%s is marked passes:true but its dependency %s is not. The loop's rule is "+
				"that a story may only be selected once every depends_on passes — accepting work "+
				"before its prerequisite means the work inherits whatever risk the prerequisite "+
				"exists to remove. If this is deliberate, add it to knownOrderingViolations WITH "+
				"A REASON.", task.ID, dep)
		}
	}

	// A stale allow-list entry is its own bug: it would silently permit a real
	// violation later.
	for key := range knownOrderingViolations {
		if !seen[key] {
			t.Errorf("knownOrderingViolations has a stale entry %q — the violation is gone, so "+
				"remove the exemption rather than leaving it to cover a future one", key)
		}
	}
}

// TestMilestonePhasesResolve: every phase a milestone names must be a real
// phase. M1 carried the prose string "phase-1 / phase-1b" where every other
// milestone used an id, so the Week-6 gate milestone could not be linked to the
// stories that satisfy it — the record existed but was not answerable.
func TestMilestonePhasesResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "prd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Phases []struct {
			ID    string `json:"id"`
			Tasks []struct {
				Passes bool `json:"passes"`
			} `json:"tasks"`
		} `json:"phases"`
		Milestones struct {
			List []struct {
				ID     string   `json:"id"`
				Name   string   `json:"name"`
				Phases []string `json:"phases"`
			} `json:"list"`
		} `json:"milestones"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Milestones.List) == 0 {
		t.Fatal("no milestones parsed; this audit would pass over nothing")
	}

	known := map[string]int{} // phase id -> passing story count
	total := map[string]int{}
	for _, ph := range doc.Phases {
		total[ph.ID] = len(ph.Tasks)
		for _, task := range ph.Tasks {
			if task.Passes {
				known[ph.ID]++
			}
		}
	}

	for _, m := range doc.Milestones.List {
		if len(m.Phases) == 0 {
			t.Errorf("milestone %s names no phases, so nothing can decide whether it is met", m.ID)
			continue
		}
		for _, p := range m.Phases {
			if _, ok := total[p]; !ok {
				t.Errorf("milestone %s references phase %q, which does not exist", m.ID, p)
				continue
			}
			t.Logf("%s -> %s: %d/%d stories passing", m.ID, p, known[p], total[p])
		}
	}
}
