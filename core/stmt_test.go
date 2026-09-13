package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---- test doubles ----

type fakeRows struct{}

func (fakeRows) Close()                                       {}
func (fakeRows) Err() error                                   { return nil }
func (fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (fakeRows) Next() bool                                   { return false }
func (fakeRows) Scan(dest ...any) error                       { return pgx.ErrNoRows }
func (fakeRows) Values() ([]any, error)                       { return nil, nil }
func (fakeRows) RawValues() [][]byte                          { return nil }
func (fakeRows) Conn() *pgx.Conn                              { return nil }

type fakeDB struct {
	queries []string
	argss   [][]any
}

func (f *fakeDB) record(sql string, args []any) {
	f.queries = append(f.queries, sql)
	f.argss = append(f.argss, args)
}

func (f *fakeDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.record(sql, args)
	return fakeRows{}, nil
}

func (f *fakeDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.record(sql, args)
	return nil
}

func (f *fakeDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.record(sql, args)
	return pgconn.CommandTag{}, nil
}

type fakeTx struct {
	fakeDB
	committed  bool
	rolledBack bool
}

func (t *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, errors.New("pgb fake: nested tx unsupported")
}

func (t *fakeTx) Commit(ctx context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(ctx context.Context) error { t.rolledBack = true; return nil }

func (t *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("pgb fake: CopyFrom unsupported")
}

func (t *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return nil
}

func (t *fakeTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }

func (t *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("pgb fake: Prepare unsupported")
}

func (t *fakeTx) Conn() *pgx.Conn { return nil }

type fakePool struct {
	fakeDB
	tx *fakeTx
}

func (p *fakePool) Begin(ctx context.Context) (pgx.Tx, error) {
	p.tx = &fakeTx{}
	return p.tx, nil
}

// ---- helpers ----

func col(name string) Expr { return Col{Name: name} }

func eqCol(name string, v any) Expr {
	return Bin{Op: "=", L: col(name), R: Lit{V: v}}
}

// ---- Select ----

func TestSelectSQL(t *testing.T) {
	since := "2026-01-01"
	tests := []struct {
		name     string
		build    func() *Select
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "bare select qualified table",
			build: func() *Select {
				return NewSelect("public.users", col("id"), col("email"))
			},
			wantSQL: "SELECT id, email FROM public.users",
		},
		{
			name: "bare table and reserved-word quoting",
			build: func() *Select {
				return NewSelect("users", Col{Table: "users", Name: "order"})
			},
			wantSQL: `SELECT users."order" FROM users`,
		},
		{
			name: "where order limit",
			build: func() *Select {
				return NewSelect("public.users", col("id"), col("email")).
					Where(Bin{Op: "LIKE", L: col("email"), R: Lit{V: "%@corp.io"}}).
					OrderBy(Asc(col("id"))).
					Limit(20)
			},
			wantSQL:  "SELECT id, email FROM public.users WHERE email LIKE $1 ORDER BY id ASC LIMIT $2",
			wantArgs: []any{"%@corp.io", 20},
		},
		{
			name: "distinct on group having offset lock",
			build: func() *Select {
				return NewSelect("public.orders", col("id")).
					DistinctOn(col("user_id")).
					Where(eqCol("status", "paid")).
					GroupBy(col("tenant_id")).
					Having(Bin{Op: ">", L: Call{Fn: "count", Args: []Expr{col("id")}}, R: Lit{V: 5}}).
					OrderBy(Asc(col("user_id")), Desc(col("placed_at"))).
					Limit(10).
					Offset(20).
					Lock("FOR UPDATE")
			},
			wantSQL: "SELECT DISTINCT ON (user_id) id FROM public.orders WHERE status = $1 " +
				"GROUP BY tenant_id HAVING count(id) > $2 ORDER BY user_id ASC, placed_at DESC " +
				"LIMIT $3 OFFSET $4 FOR UPDATE",
			wantArgs: []any{"paid", 5, 10, 20},
		},
		{
			name: "with placement before select keyword",
			build: func() *Select {
				recent := NewSelect("public.orders", col("user_id")).
					Where(Bin{Op: ">=", L: col("placed_at"), R: Lit{V: since}})
				return NewSelect("public.users", col("id")).
					With("recent_orders", recent).
					Where(Raw{SQL: "id IN (SELECT user_id FROM recent_orders)"}).
					OrderBy(Asc(col("id")))
			},
			wantSQL: "WITH recent_orders AS (SELECT user_id FROM public.orders WHERE placed_at >= $1) " +
				"SELECT id FROM public.users WHERE id IN (SELECT user_id FROM recent_orders) ORDER BY id ASC",
			wantArgs: []any{since},
		},
		{
			name: "two ctes in call order",
			build: func() *Select {
				a := NewSelect("ta", col("x"))
				b := NewSelect("tb", col("y"))
				return NewSelect("t", col("id")).With("cte_b", b).With("cte_a", a)
			},
			wantSQL: "WITH cte_b AS (SELECT y FROM tb), cte_a AS (SELECT x FROM ta) SELECT id FROM t",
		},
		{
			name: "limit and offset zero are no-ops",
			build: func() *Select {
				return NewSelect("t", col("id")).Limit(0).Offset(0).Limit(-5).Offset(-1)
			},
			wantSQL: "SELECT id FROM t",
		},
		{
			name: "or and not via whereexpr",
			build: func() *Select {
				return NewSelect("t", col("id")).WhereExpr(
					Not{E: Or{Parts: []Expr{
						eqCol("a", 1),
						eqCol("b", 2),
					}}},
				)
			},
			wantSQL:  "SELECT id FROM t WHERE NOT ((a = $1 OR b = $2))",
			wantArgs: []any{1, 2},
		},
		{
			name: "multi-part where accumulates flat conjunction",
			build: func() *Select {
				return NewSelect("public.users", col("id")).
					Where(
						Or{Parts: []Expr{
							Bin{Op: "LIKE", L: col("email"), R: Lit{V: "%@corp.io"}},
							Bin{Op: "LIKE", L: col("email"), R: Lit{V: "%@example.com"}},
						}},
						Bin{Op: ">=", L: col("last_login_at"), R: Lit{V: since}},
					).
					Where(eqCol("active", true))
			},
			wantSQL: "SELECT id FROM public.users WHERE " +
				"(email LIKE $1 OR email LIKE $2) AND last_login_at >= $3 AND active = $4",
			wantArgs: []any{"%@corp.io", "%@example.com", since, true},
		},
		{
			name: "empty where call is a no-op",
			build: func() *Select {
				return NewSelect("t", col("id")).Where().WhereExpr(nil)
			},
			wantSQL: "SELECT id FROM t",
		},
		{
			name: "raw renumbering after bound params",
			build: func() *Select {
				return NewSelect("t", col("id")).
					Where(
						eqCol("name", "x"),
						Raw{SQL: "id IN (?, ?)", Args: []any{7, 9}},
					)
			},
			wantSQL:  `SELECT id FROM t WHERE "name" = $1 AND id IN ($2, $3)`,
			wantArgs: []any{"x", 7, 9},
		},
		{
			name: "raw renumbering before bound params",
			build: func() *Select {
				return NewSelect("t", col("id")).
					Where(
						Raw{SQL: "id IN (?, ?)", Args: []any{7, 9}},
						eqCol("name", "x"),
					)
			},
			wantSQL:  `SELECT id FROM t WHERE id IN ($1, $2) AND "name" = $3`,
			wantArgs: []any{7, 9, "x"},
		},
		{
			name: "nested select as cte renumbers into outer sequence",
			build: func() *Select {
				inner := NewSelect("ti", col("v")).
					Where(Raw{SQL: "k IN (?, ?)", Args: []any{1, 2}})
				return NewSelect("orders", col("id")).
					With("i", inner).
					Where(eqCol("z", 3))
			},
			wantSQL:  "WITH i AS (SELECT v FROM ti WHERE k IN ($1, $2)) SELECT id FROM orders WHERE z = $3",
			wantArgs: []any{1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.build()
			gotSQL, gotArgs := s.SQL()
			if gotSQL != tt.wantSQL {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) == 0 && len(tt.wantArgs) == 0 {
				// ok
			} else if !equalArgs(gotArgs, tt.wantArgs) {
				t.Errorf("args mismatch\n got: %#v\nwant: %#v", gotArgs, tt.wantArgs)
			}
			// SQL() must be pure: a second render is byte-identical.
			sql2, args2 := s.SQL()
			if sql2 != gotSQL || !equalArgs(args2, gotArgs) {
				t.Errorf("SQL() not deterministic:\n 1st: %s %#v\n 2nd: %s %#v", gotSQL, gotArgs, sql2, args2)
			}
		})
	}
}

func equalArgs(a, b []any) bool {
	return reflect.DeepEqual(a, b)
}

func TestSelectRun(t *testing.T) {
	db := &fakeDB{}
	s := NewSelect("public.users", col("id")).
		Where(eqCol("id", int64(1))).
		Limit(2)
	if _, err := s.Run(context.Background(), db); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "SELECT id FROM public.users WHERE id = $1 LIMIT $2"
	if len(db.queries) != 1 || db.queries[0] != want {
		t.Errorf("query mismatch\n got: %q\nwant: %q", db.queries, want)
	}
	if !equalArgs(db.argss[0], []any{int64(1), 2}) {
		t.Errorf("args mismatch: %#v", db.argss[0])
	}
}

// ---- Insert ----

func TestInsertSQL(t *testing.T) {
	tests := []struct {
		name     string
		build    func() *Insert
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "single row with returning",
			build: func() *Insert {
				return NewInsert("public.users", []string{"email", "name"},
					[]Expr{Lit{V: "ada@corp.io"}, Lit{V: "Ada Lovelace"}},
				).Returning(col("id"), col("created_at"))
			},
			wantSQL:  `INSERT INTO public.users (email, "name") VALUES ($1, $2) RETURNING id, created_at`,
			wantArgs: []any{"ada@corp.io", "Ada Lovelace"},
		},
		{
			name: "multi row values",
			build: func() *Insert {
				return NewInsert("public.tags", []string{"tag", "n"},
					[]Expr{Lit{V: "a"}, Lit{V: 1}},
					[]Expr{Lit{V: "b"}, Lit{V: 2}},
				)
			},
			wantSQL:  "INSERT INTO public.tags (tag, n) VALUES ($1, $2), ($3, $4)",
			wantArgs: []any{"a", 1, "b", 2},
		},
		{
			name: "on conflict do nothing",
			build: func() *Insert {
				return NewInsert("public.users", []string{"email"},
					[]Expr{Lit{V: "ada@corp.io"}},
				).OnConflict(OnConflict{Target: []string{"email"}, DoNothing: true})
			},
			wantSQL:  "INSERT INTO public.users (email) VALUES ($1) ON CONFLICT (email) DO NOTHING",
			wantArgs: []any{"ada@corp.io"},
		},
		{
			name: "on conflict do update with where",
			build: func() *Insert {
				return NewInsert("public.users", []string{"email", "name"},
					[]Expr{Lit{V: "ada@corp.io"}, Lit{V: "Ada King"}},
				).OnConflict(OnConflict{
					Target: []string{"email"},
					Sets: []SetClause{
						{Col: "name", E: Lit{V: "Ada King"}},
						{Col: "bio", E: Lit{V: nil}},
					},
					Where: Bin{Op: "IS DISTINCT FROM", L: Col{Table: "users", Name: "name"}, R: Raw{SQL: "EXCLUDED.name"}},
				}).Returning(col("id"))
			},
			wantSQL: `INSERT INTO public.users (email, "name") VALUES ($1, $2) ` +
				`ON CONFLICT (email) DO UPDATE SET "name" = $3, bio = NULL ` +
				`WHERE users."name" IS DISTINCT FROM EXCLUDED.name RETURNING id`,
			wantArgs: []any{"ada@corp.io", "Ada King", "Ada King"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := tt.build().SQL()
			if gotSQL != tt.wantSQL {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", gotSQL, tt.wantSQL)
			}
			if !equalArgs(gotArgs, tt.wantArgs) {
				t.Errorf("args mismatch\n got: %#v\nwant: %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestInsertRunExec(t *testing.T) {
	ctx := context.Background()
	ins := NewInsert("public.users", []string{"email"}, []Expr{Lit{V: "a@b"}}).Returning(col("id"))

	q := &fakeDB{}
	if _, err := ins.Run(ctx, q); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(q.queries) != 1 || q.queries[0] != "INSERT INTO public.users (email) VALUES ($1) RETURNING id" {
		t.Errorf("Run query mismatch: %q", q.queries)
	}

	e := &fakeDB{}
	if _, err := ins.Exec(ctx, e); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(e.queries) != 1 || e.queries[0] != "INSERT INTO public.users (email) VALUES ($1) RETURNING id" {
		t.Errorf("Exec query mismatch: %q", e.queries)
	}
}

// ---- Update ----

func TestUpdateSQL(t *testing.T) {
	tests := []struct {
		name     string
		build    func() *Update
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "set where returning with NULL literal",
			build: func() *Update {
				return NewUpdate("public.users").
					Set("bio", Lit{V: "Principal engineer"}).
					Set("name", Lit{V: nil}).
					Where(Bin{Op: "@>", L: col("tags"), R: Lit{V: []string{"staff"}}}).
					Returning(col("id"), col("email"))
			},
			wantSQL:  `UPDATE public.users SET bio = $1, "name" = NULL WHERE tags @> $2 RETURNING id, email`,
			wantArgs: []any{"Principal engineer", []string{"staff"}},
		},
		{
			name: "where calls accumulate and whereexpr composes not",
			build: func() *Update {
				return NewUpdate("t").
					Set("x", Lit{V: 1}).
					Where(eqCol("a", 1)).
					WhereExpr(Not{E: eqCol("b", 2)})
			},
			wantSQL:  "UPDATE t SET x = $1 WHERE a = $2 AND NOT b = $3",
			wantArgs: []any{1, 1, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs, err := tt.build().SQL()
			if err != nil {
				t.Fatalf("SQL: %v", err)
			}
			if gotSQL != tt.wantSQL {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", gotSQL, tt.wantSQL)
			}
			if !equalArgs(gotArgs, tt.wantArgs) {
				t.Errorf("args mismatch\n got: %#v\nwant: %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestUpdateErrNoWhere(t *testing.T) {
	ctx := context.Background()
	u := NewUpdate("public.users").Set("bio", Lit{V: "x"})

	if _, _, err := u.SQL(); !errors.Is(err, ErrNoWhere) {
		t.Errorf("SQL() err = %v, want ErrNoWhere", err)
	}
	db := &fakeDB{}
	if _, err := u.Run(ctx, db); !errors.Is(err, ErrNoWhere) {
		t.Errorf("Run err = %v, want ErrNoWhere", err)
	}
	if _, err := u.Exec(ctx, db); !errors.Is(err, ErrNoWhere) {
		t.Errorf("Exec err = %v, want ErrNoWhere", err)
	}
	if len(db.queries) != 0 {
		t.Errorf("ErrNoWhere must fire before any query, got %q", db.queries)
	}

	// With a Where the guard lifts and the statement routes through.
	db2 := &fakeDB{}
	u2 := u.Where(eqCol("id", int64(3)))
	if _, err := u2.Exec(ctx, db2); err != nil {
		t.Fatalf("Exec with where: %v", err)
	}
	if db2.queries[0] != "UPDATE public.users SET bio = $1 WHERE id = $2" {
		t.Errorf("query mismatch: %q", db2.queries[0])
	}
	if !equalArgs(db2.argss[0], []any{"x", int64(3)}) {
		t.Errorf("args mismatch: %#v", db2.argss[0])
	}
}

// ---- Delete ----

func TestDeleteSQL(t *testing.T) {
	tests := []struct {
		name     string
		build    func() *Delete
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "multi predicate where",
			build: func() *Delete {
				return NewDelete("public.users").
					Where(
						Bin{Op: "<", L: col("last_login_at"), R: Lit{V: "2026-01-01"}},
						Raw{SQL: "bio IS NULL"},
					)
			},
			wantSQL:  "DELETE FROM public.users WHERE last_login_at < $1 AND bio IS NULL",
			wantArgs: []any{"2026-01-01"},
		},
		{
			name: "whereexpr with returning",
			build: func() *Delete {
				return NewDelete("t").
					WhereExpr(Or{Parts: []Expr{eqCol("a", 1), eqCol("b", 2)}}).
					Returning(col("id"))
			},
			wantSQL:  "DELETE FROM t WHERE (a = $1 OR b = $2) RETURNING id",
			wantArgs: []any{1, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs, err := tt.build().SQL()
			if err != nil {
				t.Fatalf("SQL: %v", err)
			}
			if gotSQL != tt.wantSQL {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", gotSQL, tt.wantSQL)
			}
			if !equalArgs(gotArgs, tt.wantArgs) {
				t.Errorf("args mismatch\n got: %#v\nwant: %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestDeleteErrNoWhere(t *testing.T) {
	ctx := context.Background()
	d := NewDelete("public.users")

	if _, _, err := d.SQL(); !errors.Is(err, ErrNoWhere) {
		t.Errorf("SQL() err = %v, want ErrNoWhere", err)
	}
	db := &fakeDB{}
	if _, err := d.Run(ctx, db); !errors.Is(err, ErrNoWhere) {
		t.Errorf("Run err = %v, want ErrNoWhere", err)
	}
	if _, err := d.Exec(ctx, db); !errors.Is(err, ErrNoWhere) {
		t.Errorf("Exec err = %v, want ErrNoWhere", err)
	}
	if len(db.queries) != 0 {
		t.Errorf("ErrNoWhere must fire before any query, got %q", db.queries)
	}
}

// ---- TableMeta ----

func TestNewTableMeta(t *testing.T) {
	m := NewTableMeta("public", "users")
	if m.Schema != "public" || m.Name != "users" {
		t.Errorf("NewTableMeta = %+v, want {public users}", m)
	}
}

// ---- WithTx ----

func TestWithTx(t *testing.T) {
	ctx := context.Background()

	t.Run("commits on success", func(t *testing.T) {
		p := &fakePool{}
		got, err := WithTx(ctx, p, func(tx DBTX) (string, error) {
			_, err := NewSelect("t", col("id")).Run(ctx, tx)
			return "v", err
		})
		if err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		if got != "v" {
			t.Errorf("got %q, want %q", got, "v")
		}
		if p.tx == nil || !p.tx.committed || p.tx.rolledBack {
			t.Errorf("tx state wrong: committed=%v rolledBack=%v", p.tx.committed, p.tx.rolledBack)
		}
		if len(p.tx.queries) != 1 {
			t.Errorf("fn should run on the tx, queries: %q", p.tx.queries)
		}
	})

	t.Run("rolls back on error", func(t *testing.T) {
		p := &fakePool{}
		boom := errors.New("boom")
		_, err := WithTx(ctx, p, func(tx DBTX) (int, error) { return 0, boom })
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want boom", err)
		}
		if !p.tx.rolledBack || p.tx.committed {
			t.Errorf("tx state wrong: committed=%v rolledBack=%v", p.tx.committed, p.tx.rolledBack)
		}
	})

	t.Run("rolls back and re-raises on panic", func(t *testing.T) {
		p := &fakePool{}
		defer func() {
			if recover() == nil {
				t.Error("panic was not re-raised")
			}
			if !p.tx.rolledBack || p.tx.committed {
				t.Errorf("tx state wrong: committed=%v rolledBack=%v", p.tx.committed, p.tx.rolledBack)
			}
		}()
		WithTx(ctx, p, func(tx DBTX) (int, error) { panic("kaboom") })
	})

	t.Run("rejects non-transactional db", func(t *testing.T) {
		plain := &fakeDB{}
		_, err := WithTx(ctx, plain, func(tx DBTX) (int, error) { return 0, nil })
		if err == nil {
			t.Fatal("want error for non-beginning DBTX")
		}
		if !strings.Contains(err.Error(), "WithTx requires a connection or pool that can begin a transaction") {
			t.Errorf("error message mismatch: %v", err)
		}
		if len(plain.queries) != 0 {
			t.Errorf("fn must not run on rejected db, got %q", plain.queries)
		}
	})
}

// TestApplyListOrder pins the ListOpt.Order hook: explicit ordering terms
// land before LIMIT/OFFSET so unordered lists (and keyset pagination) get a
// deterministic row order.
func TestApplyListOrder(t *testing.T) {
	s := NewSelect("public.users", Col{Table: "users", Name: "id"}).
		ApplyList(ListOpt{
			Order: []Order{Asc(Col{Table: "users", Name: "created_at"}), Desc(Col{Table: "users", Name: "id"})},
			Limit: 10,
		})
	sql, _ := s.SQL()
	want := "SELECT users.id FROM public.users ORDER BY users.created_at ASC, users.id DESC LIMIT $1"
	if sql != want {
		t.Fatalf("SQL = %s", sql)
	}
}
