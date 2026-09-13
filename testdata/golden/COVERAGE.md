# Golden fixture — coverage matrix

`schema.sql` + `queries.sql` are the canonical inputs for every verification
layer (golden generated-code tests, fork-parity, both editions). Each row maps
a schema feature to the generated artifact that proves codegen handled it and
to the pass that owns it ([pipeline](/internals/codegen-pipeline)).

| # | fixture feature | location | exercised artifact | pass |
|---|---|---|---|---|
| 1 | serial / bigserial PK | `users.id`, everywhere | scan + Insert omits column + Get param | A/C |
| 2 | identity ALWAYS (`orders.id`) | §D | INSERT column-list omission | A/C |
| 3 | identity BY DEFAULT (`inventory.item_id`, `skippable_partner.id`) | §J, §L | same, `IncludeDefaults` opt-in. 2026-09-13: `skippable_partner` lost its stray `pgb:skip` comment and now generates (model + builder + statics); `pgb_ignored` is the only skip case | C |
| 4 | standalone sequence default (`tickets.number`) | §J | default-omission + filter presence | A/C |
| 5 | all numeric families (int2..int8, numeric(p,s), float4/8, money) | §B, §C, §D | typed predicates `Gt/Lt/Between`, scans, Set[T] | A/B/C |
| 6 | text family incl. citext + COLLATE | `users.email`, `users.login_ci`, `users.collate_col` | `Eq/Like/ILike`, scans | A/B |
| 7 | bytea / bit / varbit | `users.avatar`, `bitfield`, `flags` | scans, three-state writes | A/C |
| 8 | date/time family (timestamptz, date, timetz) | §B, §D | ordered-type predicates `Gt/Between`, keyset | A/B |
| 9 | json + jsonb (+ `pgb:type=map[string]any` override) | `users.settings`, `users.metadata` | `KeyEq` shorthand, typed override | A/B |
| 10 | uuid + gen_random_uuid default | `users.uuid_col`, `docs.id` | filter, scan, default-omission | A/C |
| 11 | network (inet/cidr/macaddr/macaddr8) | §B | scans + Eq predicates | A/B |
| 12 | bitstring (bit, varbit) | §B | scans | A |
| 13 | ranges (daterange, tstzrange via `during`) | §D, §F | pgtype.Range scans, overlap helpers (Raw until M5) | A |
| 14 | multirange (tstzmultirange) | §D | pgtype.Multirange scan | A |
| 15 | geometric (point/line/lseg/box/path/polygon/circle) | §B | pgtype scans | A |
| 16 | text search (tsvector, tsquery) | §B | scans | A |
| 17 | xml / pg_lsn / xid8 / tid / money | §B | scans | A |
| 18 | enums (multi-value, label with space) | §A, `users.status`, `orders.channel` | generated string type + Eq/In | A/B |
| 19 | domains (over text w/ CHECK, over int4) | §A, `users.email_domain`, `users.positive` | unwrap to base type | A |
| 20 | composite type (address_t) | §B | generated struct scan/encode | A |
| 21 | arrays 1-d (text[], int4[]) + multidim (int4[][]) + defaults | §B | `[]T` scans, `Contains` predicate | A/B |
| 22 | nullable vs NOT NULL vs NOT NULL DEFAULT | §B everywhere | nullability mode rendering | A |
| 23 | generated columns: VIRTUAL (PG18) + STORED | `users.search_slug`, `users.name_upper` | excluded from INSERT/UPDATE lists | C |
| 24 | PG18+ virtual as the default kind | same | same gate as above | C |
| 25 | CHECK constraints (single + table-level) | §C, §D, §E | parse-only (server-enforced) | parse |
| 26 | composite PRIMARY KEY (2-col, 3-col) | §D, §E | keyset composite cursor, Get/Upsert targets | C |
| 27 | UNIQUE single | `users.email`, `products.sku` | upsert conflict target | C |
| 28 | FK + ON DELETE CASCADE / RESTRICT / SET NULL | §D, §E, §H | parse-only (no ORM joins in v1) | parse |
| 29 | DEFERRABLE INITIALLY DEFERRED | §E | parse-only | parse |
| 30 | temporal EXCLUDE USING gist (WITHOUT OVERLAPS) | §F | IR `IsTemporal` + v1 explicit Raw error | parse |
| 31 | partitioned table (RANGE) + partitions | §G | parent as model, partitions not duplicated | A |
| 32 | UNLOGGED table | §J | normal model (persistence note in header) | A |
| 33 | second schema (app.*) incl. same table name | §H | schema-qualified emission, package-safe naming | A/B/C |
| 34 | cross-schema FK | `app.users.person_id` | parse-only | parse |
| 35 | views | §I | read-only model + select | A |
| 36 | materialized view | §I | read-only model | A |
| 37 | self-referential FK | §J categories | parse-only (inert FK detection) | parse |
| 38 | keyword-collision identifiers ("type", "range", "func"…) | §K | Go-safe renamed fields with quoted SQL | A/B |
| 39 | mixed-case quoted identifier ("Email" ≠ email) | §K | distinct fields, quoted emission | A/B |
| 40 | unicode identifier ("café") | §K | sanitized Go name, quoted SQL | A/B |
| 41 | 63-char identifier (PG truncation point) | `users.very_long_column_…` | full-length handling without silent cut | A |
| 42 | COLLATE "C" | §B, §K | parse-only | parse |
| 43 | pgb:skip table | §L `pgb_ignored` | NO files emitted (negative assert) | B/C |
| 44 | pgb:no_filter | `users.password` | no predicate methods | B |
| 45 | pgb:no_patch | `users.password` | excluded from UserSet | C |
| 46 | pgb:type= override (map[string]any) | `users.settings` | override resolution (directive > config) | A |
| 47 | pgb:type= pgvector override | `products.embedding` | pgvector scan/encode | A |
| 48 | pg_search index: key_field, tokenizer casts, JSON alias | §C | search methods scoped to indexed cols, score on key_field | B/C |
| 49 | pg_search: uuid key_field, edge_ngram, aliased double-index, stemming | §M | same, second shape | B/C |
| 50 | table WITHOUT pg_search index | §B users | no search methods generated | B |
| 51 | partial / expression / INCLUDE (covering) indexes | §C | parse-only; docs note index expectations | parse |
| 52 | pg_trgm fallback pair (gin gin_trgm_ops) | §C | recipes recipe (f) exercises it | parse |
| 53 | handwritten :one/:many/:exec/:execrows/:batchexec/:copyfrom | queries.sql | pass A wrapper emission per annotation | A |
| 54 | handwritten join / aggregate / window / CTE | queries.sql | wrapper types (Row structs) | A |
| 55 | handwritten pg_search + ::float8 cast convention | queries.sql `SearchProducts` | typed score (no `any`) | A |
| 56 | handwritten vector query (`<=>` + override) | queries.sql `VectorNeighbors` | pgvector param + scan | A |

