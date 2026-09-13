# pgb edge fixture — hostile-codegen results (D2)

Compile-only proof that pgb v0.1 survives hostile-but-legal PostgreSQL DDL.
No database is used anywhere in this fixture.

Files:
- `schema.sql` — the hostile schema (items E1–E11, mapped in the header comment)
- `queries.sql` — sqlc parse/analyze acceptance over the hostile names
- `sqlc.yaml` — pgb plugin config (`cmd: sqlc-gen-pgb`, resolved via PATH)
- `gen/` — checked-in plugin output from the run documented below

## Reproduce

```sh
export PATH=$PATH:$HOME/go/bin
make plugin   # builds bin/sqlc-gen-pgb (sqlc execs the cmd directly, no shell)
cd testdata/edge
sed "s|cmd: \"sqlc-gen-pgb\"|cmd: \"$PWD/../bin/sqlc-gen-pgb\"|" \
    sqlc.yaml > .pgb-plugin-gen.yaml
sqlc generate -f .pgb-plugin-gen.yaml
rm .pgb-plugin-gen.yaml
cd ../.. && go build ./testdata/edge/gen/...   # same layout as golden gen-plugin
```

sqlc v1.31.1, pgb plugin v0.1.0. sqlc requires a `queries:` entry even when the
plugin ignores queries — a config with `schema:` only fails with
`error parsing queries: no queries contained in paths`.

## Per-item outcomes

| Item | Hostile input | Outcome |
|------|---------------|---------|
| E1 | table `"order"` (reserved word) | **survived** — model `Order`, descriptor `Orders`; emitted SQL quotes `public."order"` (gen/order_repo.gen.go:157,185) |
| E2 | table `"user"` (reserved word) | **survived** — `public."user"` quoted in raw SQL (gen/user_repo.gen.go:162); no-PK so no Get/Upsert (correct) |
| E3 | columns `"select"`, `"where"`, `"order"` | **survived** — Go fields `Select`/`Where`/`Order` (legal, exported); SET/RETURNING lists quote every occurrence (gen/user_repo.gen.go:146,198–216) |
| E4 | mixed-case quoted `"UserData"` / `"ColumnName"` / `"ID"` | **survived** — quoting is case-driven via `needsQuote` (core/emit.go); fields `ColumnName`, `ID` |
| E5 | no-PK (`loose`), only-nullable-text (`drafts`), one-column (`singleton`) | **survived** — no Get/Upsert/Update-by-key emitted (PK heuristic correctly declines); Insert/InsertMany/List/Count/Update/Delete-by-where only (gen/loose_repo.gen.go, gen/drafts_repo.gen.go, gen/singleton_repo.gen.go) |
| E6 | `pgb:no_filter` / `pgb:no_patch` via COMMENT ON quoted names | **survived** — DDL pass resolved `"user"."select"` and `"order"."where"`; `UserFilter` has no Select field; `OrderSet`/UpdateOrders skips Where (gen/user_repo.gen.go:12–31, gen/order_repo.gen.go:210–240) |
| E7 | interval/inet/bytea/uuid/timestamp columns | **survived** — `pgtype.Interval`, `netip.Prefix`, `[]byte`, `uuid.UUID`, `pgtype.Timestamptz`; batch-insert casts `$2::interval[], $3::inet[], $5::timestamp[]` (gen/timers_repo.gen.go:195, models.gen.go:48–54) |
| E8 | non-public schema `billing` (plain + mixed-case table) | **survived** — models/descriptors prefixed `Billing…`; statics qualify `billing.invoices` / `billing."TaxRate"` (gen/billing_invoices_repo.gen.go, gen/billing_TaxRate_repo.gen.go) |
| E9 | 63-character table and column identifiers (PG name limit) | **survived** — file `sales_summary_report_…_tota.gen.go`, struct `SalesSummaryReportWithTwelveMonthsOfAggregatedLineTota`, field `CumulativeGrossRevenueInMinorUnitsIncludingTaxAndAdjus` (models.gen.go:57–59) |
| E10 | column `id` that is NOT the PK (PK is `code`) | **survived** — statics keyed on `code`: `GetWeirdPk(…, code string)` etc. (gen/weird_pk_repo.gen.go:114,194,270,295) |
| E11 | `ALTER TABLE … ADD CONSTRAINT … PRIMARY KEY` | **NOT SUPPORTED** — see below |

