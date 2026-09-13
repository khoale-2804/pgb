# CONTRACTS — frozen signatures for the pgb v0.1 vertical slice

Module: `github.com/khoale-2804/pgb`. Everything below is FROZEN — implement
exactly these signatures. When the DESIGN and this file disagree, this file
wins. When something is missing here, follow `docs/generated-code/*.mdx` (the
examples there are the emitted-code spec) and note the deviation in your reply.

Scope of this slice (v0.1 core): models + builders + statics + wrappers +
pagination for NON-search tables. pg_search codegen (Search*/HybridSearch),
directives via oliphant, and the DDL extraction pass are M1/M3 — leave the
hooks, don't implement.

## Package `core` (runtime — imported by generated code)

Already written (do not modify): `expr.go` (Expr/Col/Lit/Bin/Call/Cast/And/
Or/Not/Raw + Emit + QuoteIdent), `emit.go` (emitter).

B1 implements in `core/`:

```go
// dbtx.go
type DBTX interface {
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
// pgxpool.Pool, pgx.Conn, pgx.Tx satisfy it.

var ErrNotFound, ErrNoWhere, ErrCursorMismatch error
// ErrNotFound wraps pgx.ErrNoRows (fmt.Errorf with %w) — errors.Is works.

func WithTx[T any](ctx context.Context, db DBTX, fn func(DBTX) (T, error)) (T, error)
// db must implement interface{ Begin(ctx context.Context) (pgx.Tx, error) };
// if it doesn't, return db unchanged-wrapped error "pgb: WithTx requires a
// connection or pool that can begin a transaction". Rollback on error/panic.
```

```go
// stmt.go
type Order struct { E Expr; Desc bool }
func Asc(e Expr) Order   // ORDER BY <e> ASC
func Desc(e Expr) Order  // ORDER BY <e> DESC

type CTE struct { Name string; Select *Select }

type Select struct { /* unexported fields: cols, from, where, group, having,
    order, limit, offset, distinctOn, lock, ctes */ }
func NewSelect(table string, cols ...Expr) *Select
func (s *Select) Where(parts ...Expr) *Select      // And{parts}; empty = no-op
func (s *Select) WhereExpr(e Expr) *Select         // use when composing Or/Not
func (s *Select) OrderBy(o ...Order) *Select
func (s *Select) Limit(n int) *Select              // n<=0 = no clause
func (s *Select) Offset(n int) *Select
func (s *Select) GroupBy(e ...Expr) *Select
func (s *Select) Having(e Expr) *Select
func (s *Select) DistinctOn(e ...Expr) *Select     // DISTINCT ON (...)
func (s *Select) Lock(clause string) *Select       // "FOR UPDATE" etc, verbatim
func (s *Select) With(name string, sel *Select) *Select  // CTE, prepends
func (s *Select) SQL() (string, []any)             // deterministic, for tests
func (s *Select) Run(ctx context.Context, db DBTX) (pgx.Rows, error)
// SQL shape: SELECT <cols> FROM <table> [WITH ctes BEFORE select keyword:
// WITH name AS (...) SELECT ...] [WHERE ...] [GROUP BY ...] [HAVING ...]
// [ORDER BY ... [ASC|DESC]] [LIMIT $n] [OFFSET $n] [lock]

type OnConflict struct {
    Target []string           // (col, col)
    DoNothing bool
    Sets   []SetClause        // DO UPDATE SET ...
    Where  Expr               // DO UPDATE ... WHERE ...
}
type SetClause struct { Col string; E Expr }   // E is Lit or Raw etc

type Insert struct { /* cols, rows [][]Expr, onConflict, returning */ }
func NewInsert(table string, cols []string, rows ...[]Expr) *Insert
func (i *Insert) OnConflict(o OnConflict) *Insert
func (i *Insert) Returning(cols ...Expr) *Insert
func (i *Insert) SQL() (string, []any)
func (i *Insert) Run(ctx, db) (pgx.Rows, error)   // used when Returning set
func (i *Insert) Exec(ctx, db) (pgconn.CommandTag, error)

type Update struct { /* table, sets, where, returning */ }
func NewUpdate(table string) *Update
func (u *Update) Set(col string, e Expr) *Update
func (u *Update) Where(parts ...Expr) *Update
func (u *Update) WhereExpr(e Expr) *Update
func (u *Update) Returning(cols ...Expr) *Update
func (u *Update) SQL() (string, []any)
func (u *Update) Run(ctx, db) (pgx.Rows, error)
func (u *Update) Exec(ctx, db) (pgconn.CommandTag, error)

type Delete struct { /* table, where, returning */ }
func NewDelete(table string) *Delete
func (d *Delete) Where(parts ...Expr) *Delete
func (d *Delete) WhereExpr(e Expr) *Delete
func (d *Delete) Returning(cols ...Expr) *Delete
func (d *Delete) SQL() (string, []any)
func (d *Delete) Run(ctx, db) (pgx.Rows, error)
func (d *Delete) Exec(ctx, db) (pgconn.CommandTag, error)
// Where guarantee: Update/Delete with empty where must return
// pgb.ErrNoWhere from SQL() — the ErrNoWhere guard lives HERE in core.
```