## Deliberately NOT in the golden fixture

| item | why | where it lives |
|---|---|---|
| zero-column table | legal PG but degenerate; asserted in negative suite | `testdata/negative/zero-columns.sql` |
| unknown pgb directive | must warn, not fail | `testdata/negative/unknown-directive.sql` |
| duplicate table name | must fail loudly | `testdata/negative/duplicate-table.sql` |
| typed table (`OF composite`) | rare; parser handles, catalog modeling deferred | tracked in DESIGN.md §12 notes |
| FDW foreign tables | needs a running FDW extension in test images | deferred to integration lane |
| hstore / ltree / postgis columns | extension-dependent; covered by the override rule, not the fixture | extension registry (edition B) |

## Empirical findings (sqlc@main, 2026-09-13 fixture run)

- Bare casts in index column lists (`title::pdb.icu`) are REJECTED by the PG18 parser — parenthesize: `(title::pdb.icu)` (matches ParadeDB docs).
- String-form pgvector override emits INVALID Go (`pgvector-go.Vector` — hyphen in derived package name). Use the structured override (`import`/`package`/`type`). The error surfaces as `expected ';', found '-'`.
- Empirical stock mappings: tstzmultirange → pgtype.Multirange[pgtype.Range[pgtype.Timestamptz]]; xid8 → pgtype.Uint64; tid → pgtype.TID; money → pgtype.Numeric; **tsvector / xml / pg_lsn → any** (the leak the standalone edition catalog fixes); citext → unknown ext, resolves via override.
- Keyword/quoted/unicode identifiers sanitize cleanly (Type/Range/Select/Email/Café), labels with spaces preserved in enum constants (Passwordreset).
