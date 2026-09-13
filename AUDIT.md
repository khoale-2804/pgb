# pgb code-quality audit — 2026-09-14

Full audit (core/, gen/, emitted snapshots, staticcheck) after the real-world
validation round. Findings fixed immediately are listed under "Fixed"; the
rest are recorded here as the known-issues ledger, priority-ordered.

## Fixed in this round

- **P0** `In` emitted `col = ANY $1::cast[]` (missing parens, 42601 on every
  use) → now Raw `col = ANY(?::cast[])`; serial pseudo-types map to their
  underlying int (`bigserial[]` does not exist as a type). gen/pass_builders.go
- **P0** `KeyEq` emitted `col ->> $1` with two bound args and no comparison
  (08P01) → now `col ->> ? = ?`. gen/pass_builders.go
- **P1** unnest batch statics for array/jsonb/bytea columns bound
  arrays-of-arrays pgx cannot encode (42804) → batch suppressed when any
  insertable column's Go type is a slice. gen/pass_statics.go
- **P1** aliased JSON-path search predicates omitted the
  `::pdb.literal('alias=…')` cast → pg_search 0.25.9 resolves aliased fields
  ONLY through the cast ("field … is not part of the pg_search index");
  path expressions also quote the column now. gen/pass_search.go
- **P2** `Call` with named-only args rendered `f(, a => $1)`. core/expr.go
- **P2** `Order{E: nil}` emitted a bare ` ASC`. core/stmt.go
- **P2** doc typo "then the spgb." in Scan doc comment. gen/pass_search.go
- New runtime gate: `TestEmittedPredicateShapes` (testdata/integration)
  executes 11 emitted predicate shapes against the real server — snapshot
  tests pin Go shapes, only this test pins that the SQL is valid.

## Open findings (deferred, priority order)

### P1

1. **heuristicPK fails soft** — PARTIALLY FIXED (shift of 2026-09-14):
   ALTER TABLE ADD CONSTRAINT keys are now extracted (kills the main
   silent-wrong path) and a stderr warning fires when the id heuristic keys
   the statics. The "skip the statics" option remains open for debate. — gen/pass_statics.go: `heuristicPK` falls back
   to "single NOT NULL id column" and `load_ddl.Enrich` swallows parse
   errors, so an unparseable schema silently keys Get/Update/Upsert/Delete on
   a possibly non-unique `id` (wrong-row reads; `ON CONFLICT (id)` → 42P10).
   Fix direction: log a warning to stderr and skip key-based statics when the
   heuristic fires.
2. **ALTER TABLE ADD PRIMARY KEY not supported** — FIXED (6bb99b9):
   gen/load_ddl.go extracts ADD CONSTRAINT ... PRIMARY KEY|UNIQUE; sqlc's own
   catalog proto still ignores them (upstream), which is exactly why the DDL
   pass exists.
3. **Reserved-word quoting list is incomplete** — FIXED (513fb52):
   core/keywords.go generated from pg_get_keywords() on the pinned PG18
   server (494 words, all classes); QuoteIdent quotes any collision
   defensively. Regeneration one-liner in the file header.
4. **Column overrides match by name suffix only** — gen/maptype.go:
   `products.embedding` matches every table's `embedding`. Resolve overrides
   with full schema.table.column context.
5. **Plain `DEFAULT` clauses are not extracted** — gen/load_ddl.go sets
   HasDefault only for identity columns; inserts bind every settable column,
   so a Go zero value reaches the server as an explicit NULL that overrides
   the server default (23502 on NOT NULL defaults). Needs a semantic decision
   (three-state params for defaulted columns vs omit from INSERT list) before
   implementation — CONTRACTS.md-relevant.
6. **Hit struct hardcodes `Product` field** — FIXED (c1a5d78): the field is
   `Row` on every `Search<T>Hit`; docs and both DB-backed suites updated.

### P2

7. Cross-schema Go-name collisions possible (schema `app` + table `users` vs
   default-schema `app_users`) — no dedupe pass (pass_models.go,
   pass_builders.go).
8. `series` pluralizes to `Seriess` (exception map defeats the
   identity-plural guard in goTableNames).
9. Repo files import every column's type even when unused in that file
   (no_filter + pgb:type combos can produce "imported and not used" in
   rare combinations).
10. Malformed `pgb:*` COMMENT tokens are dropped silently; malformed plugin
    options JSON reverts to defaults silently — surface warnings.
11. `joinPath` doesn't escape `'` in JSON keys → invalid path SQL for keys
    containing quotes (gen/load_ddl.go).
12. `SearchIndex.Options` written, never read (dead data) — sort keys before
    any future emission.
13. `ListOpt` has no ORDER BY hook — keyset pagination on unordered results
    is unsound on live data (documented risk; docs/guides).
14. Nullable-array elements (`text[]` with NULL elements) scan into `[]string`
    → pgx error; matches stock sqlc behavior, worth a docs note.
15. Hot-path `fmt.Sprintf` per param in emitter (core/emit.go, core/expr.go)
    — strconv appends would do; codegen-time only, cosmetic.

## Verdict (post-fix)

The two P0 SQL-shape bugs and the four runtime-breaking P1s found by the
audit + real-world run are fixed and covered by server-executing tests.
Remaining items are correctness-adjacent hardening (1–4) and one API decision
(5–6). staticcheck: clean. Emitted code reviewed as mergeable.
