# pgb — two-stage Postgres codegen with full ParadeDB support (design)

**Targets:** ParadeDB 0.25.9+ / 0.26-rc (v2 API) · PostgreSQL 18 & 19 · Go ≥ 1.24 · pgx/v5 · sqlc v1.31.1+ (main where noted)
**Status:** design, 2026-09-13. Working name `pgb` (check availability before publishing).

---

## 0. What this is

A sqlc process plugin that turns a schema (plus optional handwritten queries) into four layers of Go:

1. **Models** — typed structs (forked sqlc-gen-go behavior, one type map).
2. **Stage 1** — per-table **column descriptors + query builders**: typed predicates (including every ParadeDB v2 search operator on bm25-indexed columns), composable statement builders.
3. **Stage 2** — **static functions** per table: `Get/List/Count/Insert/InsertBatch/Update/Upsert/Delete/Search/HybridSearch`. Repetitive SQL never gets hand-written; handwritten `.sql` files remain only for genuinely complex queries.
4. **Query wrappers** — typed Go functions for the handwritten queries sqlc already resolved (params + result columns arrive in the plugin request).

### Non-goals

- No ORM: no relationships/eager-loading, hooks, identity map, unit-of-work. *(Planned to flip in pgb 2.0 — see §16.)*
- No migrations tool. *(Planned to flip in pgb 2.0 — see §16.)*
- No other databases (single Postgres dialect, version-gated).
- No MySQL/SQLite.

---

## 1. Architecture

```
schema.sql (+ queries.sql)                       sqlc.yaml (options = pgb config)
     │                                                │
     ▼                                                ▼
┌───────────────────────────  sqlc  ───────────────────────────────┐
│  parse (libpg_query 18 via sqlc-dev/oliphant, on main)           │
│  analyze → catalog (tables/columns/types) + resolved queries     │
└──────────────┬───────────────────────────────────────────────────┘
               │ GenerateRequest (protobuf, stdin)
               ▼
┌───────────────────────────  pgb plugin  ─────────────────────────┐
│ 1. build SchemaIR:  catalog proto + oliphant re-parse of the     │
│    schema files (index DDL, generated columns, COMMENT ON        │
│    directives — none of which survive into the proto)            │
│ 2. pass A: models + handwritten-query wrappers                   │
│ 3. pass B: descriptors + builders          (stage 1)             │
│ 4. pass C: static functions + search layer (stage 2)             │
└──────────────┬───────────────────────────────────────────────────┘
               │ GenerateResponse { File{name, contents} }
               ▼
        db/  — ONE generated package:
               models.gen.go · <table>.gen.go per table (descriptors +
               builders + statics) · <table>_search.gen.go
               │ imports (runtime only)
               ▼
        core/ — handwritten runtime: expression tree, SQL emitter,
                pgx scan helpers, pg_search predicate emitters
```

**Key architectural decisions**

- **One plugin replaces `gen: go:`** instead of coexisting with it. The plugin protocol hands us the same catalog + resolved queries sqlc-gen-go gets, so we fork sqlc-gen-go (MIT) for models/query emission. This gives **one type map** — the classic failure mode (sqlc models use one override set, a second plugin silently assumes another) is eliminated at the root. Everything still runs through `sqlc generate`, `sqlc vet`, etc.
- **SchemaIR is the contract.** Parsing (sqlc catalog, oliphant DDL pass) and codegen (three passes) only meet through the IR. This makes a **standalone mode** (no sqlc) fall out for free later: a second main that builds the IR purely from oliphant — useful if sqlc's analyzer ever blocks a schema.
- **The pg_search emitter lives in exactly one handwritten file** (`core/search.go`) behind a `SearchVersion` gate. ParadeDB churned the API twice in 2025 (`paradedb.*`→`pdb.*`, `USING bm25`→`USING paradedb`); churn cost is one file.

---

## 2. SchemaIR

The intermediate representation. Built once, consumed by all passes.

```go
package ir

type Schema struct {
    DefaultSchema string        // "public"
    Tables        []Table
    Enums         []Enum        // named, per schema
    Composites    []Composite
    Domains       map[string]string // domain -> base type
}

type Table struct {
    Schema, Name string
    Comment      string
    Columns      []Column
    PrimaryKey   []string
    Uniques      [][]string     // incl. WITHOUT OVERLAPS (flagged temporal)
    ForeignKeys  []ForeignKey   // detection only, v1 keeps this inert
    Search       *SearchIndex   // non-nil iff a USING paradedb/bm25 index exists
}

type Column struct {
    Name        string
    PGType      string       // resolved base type (domain unwrapped)
    NotNull     bool
    IsArray     bool
    HasDefault  bool
    DefaultExpr string
    Generated   string       // "stored" | "virtual" (PG18 virtual = default kind)
    IsTemporal  bool         // participates in a WITHOUT OVERLAPS constraint
    Comment     string
}

type SearchIndex struct {
    Name     string           // index name
    Using    string           // "paradedb" | "bm25" (accepted alias)
    KeyField string           // from WITH (key_field='id'); first col fallback
    Fields   []SearchField    // ordered per DDL
    Options  map[string]string // rest of WITH(...) raw
}

type SearchField struct {
    Column    string
    Path      string          // "metadata->'color'" when JSON path is indexed
    Alias     string          // alias=json_color
    Tokenizer string          // icu | simple(stemmer=english) | literal | ngram(3,3) | fast=…
    PGType    string          // text | numeric | timestamp | boolean | jsonb | range | array
}
```

