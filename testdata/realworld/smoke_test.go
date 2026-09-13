//go:build integration

// Package realworld runs the pgb-generated data layer for the realworld
// fixture (testdata/realworld) against the live ParadeDB container. The
// default DSN targets the dedicated pgb_realworld database so the golden
// integration database stays untouched; override with PGB_TEST_DSN.
//
//	go test -tags integration ./testdata/realworld/ -count=1
package realworld

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	pgb "github.com/khoale-2804/pgb/core"
	db "github.com/khoale-2804/pgb/testdata/realworld/gen"
)

const dsnDefault = "postgres://postgres:postgres@localhost:15432/pgb_realworld?sslmode=disable"

func dsn() string {
	if v := os.Getenv("PGB_TEST_DSN"); v != "" {
		return v
	}
	return dsnDefault
}

// resetSchema re-applies the realworld fixture from scratch. schema.sql is
// not idempotent: public is dropped (which cascade-drops the extensions
// installed in it) and recreated; schema.sql then re-creates vector before
// pg_search, as pg_search 0.25.9 depends on it.
func resetSchema(t *testing.T) {
	t.Helper()
	sql, err := os.ReadFile("schema.sql")
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
CREATE SCHEMA public;
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
	pool, err := pgxpool.New(context.Background(), dsn())
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	resetSchema(t)
	return newPool(t)
}

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("numeric scan %q: %v", s, err)
	}
	return n
}

