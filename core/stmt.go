package core

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Statement builders over the expression tree. SQL() is pure and
// deterministic — the same builder state always renders byte-identical SQL
// with the same bound-argument order — so tests can pin shapes exactly.
// Run/Exec are thin wrappers that route the rendered statement through DBTX.

// emitTable renders a possibly schema-qualified table reference. The string
// is split on "." so "public.users" emits as public.users and bare names
// stay bare; unusual identifiers are quoted part-wise via QuoteIdent.
func emitTable(e *emitter, table string) {
	e.str(QuoteIdent(strings.Split(table, ".")...))
}

// emitExprList renders comma-separated expressions.
func emitExprList(e *emitter, exprs []Expr) {
	for i, x := range exprs {
		if i > 0 {
			e.str(", ")
		}
		x.emit(e)
	}
}

// emitIdentList renders comma-separated quoted identifiers (column lists,
// conflict targets).
func emitIdentList(e *emitter, cols []string) {
	for i, c := range cols {
		if i > 0 {
			e.str(", ")
		}
		e.str(QuoteIdent(c))
	}
}

// emitSets renders "col = expr" assignments joined with ", ".
func emitSets(e *emitter, sets []SetClause) {
	for i, s := range sets {
		if i > 0 {
			e.str(", ")
		}
		e.str(QuoteIdent(s.Col) + " = ")
		if s.E != nil {
			s.E.emit(e)
		} else {
			e.str("NULL")
		}
	}
}

// appendWhere ANDs non-nil parts onto the statement's conjunct list. Empty
// input is a no-op.
func appendWhere(dst []Expr, parts []Expr) []Expr {
	for _, p := range parts {
		if p != nil {
			dst = append(dst, p)
		}
	}
	return dst
}

// Order is one ORDER BY term. Asc/Desc wrap an expression; the direction
// keyword is always emitted explicitly.
type Order struct {
	E    Expr
	Desc bool
}

// Asc orders by e ascending (ORDER BY <e> ASC).
func Asc(e Expr) Order { return Order{E: e} }

// Desc orders by e descending (ORDER BY <e> DESC).
func Desc(e Expr) Order { return Order{E: e, Desc: true} }

// CTE is one common table entry: name AS (select).
type CTE struct {
	Name   string
	Select *Select
}

// Select builds a SELECT statement. All fields are unexported; generated
// code and callers chain the fluent setters. With prepends its CTEs before
// the SELECT keyword: WITH name AS (...) SELECT ...
type Select struct {
	cols       []Expr
	from       string
	where      []Expr
	group      []Expr
	having     Expr
	order      []Order
	limit      *int
	offset     *int
	distinctOn []Expr
	lock       string
	ctes       []CTE
}

// NewSelect starts a SELECT of cols from table. With no cols it degrades to
// SELECT * — generated code always passes explicit column lists.
func NewSelect(table string, cols ...Expr) *Select {
	return &Select{from: table, cols: cols}
}

// Where ANDs the parts into the WHERE clause; an empty call is a no-op.
// Multiple calls accumulate conjuncts. Use WhereExpr for a single pre-built
// tree (Or/Not compositions).
func (s *Select) Where(parts ...Expr) *Select {
	s.where = appendWhere(s.where, parts)
	return s
}

// WhereExpr ANDs a single expression — the entry point for Or/Not trees.
func (s *Select) WhereExpr(e Expr) *Select {
	return s.Where(e)
}

// OrderBy appends ORDER BY terms.
func (s *Select) OrderBy(o ...Order) *Select {
	s.order = append(s.order, o...)
	return s
}

// Limit bounds the row count; n<=0 is a no-op (no clause emitted).
func (s *Select) Limit(n int) *Select {
	if n > 0 {
		s.limit = &n
	}
	return s
}

// Offset skips n rows; n<=0 is a no-op (no clause emitted).
func (s *Select) Offset(n int) *Select {
	if n > 0 {
		s.offset = &n
	}
	return s
}

// GroupBy appends GROUP BY expressions.
func (s *Select) GroupBy(e ...Expr) *Select {
	s.group = append(s.group, e...)
	return s
}

// Having sets the HAVING expression.
func (s *Select) Having(e Expr) *Select {
	s.having = e
	return s
}

// DistinctOn sets the DISTINCT ON (...) expressions.
func (s *Select) DistinctOn(e ...Expr) *Select {
	s.distinctOn = append(s.distinctOn, e...)
	return s
}

// Lock appends a verbatim lock clause ("FOR UPDATE", "FOR UPDATE SKIP
// LOCKED", "FOR SHARE NOWAIT", ...). Callers own its correctness.
func (s *Select) Lock(clause string) *Select {
	s.lock = clause
	return s
}

// With prepends a CTE: WITH name AS (<sel>) SELECT ...
func (s *Select) With(name string, sel *Select) *Select {
	s.ctes = append(s.ctes, CTE{Name: name, Select: sel})
	return s
}

