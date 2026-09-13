package core

import "testing"

// TestQuoteIdent pins identifier quoting: plain snake identifiers stay bare,
// keywords (both reserved and unreserved — a column named "default" in a
// column list is a syntax error unquoted), mixed-case and odd characters are
// quoted, embedded double quotes are doubled.
func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"users"}, "users"},
		{[]string{"public", "users"}, "public.users"},
		{[]string{"email_address"}, "email_address"},
		{[]string{"_private", "x2y"}, "_private.x2y"},
		// reserved class
		{[]string{"select"}, `"select"`},
		{[]string{"from"}, `"from"`},
		{[]string{"order"}, `"order"`},
		{[]string{"end"}, `"end"`},
		// unreserved / col_name class — the gaps that motivated AUDIT P1 #3
		{[]string{"default"}, `"default"`},
		{[]string{"check"}, `"check"`},
		{[]string{"language"}, `"language"`},
		{[]string{"value"}, `"value"`},
		{[]string{"path"}, `"path"`},
		// shape-based quoting is orthogonal to keywords
		{[]string{"Email"}, `"Email"`},
		{[]string{"weird name"}, `"weird name"`},
		{[]string{`a"b`}, `"a""b"`},
		// empty parts are skipped
		{[]string{"", "users", ""}, "users"},
	}
	for _, tc := range cases {
		if got := QuoteIdent(tc.in...); got != tc.want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestEmitKeywordColumn runs a keyword-named column through the full emitter
// so the quoted shape lands in real statement SQL, not just the helper.
func TestEmitKeywordColumn(t *testing.T) {
	col := Col{Table: "t", Name: "default"}
	sql, args := Emit(Bin{Op: "=", L: col, R: Lit{V: 1}})
	if sql != `t."default" = $1` {
		t.Fatalf("sql = %s", sql)
	}
	if len(args) != 1 || args[0] != 1 {
		t.Fatalf("args = %v", args)
	}
}

// TestKeywordListSanity guards the generated table: sorted, unique,
// lowercase, and covering both classes.
func TestKeywordListSanity(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for _, w := range pgKeywordList {
		if w == "" || w != lower(w) {
			t.Fatalf("bad keyword entry %q", w)
		}
		if seen[w] {
			t.Fatalf("duplicate keyword %q", w)
		}
		if w < prev {
			t.Fatalf("keyword list not sorted at %q", w)
		}
		seen[w] = true
		prev = w
	}
	for _, want := range []string{"select", "default", "check", "language", "returning"} {
		if !pgKeywords[want] {
			t.Fatalf("keyword %q missing from list", want)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
