package profile

import (
	"context"
	"sort"

	"github.com/rowshape/rowshape/internal/fixture"
)

// extensions resolves the engine extensions the in-scope schema depends on into
// fixture definitions (RFC §6.8).
//
// It exists for the same reason userTypes does, one level further out. userTypes
// closed the case where a column's type is defined IN the database (an enum, a
// domain) and the fixture had to carry the definition. This closes the case where
// the type is not definable at all from the catalog — it arrives with an
// extension. A column typed `citext` made the whole DDL transaction fail with
//
//	ERROR: type "citext" does not exist (SQLSTATE 42704)
//
// so `validate` could not run at all against any schema using an extension type,
// and citext on an email column, hstore, ltree, postgis geometry and pgvector's
// `vector` are all that shape.
//
// Two sources feed it, because an extension can be needed without any column
// naming it:
//
//   - the type OIDs the column scan saw (r.typeUses) — citext, geometry, vector;
//   - the operator class OIDs the index scan saw (r.opclassUses) — an index
//     declared `gin (email gin_trgm_ops)` needs pg_trgm even though every column
//     in it is a plain text.
//
// Both resolve through pg_depend, so an extension merely INSTALLED on the source
// contributes nothing: recording every extension present would make the fixture
// demand postgis of a disposable server for a schema that never touches it.
//
// The type set is widened one hop before the lookup, because the OID a column
// carries is not always the OID the extension owns: a `citext[]` column carries
// the array type `_citext`, and a domain over citext carries the domain. Both
// reach the extension through typelem / typbasetype.
func (r *reader) extensions(ctx context.Context) (map[string]fixture.Extension, error) {
	typeOIDs := sortedOIDs(r.typeUses)
	opclassOIDs := sortedOIDs(r.opclassUses)
	if len(typeOIDs) == 0 && len(opclassOIDs) == 0 {
		return nil, nil
	}

	// Two arms, unioned. A type or an operator class that belongs to an extension has
	// a pg_depend row pointing at it with deptype 'e' (extension member); one that
	// does not belong to any extension simply matches nothing, which is the common
	// case and costs a row lookup.
	//
	// For a type the dependency can hang off the type itself or, for a table-less
	// composite, off nothing at all — only the direct form is used here, because a
	// type whose extension membership is not recorded is a type we cannot honestly
	// attribute.
	const q = `
WITH seen AS (
    SELECT oid FROM pg_type WHERE oid = ANY($1)
  UNION
    SELECT typelem FROM pg_type WHERE oid = ANY($1) AND typelem <> 0
  UNION
    SELECT typbasetype FROM pg_type WHERE oid = ANY($1) AND typbasetype <> 0
)
SELECT DISTINCT e.extname, n.nspname
FROM pg_depend d
JOIN pg_extension e ON e.oid = d.refobjid
JOIN pg_namespace n ON n.oid = e.extnamespace
WHERE d.refclassid = 'pg_extension'::regclass
  AND d.deptype = 'e'
  AND ( (d.classid = 'pg_type'::regclass AND d.objid IN (SELECT oid FROM seen))
     OR (d.classid = 'pg_opclass'::regclass AND d.objid = ANY($2)) )`

	rows, err := r.tx.Query(ctx, q, typeOIDs, opclassOIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]fixture.Extension{}
	for rows.Next() {
		var name, schema string
		if err := rows.Scan(&name, &schema); err != nil {
			return nil, err
		}
		// plpgsql is in every database from initdb onward, so requiring it says
		// nothing and would appear in every fixture ever emitted.
		if name == "plpgsql" {
			continue
		}
		out[name] = fixture.Extension{Schema: schema}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// sortedOIDs returns a map's OID keys in ascending order, so the query — and any
// server-side plan or error — is deterministic.
func sortedOIDs[V any](m map[uint32]V) []uint32 {
	if len(m) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(m))
	for oid := range m {
		out = append(out, oid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
