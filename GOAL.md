# GOAL — pgb autonomous work shift

You are continuing an in-progress implementation. Work SOLO (subagent concurrency
cap ≈3; prefer inline). Repo: /home/rei/data/pgb (module
github.com/khoale-2804/pgb, public, MIT). Everything is committed/pushed at
start; work on main, commit+push after every green milestone.

## GOAL

Ship pgb v0.1 complete per DESIGN.md milestones, fully tested, docs in sync:
**M3 (oliphant DDL pass → directives, paradedb search codegen, real
PK/generated-column data) → M1 (golden snapshot tests + docs sync) → polish.**

## READ FIRST (in this order)

1. /home/rei/data/pgb/GOAL.md (this file)
2. /home/rei/data/pgb/CONTRACTS.md (frozen signatures — extend, never break)
3. /home/rei/data/pgb/DESIGN.md (§2 SchemaIR, §6 core, §8 pg_search layer,
   §9 emitted shapes, §12 testing)
4. /home/rei/data/pgb/testdata/golden/COVERAGE.md (feature matrix)
5. `git log --oneline -5` (see ALREADY-DONE below)

## ALREADY DONE (do not redo)

- v0.1 vertical slice (commit dbd647f): ir/, core/ (expr/emit/stmt/dbtx/
  page/tablemeta, 48 tests), gen/ (sqlcat catalog→IR, options, maptype,
  pass_models, pass_builders, pass_statics, gen.go orchestrator),
  cmd/sqlc-gen-pgb, Makefile targets (plugin/test/install-plugin/
  validate-plugin/fmt-*), integration test (sqlc generate through plugin,
  emitted package compiles).
- Golden fixture testdata/golden/ + COVERAGE.md + negative suite.
- Docs site (docs/) 35 pages, layered IA; formatter tools/fmt-docs.py;
  schema formatter (.sqlfluff + Makefile fmt-schema).
- Known proto limits (documented): catalog proto has NO PK/unique/view-flag/
  generated-col data → v0.1 slice uses id-column heuristic for upsert/keyset
  and cannot exclude generated columns from INSERT — M3a fixes exactly this.
- M3c (2026-09-14): gen/pass_search.go — pass D emits <table>_search.gen.go
  (search predicate methods on the indexed pass-B col types, JSON-path
  wrappers via core.Raw, table Score(), Search<Table> + Scan<Table> +
  hit/opts types), wired into gen.Generate after pass C. Fixture fix:
  skippable_partner no longer pgb:skip (identity-BY-DEFAULT coverage;
  COVERAGE row 3 notes it).
- M1 (2026-09-14): golden snapshots — gen/golden_test.go replays the real
  sqlc request (testdata/golden/plugin_request.pb, the raw GenerateRequest
  proto captured from the plugin's stdin) through gen.Generate and
  byte-compares against testdata/snapshots/ (44 files; -update rewrites,
  stale snapshots pruned). go-cmp added. Backlog 3+4 done; docs sync (item 5)
  remains. NOTE: gofmt -l flags pre-existing ir/ir.go alignment (out of
  bounds for gen builders).

## BACKLOG (priority order; stop and commit+push at each green boundary)

1. **M3a — DDL extraction pass (`gen/load_ddl.go`)**: add dep
   github.com/sqlc-dev/oliphant@latest (inspect API: `go doc
   github.com/sqlc-dev/oliphant/...`; it is a pure-Go libpg_query-18 port —
   find the Parse entrypoint returning the protobuf AST). Parse every file in
   req's schema paths (paths are in req.Settings.Schema relative to the
   config dir — resolve carefully; the integration test proves what works).
   Extract into ir:
   - `CREATE INDEX ... USING paradedb|bm25` → ir.Table.Search (SearchIndex:
     key_field from WITH, column casts → SearchField tokenizer/alias/path)
   - `COMMENT ON TABLE/COLUMN` → directives (pgb:skip, pgb:no_filter,
     pgb:no_patch, pgb:type=...) — merge/override gen.DirectiveSet
   - generated columns: GENERATED ALWAYS AS (...) STORED|VIRTUAL →
     ir.Column.Generated
   - PK/UNIQUE constraints from CREATE TABLE → ir.Table.PrimaryKey/Uniques
     (replace the id-heuristic in gen/pass_statics.go: upsert target + keyset
     key = real PK; upsert only when a single-column unique exists)
   - views: CREATE VIEW/MATERIALIZED VIEW → ir.Table.View
   Wire into gen.Generate: sqlcat.Build → load_ddl.Enrich(req, &sch).
   Unit tests with inline DDL strings per feature (no DB needed).
