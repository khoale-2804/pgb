package gen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/khoale-2804/pgb/ir"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// ddlSchema is the hand-built IR the DDL tests enrich: the catalog side of
// the fixture tables the inline DDL statements refer to.
func ddlSchema() ir.Schema {
	return ir.Schema{
		DefaultSchema: "public",
		Tables: []ir.Table{
			{Schema: "public", Name: "products", Columns: []ir.Column{
				{Name: "id", PGType: "int8", NotNull: true},
				{Name: "title", PGType: "text", NotNull: true},
				{Name: "description", PGType: "text", NotNull: true},
				{Name: "metadata", PGType: "jsonb"},
				{Name: "rating", PGType: "numeric"},
			}},
			{Schema: "public", Name: "docs", Columns: []ir.Column{
				{Name: "id", PGType: "uuid", NotNull: true},
				{Name: "title", PGType: "text", NotNull: true},
				{Name: "body", PGType: "text", NotNull: true},
			}},
			{Schema: "public", Name: "users", Columns: []ir.Column{
				{Name: "id", PGType: "int8", NotNull: true},
				{Name: "password", PGType: "text", NotNull: true},
				{Name: "settings", PGType: "json"},
				{Name: "metadata", PGType: "jsonb"},
			}},
			{Schema: "public", Name: "active_users", Columns: []ir.Column{
				{Name: "id", PGType: "int8"},
			}},
			{Schema: "public", Name: "order_stats", Columns: []ir.Column{
				{Name: "user_id", PGType: "int8"},
			}},
			{Schema: "public", Name: "orders", Columns: []ir.Column{
				{Name: "id", PGType: "int8"},
				{Name: "shop_id", PGType: "int4", NotNull: true},
				{Name: "sku", PGType: "text", NotNull: true},
				{Name: "a", PGType: "int4"},
				{Name: "b", PGType: "int4"},
			}},
			{Schema: "app", Name: "users", Columns: []ir.Column{
				{Name: "id", PGType: "int8"},
			}},
		},
	}
}

// tableByName fetches one IR table by (schema, name).
func tableByName(t *testing.T, sch *ir.Schema, schema, name string) *ir.Table {
	t.Helper()
	for i := range sch.Tables {
		if sch.Tables[i].Schema == schema && sch.Tables[i].Name == name {
			return &sch.Tables[i]
		}
	}
	t.Fatalf("table %s.%s not in schema", schema, name)
	return nil
}

// runEnrich writes the inline DDL to a temp file, runs Enrich over sch and
// returns the directives it parsed.
func runEnrich(t *testing.T, sch *ir.Schema, ddl string) DirectiveSet {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte(ddl), 0o644); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	set := DirectiveSet{Tables: map[string]Directives{}, Columns: map[string]Directives{}}
	req := &plugin.GenerateRequest{Settings: &plugin.Settings{Schema: []string{path}}}
	if err := Enrich(req, sch, &set, Options{}); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	return set
}

// captureStderr swaps os.Stderr for a pipe around fn and returns what was
// written (the pass logs warnings there instead of failing).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stderr = old
	return <-done
}

func TestEnrichParadedbIndex(t *testing.T) {
	sch := ddlSchema()
	runEnrich(t, &sch, `
CREATE INDEX products_search ON products USING paradedb
  (id, (title::pdb.icu), description, ((metadata->'color')::pdb.literal('alias=json_color')),
   rating)
  WITH (key_field='id', default_field='description');
CREATE INDEX products_title_trgm ON products USING gin (title gin_trgm_ops);
`)
	p := tableByName(t, &sch, "public", "products")
	if p.Search == nil {
		t.Fatal("products.Search not set by paradedb index")
	}
	si := p.Search
	if si.Name != "products_search" || si.Using != "paradedb" {
		t.Errorf("Name/Using = %q/%q, want products_search/paradedb", si.Name, si.Using)
	}
	if si.KeyField != "id" {
		t.Errorf("KeyField = %q, want id", si.KeyField)
	}
	if got := si.Options["default_field"]; got != "description" {
		t.Errorf("Options[default_field] = %q, want description", got)
	}
	want := []ir.SearchField{
		{Column: "id", PGType: "int8"},
		{Column: "title", Tokenizer: "icu", PGType: "text"},
		{Column: "description", PGType: "text"},
		{Column: "metadata", Path: "metadata->'color'", Alias: "json_color", Tokenizer: "literal", PGType: "jsonb"},
		{Column: "rating", PGType: "numeric"},
	}
	if len(si.Fields) != len(want) {
		t.Fatalf("Fields = %+v, want %d entries", si.Fields, len(want))
	}
	for i, w := range want {
		if si.Fields[i] != w {
			t.Errorf("Fields[%d] = %+v, want %+v", i, si.Fields[i], w)
		}
	}
}

