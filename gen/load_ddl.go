package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/khoale-2804/pgb/ir"
	oliphant "github.com/sqlc-dev/oliphant"
	"github.com/sqlc-dev/oliphant/ast"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// Enrich is the DDL extraction pass (DESIGN §4): it re-parses the schema
// files with oliphant (pure-Go libpg_query 18 — the same parser sqlc uses)
// and fills the SchemaIR details the plugin proto drops:
//
//   - `CREATE INDEX ... USING paradedb|bm25` -> ir.Table.Search (key_field
//     from WITH, per-column tokenizer casts, JSON paths + aliases);
//   - `COMMENT ON TABLE/COLUMN` -> ir comments + pgb:* directives (parsed
//     into dir, overriding anything sqlcat derived from proto comments);
//   - `GENERATED ALWAYS AS (...) VIRTUAL|STORED` -> ir.Column.Generated;
//   - primary-key and unique constraints -> ir.Table.PrimaryKey/Uniques;
//   - `CREATE VIEW` / `CREATE MATERIALIZED VIEW` -> ir.Table.View.
//
// Paths come from req.Settings.GetSchema() (repeated strings relative to the
// sqlc config dir — the plugin process runs with cwd = config dir; absolute
// paths are used as-is; a directory entry expands to its sorted *.sql files).
//
// Failure policy (DESIGN §4): a missing or unparseable file is a warning on
// stderr and a continue — the IR degrades to catalog-only and the search
// layer simply isn't generated. Enrich therefore never fails hard; the error
// return exists for future hard-failure gates. Deterministic: files are
// processed in Settings order (directories sorted), statements in parse
// order.
func Enrich(req *plugin.GenerateRequest, sch *ir.Schema, dir *DirectiveSet, opts Options) error {
	_ = opts // reserved for future gates (e.g. paradedb version)
	if req == nil || sch == nil || dir == nil {
		return nil
	}
	for _, path := range ddlFiles(req) {
		data, err := os.ReadFile(path)
		if err != nil {
			warnf("skipping schema file %s: %v", path, err)
			continue
		}
		res, err := oliphant.Parse(string(data))
		if err != nil {
			warnf("unparseable schema file %s: %v (IR degrades to catalog-only)", path, err)
			continue
		}
		for _, rs := range res.GetStmts() {
			enrichStmt(sch, dir, rs.GetStmt())
		}
	}
	return nil
}

// warnf prints a load-time warning to stderr without failing the run.
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pgb: load_ddl: "+format+"\n", args...)
}

// ddlFiles resolves the schema paths from req.Settings: absolute paths as-is,
// relative paths against the config dir (the plugin's cwd), directory entries
// expanded to their sorted *.sql files. Missing paths warn and are skipped.
func ddlFiles(req *plugin.GenerateRequest) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range req.GetSettings().GetSchema() {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			warnf("schema path %s: %v", p, err)
			continue
		}
		if !fi.IsDir() {
			add(p)
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(p, "*.sql"))
		sort.Strings(matches)
		if len(matches) == 0 {
			warnf("schema dir %s contains no .sql files", p)
			continue
		}
		for _, m := range matches {
			add(m)
		}
	}
	return out
}

// enrichStmt dispatches one parsed statement to its extractor.
func enrichStmt(sch *ir.Schema, dir *DirectiveSet, n *ast.Node) {
	switch inner := n.GetNode().(type) {
	case *ast.Node_IndexStmt:
		enrichIndex(sch, inner.IndexStmt)
	case *ast.Node_CommentStmt:
		enrichComment(sch, dir, inner.CommentStmt)
	case *ast.Node_CreateStmt:
		enrichCreate(sch, inner.CreateStmt)
	case *ast.Node_ViewStmt:
		enrichView(sch, inner.ViewStmt)
	case *ast.Node_CreateTableAsStmt:
		enrichMatView(sch, inner.CreateTableAsStmt)
	}
}

