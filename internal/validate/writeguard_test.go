package validate

import (
	"errors"
	"testing"

	"github.com/rowshape/rowshape/internal/profile"
)

// TestWriteTargetFailsClosedWithoutASource is the regression for the
// highest-severity fail-open in the repo.
//
// `validate --target` opens a transaction, executes the migration DDL and
// COMMITS it. The single guard between that and a production database returned
// nil whenever meta.source was empty — so it silently switched itself off for any
// hand-written, vendored or hand-edited fixture, which are exactly the fixtures
// least likely to have come from a careful `rowshape pull`.
func TestWriteTargetFailsClosedWithoutASource(t *testing.T) {
	err := CheckWriteTarget("", "db.prod.internal", false)
	if err == nil {
		t.Fatal("a source-less fixture must not license a write to an arbitrary target")
	}
	if !errors.Is(err, ErrNoFixtureSource) {
		t.Errorf("want ErrNoFixtureSource, got %v", err)
	}
}

// The override exists, mirroring the precedent `pull` sets for its superuser
// refusal: decline by default, and let someone who understands the situation say
// so explicitly.
func TestWriteTargetOverride(t *testing.T) {
	if err := CheckWriteTarget("", "db.staging.internal", true); err != nil {
		t.Errorf("--i-know-target-is-writable must permit it, got %v", err)
	}
}

// The override must NOT defeat the host-match refusal. Acknowledging "I know
// this is writable" is not the same as "write to the database this fixture was
// pulled from".
func TestOverrideDoesNotDefeatTheHostMatch(t *testing.T) {
	src := profile.HashSource("db.prod.internal")
	if err := CheckWriteTarget(src, "db.prod.internal", true); err == nil {
		t.Fatal("the override must never license writing to the fixture's OWN source host")
	}
	if err := CheckWriteTarget(src, "db.prod.internal", false); err == nil {
		t.Fatal("the host-match refusal must still fire")
	}
}

// A fixture WITH a source, targeting a different host, is the normal case and
// must proceed without the override.
func TestWriteTargetNormalCase(t *testing.T) {
	src := profile.HashSource("db.prod.internal")
	if err := CheckWriteTarget(src, "branch-123.neon.tech", false); err != nil {
		t.Errorf("a source-bearing fixture against a different host must proceed, got %v", err)
	}
}

// The EPHEMERAL path stays permissive on a missing source, deliberately: it
// creates and drops a throwaway database rather than committing to one, and
// refusing every source-less fixture there would break hand-authored and
// vendored fixtures for no proportionate gain.
func TestEphemeralPathStaysPermissive(t *testing.T) {
	if err := CheckHost("", "db.anywhere.internal"); err != nil {
		t.Errorf("the disposable path must tolerate a source-less fixture, got %v", err)
	}
	// But it must still refuse the source host itself.
	src := profile.HashSource("db.prod.internal")
	if err := CheckHost(src, "db.prod.internal"); err == nil {
		t.Error("the disposable path must still refuse the fixture's own source host")
	}
}