2. **M3b — `core/search.go` + tests**: pg_search constructors on Col:
   Match (|||), MatchAll (&&&), Phrase (### + ::pdb.slop(n) on the literal),
   Exact (===), ExactAny (=== $n::text[]), Fuzzy (=== $n::pdb.fuzzy(d, pre)),
   Regex (@@@ pdb.regex($1)), Parse (@@@ pdb.parse($1)), RangeTerm,
   Boost(b) chaining on any RHS, Score(keyCol) → pdb.score(col),
   Snippet/Snippets/Highlight (named args), All() (pdb.all), Proximity (##).
   Table-driven tests pinning SQL shapes byte-exact (see docs/paradedb/*).
3. **M3c — search codegen**: pass B emits search predicate methods ONLY on
   columns present in ir.Table.Search.Fields (+Score()); pass C emits
   Search<Table>(ctx, exec, q string, opts) statics (score + optional snippet,
   ORDER BY pdb.score(key) DESC, key ASC LIMIT $n); upsert/keyset now use the
   real PK/unique from M3a; generated columns excluded from INSERT/UPDATE
   column lists now that flags are real. Update golden integration assertions.
4. **M1 — golden snapshots**: testdata/snapshots/ + golden_test.go (-update
   flag) covering models/builders/statics files for the whole fixture; wire
   `make test`; update docs/guides/testing.mdx + COVERAGE.md statuses.
5. **Docs sync + polish**: COVERAGE.md rows to "implemented"; README milestone
   status; docs/editions + guides wording where the slice changed behavior.

## VERIFY (before every commit)

```bash
cd /home/rei/data/pgb
go build ./... && go vet ./... && gofmt -l . && go test ./...
make validate          # sqlc parses the fixture (stock gen:go)
make validate-plugin   # e2e: sqlc generate through the pgb plugin
```
All must pass. Integration test needs PATH to include ~/go/bin (sqlc lives
there) — `export PATH=$PATH:$HOME/go/bin`.

## HARD CONSTRAINTS

- Never break `go test ./...` or `make validate-plugin`. Commit+push only on
  green (git add -A && git commit && git push origin main).
- CONTRACTS.md signatures: extend, never break. gofmt clean. No reflection in
  core. Every value in emitted SQL is a bound parameter.
- Deterministic emission: no map iteration without sorting, no timestamps in
  generated headers.
- pg_search SQL canon lives in core/search.go ONLY (churn barrier). Shapes:
  docs/paradedb/predicates.mdx + scoring.mdx. Anything unverified upstream:
  keep the "to be verified" convention and mark in tests.
- Do not modify testdata/golden/schema.sql semantics (fixture is the spec);
  if the fixture itself must change, note it in COVERAGE.md and commit
  separately.
- sqlc binary: ~/go/bin/sqlc (main build). Model: GLM — work inline, no
  subagents needed.
- If a milestone is fully green: commit, push, update this file's
  ALREADY-DONE, then continue to the next backlog item.

## ALREADY-DONE (2026-09-13 second shift)

- M3a (5c267ad): gen/load_ddl.go — oliphant DDL pass (paradedb indexes,
  directives, generated columns VIRTUAL/STORED, PK/uniques, views).
- M3b (966fb00): core/search.go — pg_search constructors, byte-exact pins.
- M3c + M1 (12f0e33): pass D search codegen (<table>_search.gen.go,
  Search<Table> statics, Score/Snippet), golden snapshots (44 files,
  deterministic .pb request fixture), skippable_partner unskipped,
  COVERAGE all-56-rows implemented.
- Full gate green: build/vet/test/validate/validate-plugin; emitted
  package compiles.

## END CONDITION

When backlog items 1–5 are all green and pushed: run one final full verify,
update ALREADY-DONE with the completion note, push, and then STOP (the hourly
cron will find no backlog and idle cleanly — leave a final status line here).
