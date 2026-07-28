package rlog

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() {
		SetOutput(os.Stderr) // restore, never nil: slog would write to a nil writer
		_ = SetLevel(LevelInfo)
	})
	return &buf
}

func TestLevelFiltering(t *testing.T) {
	buf := capture(t)

	if err := SetLevel(LevelWarn); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	Debug("d")
	Info("i")
	Warn("w")
	Error("e")
	got := buf.String()
	for _, absent := range []string{"msg=d", "msg=i"} {
		if strings.Contains(got, absent) {
			t.Errorf("level=warn must suppress %q, got:\n%s", absent, got)
		}
	}
	for _, present := range []string{"msg=w", "msg=e"} {
		if !strings.Contains(got, present) {
			t.Errorf("level=warn must emit %q, got:\n%s", present, got)
		}
	}
}

func TestDebugLevelEmitsEverything(t *testing.T) {
	buf := capture(t)
	if err := SetLevel(LevelDebug); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	Debug("d")
	Info("i")
	if !strings.Contains(buf.String(), "msg=d") || !strings.Contains(buf.String(), "msg=i") {
		t.Errorf("level=debug must emit everything, got:\n%s", buf.String())
	}
}

// An unrecognized level is refused rather than silently defaulting. Someone
// asking for more output should never quietly get less — the fail-open pattern
// this codebase has been removing.
func TestUnknownLevelIsRefused(t *testing.T) {
	err := SetLevel("debgu")
	if err == nil {
		t.Fatal("an unknown level must be an error, not a silent fallback")
	}
	if !strings.Contains(err.Error(), "debgu") {
		t.Errorf("the error must name the bad value, got %v", err)
	}
	for _, want := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must list the valid levels, missing %q: %v", want, err)
		}
	}
}

func TestEmptyLevelIsInfo(t *testing.T) {
	if err := SetLevel(""); err != nil {
		t.Fatalf("an empty level must default to info, got %v", err)
	}
	if !Enabled(slog.LevelInfo) || Enabled(slog.LevelDebug) {
		t.Error("empty level must resolve to info")
	}
}

// Timestamps are stripped: the CI system already stamps every line, and their
// presence would make golden-output comparison impossible.
func TestNoTimestamps(t *testing.T) {
	buf := capture(t)
	_ = SetLevel(LevelInfo)
	buf.Reset()
	Info("hello", "k", "v")
	if strings.Contains(buf.String(), "time=") {
		t.Errorf("log lines must carry no timestamp, got:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "k=v") {
		t.Errorf("attributes must survive, got:\n%s", buf.String())
	}
}
