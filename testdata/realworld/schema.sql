-- ============================================================================
-- pgb realworld fixture — a realistic multi-tenant e-commerce schema
--
-- Exercises pgb against the shapes a real SaaS product ships, not the
-- kitchen-sink golden fixture: uuid PKs, identity PKs, a composite PK,
-- enums, text[] arrays, jsonb, numeric money, timestamptz everywhere, a
-- stored generated column, a view + materialized view, the full
-- customers -> orders -> order_items -> products FK chain, two pg_search
-- (paradedb) indexes with different tokenizers, and pgb directives.
--
-- Tables (11 generating + 1 skipped): merchants, customers, products,
-- stock_levels, orders, order_items, product_reviews, help_articles,
-- coupons, webhook_endpoints, audit_log (pgb:skip).
--
-- Extensions: vector must be created BEFORE pg_search (pg_search 0.25.9
-- depends on it).
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_search;

-- ============================================================================
-- enums
-- ============================================================================

CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped', 'delivered', 'cancelled', 'refunded');
CREATE TYPE customer_tier AS ENUM ('free', 'standard', 'premium', 'enterprise');

-- ============================================================================
-- merchants — uuid PK, jsonb settings, soft off-boarding
-- ============================================================================

CREATE TABLE merchants (
  id             uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
  slug           text        NOT NULL UNIQUE,
  name           text        NOT NULL,
  support_email  text,
  settings       jsonb       NOT NULL DEFAULT '{}',
  created_at     timestamptz NOT NULL DEFAULT now(),
  deactivated_at timestamptz
);

-- ============================================================================
-- customers — identity PK, enum tier, text[] tags, jsonb address (with a
-- pgb:type directive), soft-delete column
-- ============================================================================

CREATE TABLE customers (
  id               bigint        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  merchant_id      uuid          NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
  email            text          NOT NULL,
  full_name        text          NOT NULL DEFAULT '',
  tier             customer_tier NOT NULL DEFAULT 'standard',
  tags             text[]        NOT NULL DEFAULT '{}',
  shipping_address jsonb,
  loyalty_points   int           NOT NULL DEFAULT 0,
  lifetime_value   numeric(12,2) NOT NULL DEFAULT 0,
  signup_at        timestamptz   NOT NULL DEFAULT now(),
  last_login_at    timestamptz,
  deleted_at       timestamptz,
  UNIQUE (merchant_id, email)
);

-- pgb directive: untyped jsonb document, callers own the shape
COMMENT ON COLUMN customers.shipping_address IS 'pgb:type=map[string]any';

CREATE INDEX idx_customers_merchant_signup ON customers (merchant_id, signup_at DESC);
CREATE INDEX idx_customers_tier            ON customers (tier) WHERE deleted_at IS NULL;

-- ============================================================================
-- products — identity PK, numeric money, jsonb attributes, the default-
-- tokenizer pg_search index with an aliased JSON path
-- ============================================================================

CREATE TABLE products (
  id          bigint        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  merchant_id uuid          NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
  sku         text          NOT NULL,
  title       text          NOT NULL,
  description text          NOT NULL DEFAULT '',
  category    text          NOT NULL,
  price       numeric(10,2) NOT NULL CHECK (price >= 0),
  in_stock    boolean       NOT NULL DEFAULT true,
  attributes  jsonb         NOT NULL DEFAULT '{}',
  tags        text[]        NOT NULL DEFAULT '{}',
  created_at  timestamptz   NOT NULL DEFAULT now(),
  updated_at  timestamptz   NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, sku)
);

-- default tokenizer on title/description/category, alias on a JSON path
CREATE INDEX products_search ON products USING paradedb
  (id, title, description, category, ((attributes->'brand')::pdb.literal('alias=brand')))
  WITH (key_field='id');

CREATE INDEX idx_products_merchant_category ON products (merchant_id, category);

-- ============================================================================
-- stock_levels — composite PK (product, warehouse)
-- ============================================================================

CREATE TABLE stock_levels (
  product_id  bigint      NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  warehouse   text        NOT NULL,
  qty_on_hand int         NOT NULL DEFAULT 0 CHECK (qty_on_hand >= 0),
  restock_at  timestamptz,
  PRIMARY KEY (product_id, warehouse)
);

-- ============================================================================
-- orders — identity PK, enum status, money totals, text[] promo codes;
-- FK chain leg 1: customers -> orders
-- ============================================================================

