package validate

import (
	"sync"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
	"github.com/rowshape/rowshape/internal/verdict"
)

type noopAnalyzer struct{ code string }

func (n noopAnalyzer) Analyze(*fixture.Fixture, *Capture) []verdict.Finding { return nil }

// TestRegistryIsRaceFree exercises concurrent Register and Registered under
// -race.
//
// Only init functions write the registry in the CLI, so this was safe in
// practice — but Register is EXPORTED and the stated phase-5 goal is a cloud API
// importing this package, where a request-time Register is an unsynchronized
// append (a data race and a torn slice header) and a caller holding the live
// backing array could observe a concurrent append mid-write while BuildResult
// was ranging it.
//
// Run with: go test -race ./internal/validate/
func TestRegistryIsRaceFree(t *testing.T) {
	before := len(Registered())

	var wg sync.WaitGroup
	const writers, readers, iters = 4, 8, 200

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				Register(noopAnalyzer{code: "x"})
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Range it the way BuildResult does: this is where observing a
				// concurrent append mid-write would bite.
				for _, a := range Registered() {
					_ = a
				}
			}
		}()
	}
	wg.Wait()

	if got, want := len(Registered()), before+writers*iters; got != want {
		t.Errorf("registry length = %d, want %d — an append was lost", got, want)
	}
}

// Registered must hand out a COPY: returning the package's own backing array let
// a caller mutate the registry by writing through the slice it was given.
func TestRegisteredReturnsACopy(t *testing.T) {
	Register(noopAnalyzer{code: "original"})
	got := Registered()
	if len(got) == 0 {
		t.Fatal("registry unexpectedly empty")
	}

	// Overwrite the caller's slice; the registry must be unaffected.
	for i := range got {
		got[i] = noopAnalyzer{code: "clobbered"}
	}

	again := Registered()
	for i, a := range again {
		if n, ok := a.(noopAnalyzer); ok && n.code == "clobbered" {
			t.Fatalf("entry %d was clobbered through the returned slice — Registered handed out the live backing array", i)
		}
	}
}