func numString(t *testing.T, n pgtype.Numeric) string {
	t.Helper()
	v, err := n.Value()
	if err != nil {
		t.Fatalf("numeric value: %v", err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("numeric value: want string, got %T", v)
	}
	return s
}

func tsNow() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// seedMerchant inserts one merchant and returns it.
func seedMerchant(t *testing.T, exec pgb.DBTX) db.Merchant {
	t.Helper()
	m, err := db.InsertMerchant(context.Background(), exec, db.InsertMerchantParams{
		ID:           uuid.New(),
		Slug:         "acme-" + uuid.NewString()[:8],
		Name:         "Acme Co",
		SupportEmail: text("help@acme.test"),
		Settings:     []byte(`{"currency":"USD"}`),
		CreatedAt:    tsNow(),
	})
	if err != nil {
		t.Fatalf("InsertMerchant: %v", err)
	}
	return m
}

// seedCustomer inserts one customer. Tier and SignupAt are set explicitly:
// the generated INSERT lists every non-identity column (pgb only strips
// identity/serial columns), so a zero Tier or timestamp is sent as an
// explicit value and overrides the column's server DEFAULT.
func seedCustomer(t *testing.T, exec pgb.DBTX, merchantID uuid.UUID, email string) db.Customer {
	t.Helper()
	c, err := db.InsertCustomer(context.Background(), exec, db.InsertCustomerParams{
		MerchantID:    merchantID,
		Email:         email,
		FullName:      "User",
		Tier:          db.CustomerTierStandard,
		Tags:          []string{},
		LifetimeValue: num(t, "0"),
		SignupAt:      tsNow(),
	})
	if err != nil {
		t.Fatalf("InsertCustomer %s: %v", email, err)
	}
	return c
}

// seedProduct inserts one product. Single-row statics are used throughout
// the smoke flows because the unnest batch statics are runtime-broken for
// array/jsonb columns (see NOTES.md: the generated unnest casts a text[]
// column to $n::text[], so unnest yields scalar text rows).
func seedProduct(t *testing.T, exec pgb.DBTX, merchantID uuid.UUID, sku, title, description, category, price, attributes string) db.Product {
	t.Helper()
	p, err := db.InsertProduct(context.Background(), exec, db.InsertProductParams{
		MerchantID: merchantID, Sku: sku, Title: title,
		Description: description, Category: category,
		Price: num(t, price), InStock: true,
		Attributes: []byte(attributes), Tags: []string{},
		CreatedAt: tsNow(), UpdatedAt: tsNow(),
	})
	if err != nil {
		t.Fatalf("InsertProduct %s: %v", sku, err)
	}
	return p
}

// TestCrudFKChain walks the customers -> orders -> order_items -> products
// chain through the generated statics: uuid PK roundtrip, identity PK
// return, composite PK inserts, the stored generated money column, enum
// sets, three-state patch semantics (SetOf / SetNull) and the count/filter
// surface.
func TestCrudFKChain(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	m := seedMerchant(t, pool)

	// uuid PK roundtrip
	got, err := db.GetMerchant(ctx, pool, m.ID)
	if err != nil {
		t.Fatalf("GetMerchant: %v", err)
	}
	if got.Slug != m.Slug || got.ID != m.ID {
		t.Fatalf("merchant roundtrip mismatch: %+v vs %+v", got, m)
	}

	c, err := db.InsertCustomer(ctx, pool, db.InsertCustomerParams{
		MerchantID:      m.ID,
		Email:           "ada@acme.test",
		FullName:        "Ada Lovelace",
		Tier:            db.CustomerTierPremium,
		Tags:            []string{"vip", "beta"},
		ShippingAddress: map[string]any{"city": "London", "zip": "E1 6AN"},
		LifetimeValue:   num(t, "0"),
		SignupAt:        tsNow(),
	})
	if err != nil {
		t.Fatalf("InsertCustomer: %v", err)
	}
	if c.ID <= 0 {
		t.Fatalf("identity PK not returned: %+v", c)
	}
	if c.ShippingAddress["city"] != "London" {
		t.Fatalf("jsonb roundtrip mismatch: %+v", c.ShippingAddress)
	}

	grinder := seedProduct(t, pool, m.ID, "SKU-GRIND", "Precision Coffee Grinder",
		"Burr grinder for espresso coffee", "kitchen", "89.90", `{"brand":"breville"}`)
	tea := seedProduct(t, pool, m.ID, "SKU-TEA", "Ceramic Tea Set",
		"Glazed stoneware teapot", "kitchen", "45.00", `{"brand":"kyoto"}`)
	book := seedProduct(t, pool, m.ID, "SKU-BOOK", "Coffee Table Book",
		"Photographs of mountains", "books", "25.00", `{"brand":"folio"}`)
	prods := []db.Product{grinder, tea, book}

	order, err := db.InsertOrder(ctx, pool, db.InsertOrderParams{
		MerchantID: m.ID, CustomerID: c.ID, Status: db.OrderStatusPending,
		Currency: "USD", PromoCodes: []string{"WELCOME10"}, PlacedAt: tsNow(),
		// money columns carry DEFAULT 0 in DDL, but the generated INSERT
		// lists them anyway — a zero pgtype.Numeric is sent as NULL, so
		// every default-bearing numeric must be set explicitly
		Subtotal: num(t, "29.68"), TaxTotal: num(t, "2.29"), GrandTotal: num(t, "31.97"),
	})
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}

	items, err := db.InsertOrderItems(ctx, pool, []db.InsertOrderItemParams{
		{OrderID: order.ID, LineNumber: 1, ProductID: prods[0].ID, Quantity: 2, UnitPrice: num(t, "12.34")},
		{OrderID: order.ID, LineNumber: 2, ProductID: prods[1].ID, Quantity: 1, UnitPrice: num(t, "5.00")},
	})
	if err != nil {
		t.Fatalf("InsertOrderItems: %v", err)
	}
	// line_total is GENERATED ALWAYS AS ... STORED — read back, never written
	if s := numString(t, items[0].LineTotal); s != "24.68" {
		t.Fatalf("generated line_total: want 24.68, got %q", s)
	}
	if s := numString(t, items[1].LineTotal); s != "5.00" {
		t.Fatalf("generated line_total: want 5.00, got %q", s)
	}
	loaded, err := db.ListOrderItems(ctx, pool, db.OrderItemFilter{OrderID: db.Opt(order.ID)})
	if err != nil {
		t.Fatalf("ListOrderItems: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2 order items, got %d", len(loaded))
	}

	// three-state patch: SetOf writes values, SetNull writes NULL, zero
	// fields stay untouched
	if _, err := db.UpdateOrder(ctx, pool, order.ID, db.OrderSet{
		Status:     db.SetOf(db.OrderStatusPaid),
		Subtotal:   db.SetOf(num(t, "29.68")),
		GrandTotal: db.SetOf(num(t, "31.97")),
	}); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}
	if _, err := db.UpdateCustomer(ctx, pool, c.ID, db.CustomerSet{
		LoyaltyPoints: db.SetOf(int32(150)),
		LastLoginAt:   db.SetNull[pgtype.Timestamptz](),
	}); err != nil {
		t.Fatalf("UpdateCustomer: %v", err)
	}

	o, err := db.GetOrder(ctx, pool, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if o.Status != db.OrderStatusPaid {
		t.Fatalf("status: want paid, got %q", o.Status)
	}
	if s := numString(t, o.GrandTotal); s != "31.97" {
		t.Fatalf("grand_total: want 31.97, got %q", s)
	}
	if o.PromoCodes[0] != "WELCOME10" {
		t.Fatalf("promo_codes untouched by patch: %+v", o.PromoCodes)
	}
	c2, err := db.GetCustomer(ctx, pool, c.ID)
	if err != nil {
		t.Fatalf("GetCustomer: %v", err)
	}
	if c2.LoyaltyPoints != 150 {
		t.Fatalf("loyalty_points: want 150, got %d", c2.LoyaltyPoints)
	}
	if c2.LastLoginAt.Valid {
		t.Fatalf("SetNull did not clear last_login_at: %+v", c2.LastLoginAt)
	}

	// filter + count surface
	n, err := db.CountCustomers(ctx, pool, db.CustomerFilter{Tier: db.Opt(db.CustomerTierPremium)})
	if err != nil {
		t.Fatalf("CountCustomers: %v", err)
	}
	if n != 1 {
		t.Fatalf("count premium customers: want 1, got %d", n)
	}

	// delete guard: empty WHERE must fail closed
	if _, err := db.DeleteProducts(ctx, pool, nil); !errors.Is(err, pgb.ErrNoWhere) {
		t.Fatalf("DeleteProducts with empty where: want pgb.ErrNoWhere, got %v", err)
	}
}

