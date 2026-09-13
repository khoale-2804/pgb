# pgb realworld fixture — findings (D1, 2026-09-13)

Fixture: realistic multi-tenant e-commerce schema (11 generating tables + 1
`pgb:skip` table, 2 enums, a view + materialized view, 2 pg_search indexes
with different tokenizers). Generated via sqlc 1.31.1 through the pgb plugin;
exercised against paradedb/paradedb:0.25.9 (PostgreSQL), database
`pgb_realworld`.

Everything here was observed on real generated output + live DB, not
speculation. Ordered by severity.

## P1 — generated code that fails at runtime

### 1. Unnest batch INSERT statics are broken for array and jsonb columns
- Emitted: `gen/products_repo.gen.go:313` (insertProductsSQL),
  `gen/webhook_endpoints_repo.gen.go:187`, same shape anywhere a `text[]` /
  `jsonb` column is insertable.
- The cast is built as `pgType + "[]"` (gen/pass_statics.go:449). For a
  `text[]` column that yields `$9::text[]`, so `unnest` produces **scalar
  text** rows and Postgres rejects the INSERT:
  `ERROR: column "tags" is of type text[] but expression is of type text (SQLSTATE 42804)`.
  Correct cast for unnest is `text[][]` (array-of-arrays), and `jsonb[]`
  additionally needs per-element jsonb encoding, not pgx's default bytea.
- Consequence: every table with an array/jsonb insertable column ships a
  batch static that 500s on first use. Tables whose columns are all scalars
  (coupons, product_reviews, order_items) work fine.
- Unrelated-but-adjacent: tables with an **enum** insertable column silently
  get NO batch static at all (gen/pass_statics.go:441, pgx lacks []Enum
  codecs) — `customers` and `orders` have no `InsertXs`. That part is
  documented in the generator comment; the array/jsonb breakage is not.

### 2. Aliased JSON-path search predicates fail at runtime
- Emitted: `gen/products_search.gen.go:223-245` — every method on
  `ProductAttributesBrandCol` emits raw SQL re-using the INDEX-side
  expression shape (`products.attributes->'brand' === ?`) but WITHOUT the
  `::pdb.literal('alias=brand')` cast the index DDL needed.
- Runtime: `ERROR: field 'attributes.brand' is not part of the pg_search index`.
  pg_search resolves the query-side field by the computed name
  `attributes.brand`, but the index registered the field under the alias
  `brand`. Verified both qualified and unqualified. The working query form
  is the cast form: `(attributes->'brand')::pdb.literal('alias=brand') === 'breville'`.
- Related: the `ProductAttributesBrandCol` type has NO accessor on
  `ProductTable` (pass B emits `Title()`, `Sku()`, ... but nothing hands out
  the alias column), so the type is unreachable except by manual struct
  literal — and constructing it with any `pgb.Col` values still emits the
  broken hardcoded SQL. Golden has the same shape (json_color), so this is
  systemic, not fixture-specific.

### 3. RESOLVED (2026-09-14): Default-bearing columns ride in INSERT lists; explicit NULL overrides the default
- `gen/pass_statics.go:155` / `gen/load_ddl.go:376-380`: `HasDefault` is only
  set for IDENTITY columns. Plain `DEFAULT` clauses (`DEFAULT now()`,
  `DEFAULT gen_random_uuid()`, `DEFAULT 0`, `DEFAULT '{}'`) are ignored, so
  `InsertCustomerParams` includes `signup_at`, `tags`, `lifetime_value`,
  `loyalty_points`, etc., and `InsertOrderParams` includes `subtotal`,
  `tax_total`, `grand_total`.
- Consequence A (correctness trap): the emitted doc comment promises
  "default-bearing columns join only with the include_defaults option" but
  that is only true for identity. Every smoke flow that relied on a server
  default failed with `SQLSTATE 23502` (e.g. `signup_at`, `subtotal`,
  `tags`, `promo_codes`) because the generated INSERT sends an explicit NULL.
- Consequence B (uuid PKs): `InsertMerchantParams`/`InsertHelpArticleParams`
  require the caller to synthesize `id` even though the DDL defaults to
  `gen_random_uuid()`.
- `include_defaults: true` therefore has no observable effect on this schema.

## P2 — sharp but survivable

### 4. No exported row scanner for builder output
`ScanProducts` / `ScanHelpArticles` are exported only for the two search
statics (gen/products_search.gen.go:268, help_articles_search.gen.go:186).
Per-table scanners (`scanCustomer` etc.) are unexported, so keyset
pagination or ad-hoc SELECTs built with `Customers.Select()...Run()` cannot
scan rows without hand-writing positional Scan calls. This pushes users
toward List statics, which expose Limit/Offset but NO ORDER BY hook
(`pgb.ListOpt` has only Limit/Offset, core/listopt.go) — so generated lists
are unordered the moment you paginate (our cursor flow leans on heap order
of a fresh table; on a live table that is not sound).