// enrichIndex extracts `CREATE INDEX ... USING paradedb|bm25` into
// ir.Table.Search. One search index per table (ParadeDB rule) — a later
// index for the same table replaces an earlier one. Non-search access
// methods are ignored.
func enrichIndex(sch *ir.Schema, st *ast.IndexStmt) {
	if st.GetRelation() == nil {
		return
	}
	using := strings.ToLower(st.GetAccessMethod())
	if using != "paradedb" && using != "bm25" {
		return
	}
	t := findTable(sch, st.GetRelation().GetSchemaname(), st.GetRelation().GetRelname())
	if t == nil {
		return
	}
	si := &ir.SearchIndex{Name: st.GetIdxname(), Using: using}
	firstCol := ""
	for _, p := range st.GetIndexParams() {
		el := p.GetIndexElem()
		if el == nil {
			continue
		}
		f, ok := searchField(t, el)
		if !ok {
			continue
		}
		si.Fields = append(si.Fields, f)
		if firstCol == "" && f.Path == "" && f.Column != "" {
			firstCol = f.Column
		}
	}
	for _, on := range st.GetOptions() {
		de := on.GetDefElem()
		if de == nil {
			continue
		}
		v := defArgString(de.GetArg())
		if v == "" {
			continue
		}
		if de.GetDefname() == "key_field" {
			si.KeyField = v
			continue
		}
		if si.Options == nil {
			si.Options = map[string]string{}
		}
		si.Options[de.GetDefname()] = v
	}
	// Fallback when WITH (key_field=...) is absent: the first plain column.
	if si.KeyField == "" {
		si.KeyField = firstCol
	}
	t.Search = si
}

// searchField builds one ir.SearchField from an index element: a plain
// column ("description"), a tokenizer cast ("(title::pdb.icu)") or a JSON
// path cast ("((metadata->'color')::pdb.literal('alias=json_color'))").
func searchField(t *ir.Table, el *ast.IndexElem) (ir.SearchField, bool) {
	var f ir.SearchField
	if el.GetName() != "" {
		f.Column = el.GetName()
		f.PGType = columnPGType(t, f.Column)
		return f, true
	}
	expr := el.GetExpr()
	if expr == nil {
		return f, false
	}
	// An unparenthesized bare column reference.
	if name, ok := exprPath(expr); ok && !hasJSONPath(expr) {
		f.Column = name
		f.PGType = columnPGType(t, f.Column)
		return f, true
	}
	tc := expr.GetTypeCast()
	if tc == nil {
		// A JSON path without a tokenizer cast: keep the path, no tokenizer.
		if name, path, ok := exprJSONPath(expr); ok {
			f.Column, f.Path = name, path
			f.PGType = columnPGType(t, f.Column)
			return f, true
		}
		return f, false
	}
	name, path, ok := exprJSONPath(tc.GetArg())
	if !ok {
		return f, false
	}
	f.Column, f.Path = name, path
	base, alias, args := typmodParts(tc.GetTypeName())
	f.Alias = alias
	if base != "" {
		if len(args) > 0 {
			f.Tokenizer = base + "(" + strings.Join(args, ",") + ")"
		} else {
			f.Tokenizer = base
		}
	}
	f.PGType = columnPGType(t, f.Column)
	return f, true
}

// hasJSONPath reports whether the expression contains a json access chain
// ("->" / indirection), as opposed to a bare column reference.
func hasJSONPath(n *ast.Node) bool {
	_, path, ok := exprJSONPath(n)
	return ok && path != ""
}

// exprPath resolves a bare ColumnRef to its column name.
func exprPath(n *ast.Node) (string, bool) {
	if cr := n.GetColumnRef(); cr != nil {
		return lastString(cr.GetFields())
	}
	return "", false
}

