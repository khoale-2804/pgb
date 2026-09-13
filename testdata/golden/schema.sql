-- ============================================================================
-- pgb golden fixture — canonical all-cases schema
--
-- Every statement here is real PostgreSQL 18 DDL. The fixture feeds:
--   * golden generated-code tests (both editions must produce identical output)
--   * the fork-parity test against upstream sqlc-gen-go
--   * the docs examples (guides reference these tables by name)
--
-- Sections:
--   A  enum types, domains, composite types        F  temporal constraint
--   B  users — scalar type families                G  partitioned table
--   C  products — pg_search index family           H  second schema (app)
--   D  orders — composite PK, identity, ranges     I  views + matview
--   E  order_items — composite FK, deferrable      J  self-FK, unlogged, seq
--   K  keyword / case / unicode identifiers        L  pgb:skip directive
--   M  docs — second pg_search shape               N  identity variants
--
-- Extensions required (provided by the pgb test images):
--   pg_search (paradedb), vector, citext, btree_gist, pg_trgm
-- ============================================================================

CREATE EXTENSION IF NOT EXISTS pg_search;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ============================================================================
-- A. enum types, domains, composite types
-- ============================================================================

CREATE TYPE user_status AS ENUM ('active', 'inactive', 'banned', 'password reset');
CREATE TYPE order_channel AS ENUM ('web', 'mobile', 'phone', 'store');

CREATE DOMAIN email_addr AS text
  CONSTRAINT email_format CHECK (VALUE ~ '^[^@[:space:]]+@[^@[:space:]]+$');

CREATE DOMAIN positive_int AS int4
  CONSTRAINT positive CHECK (VALUE > 0);

CREATE TYPE address_t AS (
  street      text,
  city        text,
  postal_code text,
  country     text
);

-- ============================================================================
-- B. users — one column per scalar type family, defaults, directives,
--    generated columns (PG18 virtual + stored), collation, soft delete
-- ============================================================================

CREATE TABLE users (
  id            bigserial PRIMARY KEY,                 -- serial family
  email         text        NOT NULL UNIQUE,
  password      text        NOT NULL,                  -- directives below
  name          text        NOT NULL DEFAULT 'unknown',
  bio           text,                                  -- nullable, no default
  age           smallint,
  balance       numeric(12,2) NOT NULL DEFAULT 0,
  rating        real,
  score         double precision NOT NULL DEFAULT 0.0,
  is_active     boolean     NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz,
  birth_date    date,
  last_seen     timetz,
  avatar        bytea,
  metadata      jsonb       NOT NULL DEFAULT '{}',
  settings      json,
  homepage      inet,
  lan           cidr,
  mac           macaddr,
  mac8          macaddr8,
  bitfield      bit(8)      NOT NULL DEFAULT B'00000000',
  flags         varbit(16),
  tags          text[]      NOT NULL DEFAULT '{}',
  scores        int4[]      NOT NULL DEFAULT ARRAY[]::int4[],
  grid          int4[][],                              -- multidim (PG stores int4[])
  homesite      point,
  seg           lseg,
  box_col       box,
  path_col      path,
  poly          polygon,
  circle_col    circle,
  line_col      line,
  tsv           tsvector,
  tsq           tsquery,
  xml_doc       xml,
  log_lsn       pg_lsn,
  last_xid      xid8,
  slot_tid      tid,
  cash          money       NOT NULL DEFAULT 0,
  status        user_status NOT NULL DEFAULT 'active',
  addr          address_t,
  email_domain  email_addr,
  positive      positive_int,
  uuid_col      uuid        NOT NULL DEFAULT gen_random_uuid(),
  login_ci      citext      NOT NULL UNIQUE,
  collate_col   text COLLATE "C",
  search_slug   text GENERATED ALWAYS AS (lower(email)) VIRTUAL,  -- PG18+
  name_upper    text GENERATED ALWAYS AS (upper(name)) STORED,
  deleted_at    timestamptz,                           -- soft-delete recipe
  very_long_column_identifier_exactly_sixty_three_characters_in_len text
);

