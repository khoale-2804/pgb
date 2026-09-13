# pgb

[![status](https://img.shields.io/badge/status-in%20progress-orange)](DESIGN.md#14-milestones)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
![postgres](https://img.shields.io/badge/postgres-15–19-blue)

**Two-stage Postgres codegen with full ParadeDB support** — a [sqlc](https://sqlc.dev) process plugin that turns a Postgres schema into typed Go: models, per-table query builders, and static query functions, with first-class [pg_search](https://docs.paradedb.com) (BM25) coverage.

> **Status: in progress — M0–M3 implemented and tested.** The sqlc plugin works end-to-end against the golden fixture (models, builders, statics, pg_search codegen, snapshot tests), and an integration lane runs the generated code against `paradedb/paradedb:0.25.9` (pg_search 0.25.9 — the tested floor; 0.24 and earlier only accept the legacy `USING bm25` index spelling pgb does not emit). Remaining: `pgb fmt`, the DSL (P1) and migrations (P2). See the [roadmap](DESIGN.md).

```go
// static layer — zero hand-written SQL
users, err := dbgen.ListUsers(ctx, db, dbgen.UserFilter{
    EmailLike: pgb.Opt("%@corp.io"),
}, pgb.Limit(20))

// search — generated from the USING paradedb index definition
hits, err := dbgen.SearchProducts(ctx, db, "running shoes",
    dbgen.SearchOpts{Fuzzy: 1, Snippet: "description"})
```

## Status

Design + docs phase — implementation milestones M0–M5 are defined in [DESIGN.md](DESIGN.md).

## What's here

| path | what |
|---|---|
| [DESIGN.md](DESIGN.md) | full engineering design: SchemaIR, emitter, dialect gates, plugin architecture, version policy, 2.0 vision |
| [docs/](docs/) | Mintlify documentation site (what the site renders — run it locally or read on GitHub) |

## Run the docs locally

```bash
cd docs
npm install
npx mint dev --port 3736
```

See [docs/README.md](docs/README.md) for details (search needs a one-time free `mint login`).

## Targets

- PostgreSQL 18 (full) · 19 (gated, beta)
- ParadeDB pg_search 0.25.9+ / 0.26-rc — v2 operator API, aggregations, pushdown-aware helpers
- Go 1.24+ · pgx/v5

## License

MIT — see [LICENSE](LICENSE). Note: the ParadeDB pg_search extension this tool targets is AGPL-3.0; using it in your stack is unaffected — AGPL governs redistribution of the extension itself.