`sqlc generate` completed with exit 0 and no panics; `go build` and `go vet`
on `gen/` passed first try. **No gen/ or core/ fixes were needed** — identifier
quoting (core `QuoteIdent` + gen raw-SQL emission) handled every case.

## E11: ALTER TABLE ADD CONSTRAINT — detailed answer

Parsing is accepted (sqlc exits 0), but the constraint never reaches codegen:

- pgb's DDL pass only inspects CreateStmt / IndexStmt / CommentStmt / ViewStmt /
  CreateTableAsStmt — there is no AlterTableStmt handler (gen/load_ddl.go,
  `enrichStmt`), so an ALTER-added PK can never set `ir.Table.PrimaryKey`.
- The sqlc catalog does not carry it either (proved empirically): a table with
  `ALTER TABLE … ADD PRIMARY KEY (code)` and an `id int4 NOT NULL` column
  generated `GetProbeAlt(…, id int32)` — the PK statics keyed on `id`, not
  `code`, i.e. the "heuristic PK" fallback (pass_statics.go `heuristicPK`)
  fired, which only happens when PrimaryKey/Uniques are both empty.
- Same probe with no `id` column: no Get/Upsert/Update-by-key at all — the
  table silently degrades to the no-PK shape (List/Count/Insert/Update/Delete
  by where).

Verdict: **legal DDL, silently ignored** (parse OK, no error). Workaround:
declare PK/UNIQUE inline in CREATE TABLE. Deferred to upstream/finding —
adding an AlterTableStmt case to the DDL pass is structural, not a small fix.
`ADD CONSTRAINT … UNIQUE`/`CHECK` were not probed separately; UNIQUE flows
through the same two code paths and should behave identically.

## Findings (recorded, not fixed)

1. **ALTER-PK + `id` column silently mis-keys statics** (P1). The E11 probe
   produced `GetProbeAlt(id)` while the declared PK was `code` — the id
   heuristic masks the dropped constraint. A generated repo that Get/Updates
   by the wrong key is a correctness trap. Suggested gate: warn when the
   heuristic PK fires on a table that also has table-level constraints.
2. **`timestamp` maps to `pgtype.Timestamptz`** — deliberate and frozen:
   CONTRACTS.md:155–156 and gen/maptype.go:80. Stock sqlc-gen-go emits
   `pgtype.Timestamp`. Encoding a timestamptz-typed param into a `timestamp`
   column works via assignment cast but loses the type distinction at the Go
   boundary. Documented divergence, left as-is.
3. **Pluralization of already-Pascal names is naive**: `"UserData"` →
   descriptor `UserDatas` (pass_builders.go `goTableNames`). Ugly but
   deterministic and collision-free.
4. **Mixed-case file names**: `billing_TaxRate.gen.go`, `UserData.gen.go` —
   generated file stems use the raw table name (`goTableNames` `fb`), so the
   output tree mixes casing; harmless on Linux, awkward on case-insensitive
   filesystems.
5. **snake_case PK param names pass through**: `GetBillingInvoice(…,
   invoice_number string)` — `goParamName` (pass_statics.go:126–134) only
   sanitizes Go keywords; valid-but-un-Go-like identifiers are kept verbatim.
6. **Dropped `pgb:*` tokens are silent**: a comment like
   `'pgb:no_patch on qty'` (directive + trailing prose, no `;` split) matches
   no directive case and is discarded without warning (gen/sqlcat.go
   `parseDirectives`). A typo'd directive (`pgb:no_filtre`) is likewise
   silently ignored.

## Verification (verbatim)

```
$ go build ./testdata/edge/gen/...          # exit 0, no output
$ go vet ./testdata/edge/gen/...            # exit 0, no output
$ gofmt -l testdata/edge/gen/               # clean
$ sqlc generate -f .pgb-plugin-gen.yaml     # exit 0, 24 files in gen/
$ go test ./core ./gen
ok  	github.com/khoale-2804/pgb/core	(cached)
ok  	github.com/khoale-2804/pgb/gen	0.851s
```