// SQL renders the statement deterministically:
//
//	[WITH ctes] SELECT [DISTINCT ON (...)] cols FROM table
//	[WHERE ...] [GROUP BY ...] [HAVING ...] [ORDER BY ... ASC|DESC]
//	[LIMIT $n] [OFFSET $n] [lock]
func (s *Select) SQL() (string, []any) {
	e := &emitter{}
	s.emit(e)
	return e.sb.String(), e.args
}

func (s *Select) emit(e *emitter) {
	if len(s.ctes) > 0 {
		e.str("WITH ")
		for i, c := range s.ctes {
			if i > 0 {
				e.str(", ")
			}
			e.str(QuoteIdent(c.Name) + " AS (")
			if c.Select != nil {
				c.Select.emit(e)
			}
			e.str(")")
		}
		e.str(" ")
	}
	e.str("SELECT ")
	if len(s.distinctOn) > 0 {
		e.str("DISTINCT ON (")
		emitExprList(e, s.distinctOn)
		e.str(") ")
	}
	if len(s.cols) > 0 {
		emitExprList(e, s.cols)
	} else {
		e.str("*")
	}
	e.str(" FROM ")
	emitTable(e, s.from)
	if len(s.where) > 0 {
		e.str(" WHERE ")
		emitJoined(e, s.where, " AND ", false)
	}
	if len(s.group) > 0 {
		e.str(" GROUP BY ")
		emitExprList(e, s.group)
	}
	if s.having != nil {
		e.str(" HAVING ")
		s.having.emit(e)
	}
	if len(s.order) > 0 {
		e.str(" ORDER BY ")
		for i, o := range s.order {
			if i > 0 {
				e.str(", ")
			}
			if o.E == nil {
				continue
			}
			o.E.emit(e)
			if o.Desc {
				e.str(" DESC")
			} else {
				e.str(" ASC")
			}
		}
	}
	if s.limit != nil {
		e.str(" LIMIT ")
		e.param(*s.limit, "")
	}
	if s.offset != nil {
		e.str(" OFFSET ")
		e.param(*s.offset, "")
	}
	if s.lock != "" {
		e.str(" " + s.lock)
	}
}

// Run executes the statement via db.Query.
func (s *Select) Run(ctx context.Context, db DBTX) (pgx.Rows, error) {
	sql, args := s.SQL()
	return db.Query(ctx, sql, args...)
}

// SetClause is one assignment: col = E, where E is usually a Lit (its nil V
// renders NULL) or a Raw such as EXCLUDED.name.
type SetClause struct {
	Col string
	E   Expr
}

// OnConflict configures the ON CONFLICT clause of an Insert.
type OnConflict struct {
	Target    []string    // conflict target columns: ON CONFLICT (a, b)
	DoNothing bool        // DO NOTHING (wins over Sets when both set)
	Sets      []SetClause // DO UPDATE SET ...
	Where     Expr        // DO UPDATE ... WHERE ...
}

// Insert builds an INSERT statement with explicit columns and value rows.
type Insert struct {
	table      string
	cols       []string
	rows       [][]Expr
	onConflict *OnConflict
	returning  []Expr
}

// NewInsert starts INSERT INTO table (cols...) VALUES (rows...). Each row
// must line up with cols positionally.
func NewInsert(table string, cols []string, rows ...[]Expr) *Insert {
	return &Insert{table: table, cols: cols, rows: rows}
}

// OnConflict sets the ON CONFLICT clause.
func (i *Insert) OnConflict(o OnConflict) *Insert {
	oc := o
	i.onConflict = &oc
	return i
}

// Returning appends RETURNING expressions.
func (i *Insert) Returning(cols ...Expr) *Insert {
	i.returning = append(i.returning, cols...)
	return i
}

// SQL renders the statement deterministically:
//
//	INSERT INTO table (cols) VALUES (...), (...) [ON CONFLICT ...] [RETURNING ...]
func (i *Insert) SQL() (string, []any) {
	e := &emitter{}
	i.emit(e)
	return e.sb.String(), e.args
}

func (i *Insert) emit(e *emitter) {
	e.str("INSERT INTO ")
	emitTable(e, i.table)
	if len(i.cols) > 0 {
		e.str(" (")
		emitIdentList(e, i.cols)
		e.str(")")
	}
	if len(i.rows) == 0 {
		e.str(" DEFAULT VALUES")
	} else {
		e.str(" VALUES ")
		for r, row := range i.rows {
			if r > 0 {
				e.str(", ")
			}
			e.str("(")
			emitExprList(e, row)
			e.str(")")
		}
	}
	if i.onConflict != nil {
		e.str(" ON CONFLICT")
		if len(i.onConflict.Target) > 0 {
			e.str(" (")
			emitIdentList(e, i.onConflict.Target)
			e.str(")")
		}
		switch {
		case i.onConflict.DoNothing:
			e.str(" DO NOTHING")
		case len(i.onConflict.Sets) > 0:
			e.str(" DO UPDATE SET ")
			emitSets(e, i.onConflict.Sets)
			if i.onConflict.Where != nil {
				e.str(" WHERE ")
				i.onConflict.Where.emit(e)
			}
		}
	}
	if len(i.returning) > 0 {
		e.str(" RETURNING ")
		emitExprList(e, i.returning)
	}
}