```go
// page.go
func EncodeCursor(vals []any) (string, error)   // json -> base64 raw std
func DecodeCursor(s string) ([]any, error)      // ErrCursorMismatch wrapper is
                                                // generated-side; decode only
func CursorMismatch(numWant, numGot int) error  // builds ErrCursorMismatch msg
```

Rules for B1: no reflection; deterministic emission (map keys sorted, struct
field order fixed); SQL() must be pure (no context); Run/Exec thin wrappers
over db.Query/Exec. gofmt clean. Add table-driven tests in core/*_test.go for
Select/Insert/Update/Delete SQL shapes + Raw renumbering + ErrNoWhere.

## Package `gen` (the codegen)

```go
// gen.go
type Options struct {
    Package string        // default "db"
    Core    string        // default "github.com/khoale-2804/pgb/core"
    Target  string        // "18"|"19", default "18"
    Overrides []TypeOverride
    IncludeDefaults bool
}
type TypeOverride struct {
    DBType string         // e.g. "vector"
    Column string          // e.g. "products.embedding" (wins over DBType)
    Import, Package, Type string
}
func Generate(ctx context.Context, req *plugin.GenerateRequest) (*plugin.GenerateResponse, error)
// Options come from req.PluginOptions (JSON). Deterministic ordering: iterate
// req.Catalog.Schemas/Tables in proto order; sort generated file list.
```

```go
// sqlcat.go
func Build(req *plugin.GenerateRequest, opts Options) (ir.Schema, error)
// walks req.Catalog: schemas -> tables -> columns. Map: proto Type.Name may be
// schema-qualified ("public.int4"); split and keep base. enums from
// catalog enums (inspect the sdk proto: go doc github.com/sqlc-dev/plugin-sdk-go/sdk
// — read the module source in $(go env GOMODCACHE)/github.com/sqlc-dev/sqlc@*/protos/plugin/codegen.proto
// if unsure). Table/Column Comment fields carry the catalog comments — map
// pgb:* directives from them (Split ";" on the prefix "pgb:"). Views: table
// with proto table "is materialized view"? if the proto lacks the flag, treat
// all as tables for now and note it.
```

```go
// maptype.go
func GoType(c ir.Column, opts Options) (goType string, importPath string)
// canonical mapping (DESIGN §5 table): int4->int32, int8->int64, text->string,
// bool->bool, float4->float32, float8->float64, numeric->pgtype.Numeric,
// uuid->uuid.UUID, bytea->[]byte, json/jsonb->[]byte, timestamptz/timestamp->
// pgtype.Timestamptz, date->pgtype.Date, time->pgtype.Time, timetz->pgtype.Timetz,
// interval->pgtype.Interval, inet->pgtype.Inet, cidr->pgtype.CIDR,
// macaddr->pgtype.Macaddr, macaddr8->pgtype.Macaddr8, money->pgtype.Numeric,
// xml->string, tsvector/tsquery->any, pg_lsn->any, xid8->pgtype.Uint64,
// tid->pgtype.TID, point->pgtype.Point, line->pgtype.Line, lseg->pgtype.Lseg,
// box->pgtype.Box, path->pgtype.Path, polygon->pgtype.Polygon, circle->pgtype.Circle,
// ranges->pgtype.Range[...], multiranges->pgtype.Multirange[...], geo array skip.
// NOT NULL strips the pgtype wrapper to the bare value type where pgtype
// supports it (timestamptz NOT NULL -> time.Time is NOT done in v0.1 — keep
// pgtype.X even when NOT NULL, matching sqlc-gen-go). unknown -> "any".
// Overrides: Column match wins, then DBType, then table above.
func Nullable(c ir.Column) bool  // !c.NotNull && no override forcing value
```

```go
// pass_models.go — emits models.gen.go
func PassModels(sch ir.Schema, opts Options) []*plugin.File? — no: return
([]byte, error) raw file content; caller wraps in plugin.File{Name,Contents}.
// contents: header comment "// Code generated by pgb. DO NOT EDIT.", package,
// imports, one struct per table (NOT views — views get read structs too, same
// shape), singularized Go names (users->User, statuses->Status? keep simple
// rules: trim trailing "s", "es"; "ies"->"y"; exceptions map {statuses: Status}),
// fields pascal-cased from columns, Go-keyword-safe (exported, so keywords are
// fine), dedupe case-insensitive collisions with numeric suffix,
// json tags = column name, pgtype imports as needed. Enums: `type UserStatus string`
// + consts. pgb:skip tables emit nothing.
```

```go
// pass_wrappers.go — emits queries.gen.go from req.Queries
// one func per query, per annotation:
//   :one -> (T, error) row struct inline if multi-table (QueryRow)
//   :many -> ([]T, error) (pgx.CollectRows with positional scan closure)
//   :exec -> error;  :execrows -> (int64, error)
// query text embedded as const string; params become typed args in order.
// Row structs: for single-table selects reuse the model; else inline struct
// QueryRow with pascal fields from result columns.
```

```go
// pass_builders.go — emits <table>.gen.go per table (B pass)
// EXACT shape (from docs/generated-code/builders):
//   type UserTable struct{ core.TableMeta }
//   var Users = UserTable{core.NewTableMeta("public", "users")}
//   func (t UserTable) ID() UserIDCol { return UserIDCol{core.Col{Table: "users", Name: "id"}} }
//   type UserIDCol struct{ core.Col }
//   func (c UserIDCol) Eq(v int64) core.Expr { return core.Bin{Op: "=", L: c.Col, R: core.Lit{V: v}} }
// predicate methods by type family (DESIGN §9): Eq/Ne/In/IsNull/NotNull on all;
// Gt/Lt/Gte/Lte/Between on ordered; Like/ILike on text; KeyEq on jsonb (Raw);
// Contains on arrays (tags @> $1); skip pgb:no_filter columns.
//   func (t UserTable) Select() *core.Select { return core.NewSelect("users", <all cols as Col>) }
//   Update/Delete constructors with ErrNoWhere handled by core.
// TableMeta in core: B1 adds to core/tablemeta.go:
//   type TableMeta struct{ Schema, Name string }
//   func NewTableMeta(schema, name string) TableMeta
```

```go
// pass_statics.go — appends to <table>.gen.go (C pass): filter/set/params
// types + scan func + statics per docs/generated-code/statics + pagination
// when the table has a single-column unique key (models+builders first).
// statics take exec pgb core.DBTX as FIRST ctx-adjacent param, named exec;
// call sites use pool. ErrNotFound wrapping on Get; ErrNoWhere from core.
```

## Emitted-code conventions (frozen)

- package name from Options.Package; import the runtime as `core "<Options.Core>"`.
- Deterministic: no timestamps in headers; header is exactly
  `// Code generated by pgb. DO NOT EDIT.` + `// versions:` style block.
- Run everything through go/format before returning (go/format.Source).
- pgb:skip tables: no descriptors, no statics, no model.

## Verification (every builder runs before reporting)

```bash
cd /home/rei/data/pgb && go build ./... && go vet ./... && gofmt -l .
```
plus the unit tests for the package you own (`go test ./core/...` for B1).