// exprJSONPath resolves an index element expression to its base column and
// json access path. A plain ColumnRef yields ok with an empty path; json
// operator chains (A_Expr with "->" — the shape `metadata->'color'` parses
// to) and A_Indirection chains append 'sub' segments, producing e.g.
// "metadata->'color'".
func exprJSONPath(n *ast.Node) (col, path string, ok bool) {
	switch e := n.GetNode().(type) {
	case *ast.Node_ColumnRef:
		name, ok := lastString(e.ColumnRef.GetFields())
		if !ok {
			return "", "", false
		}
		return name, "", true
	case *ast.Node_AExpr:
		ae := e.AExpr
		if op, _ := lastString(ae.GetName()); op != "->" {
			return "", "", false
		}
		lcol, lpath, lok := exprJSONPath(ae.GetLexpr())
		if !lok {
			return "", "", false
		}
		val, rok := constString(ae.GetRexpr())
		if !rok {
			return "", "", false
		}
		if lpath == "" {
			lpath = lcol
		}
		return lcol, joinPath(lpath, val), true
	case *ast.Node_AIndirection:
		ai := e.AIndirection
		col, p, ok := exprJSONPath(ai.GetArg())
		if !ok {
			return "", "", false
		}
		if p == "" {
			p = col
		}
		for _, ind := range ai.GetIndirection() {
			if idx := ind.GetAIndices(); idx != nil {
				if v, ok2 := constString(idx.GetUidx()); ok2 {
					p = joinPath(p, v)
					continue
				}
				return "", "", false
			}
			if s := ind.GetString_(); s != nil {
				p = joinPath(p, s.GetSval())
				continue
			}
			return "", "", false
		}
		return col, p, true
	}
	return "", "", false
}

// joinPath appends one 'field' segment to a json path.
func joinPath(p, field string) string {
	return p + "->'" + field + "'"
}

// typmodParts splits a pdb.* type name into its base tokenizer (the last
// name segment), an optional "alias=..." value from the typmods, and the
// remaining typmod arguments rendered as strings ("stemmer=english", "3").
func typmodParts(tn *ast.TypeName) (base, alias string, args []string) {
	if tn == nil {
		return "", "", nil
	}
	if _, ok := lastString(tn.GetNames()); ok {
		base, _ = lastString(tn.GetNames())
	}
	for _, tm := range tn.GetTypmods() {
		if de := tm.GetDefElem(); de != nil {
			if v := defArgString(de.GetArg()); v != "" {
				args = append(args, de.GetDefname()+"="+v)
			}
			continue
		}
		v, ok := constString(tm)
		if !ok {
			// `fast=true` arrives as an A_Expr inside the typmods.
			if ae := tm.GetAExpr(); ae != nil {
				if op, _ := lastString(ae.GetName()); op == "=" {
					if l, ok2 := exprPath(ae.GetLexpr()); ok2 {
						if rv, ok3 := constString(ae.GetRexpr()); ok3 {
							args = append(args, l+"="+rv)
						}
					}
				}
			}
			continue
		}
		if a, found := strings.CutPrefix(v, "alias="); found {
			alias = a
			continue
		}
		args = append(args, v)
	}
	return base, alias, args
}

// enrichCreate extracts the CREATE TABLE details the proto lacks: generated
// columns and primary-key/unique constraints (both column-level and
// table-level). Constraints for tables absent from the catalog are ignored.
func enrichCreate(sch *ir.Schema, st *ast.CreateStmt) {
	if st.GetRelation() == nil {
		return
	}
	t := findTable(sch, st.GetRelation().GetSchemaname(), st.GetRelation().GetRelname())
	if t == nil {
		return
	}
	for _, elt := range st.GetTableElts() {
		switch c := elt.GetNode().(type) {
		case *ast.Node_ColumnDef:
			cd := c.ColumnDef
			for _, cn := range cd.GetConstraints() {
				con := cn.GetConstraint()
				if con == nil {
					continue
				}
				if con.GetContype() == ast.ConstrType_CONSTR_GENERATED {
					setGenerated(t, cd.GetColname(), con.GetGeneratedKind())
					continue
				}
				if con.GetContype() == ast.ConstrType_CONSTR_IDENTITY {
					// GENERATED ALWAYS/BY DEFAULT AS IDENTITY: the value
					// comes from the implicit sequence default, so the
					// column must not join INSERT column lists (the server
					// rejects non-DEFAULT inserts into ALWAYS, SQLSTATE
					// 428C9). HasDefault routes it through insertableIdx.
					if c := findColumn(t, cd.GetColname()); c != nil {
						c.HasDefault = true
					}
					continue
				}
				applyConstraint(t, con, cd.GetColname())
			}
		case *ast.Node_Constraint:
			applyConstraint(t, c.Constraint, "")
		}
	}
	for _, cn := range st.GetConstraints() {
		applyConstraint(t, cn.GetConstraint(), "")
	}
}