func TestEnrichBM25Alias(t *testing.T) {
	sch := ddlSchema()
	runEnrich(t, &sch, `
CREATE INDEX docs_search ON docs USING bm25
  (id, (title::pdb.edge_ngram), (title::pdb.literal('alias=title_exact')),
   (body::pdb.simple('stemmer=english')), (code::pdb.ngram(3,3)))
  WITH (key_field='id');
`)
	// bm25 is accepted as the legacy alias for USING paradedb, and the docs
	// table carries a second indexed column (double-indexed title).
	d := tableByName(t, &sch, "public", "docs")
	if d.Search == nil {
		t.Fatal("docs.Search not set by bm25 index")
	}
	si := d.Search
	if si.Using != "bm25" {
		t.Errorf("Using = %q, want bm25", si.Using)
	}
	if si.KeyField != "id" {
		t.Errorf("KeyField = %q, want id", si.KeyField)
	}
	want := []ir.SearchField{
		{Column: "id", PGType: "uuid"},
		{Column: "title", Tokenizer: "edge_ngram", PGType: "text"},
		{Column: "title", Alias: "title_exact", Tokenizer: "literal", PGType: "text"},
		{Column: "body", Tokenizer: "simple(stemmer=english)", PGType: "text"},
		{Column: "code", Tokenizer: "ngram(3,3)", PGType: ""},
	}
	if len(si.Fields) != len(want) {
		t.Fatalf("Fields = %+v, want %d entries", si.Fields, len(want))
	}
	for i, w := range want {
		if si.Fields[i] != w {
			t.Errorf("Fields[%d] = %+v, want %+v", i, si.Fields[i], w)
		}
	}
}

func TestEnrichKeyFieldFallback(t *testing.T) {
	sch := ddlSchema()
	// No WITH (key_field=...): the first plain index param becomes the key.
	runEnrich(t, &sch, `
CREATE INDEX products_search ON products USING paradedb (id, description);
`)
	p := tableByName(t, &sch, "public", "products")
	if p.Search == nil || p.Search.KeyField != "id" {
		t.Errorf("KeyField = %+v, want fallback id", p.Search)
	}
}

func TestEnrichCommentDirectives(t *testing.T) {
	sch := ddlSchema()
	set := runEnrich(t, &sch, `
COMMENT ON COLUMN users.password IS 'pgb:no_filter; pgb:no_patch';
COMMENT ON COLUMN users.settings  IS 'pgb:type=map[string]any';
COMMENT ON TABLE  users           IS 'shop customers and staff accounts';
COMMENT ON COLUMN users.metadata  IS 'arbitrary profile metadata (jsonb)';
`)
	if d := set.Column("public", "users", "password"); !d.NoFilter || !d.NoPatch {
		t.Errorf("password directives = %+v, want NoFilter+NoPatch", d)
	}
	if d := set.Column("public", "users", "settings"); d.TypeOverride != "map[string]any" {
		t.Errorf("settings directives = %+v, want type override", d)
	}
	u := tableByName(t, &sch, "public", "users")
	if u.Comment != "shop customers and staff accounts" {
		t.Errorf("table comment = %q", u.Comment)
	}
	m := findColumn(u, "metadata")
	if m == nil || m.Comment != "arbitrary profile metadata (jsonb)" {
		t.Errorf("metadata comment = %+v", m)
	}
	pw := findColumn(u, "password")
	if pw == nil || pw.Comment != "" {
		t.Errorf("password comment = %+v (directives only, no plain text)", pw)
	}
	// Plain comments must not create directive entries.
	if d := set.Table("public", "users"); d.Has() {
		t.Errorf("plain table comment leaked directives: %+v", d)
	}
	if d := set.Column("public", "users", "metadata"); d.Has() {
		t.Errorf("plain column comment leaked directives: %+v", d)
	}
}