-- directive coverage: filter surface, patch surface, Go type override
COMMENT ON COLUMN users.password IS 'pgb:no_filter; pgb:no_patch';
COMMENT ON COLUMN users.settings  IS 'pgb:type=map[string]any';

-- plain documentation comment (must be treated as docs, not a directive)
COMMENT ON TABLE  users         IS 'shop customers and staff accounts';
COMMENT ON COLUMN users.metadata IS 'arbitrary profile metadata (jsonb)';

-- index shapes: plain, partial, expression
CREATE INDEX idx_users_created_at   ON users (created_at);
CREATE INDEX idx_users_active_email ON users (email) WHERE is_active;
CREATE INDEX idx_users_email_lower  ON users (lower(email));

-- ============================================================================
-- C. products — pg_search index (tokenizer casts, JSON alias, key_field),
--    trigram fallback, partial / expression / covering (INCLUDE) indexes
-- ============================================================================

CREATE TABLE products (
  id          bigserial PRIMARY KEY,
  sku         text        NOT NULL UNIQUE,
  title       text        NOT NULL,
  description text        NOT NULL,
  category    text        NOT NULL,
  rating      numeric(3,2) CHECK (rating >= 0),
  price       numeric(10,2) NOT NULL CHECK (price > 0),
  in_stock    boolean     NOT NULL DEFAULT true,
  metadata    jsonb       NOT NULL DEFAULT '{}',
  embedding   vector(768),
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX products_search ON products USING paradedb
  (id, (title::pdb.icu), description, ((metadata->'color')::pdb.literal('alias=json_color')),
   rating)
  WITH (key_field='id');

CREATE INDEX products_title_trgm    ON products USING gin (title gin_trgm_ops);
CREATE INDEX products_stock_listing ON products (category, price) WHERE in_stock;
CREATE INDEX products_title_lower   ON products (lower(title));
CREATE INDEX products_category_inc  ON products (category) INCLUDE (price);

COMMENT ON COLUMN products.embedding IS 'pgb:type=github.com/pgvector/pgvector-go.Vector';

-- ============================================================================
-- D. orders — composite primary key, identity column, enum, range +
--    multirange columns, ON DELETE CASCADE
-- ============================================================================

CREATE TABLE orders (
  id          bigint GENERATED ALWAYS AS IDENTITY,
  shop_id     integer     NOT NULL,
  user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel     order_channel NOT NULL,
  total       numeric(12,2) NOT NULL CHECK (total >= 0),
  placed_at   timestamptz NOT NULL DEFAULT now(),
  ship_window daterange,
  schedule    tstzmultirange,
  notes       text,
  PRIMARY KEY (shop_id, id)
);

CREATE INDEX idx_orders_user   ON orders (user_id);
CREATE INDEX idx_orders_placed ON orders (shop_id, placed_at DESC);

-- ============================================================================
-- E. order_items — composite FK, ON DELETE RESTRICT, deferrable constraint
-- ============================================================================

CREATE TABLE order_items (
  order_shop_id integer       NOT NULL,
  order_id      bigint        NOT NULL,
  product_id    bigint        NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
  quantity      int4          NOT NULL CHECK (quantity > 0),
  unit_price    numeric(10,2) NOT NULL,
  gift_wrap     boolean       NOT NULL DEFAULT false,
  PRIMARY KEY (order_shop_id, order_id, product_id),
  FOREIGN KEY (order_shop_id, order_id) REFERENCES orders(shop_id, id)
    ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
);

-- ============================================================================
-- F. shifts — temporal constraint (EXCLUDE USING gist, WITHOUT OVERLAPS shape)
-- ============================================================================

CREATE TABLE shifts (
  id          bigserial PRIMARY KEY,
  employee_id integer   NOT NULL,
  during      tstzrange NOT NULL,
  CONSTRAINT no_double_booking EXCLUDE USING gist
    (employee_id WITH =, during WITH &&)
);

-- ============================================================================
-- G. events — partitioned table (RANGE) with two partitions
-- ============================================================================

CREATE TABLE events (
  id          bigserial,
  user_id     bigint      NOT NULL,
  kind        text        NOT NULL,
  payload     jsonb,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE events_2026h1 PARTITION OF events
  FOR VALUES FROM ('2026-01-01') TO ('2026-07-01');
CREATE TABLE events_2026h2 PARTITION OF events
  FOR VALUES FROM ('2026-07-01') TO ('2027-01-01');

-- ============================================================================
-- H. app schema — second schema, same table name (public.users vs app.users),
--    cross-schema FK, ON DELETE SET NULL
-- ============================================================================

CREATE SCHEMA app;

CREATE TABLE app.users (
  id        bigserial PRIMARY KEY,
  login     text   NOT NULL UNIQUE,
  person_id bigint REFERENCES public.users(id) ON DELETE SET NULL,
  display   text
);

COMMENT ON TABLE app.users IS 'app-schema twin of public.users — qualification edge';

CREATE TABLE app.audit_log (
  id        bigserial PRIMARY KEY,
  entity    text        NOT NULL,
  entity_id bigint      NOT NULL,
  payload   jsonb       NOT NULL,
  at        timestamptz NOT NULL DEFAULT now()
);

-- ============================================================================
-- I. views — catalog must include them; generated readers are read-only
-- ============================================================================

CREATE VIEW active_users AS
  SELECT id, email, name, created_at
  FROM users
  WHERE is_active AND deleted_at IS NULL;

CREATE MATERIALIZED VIEW order_stats AS
  SELECT user_id, count(*) AS order_count, sum(total) AS lifetime_value
  FROM orders
  GROUP BY user_id;

-- ============================================================================
-- J. self-referential FK, unlogged table, standalone sequence default,
--    identity variants (ALWAYS vs BY DEFAULT)
-- ============================================================================

CREATE TABLE categories (
  id        bigserial PRIMARY KEY,
  name      text   NOT NULL,
  parent_id bigint REFERENCES categories(id) ON DELETE SET NULL
);

CREATE UNLOGGED TABLE cache_blob (
  key        text PRIMARY KEY,
  value      bytea,
  expires_at timestamptz NOT NULL
);

CREATE SEQUENCE ticket_number_seq;

CREATE TABLE tickets (
  id         bigserial PRIMARY KEY,
  number     bigint      NOT NULL DEFAULT nextval('ticket_number_seq') UNIQUE,
  subject    text        NOT NULL,
  priority   text        NOT NULL DEFAULT 'normal',
  status     text        NOT NULL DEFAULT 'open',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE inventory (
  item_id    bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  qty        int4        NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- ============================================================================
-- K. keyword / case / unicode / collation identifier edges
--    (generator must sanitize Go-keyword collisions and quote safely)
-- ============================================================================

CREATE TABLE keyword_cols (
  id        bigserial PRIMARY KEY,
  "type"    text,
  "range"   int4,
  "select"  text,
  "class"   text,
  "interface" text,
  "map"     text,
  "func"    text,
  "return"  text,
  "chan"    text
);

CREATE TABLE case_cols (
  id           bigserial PRIMARY KEY,
  "Email"      text,       -- mixed case, quoted (distinct from email)
  mixedCase    int4,       -- unquoted folds to mixedcase
  "café"       text,       -- unicode identifier
  plain_col    text COLLATE "C"
);

-- ============================================================================
-- L. pgb:skip directive — pgb_ignored must produce NO generated artifacts
-- ============================================================================

CREATE TABLE pgb_ignored (
  id  integer PRIMARY KEY,
  why text
);

COMMENT ON TABLE pgb_ignored IS 'pgb:skip';

-- skippable_partner intentionally carries NO pgb:skip comment: it is the
-- identity-BY-DEFAULT coverage table (COVERAGE.md row 3) and must generate.
CREATE TABLE skippable_partner (
  id    bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  label text NOT NULL
);

-- ============================================================================
-- M. docs — second pg_search shape: uuid key_field, edge_ngram tokenizer,
--    aliased double-indexed column, stemming
-- ============================================================================

CREATE TABLE docs (
  id    uuid   NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
  title text   NOT NULL,
  body  text   NOT NULL
);

CREATE INDEX docs_search ON docs USING paradedb
  (id, (title::pdb.edge_ngram(2, 10)), (title::pdb.literal('alias=title_exact')),
   (body::pdb.simple('stemmer=english')))
  WITH (key_field='id');
