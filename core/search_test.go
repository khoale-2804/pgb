package core

import (
	"testing"
)

// Byte-exact pins for every pg_search shape pgb emits. The SQL canon lives
// here; docs/paradedb/predicates.mdx mirrors these shapes.

func TestSearchPredicates(t *testing.T) {
	col := Col{Table: "products", Name: "description"}
	cat := Col{Table: "products", Name: "category"}
	tests := []struct {
		name string
		e    Expr
		sql  string
		args int
	}{
		{"match", Match(col, "running shoes"),
			`products.description ||| $1`, 1},
		{"match-all", MatchAll(col, "shoes"),
			`products.description &&& $1`, 1},
		{"phrase", Phrase(col, "running shoes", 2),
			`products.description ### $1::pdb.slop(2)`, 1},
		{"phrase-no-slop", Phrase(col, "running shoes", 0),
			`products.description ### $1`, 1},
		{"exact", Exact(cat, "shoes"),
			`products.category === $1`, 1},
		{"exact-any", ExactAny(cat, []string{"shoes", "boots"}, "text"),
			`products.category === $1::text[]`, 1},
		{"fuzzy", Fuzzy(col, "shose", 1, false),
			`products.description === $1::pdb.fuzzy(1)`, 1},
		{"fuzzy-prefix", Fuzzy(col, "shose", 2, true),
			`products.description === $1::pdb.fuzzy(2, true)`, 1},
		{"regex", Regex(col, "key.*"),
			`products.description @@@ pdb.regex($1)`, 1},
		{"parse", Parse(col, "shoes + running", true),
			`products.description @@@ pdb.parse($1, lenient => $2)`, 2},
		{"parse-strict", Parse(col, "shoes", false),
			`products.description @@@ pdb.parse($1)`, 1},
		{"range-term", RangeTerm(cat, "[3,7)", "int4range", "Intersects"),
			`products.category @@@ pdb.range_term($1::int4range, $2)`, 2},
		{"force-index", ForceIndex(Col{Table: "products", Name: "id"}),
			`products.id @@@ pdb.all()`, 0},
		{"boost-literal", Boost(Match(col, "shoes"), 2),
			`products.description ||| $1::pdb.boost(2)`, 1},
		{"boost-frac", Boost(Match(col, "shoes"), 1.5),
			`products.description ||| $1::pdb.boost(1.5)`, 1},
		{"boost-on-regex", Boost(Regex(col, "key.*"), 2),
			`products.description @@@ pdb.regex($1)::pdb.boost(2)`, 1},
		{"boost-on-fuzzy", Boost(Fuzzy(col, "shose", 1, false), 2),
			`products.description === $1::pdb.fuzzy(1)::pdb.boost(2)`, 1},
		{"score", Score(Col{Table: "products", Name: "id"}),
			`pdb.score(products.id)`, 0},
		{"highlight", Highlight(col),
			`pdb.highlight(products.description)`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := Emit(tt.e)
			if sql != tt.sql {
				t.Fatalf("sql:\n got %q\nwant %q", sql, tt.sql)
			}
			if len(args) != tt.args {
				t.Fatalf("args: got %d want %d", len(args), tt.args)
			}
		})
	}
}

func TestSearchSnippet(t *testing.T) {
	col := Col{Table: "products", Name: "description"}
	sql, args := Emit(Snippet(col, "<em>", "</em>", 150))
	want := `pdb.snippet(products.description, start_tag => $1, ` +
		`end_tag => $2, max_num_chars => $3)`
	if sql != want {
		t.Fatalf("sql:\n got %q\nwant %q", sql, want)
	}
	if len(args) != 3 {
		t.Fatalf("args: got %d want 3", len(args))
	}
	if sql2, _ := Emit(Snippet(col, "", "", 0)); sql2 != `pdb.snippet(products.description)` {
		t.Fatalf("bare snippet: %q", sql2)
	}
}

func TestSearchSnippets(t *testing.T) {
	col := Col{Table: "products", Name: "description"}
	sql, args := Emit(Snippets(col, 5, 0, "score"))
	want := `pdb.snippets(products.description, "limit" => $1, sort_by => $2)`
	if sql != want {
		t.Fatalf("sql:\n got %q\nwant %q", sql, want)
	}
	if len(args) != 2 {
		t.Fatalf("args: got %d want 2", len(args))
	}
}

func TestProximity(t *testing.T) {
	sql, args := Emit(Proximity("sleek", "shoes", 2))
	if sql != "$1 ## 2 ## $2" {
		t.Fatalf("sql: %q", sql)
	}
	if len(args) != 2 || args[0] != "sleek" || args[1] != "shoes" {
		t.Fatalf("args: %#v", args)
	}
}

func TestBoostPassthrough(t *testing.T) {
	// Boost on a non-predicate expression passes through untouched.
	col := Col{Table: "t", Name: "c"}
	if sql, _ := Emit(Boost(col, 2)); sql != `t.c` {
		t.Fatalf("passthrough changed the expression: %q", sql)
	}
}