// TestBM25SearchStatics exercises the generated pg_search surfaces: the
// scored document query (pdb.parse + pdb.score) over the default-tokenizer
// products index, snippet projection, the edge_ngram autocomplete index on
// help_articles, and BM25 predicates composed through the plain filter's
// Extra escape hatch.
func TestBM25SearchStatics(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	m := seedMerchant(t, pool)

	grinder := seedProduct(t, pool, m.ID, "SKU-GRIND", "Precision Coffee Grinder",
		"Burr grinder for espresso coffee", "kitchen", "89.90", `{}`)
	tea := seedProduct(t, pool, m.ID, "SKU-TEA", "Ceramic Tea Set",
		"Glazed stoneware teapot", "kitchen", "45.00", `{}`)
	book := seedProduct(t, pool, m.ID, "SKU-BOOK", "Coffee Table Book",
		"Photographs of mountains", "books", "25.00", `{}`)
	if _, err := db.InsertHelpArticle(ctx, pool, db.InsertHelpArticleParams{
		ID: uuid.New(), Slug: "refunds", Title: "Refund policy explained",
		Body: "We process refund requests within five business days.",
		Tags: []string{"billing"}, Published: true, UpdatedAt: tsNow(),
	}); err != nil {
		t.Fatalf("InsertHelpArticle: %v", err)
	}

	// (a) scored document query over the default-tokenizer index
	hits, err := db.SearchProducts(ctx, pool, "coffee", db.SearchProductsOpts{Limit: 10})
	if err != nil {
		t.Fatalf("SearchProducts: %v", err)
	}
	skus := map[string]bool{}
	for _, h := range hits {
		if h.Score <= 0 {
			t.Fatalf("expected positive BM25 score, got %v for %+v", h.Score, h.Row)
		}
		skus[h.Row.Sku] = true
	}
	if !skus[grinder.Sku] || !skus[book.Sku] {
		t.Fatalf("BM25 'coffee' should match grinder and book, got %v", skus)
	}
	if skus[tea.Sku] {
		t.Fatalf("BM25 'coffee' must not match the tea set: %v", skus)
	}

	// (b) snippet projection
	sh, err := db.SearchProducts(ctx, pool, "espresso", db.SearchProductsOpts{
		Limit: 5, SnippetCol: "description",
	})
	if err != nil {
		t.Fatalf("SearchProducts with snippet: %v", err)
	}
	if len(sh) != 1 || !sh[0].Snippet.Valid || sh[0].Snippet.String == "" {
		t.Fatalf("expected one snippet hit, got %+v", sh)
	}

	// (c) edge_ngram(2,10) autocomplete index: partial token matches
	ah, err := db.SearchHelpArticles(ctx, pool, "refu", db.SearchHelpArticlesOpts{Limit: 5})
	if err != nil {
		t.Fatalf("SearchHelpArticles: %v", err)
	}
	if len(ah) != 1 || ah[0].Row.Slug != "refunds" {
		t.Fatalf("edge_ngram 'refu' should match the refunds article, got %+v", ah)
	}

	// (d) BM25 predicate composed through the filter surface's Extra
	extra, err := db.ListProducts(ctx, pool, db.ProductFilter{
		Extra: []pgb.Expr{db.Products.Title().Match("coffee")},
	}, pgb.ListOpt{Limit: 10})
	if err != nil {
		t.Fatalf("ListProducts with title match: %v", err)
	}
	if len(extra) != 2 {
		t.Fatalf("title ||| 'coffee' via Extra: want 2 products, got %d", len(extra))
	}
}