func TestEnrichCommentDirectivesOverride(t *testing.T) {
	sch := ddlSchema()
	// sqlcat derived directives from proto comments; a COMMENT ON statement
	// must override them wholesale.
	path := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte(`COMMENT ON TABLE users IS 'pgb:skip';`), 0o644); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	set := DirectiveSet{
		Tables:  map[string]Directives{"public.users": {NoFilter: true}},
		Columns: map[string]Directives{"public.users.password": {NoFilter: true}},
	}
	req := &plugin.GenerateRequest{Settings: &plugin.Settings{Schema: []string{path}}}
	if err := Enrich(req, &sch, &set, Options{}); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if d := set.Table("public", "users"); !d.Skip || d.NoFilter {
		t.Errorf("table directives = %+v, want skip only (override)", d)
	}
	// Columns untouched by DDL keep their sqlcat-derived directives.
	if d := set.Column("public", "users", "password"); !d.NoFilter {
		t.Errorf("column directives = %+v, want preserved NoFilter", d)
	}
}

func TestEnrichQualifiedComment(t *testing.T) {
	sch := ddlSchema()
	set := runEnrich(t, &sch, `
COMMENT ON TABLE  app.users          IS 'app-schema twin of public.users';
COMMENT ON COLUMN public.users.password IS 'pgb:no_filter';
`)
	a := tableByName(t, &sch, "app", "users")
	if a.Comment != "app-schema twin of public.users" {
		t.Errorf("app.users comment = %q", a.Comment)
	}
	if d := set.Column("public", "users", "password"); !d.NoFilter {
		t.Errorf("public.users.password directives = %+v", d)
	}
}

func TestEnrichGeneratedColumns(t *testing.T) {
	sch := ddlSchema()
	runEnrich(t, &sch, `
CREATE TABLE users (
  id          bigserial PRIMARY KEY,
  email       text NOT NULL,
  search_slug text GENERATED ALWAYS AS (lower(email)) VIRTUAL,
  name_upper  text GENERATED ALWAYS AS (upper(email)) STORED
);
`)
	// The catalog table wins the lookup; only the Generated flags are merged.
	u := tableByName(t, &sch, "public", "users")
	want := map[string]string{"search_slug": "virtual", "name_upper": "stored"}
	for _, c := range u.Columns {
		if c.Generated != want[c.Name] {
			t.Errorf("column %s Generated = %q, want %q", c.Name, c.Generated, want[c.Name])
		}
	}
	if got := (findColumn(u, "id")).Generated; got != "" {
		t.Errorf("plain column Generated = %q", got)
	}
}

func TestEnrichKeys(t *testing.T) {
	t.Run("column level", func(t *testing.T) {
		sch := ddlSchema()
		runEnrich(t, &sch, `CREATE TABLE users (id bigserial PRIMARY KEY, email text NOT NULL UNIQUE);`)
		u := tableByName(t, &sch, "public", "users")
		if !reflect.DeepEqual(u.PrimaryKey, []string{"id"}) {
			t.Errorf("PrimaryKey = %v, want [id]", u.PrimaryKey)
		}
		want := [][]string{{"email"}}
		if !reflect.DeepEqual(u.Uniques, want) {
			t.Errorf("Uniques = %v, want %v", u.Uniques, want)
		}
	})
	t.Run("composite table level", func(t *testing.T) {
		sch := ddlSchema()
		runEnrich(t, &sch, `
CREATE TABLE orders (
  id      bigint,
  shop_id integer NOT NULL,
  a       int4,
  b       int4,
  PRIMARY KEY (shop_id, id),
  CONSTRAINT uq_ab UNIQUE (a, b)
);`)
		o := tableByName(t, &sch, "public", "orders")
		if !reflect.DeepEqual(o.PrimaryKey, []string{"shop_id", "id"}) {
			t.Errorf("PrimaryKey = %v, want [shop_id id]", o.PrimaryKey)
		}
		if !reflect.DeepEqual(o.Uniques, [][]string{{"a", "b"}}) {
			t.Errorf("Uniques = %v, want [[a b]]", o.Uniques)
		}
	})
	t.Run("duplicate unique collapses", func(t *testing.T) {
		sch := ddlSchema()
		runEnrich(t, &sch, `CREATE TABLE docs (id uuid PRIMARY KEY, title text UNIQUE, UNIQUE (title));`)
		d := tableByName(t, &sch, "public", "docs")
		if !reflect.DeepEqual(d.Uniques, [][]string{{"title"}}) {
			t.Errorf("Uniques = %v, want [[title]] once", d.Uniques)
		}
	})
}