// applyConstraint records one primary-key or unique constraint on t. col is
// the owning column for column-level constraints (whose Keys list is empty)
// and empty for table-level ones. A table keeps its first primary key;
// duplicate unique definitions are ignored.
func applyConstraint(t *ir.Table, con *ast.Constraint, col string) {
	if con == nil {
		return
	}
	switch con.GetContype() {
	case ast.ConstrType_CONSTR_PRIMARY:
		keys := nodeStrings(con.GetKeys())
		if len(keys) == 0 && col != "" {
			keys = []string{col}
		}
		if len(keys) > 0 && len(t.PrimaryKey) == 0 {
			t.PrimaryKey = keys
		}
	case ast.ConstrType_CONSTR_UNIQUE:
		keys := nodeStrings(con.GetKeys())
		if len(keys) == 0 && col != "" {
			keys = []string{col}
		}
		if len(keys) > 0 && !hasUnique(t.Uniques, keys) {
			t.Uniques = append(t.Uniques, keys)
		}
	}
}

// setGenerated maps a CONSTR_GENERATED kind onto the column: "v" (PG18
// virtual) -> "virtual", "s" -> "stored". Unknown kinds are ignored.
func setGenerated(t *ir.Table, colName, kind string) {
	var gen string
	switch kind {
	case "v":
		gen = "virtual"
	case "s":
		gen = "stored"
	default:
		return
	}
	if c := findColumn(t, colName); c != nil {
		c.Generated = gen
	}
}

