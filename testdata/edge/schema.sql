-- ============================================================================
-- pgb edge fixture — hostile-but-legal PostgreSQL DDL (compile-only)
--
-- Every item here is valid PostgreSQL 18 DDL a real project could ship. The
-- fixture exists to break codegen assumptions: reserved words, mixed-case
-- quoted identifiers, missing primary keys, non-public schemas, 63-character
-- identifiers, and degenerate table shapes. No extensions required.
--
-- Item map (referenced from NOTES.md):
--   E1  reserved-word table "order"            E7  interval/inet/bytea/uuid/
--   E2  reserved-word table "user"                 timestamp types
--   E3  reserved-word columns "select"/      E8  non-public schema `billing`
--       "where"/"order"                          (2 tables)
--   E4  mixed-case quoted identifiers        E9  63-character identifiers
--   E5  no-PK table, only-nullable-text      E10 column `id` that is NOT
--       table, one-column table                  the PK
--   E6  pgb:no_filter on a quoted column     E11 ALTER TABLE ADD CONSTRAINT
--       (COMMENT ON, via the DDL pass)           (DDL-pass support probe —
--                                                see NOTES.md E11)
-- ============================================================================

-- E1: `order` is a fully reserved SQL word; every reference must re-quote.
-- Also carries an E3 column ("where") and a server-default column.
CREATE TABLE "order" (
  "select"  text,
  qty       int4 NOT NULL DEFAULT 1,
  "where"   date,
  placed_at timestamptz NOT NULL DEFAULT now()
);

-- E2: `user` is reserved (and the classic footgun). E3: three reserved-word
-- columns plus an ordinary one.
CREATE TABLE "user" (
  "select" text NOT NULL,
  "where"  timestamptz NOT NULL DEFAULT now(),
  "order"  numeric(12,2),
  email    text
);

-- E4: mixed-case quoted identifiers — the catalog keeps the exact case, so
-- every reference must re-quote or miss.
CREATE TABLE "UserData" (
  "ColumnName" text NOT NULL,
  "ID"         uuid NOT NULL DEFAULT gen_random_uuid(),
  PRIMARY KEY ("ID")
);

-- E5a: no-PK table (no PRIMARY KEY, no UNIQUE anywhere).
CREATE TABLE loose (
  body    text,
  qty     int4,
  created timestamptz
);

-- E5b: only-nullable-text columns (implies no-PK; every value may be NULL).
CREATE TABLE drafts (
  a text,
  b text
);

-- E5c: one-column table.
CREATE TABLE singleton (
  value text
);

-- E7: the type menagerie.
CREATE TABLE timers (
  id      uuid NOT NULL DEFAULT gen_random_uuid(),
  span    interval NOT NULL,
  addr    inet,
  payload bytea,
  at      timestamp,
  atz     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id)
);

-- E9: identifiers at PostgreSQL's 63-byte name limit (both would be silently
-- truncated if one byte longer).
CREATE TABLE sales_summary_report_with_twelve_months_of_aggregated_line_tota (
  id                                                               int4 NOT NULL,
  cumulative_gross_revenue_in_minor_units_including_tax_and_adjus  numeric,
  PRIMARY KEY (id)
);

-- E10: a column named `id` that is NOT the primary key — the "heuristic PK"
-- fallback must not latch onto it (the real PK is `code`).
CREATE TABLE weird_pk (
  id    int4 NOT NULL,
  code  text NOT NULL,
  label text,
  PRIMARY KEY (code)
);

-- ============================================================================
-- E8: non-public schema. Two tables, one plain, one with a mixed-case quoted
-- name so the schema prefix and quoting must compose.
-- ============================================================================

CREATE SCHEMA billing;

CREATE TABLE billing.invoices (
  invoice_number text NOT NULL,
  amount         numeric(12,2) NOT NULL,
  customer_email text,
  issued_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (invoice_number)
);

CREATE TABLE billing."TaxRate" (
  "RateID"  int4 NOT NULL,
  "Percent" numeric(5,4) NOT NULL,
  PRIMARY KEY ("RateID")
);

-- ============================================================================
-- E6: pgb directives via COMMENT ON, targeting quoted identifiers — the DDL
-- pass must resolve "user"."select" through the quoting.
-- ============================================================================

COMMENT ON COLUMN "user"."select" IS 'pgb:no_filter';
COMMENT ON COLUMN "order"."where" IS 'pgb:no_patch';
COMMENT ON TABLE "order" IS 'hostile reserved-word table; plain comment text';
