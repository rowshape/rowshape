package sqlkind

import (
	"strconv"
	"strings"
)

// This file holds the character-type parsing that internal/findings and
// internal/hydrate must agree on.
//
// The analyzers reasoned about `varchar(n)` widths to decide whether a type change
// rewrites a table (RS-LOCK) or truncates on the way back (RS-REVERSE). The
// synthesis engine needs the SAME reading of the same type string for a different
// reason: a generated string has to fit the column it is about to be COPYed into,
// or hydration dies on `value too long for type character(3)`. Two parsers for one
// notation is two things to get wrong, and they would drift silently — so the
// parsing lives here, one level below both, exactly as IsTxControl does.

// BaseType strips a type's length/precision modifier and normalizes case and
// spacing: BaseType("VARCHAR(255)") is "varchar", BaseType("numeric(10,2)") is
// "numeric", BaseType("text") is "text".
func BaseType(t string) string {
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(strings.TrimSpace(t))
}

// TypeLength extracts the length modifier from a type: TypeLength("varchar(200)")
// is (200, true); TypeLength("text") is (0, false). Only the first modifier is
// read, so a precision list ("numeric(10,2)") cannot mislead it into reporting the
// scale.
func TypeLength(t string) (int, bool) {
	i := strings.IndexByte(t, '(')
	if i < 0 {
		return 0, false
	}
	rest := t[i+1:]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		return 0, false
	}
	inner := rest[:j]
	if k := strings.IndexByte(inner, ','); k >= 0 {
		inner = inner[:k]
	}
	v, err := strconv.Atoi(strings.TrimSpace(inner))
	if err != nil {
		return 0, false
	}
	return v, true
}

// IsBoundedStringBase reports whether a base type is a character string type that
// can carry a length modifier. It takes a BASE type (already stripped by
// BaseType), matching how the analyzers use it.
func IsBoundedStringBase(base string) bool {
	switch base {
	case "text", "varchar", "character varying", "character", "char":
		return true
	}
	return false
}

// CharMaxLength returns the number of characters a value in this type may occupy,
// and whether such a limit exists at all.
//
//	character(3)             -> (3, true)
//	character varying(50)    -> (50, true)
//	char                     -> (1, true)   unadorned char is character(1)
//	text                     -> (0, false)  unbounded
//	character varying        -> (0, false)  unadorned varchar is unbounded
//	numeric(10,2)            -> (0, false)  not a character type
//
// The unadorned `char` case is the one worth spelling out: Postgres treats a bare
// `char` as `character(1)`, so it is the TIGHTEST character type there is despite
// carrying no modifier — reading it as unbounded would let a generated value
// overflow the narrowest column in the schema.
func CharMaxLength(t string) (int, bool) {
	base := BaseType(t)
	if !IsBoundedStringBase(base) {
		return 0, false
	}
	if n, ok := TypeLength(t); ok && n > 0 {
		return n, true
	}
	// No modifier: `char`/`character` mean character(1); text and bare varchar are
	// unbounded.
	if base == "char" || base == "character" {
		return 1, true
	}
	return 0, false
}