func TestEnrichViews(t *testing.T) {
	sch := ddlSchema()
	runEnrich(t, &sch, `
CREATE VIEW active_users AS SELECT id, email FROM users;
CREATE MATERIALIZED VIEW order_stats AS SELECT user_id FROM orders;
`)
	if !tableByName(t, &sch, "public", "active_users").View {
		t.Error("CREATE VIEW did not set View")
	}
	if !tableByName(t, &sch, "public", "order_stats").View {
		t.Error("CREATE MATERIALIZED VIEW did not set View")
	}
	if tableByName(t, &sch, "public", "users").View {
		t.Error("base table wrongly flagged as view")
	}
}

func TestEnrichMissingAndUnparseableFiles(t *testing.T) {
	sch := ddlSchema()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.sql")
	if err := os.WriteFile(good, []byte(`COMMENT ON TABLE users IS 'docs';`), 0o644); err != nil {
		t.Fatalf("write good.sql: %v", err)
	}
	bad := filepath.Join(dir, "bad.sql")
	if err := os.WriteFile(bad, []byte(`CREATE TABLE (((`), 0o644); err != nil {
		t.Fatalf("write bad.sql: %v", err)
	}
	set := DirectiveSet{Tables: map[string]Directives{}, Columns: map[string]Directives{}}
	req := &plugin.GenerateRequest{Settings: &plugin.Settings{
		Schema: []string{filepath.Join(dir, "absent.sql"), bad, good},
	}}
	log := captureStderr(t, func() {
		if e := Enrich(req, &sch, &set, Options{}); e != nil {
			t.Errorf("Enrich must degrade to warnings, got %v", e)
		}
	})
	for _, want := range []string{"absent.sql", "bad.sql"} {
		if !bytes.Contains([]byte(log), []byte(want)) {
			t.Errorf("stderr warning for %s missing; log = %q", want, log)
		}
	}
	// The good file still enriched despite its broken siblings.
	if tableByName(t, &sch, "public", "users").Comment != "docs" {
		t.Error("good.sql after bad.sql was not processed")
	}
}

func TestEnrichRelativeAndDirectoryPaths(t *testing.T) {
	sch := ddlSchema()
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "schema.sql"), []byte(`COMMENT ON COLUMN users.password IS 'pgb:no_filter';`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "b_dir.sql"), []byte(`COMMENT ON COLUMN users.settings IS 'pgb:type=map[string]any';`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "a_dir.sql"), []byte(`COMMENT ON COLUMN users.metadata IS 'pgb:no_patch';`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	set := DirectiveSet{Tables: map[string]Directives{}, Columns: map[string]Directives{}}
	req := &plugin.GenerateRequest{Settings: &plugin.Settings{
		Schema: []string{"sub/schema.sql", "."}, // relative to the config dir (cwd)
	}}
	t.Chdir(tmp)
	if err := Enrich(req, &sch, &set, Options{}); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if d := set.Column("public", "users", "password"); !d.NoFilter {
		t.Errorf("sub/schema.sql not applied: %+v", d)
	}
	// Directory expansion is sorted: a_dir.sql and b_dir.sql both applied.
	if d := set.Column("public", "users", "metadata"); !d.NoPatch {
		t.Errorf("a_dir.sql not applied: %+v", d)
	}
	if d := set.Column("public", "users", "settings"); d.TypeOverride != "map[string]any" {
		t.Errorf("b_dir.sql not applied: %+v", d)
	}
}

func TestEnrichDeterministic(t *testing.T) {
	ddl := `
CREATE INDEX products_search ON products USING paradedb
  (id, (title::pdb.icu), ((metadata->'color')::pdb.literal('alias=json_color')))
  WITH (key_field='id');
COMMENT ON COLUMN users.password IS 'pgb:no_filter; pgb:no_patch';
CREATE TABLE users (id bigserial PRIMARY KEY, email text UNIQUE,
  g text GENERATED ALWAYS AS (lower(email)) STORED);
CREATE VIEW active_users AS SELECT id FROM users;
`
	run := func() (ir.Schema, DirectiveSet) {
		sch := ddlSchema()
		set := runEnrich(t, &sch, ddl)
		return sch, set
	}
	a, da := run()
	b, db := run()
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(da, db) {
		t.Error("Enrich is not deterministic across runs")
	}
}