// TestCursorAndListOptPagination walks a keyset-pagination flow: opaque
// cursor tokens (pgb.EncodeCursor / pgb.DecodeCursor) carrying the last
// seen id, IDGt keyset predicates through the generated filter, and page
// sizing through pgb.ListOpt. Also covers Limit/Offset windowing.
func TestCursorAndListOptPagination(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	m := seedMerchant(t, pool)

	const total = 7
	for i := 0; i < total; i++ {
		seedCustomer(t, pool, m.ID, "user"+string(rune('a'+i))+"@acme.test")
	}

	// keyset walk: page size 3 over 7 rows -> 3 pages, no dupes, no gaps
	seen := map[int64]bool{}
	var cursor int64
	pages := 0
	for {
		page, err := db.ListCustomers(ctx, pool,
			db.CustomerFilter{IDGt: db.Opt(cursor)},
			pgb.ListOpt{Limit: 3})
		if err != nil {
			t.Fatalf("ListCustomers page %d: %v", pages, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 3 {
			t.Fatalf("ListOpt.Limit ignored: got %d rows", len(page))
		}
		for _, c := range page {
			if seen[c.ID] {
				t.Fatalf("duplicate customer %d across pages", c.ID)
			}
			if c.ID <= cursor {
				t.Fatalf("keyset order violated: %d after %d", c.ID, cursor)
			}
			seen[c.ID] = true
			cursor = c.ID
		}
		pages++
		if pages > total {
			t.Fatalf("pagination does not terminate: %d pages", pages)
		}
	}
	if len(seen) != total {
		t.Fatalf("walked %d rows, want %d", len(seen), total)
	}
	if pages != 3 {
		t.Fatalf("want 3 pages of 3/3/1, got %d", pages)
	}

	// cursor tokens: encode the last id, decode it back
	tok, err := pgb.EncodeCursor([]any{cursor})
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	vals, err := pgb.DecodeCursor(tok)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("cursor payload: want 1 value, got %d", len(vals))
	}
	// JSON numbers decode as float64 — the caller owns the narrowing
	f, ok := vals[0].(float64)
	if !ok || int64(f) != cursor {
		t.Fatalf("cursor roundtrip: want %d, got %#v", cursor, vals[0])
	}
	// a garbage token fails closed
	if _, err := pgb.DecodeCursor("!!!not-base64!!!"); err == nil {
		t.Fatal("DecodeCursor accepted a malformed token")
	}

	// Limit/Offset windowing
	window, err := db.ListCustomers(ctx, pool, db.CustomerFilter{}, pgb.ListOpt{Limit: 3, Offset: 5})
	if err != nil {
		t.Fatalf("ListCustomers window: %v", err)
	}
	if len(window) != 2 {
		t.Fatalf("offset window: want last 2 of 7, got %d", len(window))
	}
}

// TestTransactionAndViews covers pgb.WithTx (rollback on error, commit on
// success), the generated readers for the view and materialized view, and
// count correctness across the matview refresh.
func TestTransactionAndViews(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	m := seedMerchant(t, pool)

	// rollback path: the endpoint insert must not survive the failing tx.
	// (The unnest batch static is avoided — see NOTES.md, text[] columns
	// break its generated unnest casts.)
	_, txErr := pgb.WithTx(ctx, pool, func(tx pgb.DBTX) (db.WebhookEndpoint, error) {
		ep, err := db.InsertWebhookEndpoint(ctx, tx, db.InsertWebhookEndpointParams{
			ID: uuid.New(), MerchantID: m.ID, URL: "https://acme.test/hook",
			Secret: "s3cret", Events: []string{"order.created"}, Active: true,
			CreatedAt: tsNow(),
		})
		if err != nil {
			return db.WebhookEndpoint{}, err
		}
		return ep, errors.New("boom")
	})
	if txErr == nil || txErr.Error() != "boom" {
		t.Fatalf("WithTx: want boom, got %v", txErr)
	}
	eps, err := db.ListWebhookEndpoints(ctx, pool, db.WebhookEndpointFilter{MerchantID: db.Opt(m.ID)})
	if err != nil {
		t.Fatalf("ListWebhookEndpoints: %v", err)
	}
	if len(eps) != 0 {
		t.Fatalf("WithTx did not roll back: %d endpoints survive", len(eps))
	}

	// commit path
	_, err = pgb.WithTx(ctx, pool, func(tx pgb.DBTX) (db.WebhookEndpoint, error) {
		return db.InsertWebhookEndpoint(ctx, tx, db.InsertWebhookEndpointParams{
			ID: uuid.New(), MerchantID: m.ID, URL: "https://acme.test/hook2",
			Secret: "s3cret", Events: []string{"order.paid"}, Active: true,
			CreatedAt: tsNow(),
		})
	})
	if err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}

	// view reader: customer_overview aggregates with zero orders
	ov, err := db.ListCustomerOverviews(ctx, pool, db.CustomerOverviewFilter{}, pgb.ListOpt{Limit: 10})
	if err != nil {
		t.Fatalf("ListCustomerOverviews: %v", err)
	}
	if len(ov) != 0 {
		t.Fatalf("empty customers should give empty overview, got %d", len(ov))
	}
	if _, err := db.InsertCustomer(ctx, pool, db.InsertCustomerParams{
		MerchantID: m.ID, Email: "grace@acme.test", FullName: "Grace H",
		Tier: db.CustomerTierFree, Tags: []string{},
		LifetimeValue: num(t, "0"), SignupAt: tsNow(),
	}); err != nil {
		t.Fatalf("InsertCustomer: %v", err)
	}
	ov, err = db.ListCustomerOverviews(ctx, pool, db.CustomerOverviewFilter{}, pgb.ListOpt{Limit: 10})
	if err != nil {
		t.Fatalf("ListCustomerOverviews: %v", err)
	}
	if len(ov) != 1 || ov[0].OrderCount != 0 {
		t.Fatalf("overview: want 1 row with 0 orders, got %+v", ov)
	}

	// materialized view reader + refresh
	p, err := db.InsertProduct(ctx, pool, db.InsertProductParams{
		MerchantID: m.ID, Sku: "SKU-MV", Title: "Matview Widget",
		Description: "widget", Category: "misc",
		Price: num(t, "3.00"), InStock: true, Attributes: []byte(`{}`),
		Tags: []string{}, CreatedAt: tsNow(), UpdatedAt: tsNow(),
	})
	if err != nil {
		t.Fatalf("InsertProduct: %v", err)
	}
	order, err := db.InsertOrder(ctx, pool, db.InsertOrderParams{
		MerchantID: m.ID, CustomerID: ov[0].ID, Status: db.OrderStatusDelivered,
		Currency: "USD", PlacedAt: tsNow(), PromoCodes: []string{},
		Subtotal: num(t, "12.00"), TaxTotal: num(t, "0"), GrandTotal: num(t, "12.00"),
	})
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}
	if _, err := db.InsertOrderItems(ctx, pool, []db.InsertOrderItemParams{
		{OrderID: order.ID, LineNumber: 1, ProductID: p.ID, Quantity: 4, UnitPrice: num(t, "3.00")},
	}); err != nil {
		t.Fatalf("InsertOrderItems: %v", err)
	}
	if _, err := pool.Exec(ctx, "REFRESH MATERIALIZED VIEW product_sales"); err != nil {
		t.Fatalf("REFRESH product_sales: %v", err)
	}
	sales, err := db.ListProductSales(ctx, pool, db.ProductSaleFilter{}, pgb.ListOpt{Limit: 10})
	if err != nil {
		t.Fatalf("ListProductSales: %v", err)
	}
	if len(sales) != 1 || sales[0].UnitsSold != 4 {
		t.Fatalf("product_sales: want 1 row with 4 units, got %+v", sales)
	}
	if s := numString(t, sales[0].Revenue); s != "12.00" {
		t.Fatalf("product_sales revenue: want 12.00, got %q", s)
	}
}
