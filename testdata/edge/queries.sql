-- pgb edge fixture — queries over hostile names.
-- sqlc must parse and type-check these before the pgb plugin ever runs, so
-- every query re-quotes the hostile identifiers exactly.

-- name: OrderQtyAbove :many
SELECT * FROM "order" WHERE qty > $1 ORDER BY placed_at DESC;

-- name: UserBySelect :one
SELECT "select", "where", "order", email FROM "user" WHERE "select" = $1;

-- name: UserDataByID :one
SELECT "ColumnName", "ID" FROM "UserData" WHERE "ID" = $1;

-- name: LooseScan :many
SELECT body, qty, created FROM loose;

-- name: DraftsAll :many
SELECT a, b FROM drafts;

-- name: SingletonValue :one
SELECT value FROM singleton;

-- name: TimerSpanAt :many
SELECT id, span, addr, payload, at, atz FROM timers WHERE atz > $1;

-- name: LongReportByID :one
SELECT id, cumulative_gross_revenue_in_minor_units_including_tax_and_adjus
FROM sales_summary_report_with_twelve_months_of_aggregated_line_tota
WHERE id = $1;

-- name: WeirdPkByCode :one
SELECT id, code, label FROM weird_pk WHERE code = $1;

-- name: BillingInvoiceByNumber :one
SELECT * FROM billing.invoices WHERE invoice_number = $1;

-- name: BillingTaxRates :many
SELECT "RateID", "Percent" FROM billing."TaxRate" ORDER BY "Percent" DESC;