### 5. `HelpArticleHit.Product` is a `HelpArticle`
gen/help_articles_search.gen.go:176 — the hit struct field is named
`Product` on the help-articles surface (template copy-paste from products).
Cosmetic but confusing in exactly the way generated code should not be.

### 6. Helper namespace is the generated package, not core
`Set`/`SetOf`/`SetNull`/`Opt` live in `gen/pgb_helpers.gen.go` (package db),
not in `pgb` core. A caller writing `pgb.SetOf(...)` as the naming suggests
fails to compile. Either core should own them or the file should be named
`set_helpers.gen.go` to break the `pgb.` reflex.

### 7. Views/matviews infer column types from expressions
`coalesce(sum(oi.quantity), 0)` in the matview produced filter fields typed
`*any` (first generate) — fixed in the fixture with an explicit `::bigint`
(schema.sql, product_sales). Users mirroring real reporting views will hit
`*any` filters/models unless they cast every aggregate. A view without an
`id`-looking column (`product_sales` keys on `product_id`) generates List +
Count but no Get — reasonable, just undocumented.

### 8. Composite-PK tables get no single-row statics
`order_items` (PK order_id+line_number) generates Insert/InsertMany/
List/Count/UpdateMany/DeleteMany but no Get/Update/Delete by key
(gen/pass_statics.go:88 heuristicPK is single-column only). Expected for
v0.1 but worth a doc note since composite PKs are common in line-item
tables.

## Environment / tooling notes

### 9. sqlc cannot run `go run ...` process plugins
sqlc 1.31.1 resolves the plugin `cmd` with `exec.LookPath` on the WHOLE
string (internal/ext/process/gen.go) — `cmd: "go run github.com/..."` fails
with `process: go run ... not found`. Additionally the child process runs
with a stripped environment (only `SQLC_VERSION`), which hides `go`, HOME,
and the module cache. Fix: the checked-in `./pgb-plugin` wrapper (re-derives
PATH/HOME from the passwd database, then execs `go run`), invoked as
`cmd: "./pgb-plugin"` relative to this directory.

### 10. ParadeDB 0.25.9 operational oddities (not pgb bugs, but will bite)
- Once, an INSERT into `products` made immediately after `CREATE INDEX ...
  USING paradedb` (separate session) never appeared in the BM25 index —
  `pdb.all()` returned nothing for that row while rows inserted later were
  searchable. Could not reproduce in isolation (5+ attempts with scratch
  tables); if it recurs in CI it is an upstream visibility race, not a pgb
  issue.
- `CREATE MATERIALIZED VIEW ... AS SELECT ... FROM products LEFT JOIN
  order_items ...` prints `WARNING: Aggregate Scan (DataFusion) not used:
  all tables in the join must have BM25 indexes (table: join)` — noise from
  the pg_search planner hook on any join query in the database.
- `paradedb.status_info` and `paradedb.force_merge(...)` do not exist in
  0.25.9 despite appearing in some ParadeDB docs.

## What worked cleanly
- sqlc generate end-to-end through the plugin on the first try (after the
  wrapper); generated package compiles with zero hand-edits; `pgb:skip`
  (audit_log) and `pgb:no_filter`/`pgb:no_patch` (webhook_endpoints.secret)
  honored; `pgb:type=map[string]any` on customers.shipping_address works
  including roundtrip; enums, uuid, text[], pgtype.Numeric, timestamptz
  models all scan/encode correctly; stored generated column (line_total)
  correctly excluded from INSERT/UPDATE and readable; identity PKs returned
  from INSERT; view + matview readers; `pdb.parse` + `pdb.score` +
  `pdb.snippet` document query over the default tokenizer; edge_ngram(2,10)
  autocomplete match (`title ||| 'refu'` matches "Refund policy"); keyset
  pagination via filter + ListOpt + EncodeCursor/DecodeCursor; WithTx
  rollback/commit; ErrNoWhere guard.

## Smoke test
`go test -tags integration ./testdata/realworld/ -count=1` — 4 flows:
CRUD FK chain (+ generated column, Set/SetNull, ErrNoWhere), BM25 search
statics (default + edge_ngram + snippet + Extra-composed match), cursor/
ListOpt keyset pagination, transactions + view/matview readers.
Result: PASS (run twice for stability).
