//go:build integration

// Package integration executes the generated data layer against a real
// ParadeDB server (docker compose up -d). Run via `make integration`.
package integration

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	pgb "github.com/khoale-2804/pgb/core"
	db "github.com/khoale-2804/pgb/testdata/golden/gen-plugin"
)

const dsnDefault = "postgres://postgres:postgres@localhost:15432/postgres?sslmode=disable"

func dsn() string {
	if v := os.Getenv("PGB_TEST_DSN"); v != "" {
		return v
	}
	return dsnDefault
}

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	resetSchema(t)
	return newPool(t)
}

// resetSchema re-applies the golden fixture from scratch on a throwaway
// connection. schema.sql is not idempotent, so public (types, tables,
// sequences, views) and app are dropped first. Dropping public also
// cascade-drops the extensions installed in it, and pg_search 0.25.9
// depends on vector — so the extensions are recreated with vector first.
func resetSchema(t *testing.T) {
	t.Helper()
	sql, err := os.ReadFile("../golden/schema.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	conn, err := pgx.Connect(context.Background(), dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	const reset = `
DROP SCHEMA IF EXISTS public CASCADE;
DROP SCHEMA IF EXISTS app CASCADE;
CREATE SCHEMA public;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_search;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
`
	if _, err := conn.Exec(context.Background(), reset); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn())
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	// Register pgvector's codecs so embedding params (and []...Vector batch
	// arrays) encode in binary instead of failing with "cannot find encode
	// plan" for the unknown OID.
	//
	// money has no pgx codec; pgb's stock mapping is money → pgtype.Numeric
	// (COVERAGE.md "Empirical findings"), which scans server-side money text
	// ("$0.00") as "not a number". Register a bridge codec: encode keeps the
	// numeric text shape money accepts, decode strips currency formatting.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
			return err
		}
		var moneyOID *uint32
		if err := conn.QueryRow(ctx, "SELECT to_regtype('money')::oid").Scan(&moneyOID); err != nil {
			return err
		}
		if moneyOID != nil {
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name: "money", OID: *moneyOID, Codec: moneyCodec{},
			})
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	q := "TRUNCATE "
	for i, tb := range tables {
		if i > 0 {
			q += ", "
		}
		q += tb
	}
	q += " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(context.Background(), q); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("numeric scan %q: %v", s, err)
	}
	return n
}

// moneyCodec bridges PostgreSQL money to pgtype.Numeric (pgb's stock
// mapping). Encoding reuses the numeric text shape money parses; decoding
// strips the currency symbol and thousands separators Numeric rejects.
type moneyCodec struct{}

func (moneyCodec) FormatSupported(format int16) bool {
	return format == pgtype.TextFormatCode
}

func (moneyCodec) PreferredFormat() int16 { return pgtype.TextFormatCode }

func (moneyCodec) PlanEncode(m *pgtype.Map, oid uint32, format int16, value any) pgtype.EncodePlan {
	return pgtype.NumericCodec{}.PlanEncode(m, oid, format, value)
}

func (moneyCodec) PlanScan(m *pgtype.Map, oid uint32, format int16, target any) pgtype.ScanPlan {
	if _, ok := target.(*pgtype.Numeric); ok {
		return scanPlanMoneyToNumeric{}
	}
	return nil
}

func (c moneyCodec) DecodeDatabaseSQLValue(m *pgtype.Map, oid uint32, format int16, src []byte) (driver.Value, error) {
	if src == nil {
		return nil, nil
	}
	var n pgtype.Numeric
	if err := (&scanPlanMoneyToNumeric{}).Scan(src, &n); err != nil {
		return nil, err
	}
	return n.Value()
}

func (c moneyCodec) DecodeValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	if src == nil {
		return nil, nil
	}
	var n pgtype.Numeric
	err := (&scanPlanMoneyToNumeric{}).Scan(src, &n)
	return n, err
}

type scanPlanMoneyToNumeric struct{}

func (scanPlanMoneyToNumeric) Scan(src []byte, dst any) error {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r == '.', r == '-', r == '+':
			return r
		default:
			return -1 // currency symbols, thousands separators
		}
	}, string(src))
	return dst.(*pgtype.Numeric).Scan(s)
}

// tsnow supplies the NOT NULL timestamptz columns: pgb inserts bind every
// settable column (only serial/identity/generated are omitted from params),
// so a Go zero pgtype.Timestamptz would reach the server as NULL.
func tsnow() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

// vec768 matches the fixture's typed embedding column vector(768); the
// server rejects lower-dimensional vectors.
func vec768(fill float32) pgvector.Vector {
	x := make([]float32, 768)
	for i := range x {
		x[i] = fill
	}
	return pgvector.NewVector(x)
}

// newUserParams fills every NOT NULL column of users. Only id (bigserial),
// search_slug/name_upper (generated) are omitted by the generator; columns
// with defaults are still bound, so their Go zero values must be valid.
func newUserParams(t *testing.T, email string) db.InsertUserParams {
	return db.InsertUserParams{
		Email:     email,
		Password:  "hunter2",
		Name:      "Ada",
		Balance:   num(t, "0"),
		IsActive:  true,
		CreatedAt: tsnow(),
		Metadata:  []byte("{}"),
		Bitfield:  pgtype.Bits{Bytes: []byte{0}, Len: 8, Valid: true},
		Tags:      []string{},
		Scores:    []int32{},
		Cash:      num(t, "0"),
		Status:    db.UserStatusActive,
		LoginCi:   email,
	}
}

// newProductParams fills every NOT NULL column of products; the batch static
// feeds unnest arrays, so embedding must be a valid vector(768) per row.
func newProductParams(t *testing.T, sku, title, description, category string) db.InsertProductParams {
	return db.InsertProductParams{
		Sku:         sku,
		Title:       title,
		Description: description,
		Category:    category,
		Price:       num(t, "9.99"),
		InStock:     true,
		Metadata:    []byte("{}"),
		Embedding:   vec768(0.1),
		CreatedAt:   tsnow(),
	}
}

func TestUserCRUD(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "users")

	// insert: identity + generated columns omitted, everything else bound
	u, err := db.InsertUser(context.Background(), pool, newUserParams(t, "ada@example.com"))
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if u.ID == 0 || u.CreatedAt.Time.IsZero() {
		t.Fatalf("expected id + created_at via RETURNING, got %+v", u)
	}
	if u.SearchSlug.String != "ada@example.com" {
		t.Fatalf("virtual generated column: got %q", u.SearchSlug.String)
	}

	// get + ErrNotFound
	got, err := db.GetUser(context.Background(), pool, u.ID)
	if err != nil || got.Email != u.Email {
		t.Fatalf("GetUser: %v %+v", err, got)
	}
	if _, err := db.GetUser(context.Background(), pool, 999999); !errors.Is(err, pgb.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// list with typed filter
	users, err := db.ListUsers(context.Background(), pool, db.UserFilter{
		EmailLike: db.Opt("%@example.com"),
	}, pgb.ListOpt{Limit: 10})
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers: %v %+v", err, users)
	}

	// update with three-state sets: write email, NULL the bio, skip the rest
	up, err := db.UpdateUser(context.Background(), pool, u.ID, db.UserSet{
		Email: db.SetOf("new@example.com"),
		Bio:   db.SetNull[pgtype.Text](),
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if up.Email != "new@example.com" || up.Bio.Valid {
		t.Fatalf("update semantics: %+v", up)
	}

	// upsert on the real primary key
	upsert := newUserParams(t, "new@example.com")
	upsert.Name = "Ada II"
	up2, err := db.UpsertUser(context.Background(), pool, u.ID, upsert)
	if err != nil || up2.Name != "Ada II" {
		t.Fatalf("UpsertUser: %v %+v", err, up2)
	}

	// delete + ErrNotFound afterwards
	if err := db.DeleteUser(context.Background(), pool, u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := db.GetUser(context.Background(), pool, u.ID); !errors.Is(err, pgb.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

// TestOrderItemsBatchAndCount exercises the unnest batch lane on order_items,
// the fixture's only all-scalar insertable table: products (jsonb/vector) and
// users (text[]/enum) correctly get no batch static — array-of-array columns
// have no pgx codec (42804).
func TestOrderItemsBatchAndCount(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "users", "products", "orders", "order_items")

	u, err := db.InsertUser(context.Background(), pool, newUserParams(t, "buyer@example.com"))
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// the composite PK (order_shop_id, order_id, product_id) forces distinct
	// combos: 5 orders x 5 products = 25 batch rows
	var products []db.Product
	for i := 0; i < 5; i++ {
		p, err := db.InsertProduct(context.Background(), pool, newProductParams(t,
			fmt.Sprintf("batch-%d", i), fmt.Sprintf("Batch product %d", i), "batch test product", "batch"))
		if err != nil {
			t.Fatalf("seed product %d: %v", i, err)
		}
		products = append(products, p)
	}
	var orders []db.Order
	for i := 0; i < 5; i++ {
		o, err := db.InsertOrder(context.Background(), pool, db.InsertOrderParams{
			ShopID:   1,
			UserID:   u.ID,
			Channel:  db.OrderChannelWeb,
			Total:    num(t, "29.97"),
			PlacedAt: tsnow(),
		})
		if err != nil {
			t.Fatalf("seed order %d: %v", i, err)
		}
		orders = append(orders, o)
	}

	var params []db.InsertOrderItemParams
	for _, o := range orders {
		for _, p := range products {
			params = append(params, db.InsertOrderItemParams{
				OrderShopID: 1,
				OrderID:     o.ID,
				ProductID:   p.ID,
				Quantity:    1,
				UnitPrice:   num(t, "9.99"),
				GiftWrap:    false,
			})
		}
	}
	inserted, err := db.InsertOrderItems(context.Background(), pool, params)
	if err != nil || len(inserted) != 25 {
		t.Fatalf("InsertOrderItems: %v (%d rows)", err, len(inserted))
	}

	n, err := db.CountOrderItems(context.Background(), pool, db.OrderItemFilter{
		ProductID: db.Opt(products[0].ID),
	})
	if err != nil || n != 5 {
		t.Fatalf("CountOrderItems: %v (%d)", err, n)
	}
}

func TestUpdateProductsErrNoWhere(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "products")

	// empty where []pgb.Expr must be refused by core, not touch the table
	if _, err := db.UpdateProducts(context.Background(), pool, nil, db.ProductSet{
		Title: db.SetOf("nope"),
	}); !errors.Is(err, pgb.ErrNoWhere) {
		t.Fatalf("want ErrNoWhere, got %v", err)
	}
}

func TestUniqueViolation(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "users")

	if _, err := db.InsertUser(context.Background(), pool, newUserParams(t, "dup@example.com")); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := db.InsertUser(context.Background(), pool, newUserParams(t, "dup@example.com"))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("want unique violation 23505, got %v", err)
	}
}

func TestSearch(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "products")

	shoe := newProductParams(t, "shoe-1", "Running Shoes", "light running shoes for trails", "shoes")
	shoe.Price = num(t, "89.90")
	hat := newProductParams(t, "hat-1", "Sun Hat", "wide brim summer hat", "hats")
	hat.Price = num(t, "19.00")
	hat.InStock = false
	if _, err := db.InsertProduct(context.Background(), pool, shoe); err != nil {
		t.Fatalf("InsertProduct shoe: %v", err)
	}
	if _, err := db.InsertProduct(context.Background(), pool, hat); err != nil {
		t.Fatalf("InsertProduct hat: %v", err)
	}

	// BM25 search via the generated static: parse + score + snippet
	hits, err := db.SearchProducts(context.Background(), pool, "running shoes",
		db.SearchProductsOpts{Limit: 10, SnippetCol: "description"})
	if err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected BM25 hits")
	}
	for _, h := range hits {
		if h.Score <= 0 {
			t.Fatalf("expected positive score, got %+v", h)
		}
	}
	// the running-shoes product must outrank the hat for this query
	if hits[0].Product.Sku != "shoe-1" {
		t.Fatalf("expected shoe-1 first, got %+v", hits[0].Product)
	}
	if !hits[0].Snippet.Valid || hits[0].Snippet.String == "" {
		t.Fatalf("expected a snippet, got %+v", hits[0].Snippet)
	}
}

func TestVectorRoundtrip(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "products")

	x := make([]float32, 768)
	x[0], x[1], x[2] = 0.1, 0.2, 0.3
	vec := pgvector.NewVector(x)
	params := newProductParams(t, "vec-1", "Vector Product", "embedding test", "test")
	params.Price = num(t, "1.00")
	params.Embedding = vec
	p, err := db.InsertProduct(context.Background(), pool, params)
	if err != nil {
		t.Fatalf("InsertProduct with embedding: %v", err)
	}
	got, err := db.GetProduct(context.Background(), pool, p.ID)
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if got.Embedding.String() != vec.String() {
		t.Fatalf("embedding roundtrip mismatch")
	}
}

func TestOrdersAndCompositeKeys(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "users", "orders")

	u, err := db.InsertUser(context.Background(), pool, newUserParams(t, "orderer@example.com"))
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// composite-PK table: single insert with RETURNING works, key-based
	// statics are correctly absent
	o, err := db.InsertOrder(context.Background(), pool, db.InsertOrderParams{
		ShopID:   1,
		UserID:   u.ID,
		Channel:  db.OrderChannelWeb,
		Total:    num(t, "42.50"),
		PlacedAt: tsnow(),
	})
	if err != nil {
		t.Fatalf("InsertOrder (composite PK table): %v", err)
	}
	if o.ID == 0 {
		t.Fatalf("expected identity default, got %+v", o)
	}

	orders, err := db.ListOrders(context.Background(), pool, db.OrderFilter{
		ShopID: db.Opt(int32(1)),
	}, pgb.ListOpt{Limit: 5})
	if err != nil || len(orders) != 1 {
		t.Fatalf("ListOrders: %v %+v", err, orders)
	}

	n, err := db.DeleteOrders(context.Background(), pool, []pgb.Expr{
		db.Orders.UserID().Eq(u.ID),
	})
	if err != nil || n != 1 {
		t.Fatalf("DeleteOrders: %v (%d)", err, n)
	}
}

// countWhere wraps a builder Select carrying the given predicates in a
// COUNT(*) subquery — every emitted predicate shape gets executed by the
// real server, so broken SQL (not just broken Go) fails the suite.
func countWhere(t *testing.T, pool *pgxpool.Pool, preds ...pgb.Expr) int64 {
	t.Helper()
	sql, args := db.Users.Select().Where(preds...).SQL()
	var n int64
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM ("+sql+") AS p", args...).Scan(&n); err != nil {
		t.Fatalf("predicate query: %v\nSQL: %s", err, sql)
	}
	return n
}

// TestEmittedPredicateShapes executes the generated predicate methods against
// pg_search 0.25.9: the Go-level snapshot tests pin Go shapes, only this test
// pins that the SHAPES ARE VALID SQL.
func TestEmittedPredicateShapes(t *testing.T) {
	pool := setup(t)
	truncate(t, pool, "users")

	a := newUserParams(t, "a@example.com")
	a.Metadata = []byte(`{"color":"red"}`)
	a.Tags = []string{"vip", "beta"}
	b := newUserParams(t, "b@example.com")
	ua, err := db.InsertUser(context.Background(), pool, a)
	if err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if _, err := db.InsertUser(context.Background(), pool, b); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	cases := []struct {
		name string
		pred pgb.Expr
		want int64
	}{
		{"In text", db.Users.Email().In("a@example.com", "b@example.com", "c@example.com"), 2},
		{"In int", db.Users.ID().In(ua.ID, 999999), 1},
		{"Ne", db.Users.Email().Ne("a@example.com"), 1},
		{"ILike", db.Users.Email().ILike("%@EXAMPLE.COM"), 2},
		{"Like", db.Users.Email().Like("a@%"), 1},
		{"Gt/Lte pair", db.Users.ID().Gt(0), 2},
		{"IsNull", db.Users.Bio().IsNull(), 2},
		{"NotNull", db.Users.Bio().NotNull(), 0},
		{"KeyEq jsonb path", db.Users.Metadata().KeyEq("color", "red"), 1},
		{"Contains array", db.Users.Tags().Contains([]string{"vip"}), 1},
		{"Between", db.Users.CreatedAt().Between(
			pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
			pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countWhere(t, pool, tc.pred); got != tc.want {
				t.Fatalf("got %d rows, want %d", got, tc.want)
			}
		})
	}
}