**Directives** ride in `COMMENT ON` statements (real DDL, parsed by the oliphant pass — inline `--` comments do not survive any PG parse):

```sql
COMMENT ON TABLE  users            IS 'pgb:skip';              -- no codegen for this table
COMMENT ON COLUMN users.password   IS 'pgb:no_filter';         -- not exposed as a predicate
COMMENT ON COLUMN users.embedding  IS 'pgb:type=github.com/pgvector/pgvector-go.Vector';
COMMENT ON COLUMN users.metadata   IS 'pgb:type=map[string]any';
```

v1 directive set: `pgb:skip`, `pgb:no_filter`, `pgb:no_patch`, `pgb:type=<go type>`. Everything global lives in sqlc.yaml options instead.

---

## 3. Input modes

### 3.1 sqlc plugin (primary)

```yaml
version: '2'
sql:
  - engine: postgresql
    schema: schema.sql
    queries: queries.sql          # optional, handwritten only
    codegen:
      - out: db
        plugin: pgb
        options:
          package: db
          core: github.com/you/pgb/core
          target: "18"            # emitter dialect gate: "18" | "19"
          paradedb:
            version: "0.26"
          emit:
            batch: true           # InsertUsers via unnest
            keyset: true          # cursor pagination helpers
            hybrid: true          # SearchXxxHybrid when a vector column exists
            merge: false          # MERGE builder (PG17+), default off
          overrides:
            - { db_type: vector, go_type: github.com/pgvector/pgvector-go.Vector }
plugins:
  - name: pgb
    process: { cmd: sqlc-gen-pgb }
```

Run `sqlc generate` → the single `db/` package appears. Models, query wrappers, descriptors, builders, and static functions are emitted into ONE package (`db`) — one type map end-to-end; the executor param is named `exec` in generated signatures so call sites never shadow the package. Handwritten `.sql` queries get typed wrappers from the same plugin (pass A), so `gen: go:` is simply not configured.

**Version note (2026-09-13):** the PG18 parser (oliphant) and unknown-function tolerance are merged on sqlc **main**, not in any release (latest release v1.31.1). Until the next tagged release, the Makefile installs sqlc from main: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@main`. On v1.31.1 the schema still parses (PG17 grammar handles all of it, including `USING paradedb`), but handwritten queries touching `pdb.*` fail analysis — with main they degrade to untyped `any` columns (main-branch behavior, unreleased — confidence medium).

### 3.2 standalone (escape hatch, same IR)

`pgb-gen` binary: `pgb-gen --schema schema.sql --config pgb.json` — builds the IR purely from oliphant and runs the same three passes. Costs: its own domain/enum type resolution. Payoff: no sqlc analyzer in the loop at all. Built because the IR split makes it nearly free; not on the critical path.

---

## 4. DDL extraction pass (oliphant)

sqlc's proto catalog carries tables/columns/enums but **drops** `CREATE INDEX` and `COMMENT ON`, and doesn't flag generated columns' virtual/stored kind. The plugin therefore re-parses the schema files itself with `sqlc-dev/oliphant` (pure-Go libpg_query 18 — no cgo; same parser sqlc uses, so no grammar drift):

Extracts:
- `CreateIndexStmt` with `AccessMethod ∈ {paradedb, bm25}` → `SearchIndex` (column list, per-column casts `(description::pdb.icu)`, JSON path exprs + aliases, `WITH (key_field='…')`).
- `CreateStmt` details the proto lacks: `GENERATED ALWAYS AS … (STORED|VIRTUAL)`, defaults, temporal unique constraints (`WITHOUT OVERLAPS` → `IsTemporal`).
- `CommentOnStmt` → directives.
- `CreateExtensionStmt`: accepts any name (pg_search, pgvector) — no catalog modeling needed.

Failure policy: oliphant parse failure of a statement the proto already resolved is a warning, not an error (IR degrades to catalog-only; search layer simply isn't generated for that table).

---

## 5. Type mapping & nullability

One map, shared by all passes. Defaults mirror sqlc-gen-go so behavior is familiar:

| PG | Go (default) | notes |
|---|---|---|
| text, varchar, bpchar, citext, name | `string` | |
| boolean | `bool` | |
| int2 / int4 / int8 | `int16` / `int32` / `int64` | |
| oid | `uint32` | |
| float4 / float8 | `float32` / `float64` | |
| numeric | `pgtype.Numeric` | override to `json.Number`/`string` for money |
| uuid | `uuid.UUID` (google) | |
| bytea | `[]byte` | |
| json, jsonb | `[]byte` | override to typed struct/map |
| timestamptz / timestamp | `pgtype.Timestamptz` | |
| date | `pgtype.Date` | |
| interval | `pgtype.Interval` | |
| inet, cidr, macaddr | `pgtype.*` | |
| tsmvector/tsquery, ranges | `pgtype.*` / `pgtype.Range[T]` | |
| `<enum>` | generated string type | |
| vector (pgvector) | override → `pgvector.Vector` | required override for hybrid |
| unknown / `any` | `any` | untyped columns (e.g. `pdb.score()` under v1.31.1) |

- Nullable → `pgtype.Xxx` by default; `nullability: pointers` option flips to `*T`.
- **Three-state writes** use a small generic instead of pointers-on-pointers:

```go
package core
type Set[T any] struct { V T; IsNull bool; Defined bool }
func Set[T any](v T) Set[T]        // write value
func Null[T any]() Set[T]          // write NULL
// zero value = "skip this column"
```

- **Consistency guarantee:** pass A generates the scan/param code *assigning into* the model structs, so if pgb's mapping ever disagrees with the models it just forked, the build breaks at `go build` — never silently mis-scans. `any`-typed columns surface as explicit `any` fields, nudging casts in SQL (`pdb.score(id)::float8`).

---

## 6. Runtime core (handwritten once — `core/`)

### 6.1 Expression tree

```go
type Expr interface{ emit(*emitter) }