// hasUnique reports whether cols already appears in uniques (same length,
// same order — column order is part of the constraint).
func hasUnique(uniques [][]string, cols []string) bool {
	for _, u := range uniques {
		if len(u) != len(cols) {
			continue
		}
		same := true
		for i := range u {
			if u[i] != cols[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// enrichView flags a CREATE VIEW's target as read-only in the IR. Views the
// catalog doesn't carry are ignored (no stub tables — the codegen passes
// consume catalog columns).
func enrichView(sch *ir.Schema, st *ast.ViewStmt) {
	if st.GetView() == nil {
		return
	}
	if t := findTable(sch, st.GetView().GetSchemaname(), st.GetView().GetRelname()); t != nil {
		t.View = true
	}
}

// enrichMatView flags CREATE MATERIALIZED VIEW (which parses as a
// CreateTableAsStmt with Objtype OBJECT_MATVIEW) the same way. Plain
// SELECT INTO shares the node with a different objtype and is ignored.
func enrichMatView(sch *ir.Schema, st *ast.CreateTableAsStmt) {
	if st.GetObjtype() != ast.ObjectType_OBJECT_MATVIEW || st.GetInto() == nil {
		return
	}
	rel := st.GetInto().GetRel()
	if rel == nil {
		return
	}
	if t := findTable(sch, rel.GetSchemaname(), rel.GetRelname()); t != nil {
		t.View = true
	}
}

// enrichComment splits a COMMENT ON statement into the human-readable
// comment (stored on the IR) and pgb:* directives (stored in dir, overriding
// whatever sqlcat parsed from the proto comment fields — the DDL statement
// is the source of truth). OBJECT_TABLE/OBJECT_COLUMN only; the object name
// is a List of String nodes: [table] or [schema, table] for tables,
// [table, column] or [schema, table, column] for columns.
func enrichComment(sch *ir.Schema, dir *DirectiveSet, st *ast.CommentStmt) {
	names := listStrings(st.GetObject())
	if len(names) == 0 {
		return
	}
	d, plain := parseDirectives(st.GetComment())
	switch st.GetObjtype() {
	case ast.ObjectType_OBJECT_TABLE:
		t := findTable(sch, schemaPart(names[:len(names)-1]), names[len(names)-1])
		if t == nil {
			return
		}
		t.Comment = plain
		if d.Has() {
			dir.Tables[catalogKey(t.Schema, t.Name)] = d
		}
	case ast.ObjectType_OBJECT_COLUMN:
		if len(names) < 2 {
			return
		}
		col := names[len(names)-1]
		t := findTable(sch, schemaPart(names[:len(names)-2]), names[len(names)-2])
		if t == nil {
			return
		}
		c := findColumn(t, col)
		if c == nil {
			return
		}
		c.Comment = plain
		if d.Has() {
			dir.Columns[catalogKey(t.Schema, t.Name, c.Name)] = d
		}
	}
}

// schemaPart extracts the schema qualifier from a qualified-name prefix:
// [] or [table] -> "" (default schema), [schema] -> schema,
// [catalog, schema] -> schema.
func schemaPart(prefix []string) string {
	if len(prefix) == 0 {
		return ""
	}
	return prefix[len(prefix)-1]
}

// findTable looks up a table by (schema, name); an empty schema resolves to
// sch.DefaultSchema. Matching is case-insensitive (the raw parse tree keeps
// unquoted identifiers as written, the catalog folds them), with the first
// match in catalog order winning.
func findTable(sch *ir.Schema, schema, name string) *ir.Table {
	if schema == "" {
		schema = sch.DefaultSchema
	}
	match := func(t *ir.Table) bool {
		if !strings.EqualFold(t.Name, name) {
			return false
		}
		if strings.EqualFold(t.Schema, schema) {
			return true
		}
		// Hand-built IRs may leave Schema empty for default-schema tables.
		return t.Schema == "" && (schema == sch.DefaultSchema || schema == "")
	}
	for i := range sch.Tables {
		if match(&sch.Tables[i]) {
			return &sch.Tables[i]
		}
	}
	return nil
}

// findColumn looks up a column by name (case-insensitive fallback, catalog
// order preserved), returning a pointer into t.Columns.
func findColumn(t *ir.Table, name string) *ir.Column {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	for i := range t.Columns {
		if strings.EqualFold(t.Columns[i].Name, name) {
			return &t.Columns[i]
		}
	}
	return nil
}

// columnPGType resolves a search field's PG type from the indexed table's
// column; unknown columns yield "".
func columnPGType(t *ir.Table, name string) string {
	if c := findColumn(t, name); c != nil {
		return c.PGType
	}
	return ""
}

// defArgString stringifies a DefElem argument (a raw String node for
// reloptions, sometimes an A_Const).
func defArgString(n *ast.Node) string {
	v, _ := constString(n)
	return v
}

// constString stringifies a scalar literal node: A_Const (string, integer,
// float, bool) or a raw String node.
func constString(n *ast.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	if s := n.GetString_(); s != nil {
		return s.GetSval(), true
	}
	if c := n.GetAConst(); c != nil {
		if s := c.GetSval(); s != nil {
			return s.GetSval(), true
		}
		if i := c.GetIval(); i != nil {
			return strconv.FormatInt(int64(i.GetIval()), 10), true
		}
		if f := c.GetFval(); f != nil {
			return f.GetFval(), true
		}
		if b := c.GetBoolval(); b != nil {
			return strconv.FormatBool(b.GetBoolval()), true
		}
	}
	return "", false
}

// listStrings flattens a List node's String items ("app"."users"."password"
// -> ["app", "users", "password"]). Non-string items are skipped.
func listStrings(n *ast.Node) []string {
	if n == nil {
		return nil
	}
	var out []string
	for _, item := range n.GetList().GetItems() {
		if s := item.GetString_(); s != nil {
			out = append(out, s.GetSval())
		}
	}
	return out
}

// nodeStrings extracts the String items of a key list (constraint Keys).
func nodeStrings(items []*ast.Node) []string {
	var out []string
	for _, item := range items {
		if s := item.GetString_(); s != nil {
			out = append(out, s.GetSval())
		}
	}
	return out
}

// lastString returns the last String item of a qualified-name list.
func lastString(items []*ast.Node) (string, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		if s := items[i].GetString_(); s != nil {
			return s.GetSval(), true
		}
	}
	return "", false
}
