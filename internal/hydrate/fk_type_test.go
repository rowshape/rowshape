package hydrate

import (
	"testing"

	"github.com/rowshape/rowshape/internal/fixture"
)

// fixtureWithKeyType builds parent(id <keyType> unique) and child(parent_id
// <keyType>) with a FK, so the generated FK values can be compared against the
// parent values actually produced.
func fixtureWithKeyType(keyType string, parentRows, childRows int64) *fixture.Fixture {
	tru := true
	return &fixture.Fixture{
		RowshapeFixture: fixture.FormatVersion,
		Meta:            fixture.Meta{Engine: fixture.Engine{Name: "postgres", Version: "16"}},
		Tables: map[string]fixture.Table{
			"public.parent": {
				Rows: fixture.Fact[int64]{Value: parentRows, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"id": {Type: keyType, Unique: &fixture.Fact[bool]{Value: true, Confidence: fixture.Exact}},
				},
			},
			"public.child": {
				Rows: fixture.Fact[int64]{Value: childRows, Confidence: fixture.Exact},
				Columns: map[string]fixture.Column{
					"parent_id": {Type: keyType},
				},
				References: []fixture.Reference{{
					Column:         "parent_id",
					To:             "public.parent.id",
					Fanout:         &fixture.Fanout{Mean: 2, P50: 2, P95: 3, Max: 4},
					OrphanFraction: &fixture.Fact[float64]{Value: 0, Confidence: fixture.Exact},
				}},
			},
		},
		X: map[string]any{"_": tru},
	}
}

// TestForeignKeyValuesMatchParentKeyType is the regression for FK values being
// generated as integers regardless of the parent key's type.
//
// parentIDValue returned int64 unconditionally and derived the value through
// numericInRange, which reads only col.Range and ignores the column's type. For
// a uuid primary key the PARENT was generated as a uuid string while the CHILD's
// FK got int64(0), int64(1), ... — so the emitted SQL put a bare 0 into a uuid
// column and the load failed. uuid PKs with FKs are extremely common.
//
// The assertion is not merely "the types agree": every FK value must be one the
// parent actually produced. A same-typed value that no parent holds is an
// orphan, which is the other half of the same bug.
func TestForeignKeyValuesMatchParentKeyType(t *testing.T) {
	for _, keyType := range []string{"uuid", "text", "bigint"} {
		t.Run(keyType, func(t *testing.T) {
			f := fixtureWithKeyType(keyType, 50, 120)
			out, err := Generate(f, Options{Seed: 7, Scale: 1})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			parent := tableByName(t, out, "public.parent")
			child := tableByName(t, out, "public.child")

			// Collect the parent key values actually generated.
			parentIdx := colIndex(t, parent, "id")
			have := map[any]bool{}
			for _, row := range parent.Rows {
				have[row[parentIdx]] = true
			}
			if len(have) == 0 {
				t.Fatal("no parent rows generated")
			}

			childIdx := colIndex(t, child, "parent_id")
			for i, row := range child.Rows {
				v := row[childIdx]
				if v == nil {
					continue
				}
				// Type agreement: an int64 against a uuid/text parent is the bug.
				var pv any
				for k := range have {
					pv = k
					break
				}
				if gotT, wantT := typeName(v), typeName(pv); gotT != wantT {
					t.Fatalf("child row %d: FK value %v is %s, but the parent key is %s — the emitted SQL would not load",
						i, v, gotT, wantT)
				}
				// Referential integrity: orphan_fraction is exact 0 here, so every
				// FK must hit a real parent.
				if !have[v] {
					t.Fatalf("child row %d: FK value %v matches no parent key — hydrate invented an orphan the fixture proves absent", i, v)
				}
			}
		})
	}
}

func typeName(v any) string {
	switch v.(type) {
	case int64:
		return "int64"
	case string:
		return "string"
	case nil:
		return "nil"
	default:
		return "other"
	}
}

func tableByName(t *testing.T, out *Result, name string) *GeneratedTable {
	t.Helper()
	for i := range out.Tables {
		if out.Tables[i].Name == name {
			return &out.Tables[i]
		}
	}
	t.Fatalf("table %s not generated", name)
	return nil
}

func colIndex(t *testing.T, tb *GeneratedTable, col string) int {
	t.Helper()
	for i, c := range tb.Columns {
		if c == col {
			return i
		}
	}
	t.Fatalf("column %s not found in %s", col, tb.Name)
	return -1
}
