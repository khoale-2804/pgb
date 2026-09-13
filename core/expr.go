// Package core is pgb's handwritten runtime. Generated code composes the
// expression tree defined here; the emitter turns it into deterministic,
// fully-parameterized SQL. See DESIGN.md §6.
package core

import "fmt"

// Expr is any node of the SQL expression tree.
type Expr interface{ emit(e *emitter) }

// Col references a (optionally schema/table-qualified) column.
type Col struct{ Table, Name string }

func (c Col) emit(e *emitter) { e.str(QuoteIdent(c.Table, c.Name)) }

// Lit is a bound value with an optional cast hint ("timestamptz", "text[]"...).
// A nil V renders the literal NULL.
type Lit struct {
	V    any
	Cast string
}

func (l Lit) emit(e *emitter) { e.param(l.V, l.Cast) }

// Bin is a binary operation: L Op R. Op is emitted verbatim ("=", ">",
// "|||", "&&&", "###", "===", "@@@", "##", "##>", "<=>", "@>", ...).
type Bin struct {
	Op string
	L  Expr
	R  Expr
}

func (b Bin) emit(e *emitter) {
	b.L.emit(e)
	e.str(" " + b.Op + " ")
	b.R.emit(e)
}

// NamedArg is a named function argument (start_tag => $2).
type NamedArg struct {
	Name string
	Val  Expr
}

// Call is a function call with positional and named arguments.
type Call struct {
	Fn    string
	Args  []Expr
	Named []NamedArg
}

func (c Call) emit(e *emitter) {
	e.str(c.Fn + "(")
	for i, a := range c.Args {
		if i > 0 {
			e.str(", ")
		}
		a.emit(e)
	}
	for _, n := range c.Named {
		if len(c.Args) > 0 || len(c.Named) > 0 {
			e.str(", ")
		}
		e.str(n.Name + " => ")
		n.Val.emit(e)
	}
	e.str(")")
}

// Cast renders E::To.
type Cast struct {
	E  Expr
	To string
}

func (c Cast) emit(e *emitter) {
	c.E.emit(e)
	e.str("::" + c.To)
}

// And / Or / Not are boolean combinators. And and Or with a single part
// render that part unwrapped; empty And renders nothing (the WHERE layer
// simply omits it).
type And struct{ Parts []Expr }
type Or struct{ Parts []Expr }
type Not struct{ E Expr }

func (a And) emit(e *emitter) { emitJoined(e, a.Parts, " AND ", true) }
func (o Or) emit(e *emitter)  { emitJoined(e, o.Parts, " OR ", true) }

func emitJoined(e *emitter, parts []Expr, sep string, parens bool) {
	switch len(parts) {
	case 0:
		return
	case 1:
		parts[0].emit(e)
	default:
		if parens {
			e.str("(")
		}
		for i, p := range parts {
			if i > 0 {
				e.str(sep)
			}
			p.emit(e)
		}
		if parens {
			e.str(")")
		}
	}
}

func (n Not) emit(e *emitter) {
	e.str("NOT ")
	switch n.E.(type) {
	case And, Or, Not:
		e.str("(")
		n.E.emit(e)
		e.str(")")
	default:
		n.E.emit(e)
	}
}

// Raw carries hand-written SQL with `?`-style placeholders. Args are bound
// in order and every `?` is renumbered into the surrounding statement's $n
// sequence — Raw composes safely at any expression position.
type Raw struct {
	SQL  string
	Args []any
}

func (r Raw) emit(e *emitter) {
	start := e.n + 1
	for _, a := range r.Args {
		e.bind(a)
	}
	e.str(replaceQs(r.SQL, start))
}

// replaceQs replaces each '?' in sql with $n for n running from start.
// Caller has already appended the args in order.
func replaceQs(sql string, start int) string {
	out := make([]byte, 0, len(sql)+8)
	n := start
	for i := 0; i < len(sql); i++ {
		if sql[i] == '?' {
			out = append(out, []byte(fmt.Sprintf("$%d", n))...)
			n++
			continue
		}
		out = append(out, sql[i])
	}
	return string(out)
}
