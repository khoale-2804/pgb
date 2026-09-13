-- ============================================================================
-- pgb golden fixture — handwritten queries (pass A wrapper generation)
-- Every sqlc annotation that pgb's pass A must render appears exactly once.
-- The pg_search query uses the ::float8 cast convention (see
-- guides/handwritten-queries); the vector query exercises the override.
-- ============================================================================

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListActiveUsers :many
SELECT id, email, name, created_at
FROM users
WHERE is_active AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT $1;

-- name: SearchProducts :many
SELECT id, title, pdb.score(id)::float8 AS score
FROM products
WHERE description ||| $1
ORDER BY pdb.score(id) DESC, id
LIMIT $2;

-- name: CreateUser :one
INSERT INTO users (email, password, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateUserName :exec
UPDATE users SET name = $2 WHERE id = $1;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: CountProductsByCategory :one
SELECT count(*) FROM products WHERE category = $1;

-- name: UpsertProductPrice :one
INSERT INTO products (sku, title, description, category, price)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (sku) DO UPDATE
  SET price = EXCLUDED.price
RETURNING id, sku, price;

-- name: BatchInsertUsers :batchexec
INSERT INTO users (email, password, name) VALUES ($1, $2, $3);

-- name: CopyUsers :copyfrom
INSERT INTO users (email, password, name) VALUES ($1, $2, $3);

-- name: RevenueByMonth :many
SELECT date_trunc('month', placed_at) AS month, sum(total) AS revenue
FROM orders
WHERE shop_id = $1
GROUP BY 1
ORDER BY 1;

-- name: OrdersWithItems :many
SELECT o.id, o.shop_id, u.email, p.title, oi.quantity, oi.unit_price
FROM orders o
JOIN users u ON u.id = o.user_id
JOIN order_items oi ON oi.order_id = o.id AND oi.order_shop_id = o.shop_id
JOIN products p ON p.id = oi.product_id
WHERE o.shop_id = $1
ORDER BY o.placed_at DESC;

-- name: RecentOrdersPerUser :many
SELECT user_id, id, total,
       rank() OVER (PARTITION BY user_id ORDER BY placed_at DESC) AS recency
FROM orders;

-- name: UserOrderStats :many
WITH recent AS (
  SELECT user_id, sum(total) AS recent_total
  FROM orders
  WHERE placed_at >= $1
  GROUP BY user_id
)
SELECT u.email, coalesce(r.recent_total, 0) AS recent_total
FROM users u
LEFT JOIN recent r ON r.user_id = u.id;

-- name: VectorNeighbors :many
SELECT id, title, embedding <=> $1::vector AS distance
FROM products
ORDER BY distance
LIMIT $2;
