package core

import (
	"fmt"
	"strconv"
)

// search.go — pg_search predicate constructors. CHURN BARRIER: every
// ParadeDB-specific emission lives in this file (DESIGN §8). Shapes follow
// the v2 operator API (docs/paradedb/predicates.mdx, scoring.mdx); shapes
// not yet verified upstream are marked with a comment and pinned by tests.
//
// All values travel as bound parameters; only operators, function names and
// constant mode labels appear inline.

func searchBin(op string, col Col, rhs Expr) Expr { return Bin{Op: op, L: col, R: rhs} }

// Match is match-any (token OR): col ||| $1.
func Match(c Col, q string) Expr { return searchBin("|||", c, Lit{V: q}) }

// MatchAll is match-all (token AND): col &&& $1.
func MatchAll(c Col, q string) Expr { return searchBin("&&&", c, Lit{V: q}) }

// Phrase is phrase matching with position awareness: col ### $1[:pdb.slop(n)].
func Phrase(c Col, q string, slop int) Expr {
	if slop > 0 {
		return searchBin("###", c, Cast{E: Lit{V: q}, To: fmt.Sprintf("pdb.slop(%d)", slop)})
	}
	return searchBin("###", c, Lit{V: q})
}

// Exact is exact-term matching: col === $1.
func Exact(c Col, v any) Expr { return searchBin("===", c, Lit{V: v}) }

// ExactAny is a term set via an array RHS: col === $1::elemType[].
// cast is the element type (e.g. "text"); the slice suffix is added here.
func ExactAny(c Col, vs any, elemType string) Expr {
	return searchBin("===", c, Cast{E: Lit{V: vs}, To: elemType + "[]"})
}

// Fuzzy is fuzzy matching: col === $1::pdb.fuzzy(dist[, prefix]).
func Fuzzy(c Col, q string, dist int, prefix bool) Expr {
	cast := fmt.Sprintf("pdb.fuzzy(%d", dist)
	if prefix {
		cast += ", true"
	}
	cast += ")"
	return searchBin("===", c, Cast{E: Lit{V: q}, To: cast})
}

// Regex: col @@@ pdb.regex($1).
func Regex(c Col, pattern string) Expr {
	return searchBin("@@@", c, Call{Fn: "pdb.regex", Args: []Expr{Lit{V: pattern}}})
}

// Parse uses the full query-string syntax: col @@@ pdb.parse($1[, lenient => true]).
func Parse(c Col, q string, lenient bool) Expr {
	call := Call{Fn: "pdb.parse", Args: []Expr{Lit{V: q}}}
	if lenient {
		call.Named = append(call.Named, NamedArg{Name: "lenient", Val: Lit{V: true}})
	}
	return searchBin("@@@", c, call)
}

// RangeTerm: col @@@ pdb.range_term($1::cast, 'mode'). cast is e.g.
// "int4range"; mode is one of Intersects/Contains/Within (bound, not inlined).
func RangeTerm(c Col, r any, cast, mode string) Expr {
	return searchBin("@@@", c, Call{
		Fn:   "pdb.range_term",
		Args: []Expr{Cast{E: Lit{V: r}, To: cast}, Lit{V: mode}},
	})
}

// ForceIndex routes the query through ParadeDB's executor: col @@@ pdb.all().
func ForceIndex(c Col) Expr { return searchBin("@@@", c, Call{Fn: "pdb.all"}) }

// Proximity: $1 ## n ## $2 (ordered variant ##> is a separate constructor
// when verified upstream). TO BE VERIFIED against 0.26 docs.
func Proximity(a, b string, n int) Expr {
	return Raw{SQL: "? ## " + strconv.Itoa(n) + " ## ?", Args: []any{a, b}}
}

// Boost rewrites the RHS of a search predicate into a ::pdb.boost(f) cast:
// Match(col, q).Boost? — use core.Boost(Match(col, q), 2.0), which turns
// `col ||| $1` into `col ||| $1::pdb.boost(2)`. Works on function-call RHS
// too (`col @@@ pdb.regex($1)::pdb.boost(2)`).
func Boost(e Expr, factor float64) Expr {
	f := formatFactor(factor)
	bin, ok := e.(Bin)
	if !ok {
		return e // not a predicate — pass through untouched
	}
	to := "pdb.boost(" + f + ")"
	switch rhs := bin.R.(type) {
	case Lit:
		return Bin{Op: bin.Op, L: bin.L, R: Cast{E: Lit{V: rhs.V, Cast: rhs.Cast}, To: to}}
	case Cast:
		return Bin{Op: bin.Op, L: bin.L, R: Cast{E: rhs.E, To: rhs.To + "::" + to}}
	case Call:
		return Bin{Op: bin.Op, L: bin.L, R: Cast{E: rhs, To: to}}
	default:
		return e
	}
}

func formatFactor(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Score ranks by the index key field: pdb.score(<keyCol>).
func Score(keyCol Col) Expr { return Call{Fn: "pdb.score", Args: []Expr{keyCol}} }

// Snippet wraps the matching fragment: pdb.snippet(col[, named args]).
// Zero values skip their named argument.
func Snippet(c Col, startTag, endTag string, maxChars int) Expr {
	call := Call{Fn: "pdb.snippet", Args: []Expr{c}}
	if startTag != "" {
		call.Named = append(call.Named, NamedArg{Name: "start_tag", Val: Lit{V: startTag}})
	}
	if endTag != "" {
		call.Named = append(call.Named, NamedArg{Name: "end_tag", Val: Lit{V: endTag}})
	}
	if maxChars > 0 {
		call.Named = append(call.Named, NamedArg{Name: "max_num_chars", Val: Lit{V: maxChars}})
	}
	return call
}

// Snippets returns several fragments: pdb.snippets(col, ...).
func Snippets(c Col, limitN, offsetN int, sortBy string) Expr {
	call := Call{Fn: "pdb.snippets", Args: []Expr{c}}
	if limitN > 0 {
		call.Named = append(call.Named, NamedArg{Name: `"limit"`, Val: Lit{V: limitN}})
	}
	if offsetN > 0 {
		call.Named = append(call.Named, NamedArg{Name: `"offset"`, Val: Lit{V: offsetN}})
	}
	if sortBy != "" {
		call.Named = append(call.Named, NamedArg{Name: `sort_by`, Val: Lit{V: sortBy}})
	}
	return call
}

// Highlight returns the highlighted column value: pdb.highlight(col).
func Highlight(c Col) Expr { return Call{Fn: "pdb.highlight", Args: []Expr{c}} }
