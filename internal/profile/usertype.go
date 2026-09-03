package profile

import (
	"context"
	"sort"

	"github.com/rowshape/rowshape/internal/fixture"
)

// userTypes resolves the user-defined types referenced by in-scope columns into
// fixture definitions (RFC §6.7).
//
// It exists because a column's `type` is only a NAME. For a built-in type that is
// enough — every Postgres server already has `integer`. For an enum or a domain it
// is not: `z.mood` means nothing in a freshly created disposable database, so the
// DDL that receives hydrated rows failed outright with
//
//	ERROR: type "z.mood" does not exist (SQLSTATE 42704)
//
// and no schema using an enum or a domain could be hydrated. The definitions are
// read once, at the end of the structural pass, keyed by the OIDs the column scan
// actually saw — so a database full of unrelated types contributes nothing.
//
// Only enums and domains are resolved. They are the two kinds a column commonly
// carries that are also mechanically reconstructible: an enum is its ordered label
// list, and a domain is a base type plus constraints. Composite and range types
// are left undefined rather than guessed at, which keeps the failure honest (the
// DDL still names the missing type) instead of inventing a definition that would
// hydrate into a shape production never had.
func (r *reader) userTypes(ctx context.Context) (map[string]fixture.UserType, error) {
	if len(r.typeUses) == 0 {
		return nil, nil
	}
	oids := make([]uint32, 0, len(r.typeUses))
	for oid := range r.typeUses {
		oids = append(oids, oid)
	}
	// Sorted so the query — and any server-side plan or error — is deterministic.
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })

	// typtype 'e' is an enum, 'd' a domain. An enum's labels come back ordered by
	// enumsortorder, which is the type's declared order and is semantically
	// significant: Postgres compares and sorts the column by it.
	const q = `
SELECT t.oid,
       t.typtype::text,
       format_type(t.typbasetype, t.typtypmod),
       t.typnotnull,
       (SELECT array_agg(e.enumlabel::text ORDER BY e.enumsortorder)
          FROM pg_enum e WHERE e.enumtypid = t.oid),
       (SELECT string_agg(pg_get_constraintdef(c.oid), ' AND ' ORDER BY c.conname)
          FROM pg_constraint c WHERE c.contypid = t.oid)
FROM pg_type t
WHERE t.oid = ANY($1) AND t.typtype IN ('e', 'd')`

	rows, err := r.tx.Query(ctx, q, oids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]fixture.UserType{}
	for rows.Next() {
		var (
			oid     uint32
			typtype string
			base    string
			notnull bool
			labels  []string
			condef  *string
		)
		if err := rows.Scan(&oid, &typtype, &base, &notnull, &labels, &condef); err != nil {
			return nil, err
		}
		name := r.typeUses[oid]
		if name == "" {
			continue
		}
		switch typtype {
		case "e":
			out[name] = fixture.UserType{
				Kind:       "enum",
				Labels:     labels,
				LabelCount: len(labels),
			}
		case "d":
			u := fixture.UserType{Kind: "domain", Base: base, NotNull: notnull}
			if condef != nil {
				// pg_get_constraintdef renders "CHECK ((VALUE > 0))"; keep the
				// expression itself so the emitter can place it in a CREATE DOMAIN.
				u.Check = trimCheckWrapper(*condef)
			}
			out[name] = u
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// trimCheckWrapper reduces pg_get_constraintdef's rendering to the bare
// expression: "CHECK ((VALUE > 0))" becomes "(VALUE > 0)". Anything that does not
// match that shape is returned untouched, so an unexpected rendering is preserved
// verbatim rather than mangled.
func trimCheckWrapper(def string) string {
	const prefix = "CHECK "
	if len(def) > len(prefix) && def[:len(prefix)] == prefix {
		return def[len(prefix):]
	}
	return def
}