// Run executes the statement via db.Query — used when Returning is set.
func (i *Insert) Run(ctx context.Context, db DBTX) (pgx.Rows, error) {
	sql, args := i.SQL()
	return db.Query(ctx, sql, args...)
}

// Exec executes the statement via db.Exec.
func (i *Insert) Exec(ctx context.Context, db DBTX) (pgconn.CommandTag, error) {
	sql, args := i.SQL()
	return db.Exec(ctx, sql, args...)
}

// Update builds an UPDATE statement.
type Update struct {
	table     string
	sets      []SetClause
	where     []Expr
	returning []Expr
}

// NewUpdate starts UPDATE table.
func NewUpdate(table string) *Update {
	return &Update{table: table}
}

// Set appends col = e to the SET clause, in call order.
func (u *Update) Set(col string, e Expr) *Update {
	u.sets = append(u.sets, SetClause{Col: col, E: e})
	return u
}

// Where ANDs the parts into the WHERE clause; an empty call is a no-op.
func (u *Update) Where(parts ...Expr) *Update {
	u.where = appendWhere(u.where, parts)
	return u
}

// WhereExpr ANDs a single expression — the entry point for Or/Not trees.
func (u *Update) WhereExpr(e Expr) *Update {
	return u.Where(e)
}

// Returning appends RETURNING expressions.
func (u *Update) Returning(cols ...Expr) *Update {
	u.returning = append(u.returning, cols...)
	return u
}

// SQL renders the statement deterministically:
//
//	UPDATE table SET col = $n, ... [WHERE ...] [RETURNING ...]
//
// An empty WHERE returns ErrNoWhere — the full-table-mutation guard lives
// here in core and fires before any SQL is sent.
func (u *Update) SQL() (string, []any, error) {
	if len(u.where) == 0 {
		return "", nil, ErrNoWhere
	}
	e := &emitter{}
	u.emit(e)
	return e.sb.String(), e.args, nil
}

func (u *Update) emit(e *emitter) {
	e.str("UPDATE ")
	emitTable(e, u.table)
	if len(u.sets) > 0 {
		e.str(" SET ")
		emitSets(e, u.sets)
	}
	if len(u.where) > 0 {
		e.str(" WHERE ")
		emitJoined(e, u.where, " AND ", false)
	}
	if len(u.returning) > 0 {
		e.str(" RETURNING ")
		emitExprList(e, u.returning)
	}
}

// Run executes the statement via db.Query; ErrNoWhere propagates.
func (u *Update) Run(ctx context.Context, db DBTX) (pgx.Rows, error) {
	sql, args, err := u.SQL()
	if err != nil {
		return nil, err
	}
	return db.Query(ctx, sql, args...)
}

// Exec executes the statement via db.Exec; ErrNoWhere propagates.
func (u *Update) Exec(ctx context.Context, db DBTX) (pgconn.CommandTag, error) {
	sql, args, err := u.SQL()
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return db.Exec(ctx, sql, args...)
}

// Delete builds a DELETE statement.
type Delete struct {
	table     string
	where     []Expr
	returning []Expr
}

// NewDelete starts DELETE FROM table.
func NewDelete(table string) *Delete {
	return &Delete{table: table}
}

// Where ANDs the parts into the WHERE clause; an empty call is a no-op.
func (d *Delete) Where(parts ...Expr) *Delete {
	d.where = appendWhere(d.where, parts)
	return d
}

// WhereExpr ANDs a single expression — the entry point for Or/Not trees.
func (d *Delete) WhereExpr(e Expr) *Delete {
	return d.Where(e)
}

// Returning appends RETURNING expressions.
func (d *Delete) Returning(cols ...Expr) *Delete {
	d.returning = append(d.returning, cols...)
	return d
}

// SQL renders the statement deterministically:
//
//	DELETE FROM table [WHERE ...] [RETURNING ...]
//
// An empty WHERE returns ErrNoWhere — the full-table-mutation guard lives
// here in core and fires before any SQL is sent.
func (d *Delete) SQL() (string, []any, error) {
	if len(d.where) == 0 {
		return "", nil, ErrNoWhere
	}
	e := &emitter{}
	d.emit(e)
	return e.sb.String(), e.args, nil
}

func (d *Delete) emit(e *emitter) {
	e.str("DELETE FROM ")
	emitTable(e, d.table)
	if len(d.where) > 0 {
		e.str(" WHERE ")
		emitJoined(e, d.where, " AND ", false)
	}
	if len(d.returning) > 0 {
		e.str(" RETURNING ")
		emitExprList(e, d.returning)
	}
}

// Run executes the statement via db.Query; ErrNoWhere propagates.
func (d *Delete) Run(ctx context.Context, db DBTX) (pgx.Rows, error) {
	sql, args, err := d.SQL()
	if err != nil {
		return nil, err
	}
	return db.Query(ctx, sql, args...)
}

// Exec executes the statement via db.Exec; ErrNoWhere propagates.
func (d *Delete) Exec(ctx context.Context, db DBTX) (pgconn.CommandTag, error) {
	sql, args, err := d.SQL()
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return db.Exec(ctx, sql, args...)
}