type Col   struct{ Table, Name string }              // referenced by generated descriptors
type Lit   struct{ V any; Cast string }              // Cast renders "$n::timestamptz" style hints
type Bin   struct{ Op string; L, R Expr }            // any operator, incl. ||| === @@@ ## <=>
type Call  struct{ Fn string; Args []Expr; Named []NamedArg }  // pdb.snippet(col, start_tag => …)
type Cast  struct{ E Expr; To string }
type And   struct{ Parts []Expr }
type Or    struct{ Parts []Expr }
type Not   struct{ E Expr }
type Raw   struct{ SQL string; Args []any }          // escape hatch, args still bound
```

### 6.2 Emitter

- Single pass over the tree → `strings.Builder` + ordered `args []any`; every value becomes a positional `$n` parameter. **No value is ever inlined** — string escaping bugs are structurally impossible; structure (identifiers, operators, function names) comes only from generated code.
- Identifiers quoted as `"schema"."table"` / `"col"` when needed.
- `emitter.param(v, cast)` registers the arg and returns `$n` (+ optional cast). Empty slices get `$n::text[]`-style casts so pgx encodes them as arrays, not `''`.
- Deterministic: same IR → byte-identical SQL (golden tests rely on it).
- **Formatter stage**: generated `.go` output passes through `go/format` (gofmt) before write, and golden files are gofmt-canonical — output style is enforced by the toolchain, never hand-tidied. The docs snippets follow the same discipline via `tools/fmt-docs.py` (go/json reindent + `--check` for CI).

### 6.3 Execution

```go
type DBTX interface {                                  // pgxpool.Pool, pgx.Tx, pgx.Conn all satisfy
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
func WithTx[T any](ctx context.Context, db DBTX, fn func(pgb.DBTX) (T, error)) (T, error)
```

Scanning: generated explicit-position scan funcs (`row.Scan(&u.ID, &u.Email, …)` via pgtype.Map) — no reflection, no `SELECT *` (column lists are explicit, immune to `ALTER TABLE ADD COLUMN`).

### 6.4 Statement builders (generic, in core)

```go
type Select struct { Cols []Expr; From string; Where Expr; Group []Expr; Having Expr;
                     Order []Order; Limit, Offset *int; DistinctOn []Expr; Lock string; CTEs []CTE }
type Insert struct { Table string; Cols []string; Rows [][]Expr; OnConflict *OnConflict; Returning []Expr }
type Update struct { Table string; Sets []SetClause; Where Expr; Returning []Expr }
type Delete struct { Table string; Where Expr; Returning []Expr }
```

`OnConflict`: `Target []string`, `Action DoNothing | DoUpdate{Sets, Where} | DoSelect{}` (DoSelect gated on `target ≥ 19`).

---

## 7. SQL emitter & dialect gates (PG18/PG19)

`Emitter{Target Version}` — `target: "18"|"19"` in config. Gates:

| feature | gate | behavior |
|---|---|---|
| `RETURNING *` / explicit lists | all | always explicit column lists |
| generated columns (virtual, PG18-default kind) | all | excluded from INSERT/UPDATE/UPSERT column lists; fine in filters/RETURNING |
| default-bearing columns | all | omitted from INSERT unless `IncludeDefaults` |
| `MERGE … RETURNING` builder | ≥ 17 | milestone M4, off by default |
| `uuidv7()` | ≥ 18 | nothing emitted (defaults live in schema); DDL lint suggests uuidv7 for new uuid PKs |
| `RETURNING OLD/NEW` | ≥ 18 | emitter supports; sqlc's analyzer still can't parse it in handwritten queries (#4355) — one more reason such statements go through the generated layer |
| temporal `WITHOUT OVERLAPS` upserts | ≥ 18 | v1: detected + explicit error pointing at `Raw`; M5: emit `PERIOD` target |
| `INSERT … ON CONFLICT DO SELECT RETURNING` | ≥ 19 | UpsertOne gets `OnSelect: true` option; **verify exact syntax at PG19 GA (still beta)** |
| `FOR PORTION OF` (update/delete) | ≥ 19 | M5 |
| SQL/PGQ property graphs | ≥ 19 | non-goal — `Raw()` escape |
| `WAIT FOR` | ≥ 19 | out of scope |

PG19 parser note: no libpg_query 19 branch exists yet (18.0.0 landed 2026-05-21). Keep PG19-only DDL out of schema.sql until it lands; emitted DML is validated by the live server, not the codegen parser. ParadeDB ships no PG19 binaries yet either — the test matrix handles this (§13).

---

## 8. ParadeDB layer (`core/search.go` — the churn barrier)

Target: **0.25.9+ stable, 0.26-rc tracked** = the v2 operator API (stable since 0.20.0; 0.26.0-rc.1 changed no operator/`pdb.*` signatures). Accept both `USING paradedb` and legacy `USING bm25` at DDL parse.

> v0.1 ships this layer built-in. In 2.0 it splits into the official **`pgb-paradedb` extension plugin** — the first plugin of the extension architecture (§16 P0), keeping core generic-Postgres-only.

### 8.1 DDL model (from the oliphant pass)

```sql
CREATE INDEX products_search ON products USING paradedb
  (id, description::pdb.icu, (metadata->'color')::pdb.literal('alias=json_color'), rating)
  WITH (key_field='id');
```

→ `SearchIndex{KeyField:"id", Fields:[{description, icu}, {Path:"metadata->'color'", Alias:"json_color", literal}, {rating}]}`. One index per table (ParadeDB rule); key_field must be unique (deprecation of that requirement in progress — parse tolerantly, prefer PK).

### 8.2 Generated predicate surface (only on bm25-indexed columns)

Verified v2 shapes; anything marked ⚠ is confirmed against the 0.26 docs at implementation time (golden tests pin the SQL):

| method | emits |
|---|---|
| `.Match(q)` | `description ||| $1` |
| `.MatchAll(q)` | `description &&& $1` |
| `.Phrase(q, Slop(2))` | `description ### $1::pdb.slop(2)` |
| `.Exact(v)` | `category === $1` |
| `.ExactAny(vs)` | `category === $1::text[]` (term set via array RHS) |
| `.Fuzzy(q, 1, Prefix())` | `description === $1::pdb.fuzzy(1, true)` ⚠ alternate attach points (`\|\|\|`/`@@@`) to be verified |
| `.Regex(p)` | `description @@@ pdb.regex($1)` |
| `.Boost(b)` (chained on any of the above) | `… $1::pdb.boost(2)` / `… @@@ pdb.regex($1)::pdb.boost(2)` |
| `.RangeTerm(r, "Intersects")` | `rating @@@ pdb.range_term($1::int4range, 'Intersects')` |
| `.Proximity(a, b, n)` | `$1 ## n ## $2` |
| `.ForceIndex()` (vector-lane routing) | `id @@@ pdb.all()` |
| `.Parse(q, Lenient)` | `description @@@ pdb.parse($1, lenient => true)` |
| JSON aliased field | `metadata->'color'` path re-emitted exactly as indexed |
| combinators | plain SQL `AND / OR / NOT` between predicates |

### 8.3 Scoring & snippets (key_field comes from the IR)

```go
Users.Score()                    // Call{Fn:"pdb.score", Args:[Users.ID]} → pdb.score(id)
Users.OrderByScore()             // ORDER BY pdb.score(id) DESC, id ASC   (indexed tiebreak — required for Top-K)
Users.Description.Snippet(StartTag("<em>"), MaxChars(150))
                                 // pdb.snippet(description, start_tag => $2, end_tag => $3, max_num_chars => $4)
Users.Description.Snippets(Limit(5), SortBy("score"))
Users.Description.SnippetPositions()
```

### 8.4 Generated search functions (stage 2 payoff)

```go
type ProductHit struct {
    Product db.Product
    Score   float64
    Snippet map[string]string   // per snippet'd field
}

func SearchProducts(ctx context.Context, exec pgb.DBTX, q string, o SearchOpts) ([]ProductHit, error)
// SELECT id, name, pdb.score(id) AS score, pdb.snippet(description, start_tag => '<em>') AS snip
// FROM products WHERE description ||| $1 ORDER BY pdb.score(id) DESC, id LIMIT $2

func SearchProductsHybrid(ctx context.Context, exec pgb.DBTX, q string, vec []float32, o HybridOpts) ([]ProductHit, error)
```

Hybrid emits the documented RRF pattern (0.26-rc improves execution — vector pushdown for RRF and vector tiebreak ordering — which our shape benefits from automatically):

```sql
WITH text AS (
    SELECT id, RANK() OVER (ORDER BY pdb.score(id) DESC, id) AS rank
    FROM products WHERE description ||| $1 LIMIT 20
  ), vec AS (
    SELECT id, RANK() OVER (ORDER BY embedding <=> $2::vector) AS rank
    FROM products WHERE id @@@ pdb.all() LIMIT 20
  )
SELECT p.*, 0.7/(60+text.rank) + 0.3/(60+vec.rank) AS rrf_score
FROM products p
JOIN text  ON text.id = p.id
FULL JOIN vec ON vec.id = p.id
ORDER BY rrf_score DESC, p.id LIMIT $3;
-- weights (0.7/0.3), k (60), per-lane LIMIT from HybridOpts
```

Requires the `vector` type override + `vector_dim` config to type `$2::vector`; embedding column detected via `pgb:type=github.com/pgvector/pgvector-go.Vector`.

### 8.5–8.9 — expansion scope (2026-09-13 docs expansion)

- **8.5 Aggregations** — `pdb.agg` JSON DSL (Elasticsearch-compatible): buckets (terms/date_histogram/histogram/range/filters), metrics (avg/cardinality/value_count/min/max/percentiles/stats/sum/top_hits), `visibility => raw|threshold|auto`, JSON-path fields (0.25.9). Generated surface: typed `AggregateXxx` statics proposal (marked planned).
- **8.6 Pushdown-aware execution** — custom scan only with a ParadeDB operator present; Top-K (SegmentedTopKExec), score ordering, aggregates + GROUP BY, JOIN MPP via DataFusion (`paradedb.enable_join_custom_scan`); join requirements (all tables indexed, operator present, join/filter/ORDER-BY cols indexed with fast fields for text/JSON, LIMIT present).
- **8.7 LSM tuning surface** — mutable segment buffered across statements, frozen at `mutable_segment_rows` (default 1,000), layer merges 100KB–10GB (`background_layer_sizes`/`layer_sizes`), WAL + MVCC since 0.24.0, no refresh interval; candidate future config keys (marked proposal).
- **8.8 Tokenizers in IR** — SearchField carries tokenizer/alias/path from DDL casts; 13 tokenizers + 8 token filters; external-index filters (ltree `<@`, PostGIS-style, 0.25.5+).
- **8.9 Vector caveat** — SPANN-style index (not HNSW), Beta with reindex risk, `vector_cosine_ops` inside `USING paradedb`; 0.26-rc adds vector tiebreak + RRF vector pushdown.

---

## 9. Generated code — stage 1: descriptors + builders (`db` package)

Per table `users` (files: `db/users.gen.go`, plus `db/users_search.gen.go` when indexed):

```go
// Descriptors — package-level, immutable
var Users = newUserTable("public", "users")

type UserTable struct{ core.TableMeta }
var _ = UserTable{ /* compile-time check vs db.User field types */ }

func (t UserTable) ID() UserIDCol             // typed wrapper around core.Col
func (t UserTable) Email() UserEmailCol
func (t UserTable) CreatedAt() UserCreatedAtCol
func (t UserTable) Score() core.Expr          // pdb.score(id) — only for bm25 tables
```

Predicate methods are generated per column **by PG type family**:

```go
// every column
func (c UserIDCol) Eq(v int64) core.Expr
func (c UserIDCol) Ne(v int64) core.Expr
func (c UserIDCol) In(vs ...int64) core.Expr
func (c UserIDCol) IsNull() core.Expr          // / NotNull()
// ordered types (ints, floats, numerics, timestamps, dates)
func (c UserCreatedAtCol) Gt(v time.Time) core.Expr        // Lt Lte Gte Between
// text types
func (c UserEmailCol) Like(p string) core.Expr             // ILike NotLike
// jsonb types
func (c UserMetadataCol) KeyEq(path string, v any) core.Expr   // metadata->>$2 = $3
// array types
func (c UserTagsCol) Contains(v []string) core.Expr            // tags @> $1::text[]
// bm25 columns only (§8.2)
func (c UserBioCol) Match(q string) core.Expr
// ...and pgb:no_filter columns get none
```

Statement builders — fluent, terminators execute:

```go
u, err := db.Users.Select().
    Where(db.Or(
        db.Users.Email().Like("%@corp"),
        db.Users.CreatedAt().Gte(since))).
    OrderBy(db.Users.CreatedAt().Desc()).
    Limit(20).
    All(ctx, pool)                       // ([]db.User, error)

db.Users.Select().Where(...).Count(ctx, pool)         // (int64, error)
db.Users.Select().Where(...).Exists(ctx, pool)        // (bool, error)
db.Users.Select().Where(...).One(ctx, pool)           // (db.User, error) — errors if >1
db.Users.Delete().Where(...).Exec(ctx, pool)          // (int64, error)
db.Users.Update().Set(Email, v).Where(...).Returning().All(ctx, pool)
```

**Safety guard:** `Update`/`Delete` with an empty WHERE return `pgb.ErrNoWhere` — full-table mutations require `.AllowAll()` explicitly. (Runtime guard, not a type machine — kept simple on purpose.)

Keyset pagination (`emit.keyset`, requires a unique sortable key or `(score, id)` composite for search):

```go
rows, next, more, err := db.Users.Select().OrderBy(db.Users.ID().Asc()).
    Page(ctx, pool, pgb.CursorOf(prev), 50)
```

---

## 10. Generated code — stage 2: static functions (`db/users.gen.go`)

The "never write manual sqlc for CRUD" layer:

```go
func GetUser(ctx context.Context, exec pgb.DBTX, id int64) (db.User, error)             // pgb.ErrNotFound
func ListUsers(ctx context.Context, exec pgb.DBTX, f UserFilter, o ...pgb.ListOpt) ([]db.User, error)
func CountUsers(ctx context.Context, exec pgb.DBTX, f UserFilter) (int64, error)

type UserFilter struct {
    ID           *int64
    Emails       []string
    EmailLike    *string
    CreatedAfter *time.Time
    MetadataEq   map[string]any     // jsonb KeyEq shorthand
    Extra        []core.Expr        // escape hatch — composes with the generated filters
}

func InsertUser(ctx context.Context, exec pgb.DBTX, p InsertUserParams) (db.User, error)
func InsertUsers(ctx context.Context, exec pgb.DBTX, ps []InsertUserParams) ([]db.User, error)
    // INSERT … SELECT * FROM unnest($1::text[], $2::timestamptz[], …) RETURNING <explicit cols>
    // arbitrary batch size + RETURNING; pgx.CopyFrom variant opt-in for pure bulk (no RETURNING)

type UserSet struct {                       // three-state via core.Set[T]
    Email   core.Set[string]     // pgb.Set("x") · pgb.Null[string]() · zero = skip
    Bio     core.Set[string]
}

func UpdateUser(ctx context.Context, exec pgb.DBTX, id int64, s UserSet) (db.User, error)
func UpdateUsers(ctx context.Context, exec pgb.DBTX, where []core.Expr, s UserSet) (int64, error)
func UpsertUser(ctx context.Context, exec pgb.DBTX, p InsertUserParams, o UpsertOpts) (db.User, error)
    // ON CONFLICT (email) DO UPDATE SET … WHERE … RETURNING …
    // UpsertOpts.OnSelect on target 19 → ON CONFLICT DO SELECT RETURNING
func DeleteUser(ctx context.Context, exec pgb.DBTX, id int64) error
func DeleteUsers(ctx context.Context, exec pgb.DBTX, where []core.Expr) (int64, error)

func SearchUsers(ctx context.Context, exec pgb.DBTX, q string, o UserSearchOpts) ([]UserHit, error)        // §8.4
func SearchUsersHybrid(ctx context.Context, exec pgb.DBTX, q string, vec []float32, o UserHybridOpts) ([]UserHit, error)
```

Every function: explicit column lists, bound args, `pgb.ErrNotFound` sentinel wrapping `pgx.ErrNoRows`, tx-compatible via `DBTX`.

---

## 11. Handwritten queries coexistence

- `queries.sql` keeps only genuinely complex queries (CTEs, window tricks, PGQ). The plugin resolves them from `req.Queries` (sqlc already computed params + result columns) and emits typed wrappers into `db/` — same style as sqlc-gen-go (`:one`/`:many`/`:exec` annotations).
- Queries using `pdb.*`/`@@@`: on sqlc main they type-check as `any` columns — add `::float8` casts for clean types, or configure database-backed analysis (`database: {uri: …}, analyzer: {database: true}`) against a real ParadeDB so extension types resolve exactly.
- Anything the builder can't express yet: `core.Raw(sql, args...)` composes into any expression position.
- `sqlc vet` keeps working over the whole project.

---

## 12. Testing & CI matrix

1. **Emitter unit tests** — table-driven: tree → SQL + args, byte-exact, covering every operator incl. §8.2.
2. **Golden generated-code tests** — canonical fixture `testdata/golden/` (`schema.sql` + `queries.sql` + `sqlc.yaml` + `COVERAGE.md` 56-row feature matrix; every type family, constraint, index shape, directive, identifier edge, both pg_search shapes, negative suite in `testdata/negative/`) → snapshots; `-update` regenerates; diffs are reviewable in PRs. The fixture doubles as a parse/analyze acceptance test for sqlc@main — first run surfaced: bare index-cast rejection (parenthesized), pgvector-go hyphen override gotcha (structured form), `any` leaks for tsvector/xml/pg_lsn (standalone-catalog rationale).
3. **Round-trip property test** — the emitter's SQL is fed back through oliphant and must re-parse to the same statement shape. We ship with the parser in-process; this catches malformed emission generically.
4. **Integration (docker compose)** — every generated function executed against a real server:
   - `paradedb/paradedb:0.25.9+` on **PG18** — full matrix incl. search/hybrid.
   - plain `postgres:19-beta` — non-search matrix (search lanes skipped until ParadeDB ships PG19 builds; `pgb` emits version-agnostic SQL so nothing else changes).
5. **Fork-parity test** — pass A output diffed against upstream sqlc-gen-go on the same schema, so the fork stays honest about what it changed.

---

## 13. Repo layout

```
pgb/
  core/                    # handwritten runtime (public module)
    expr.go  emit.go  stmt.go  scan.go  tx.go  page.go
    search.go              # ← ALL pg_search emission; the churn barrier
  gen/                     # sqlc-gen-pgb (the plugin binary)
    main.go                #   codegen.Run(handler)
    ir.go                  #   SchemaIR types
    load_sqlc.go           #   proto catalog + PluginOptions → IR
    load_ddl.go            #   oliphant re-parse → indexes/comments/generated
    maptype.go             #   PG→Go mapping + overrides
    pass_models.go         #   pass A: models (forked from sqlc-gen-go) + query wrappers
    pass_builders.go       #   pass B: descriptors + predicates + builders
    pass_statics.go        #   pass C: static funcs + search funcs + pagination
    tmpl/                  #   templates (text/template, or plain string builders)
  standalone/              # pgb-gen: oliphant-only entry, same IR/passes
  internal/golden/         # golden files + runner
  example/                 # demo schema (incl. USING paradedb DDL) + docker-compose + e2e
  docs/                    # Mintlify site (3 tabs: Docs / ParadeDB / Reference; 35 pages incl. errors, performance, migrations, recipes, internals)
  Makefile                 # fmt-schema / fmt-docs / check-docs / validate targets
  tools/fmt-docs.py        # docs fenced-code formatter (gofmt, JSON, --check)
  .sqlfluff                # schema SQL formatter config (postgres dialect;
                           #   pgb fmt replaces in v0.2 — AST-based)
```

## 14. Milestones

| M | scope | exit criteria |
|---|---|---|
| M0 | fork sqlc-gen-go into `gen/pass_models.go`, plugin skeleton wired in sqlc.yaml | real schema → identical models/query-wrappers to upstream |
| M1 | IR + oliphant DDL pass + core emitter + descriptors/builders | golden tests; round-trip tests green |
| M2 | pass C statics + scan funcs + docker harness (pg18) | CRUD e2e against real Postgres |
| M3 | pg_search layer: DDL extraction, predicates, score/snippet, `SearchXxx` | e2e vs paradedb image; ⚠ shapes verified against 0.26 docs |
| M4 | PG19 gates (DoSelect upsert), keyset, hybrid RRF, batch | matrix green on pg18 + pg19-beta |
| M5 | MERGE builder (opt-in), temporal upsert, directives polish, docs | tag v0.1 |

## 15. Risks & open decisions

**Risks**
- sqlc releases lag main (PG18 parser + `any`-tolerance unreleased) → pin `@main` until the next tagged release ships these.
- ParadeDB churn (twice in 2025) → one-file emitter + `paradedb.version` gate; ⚠-marked shapes verified in M3.
- Fork drift from sqlc-gen-go → fork-parity test in CI.
- Unknown-function `any` leaking into handwritten-query types → casts / database-backed analysis.

**Open decisions (yours)**
1. `pgb` as the working name?
2. Nullability default: `pgtype.Xxx` (sqlc-familiar) vs `nullability: pointers`?
3. Pilot schema: nyuka (has full-text-ish needs?) or a fresh demo schema first?
4. Publish as OSS eventually, or private tool? (affects naming/repo layout only)

---

## 16. Future development — pgb 2.0, the Prisma-style loop (2026-09-13, user directive)

Marked as the post-v0.1 expansion. Three components, phased; the sqlc-plugin path remains fully supported — this is a second front door into the same engine, not a replacement.

**P0 — extension/plugin architecture (2026-09-13, user directive: "add paradedb plugin as official plugin… so people can play around with it").** Core becomes generic-Postgres-only; everything ParadeDB-specific re-homes into the official, in-repo, semver'd **`pgb-paradedb`** extension (first plugin of the platform). Extension = codegen side + runtime side: (1) IR-enrichment hook (namespaced `ir.Table.Extensions["paradedb"]`, SDK ships oliphant helpers); (2) DSL-attribute hook (plugins claim `@paradedb.*` attrs once P1 lands — unknown attribute errors naming the claiming plugin); (3) codegen-weave hook (new files + method weaves into generated table structs + static registrations — possible because pgb owns the generated structs); runtime modules import plugin runtimes pinned by semver, extras registered via a `DialectExtension` emitter interface (validated at generate time). Third-party plugins = process plugins over the same protobuf pattern as sqlc (any language); official plugins = in-process, in-repo. What moves: `core/search.go`, `USING paradedb` extraction, predicate/score/hybrid generation, `paradedb:` options, `@search` attrs. What stays in core: pgvector type mapping (generic), everything else. Exit criterion: **byte-identical v0.1 output before/after the split** (provable by golden tests), plus plugin template repo + author docs. Full user-facing version: `docs/future/plugins.mdx`.

**P1 — declarative multi-file schema DSL (`pgb/*.pgb`).** Prisma-style `model` declarations with first-class ParadeDB annotations (`@search(tokenizer:, path:, alias:)`, `@@paradedb(keyField:)`) — no other schema language can express a search index. Lossless projection to PG18/19 DDL (generated columns, temporal constraints, ranges). DSL parser → the same SchemaIR; SQL-first `schema.sql` path unchanged. Exit: DSL → identical codegen output as the SQL path on the same schema.

**P2 — encoded IR artifact + `pgb migrate`.** Every generation writes `.pgb/schema.bin` (protobuf SchemaIR + pgb version + content hash — the DMMF/migration-lock analogy). Companion tool diffs artifact vs live catalog **in a shadow database** (Prisma Migrate approach): `pgb migrate dev` (auto-apply in dev, destructive changes confirmed interactively), `pgb migrate deploy` (explicit, reviewable SQL for prod), `pgb migrate diff`, `pgb migrate status` (drift report). The diff engine is the hard part: full `pg_catalog` coverage — oliphant-informed catalog work pointed IR→catalog (reverse of §4). Exit: shadow-DB diff correct on the integration schema set; dev/deploy flow e2e. Until then, v0.1 documents the interim story in `guides/migrations` (goose/golang-migrate loop).

**P3 — relation-aware ORM client (pure Go, pgx-native).** Relations promoted from non-goal: `@relation(fields:, references:)` in the DSL, generated `client.User.FindMany(User.Where(...)).Include(User.Include.Posts()).Take(n).Exec(ctx)`. Eager loading via `pgx.Batch` pipelining (one round trip per include level — no N+1, no Rust engine process like Prisma). Statics/builders/search from v0.1 unchanged — the ORM adds traversal on top. Exit: Include-based loading with zero N+1 in tests.

**Prisma parity map:** schema.prisma → `pgb/*.pgb`; DMMF → `.pgb/schema.bin`; `prisma migrate dev/deploy` → `pgb migrate dev/deploy`; Prisma Client (Rust engine) → pure-Go client on the same core emitter; `$queryRaw` → `core.Raw` + the sqlc handwritten-query path; and ParadeDB search annotations have no Prisma equivalent (the wedge).

**Sequencing:** P0 first (plugin split — can start after M5, or in parallel with M3+ as the APIs stabilise); P1 after P0 (attributes are plugin-owned); P2 after P1; P3 after P2. Biggest open question: reuse existing migration tooling (goose/atlas) vs own the declarative diff engine — leaning own-the-diff-engine, compatible with plain `.sql` migration dirs for baselining.

Full user-facing version: `docs/future/vision.mdx`.

---

## 17. Version support & deprecation policy (2026-09-13)

Concise engineering version of `docs/reference/support-matrix.mdx` (the user-facing page). Supported matrix:

| component | supported | notes |
|---|---|---|
| PostgreSQL | 15, 16, 17, 18 | EOLs 2027-11-11 / 2028-11-09 / 2029-11-08 / 2030-11-14 (5 years per major); PG19 gated until GA (beta3 2026-08-13) |
| ParadeDB pg_search | 0.25.9+ / 0.26-rc | floor PG15; no PG19 binaries; pgvector required since 0.25.0; `paradedb.version: "0.26"` = rc-tracking gate |
| sqlc | v1.31.1 (2026-04-22) | pinned; Makefile installs `@main` for oliphant (PG18 parser) + unknown-function→`any` tolerance — both unreleased |
| pgb targets | 0.25.9+ / 0.26-rc | v2 operator API (§8) |

**Deprecation mechanism (PROPOSED).** (a) config `target` gate — hard error on unsupported emitter/feature combos, warning when a target nears EOL (within ~6 months); (b) `paradedb.version` gate — validation against known releases, error on unsupported combos; (c) codegen deprecation warnings emitted as comments in generated files one MINOR before a drop; (d) N-2 support window proposal — a Postgres major or ParadeDB floor is dropped only in a pgb MAJOR release, announced one MINOR ahead.

---

## 18. Editions strategy (2026-09-13, user question: "ontop of sqlc or full blown standalone?")

Decision: **standalone core, sqlc as one frontend.** One Go module (`github.com/your-org/pgb`), one semver, two editions sharing everything below the frontend boundary. User-facing docs: `docs/editions/{overview,plugin,orm}.mdx` (Editions group, first in the Docs tab).

- **Shared core:** SchemaIR → codegen passes A/B/C → runtime core (`core/`: expression tree, emitter, scan, DBTX) + the type system. Golden tests require BYTE-IDENTICAL generated output from both frontends on the same schema — drift is a CI failure, not a hope.
- **Edition A — sqlc plugin (v0.1, current design):** `cmd/sqlc-gen-pgb` shim + `sqlcfront` (GenerateRequest proto → IR). Remains the distribution channel to the sqlc community and the cheapest vertical slice; couples to sqlc's release cadence and analyzer gaps (no AST, index DDL dropped → oliphant re-parse, `pdb.*` untyped).
- **Edition B — standalone ORM (v0.2+):** `cmd/pgb` CLI + library (`go get` the module, `go install` the CLI). Own frontend: oliphant parse → own catalog model (extension registry: pg_search `pdb.*`, pgvector, ltree… full PG15–19 type matrix incl. multiranges, geometric, xid8, PG18 uuidv7/virtual-generated awareness) → the same passes. Enables the whole 2.0 roadmap: DSL (P1), declarative migrations with own catalog diffing (P2), relation/Include layer (P3).
- **Planner-aware emission commitments (edition B, and core where applicable):** sargable-by-construction predicates; constant statement shapes so pgx statement/plan caching stays valid; explicit param casts (`$1::timestamptz`); pushdown-shaped pg_search SQL (Top-K, fast fields, indexed tiebreaks); unnest/pgx.Batch batching instead of loops; `pgb vet --explain` plan assertions in CI (v0.3, planned).
- **Why not "on top of sqlc" for the ORM:** the plugin protocol is a lossy ceiling (no AST, DDL dropped, cadence outside our control) and the ORM's "every type of every version" promise requires owning the catalog model. The plugin edition already re-parses schema files with oliphant — standalone promotes that side-pass to the main entrance. Honest cost: an own catalog model is the expensive part; scope it to what codegen consumes (not arbitrary-query analysis — that stays sqlc's job in edition A; `Raw()` covers it in edition B until the P3+ analyzer lands).

---

## Changelog

- 2026-09-13 (b): generated output consolidated into ONE package db (dbgen + models_package retired); executor param renamed exec; docs IA reorganized (Postgres tab folded into Reference).
- 2026-09-13 (c): docs IA consolidated to 3 tabs (Generated code folded into Docs, Resources into Reference); 7 new pages (errors, performance, migrations, recipes, internals/emitter, internals/codegen-pipeline, paradedb/performance).
- 2026-09-13 (d): formatters — docs code formatter (tools/fmt-docs.py, gofmt+JSON, --check); schema SQL formatter (sqlfluff interim, .sqlfluff config, Makefile targets; pgb fmt AST-based v0.2); fixture already sqlfluff-formatted and sqlc-parse-verified post-format.
