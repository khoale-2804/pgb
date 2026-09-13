-- ============================================================================
-- pgb realworld fixture — handwritten named queries
--
-- The pgb plugin generates CRUD + search statics from the schema alone
-- (pass B/C + paradedb index statics), so these named queries are not the
-- codegen source — they are the sqlc parse/analyze acceptance set for a
-- realistic workload: joins across the FK chain, aggregates, filters,
-- batch inserts, ILIKE search, upserts. The BM25 search flows ship as
-- generated statics (SearchProducts / SearchHelpArticles) and need no
-- handwritten SQL.
-- ============================================================================

-- name: GetMerchantByID :one
SELECT * FROM merchants WHERE id = $1;

-- name: CreateMerchant :one
INSERT INTO merchants (slug, name, support_email, settings)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListCustomersByTier :many
SELECT id, email, full_name, tier, loyalty_points, signup_at
FROM customers
WHERE merchant_id = $1 AND tier = $2 AND deleted_at IS NULL
ORDER BY signup_at DESC
LIMIT $3;

-- name: SearchCustomersByName :many
SELECT id, email, full_name, tier
FROM customers
WHERE merchant_id = $1 AND full_name ILIKE '%' || $2 || '%'
ORDER BY full_name
LIMIT $3;

-- name: UpsertCustomer :one
INSERT INTO customers (merchant_id, email, full_name, tags)
VALUES ($1, $2, $3, $4)
ON CONFLICT (merchant_id, email) DO UPDATE
  SET full_name = EXCLUDED.full_name
RETURNING *;

-- name: AwardLoyaltyPoints :exec
UPDATE customers
SET loyalty_points = loyalty_points + $2
WHERE merchant_id = $1 AND tier = $3 AND deleted_at IS NULL;

-- name: BatchInsertProducts :batchexec
INSERT INTO products (merchant_id, sku, title, description, category, price)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ProductsTaggedWithAny :many
SELECT id, sku, title, tags
FROM products
WHERE merchant_id = $1 AND tags && $2::text[]
ORDER BY id;

-- name: UpdateProductPrice :exec
UPDATE products SET price = $3, updated_at = now()
WHERE merchant_id = $1 AND sku = $2;

-- name: GetProductWithMerchant :one
SELECT p.id, p.sku, p.title, p.price, m.slug AS merchant_slug, m.support_email
FROM products p
JOIN merchants m ON m.id = p.merchant_id
WHERE p.id = $1;

-- name: CreateOrder :one
INSERT INTO orders (merchant_id, customer_id, currency, promo_codes)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: AddOrderLine :one
INSERT INTO order_items (order_id, line_number, product_id, quantity, unit_price)
VALUES ($1, $2, $3, $4, $5)
RETURNING order_id, line_number, product_id, quantity, unit_price, line_total;

-- name: SetOrderTotals :exec
UPDATE orders
SET subtotal = $2, tax_total = $3, grand_total = $4, status = 'paid'
WHERE id = $1;

-- name: CancelAbandonedOrders :execrows
UPDATE orders
SET status = 'cancelled', notes = coalesce(notes, '') || ' [auto-cancelled]'
WHERE status = 'pending' AND placed_at < $1;

-- name: CustomerOrderHistory :many
SELECT o.id, o.status, o.grand_total, o.placed_at,
       oi.line_number, oi.quantity, oi.line_total,
       p.sku, p.title
FROM orders o
JOIN order_items oi ON oi.order_id = o.id
JOIN products p ON p.id = oi.product_id
WHERE o.customer_id = $1
ORDER BY o.placed_at DESC, o.id, oi.line_number;

-- name: RevenueByMonth :many
SELECT date_trunc('month', placed_at) AS month,
       count(*)                       AS order_count,
       sum(grand_total)               AS revenue
FROM orders
WHERE merchant_id = $1 AND status NOT IN ('cancelled', 'refunded')
GROUP BY 1
ORDER BY 1;

-- name: TopSellingProducts :many
SELECT p.id, p.sku, p.title,
       sum(oi.quantity)                AS units_sold,
       coalesce(sum(oi.line_total), 0) AS revenue
FROM order_items oi
JOIN products p ON p.id = oi.product_id
WHERE p.merchant_id = $1
GROUP BY p.id, p.sku, p.title
ORDER BY units_sold DESC, p.id
LIMIT $2;

-- name: OrdersAwaitingShipment :many
SELECT o.id, o.placed_at, c.email, c.full_name,
       count(oi.line_number) AS line_count
FROM orders o
JOIN customers c ON c.id = o.customer_id
LEFT JOIN order_items oi ON oi.order_id = o.id
WHERE o.merchant_id = $1 AND o.status = 'paid'
GROUP BY o.id, o.placed_at, c.email, c.full_name
HAVING count(oi.line_number) > 0
ORDER BY o.placed_at;

-- name: UpsertStockLevel :one
INSERT INTO stock_levels (product_id, warehouse, qty_on_hand)
VALUES ($1, $2, $3)
ON CONFLICT (product_id, warehouse) DO UPDATE
  SET qty_on_hand = stock_levels.qty_on_hand + EXCLUDED.qty_on_hand
RETURNING *;

-- name: AverageRatingByProduct :many
SELECT product_id, avg(rating)::numeric(3,2) AS avg_rating, count(*) AS review_count
FROM product_reviews
GROUP BY product_id
HAVING count(*) >= $1
ORDER BY avg_rating DESC;

-- name: ExpireCoupons :execrows
UPDATE coupons SET active = false
WHERE active AND expires_at IS NOT NULL AND expires_at < now();

-- name: CreateCoupon :one
INSERT INTO coupons (code, percent_off, max_redemptions, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: RegisterWebhook :one
INSERT INTO webhook_endpoints (merchant_id, url, secret, events)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ActiveWebhooksForEvent :many
SELECT id, url, secret
FROM webhook_endpoints
WHERE merchant_id = $1 AND active AND events @> ARRAY[$2::text]
ORDER BY created_at;

-- name: MerchantDashboard :one
SELECT
  (SELECT count(*) FROM customers c WHERE c.merchant_id = $1 AND c.deleted_at IS NULL)                    AS customer_count,
  (SELECT count(*) FROM orders o WHERE o.merchant_id = $1 AND o.status = 'pending')                       AS pending_orders,
  (SELECT coalesce(sum(o.grand_total), 0) FROM orders o WHERE o.merchant_id = $1 AND o.status = 'delivered') AS delivered_revenue;
