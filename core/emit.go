package core

import (
	"fmt"
	"strings"
)

// emitter accumulates SQL text and bound arguments. Every value becomes a
// positional $n parameter; nothing user-provided is ever inlined.
type emitter struct {
	sb   strings.Builder
	args []any
	n    int
}

func (e *emitter) str(s string) { e.sb.WriteString(s) }

// param binds v as the next positional parameter and returns its "$n" text,
// optionally followed by a "::cast" suffix.
func (e *emitter) param(v any, cast string) string {
	if v == nil {
		if cast != "" {
			e.str("NULL::" + cast)
			return ""
		}
		e.str("NULL")
		return ""
	}
	e.n++
	e.args = append(e.args, v)
	p := fmt.Sprintf("$%d", e.n)
	if cast != "" {
		p += "::" + cast
	}
	e.str(p)
	return p
}

// bind registers v as the next positional parameter without emitting its
// "$n" text. Raw uses it: its renumbered SQL already references the $n
// placeholders inline, so the text must not be emitted twice. A nil v is
// registered too, binding NULL for that placeholder.
func (e *emitter) bind(v any) {
	e.n++
	e.args = append(e.args, v)
}

// QuoteIdent renders the identifier parts, quoting any part that is not a
// plain lowercase snake identifier.
func QuoteIdent(parts ...string) string {
	q := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if needsQuote(p) {
			q = append(q, `"`+strings.ReplaceAll(p, `"`, `""`)+`"`)
			continue
		}
		q = append(q, p)
	}
	return strings.Join(q, ".")
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return true
		}
	}
	// leading digit and non-snake shapes handled above; keywords are quoted
	// defensively (see keywords.go — full pg_get_keywords list, not just the
	// reserved class, since unreserved collisions still break in some
	// positions, e.g. a column named "default" in a column list).
	return pgKeywords[s]
}

// Emit renders an expression tree to SQL + bound args. Deterministic: the
// same tree always produces byte-identical SQL.
func Emit(e Expr) (sql string, args []any) {
	ee := &emitter{}
	e.emit(ee)
	return ee.sb.String(), ee.args
}
