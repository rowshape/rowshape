package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

func manyTables(n int) *fixture.Fixture {
	f := &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables:          map[string]fixture.Table{},
	}
	for i := 0; i < n; i++ {
		cols := map[string]fixture.Column{}
		for c := 0; c < 12; c++ {
			cols[fmt.Sprintf("col_%02d", c)] = fixture.Column{Type: "text"}
		}
		f.Tables[fmt.Sprintf("public.table_%04d", i)] = fixture.Table{
			Rows:    fixture.Fact[int64]{Value: 1000, Confidence: fixture.Exact},
			Columns: cols,
		}
	}
	return f
}

// TestDescribeShapeIndexIsBounded: the schema budget disciplines the FIXED
// per-session cost. Nothing bounded the VARIABLE per-call cost, which is far
// larger in a real session — measured at ~43KB (~10,800 tokens) for 500 tables,
// more than four times the entire session's schema budget, in ONE call.
func TestDescribeShapeIndexIsBounded(t *testing.T) {
	idx := buildIndex("rowshape.yaml", manyTables(1000))
	if len(idx.Tables) > maxIndexTables {
		t.Errorf("index listed %d tables, want at most %d", len(idx.Tables), maxIndexTables)
	}
	b, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 32_000 {
		t.Errorf("index is %d bytes; a single call must not dominate an agent's context", len(b))
	}
}

// Truncation must be REPORTED. An agent that cannot see a table and is not told
// the list was cut will conclude the table does not exist — which is a worse
// answer than a long list.
func TestTruncationIsReportedNotSilent(t *testing.T) {
	idx := buildIndex("rowshape.yaml", manyTables(1000))
	if idx.Truncated == nil {
		t.Fatal("a truncated index must say so")
	}
	if idx.Truncated.Total != 1000 || idx.Truncated.Shown != maxIndexTables {
		t.Errorf("truncation = %+v, want shown=%d total=1000", idx.Truncated, maxIndexTables)
	}
	if !strings.Contains(idx.Truncated.Note, "NOT") {
		t.Errorf("the note must warn that a missing table is not an absent one, got %q", idx.Truncated.Note)
	}
}

// An ordinary schema must be returned whole, and must not carry the truncation
// marker — or the bound becomes noise.
func TestOrdinarySchemaIsNotTruncated(t *testing.T) {
	idx := buildIndex("rowshape.yaml", manyTables(50))
	if len(idx.Tables) != 50 {
		t.Errorf("listed %d of 50 tables", len(idx.Tables))
	}
	if idx.Truncated != nil {
		t.Errorf("a 50-table schema must not report truncation, got %+v", idx.Truncated)
	}
}

// Pointed at a mature migrations/ directory, the loop-closer used to read the
// project's ENTIRE history and report findings across all of it. Refusing beats
// truncating: a verdict over an arbitrary subset of statements is worse than no
// verdict.
func TestMigrationDirectoryIngestionIsBounded(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxMigrationFiles+5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%04d_m.sql", i))
		if err := os.WriteFile(p, []byte("ALTER TABLE t ADD COLUMN c int;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := migrationStatements(dir)
	if err == nil {
		t.Fatal("a directory of the whole project history must be refused, not silently analyzed")
	}
	if !strings.Contains(err.Error(), "single migration file") {
		t.Errorf("the refusal must say what to pass instead, got %v", err)
	}
}

// A normal migrations directory must still work.
func TestSmallMigrationDirectoryStillWorks(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%04d_m.sql", i))
		if err := os.WriteFile(p, []byte("ALTER TABLE t ADD COLUMN c int;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stmts, err := migrationStatements(dir)
	if err != nil {
		t.Fatalf("a 3-file directory must be read: %v", err)
	}
	if len(stmts) != 3 {
		t.Errorf("got %d statements, want 3", len(stmts))
	}
}