CREATE TABLE orders (
  id          bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  merchant_id uuid         NOT NULL REFERENCES merchants(id) ON DELETE RESTRICT,
  customer_id bigint       NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
  status      order_status NOT NULL DEFAULT 'pending',
  currency    text         NOT NULL DEFAULT 'USD',
  subtotal    numeric(12,2) NOT NULL DEFAULT 0,
  tax_total   numeric(12,2) NOT NULL DEFAULT 0,
  grand_total numeric(12,2) NOT NULL DEFAULT 0,
  promo_codes text[]       NOT NULL DEFAULT '{}',
  placed_at   timestamptz  NOT NULL DEFAULT now(),
  shipped_at  timestamptz,
  notes       text
);

CREATE INDEX idx_orders_customer ON orders (customer_id);
CREATE INDEX idx_orders_merchant_placed ON orders (merchant_id, placed_at DESC);

-- ============================================================================
-- order_items — composite PK (order, line), composite... single FK legs to
-- orders and products (chain leg 2), stored generated money column
-- ============================================================================

CREATE TABLE order_items (
  order_id    bigint        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  line_number int           NOT NULL,
  product_id  bigint        NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
  quantity    int           NOT NULL CHECK (quantity > 0),
  unit_price  numeric(10,2) NOT NULL,
  line_total  numeric(12,2) GENERATED ALWAYS AS (quantity * unit_price) STORED,
  PRIMARY KEY (order_id, line_number)
);

CREATE INDEX idx_order_items_product ON order_items (product_id);

-- ============================================================================
-- product_reviews — uuid PK again, rating bounds, FK to both sides
-- ============================================================================

CREATE TABLE product_reviews (
  id          uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
  product_id  bigint      NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  customer_id bigint      REFERENCES customers(id) ON DELETE SET NULL,
  rating      smallint    NOT NULL CHECK (rating BETWEEN 1 AND 5),
  title       text        NOT NULL DEFAULT '',
  body        text        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- ============================================================================
-- help_articles — uuid PK; second pg_search index, different tokenizers:
-- edge_ngram with an explicit typmod on the title (autocomplete shape),
-- simple+stemming on the body
-- ============================================================================

CREATE TABLE help_articles (
  id          uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
  slug        text        NOT NULL UNIQUE,
  title       text        NOT NULL,
  body        text        NOT NULL,
  tags        text[]      NOT NULL DEFAULT '{}',
  published   boolean     NOT NULL DEFAULT false,
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX help_articles_search ON help_articles USING paradedb
  (id, (title::pdb.edge_ngram(2, 10)), (body::pdb.simple('stemmer=english')))
  WITH (key_field='id');

-- ============================================================================
-- coupons — natural text PK
-- ============================================================================

CREATE TABLE coupons (
  code            text          PRIMARY KEY,
  percent_off     numeric(5,2)  NOT NULL CHECK (percent_off > 0 AND percent_off <= 100),
  max_redemptions int,
  times_redeemed  int           NOT NULL DEFAULT 0,
  expires_at      timestamptz,
  active          boolean       NOT NULL DEFAULT true
);

-- ============================================================================
-- webhook_endpoints — uuid PK, text[] event names; secret carries
-- no_filter/no_patch directives (never selectable through the filter
-- surface, never writable through patch sets)
-- ============================================================================

CREATE TABLE webhook_endpoints (
  id          uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
  merchant_id uuid        NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
  url         text        NOT NULL,
  secret      text        NOT NULL,
  events      text[]      NOT NULL DEFAULT '{order.created,order.paid}',
  active      boolean     NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN webhook_endpoints.secret IS 'pgb:no_filter; pgb:no_patch';

-- ============================================================================
-- audit_log — write-only compliance table; pgb:skip so pgb emits nothing
-- ============================================================================

CREATE TABLE audit_log (
  id       bigint      GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  entity   text        NOT NULL,
  entity_id bigint,
  action   text        NOT NULL,
  payload  jsonb       NOT NULL DEFAULT '{}',
  at       timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE audit_log IS 'pgb:skip';

-- ============================================================================
-- views — read-only generated readers
-- ============================================================================

CREATE VIEW customer_overview AS
  SELECT c.id,
         c.merchant_id,
         c.email,
         c.full_name,
         c.tier,
         count(o.id)                                AS order_count,
         coalesce(sum(o.grand_total), 0)::numeric(14,2) AS total_spent
  FROM customers c
  LEFT JOIN orders o ON o.customer_id = c.id
  GROUP BY c.id;

CREATE MATERIALIZED VIEW product_sales AS
  SELECT p.id                        AS product_id,
         p.merchant_id,
         p.title,
         -- explicit cast: an untyped coalesce(sum(...), 0) makes sqlc (and
         -- therefore pgb) fall back to `any` for the column's Go type
         coalesce(sum(oi.quantity), 0)::bigint      AS units_sold,
         coalesce(sum(oi.line_total), 0)::numeric(14,2) AS revenue
  FROM products p
  LEFT JOIN order_items oi ON oi.product_id = p.id
  GROUP BY p.id, p.merchant_id, p.title;
