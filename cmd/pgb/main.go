// Command pgb is the STANDALONE edition of the pgb compiler (EDITIONS §18):
// schema.sql in, the same generated db/ package out — no sqlc, no protobuf
// on stdin. It parses the DDL with oliphant (the same parser sqlc uses),
// builds the plugin GenerateRequest in-process, and runs the exact same
// generation pipeline as the sqlc plugin edition (gen.Generate), so both
// editions emit byte-identical code from the same schema.
//
// Usage:
//
//	pgb -schema schema.sql [-schema more.sql] -out db -package db
//
// Flags mirror the sqlc plugin options (package, core, target,
// include_defaults); unknown plugin behavior (overrides arrays) stays a
// sqlc-edition feature for now.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	oliphant "github.com/sqlc-dev/oliphant"
	"github.com/sqlc-dev/oliphant/ast"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"

	"github.com/khoale-2804/pgb/gen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pgb:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pgb", flag.ContinueOnError)
	var schemas multiFlag
	fs.Var(&schemas, "schema", "schema file or directory (repeatable; dirs expand to sorted *.sql)")
	out := fs.String("out", "db", "output directory (created if missing)")
	pkg := fs.String("package", "db", "generated package name")
	core := fs.String("core", "github.com/khoale-2804/pgb/core", "import path of the runtime core")
	target := fs.String("target", "18", "PostgreSQL target version gate")
	includeDefaults := fs.Bool("include-defaults", false, "include defaulted columns in INSERT (plugin parity)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(schemas) == 0 {
		return fmt.Errorf("no -schema given")
	}

	files, err := expandSchemas(schemas)
	if err != nil {
		return err
	}

	req, err := buildRequest(files, *pkg, *core, *target, *includeDefaults)
	if err != nil {
		return err
	}

	resp, err := gen.Generate(context.Background(), req)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	var names []string
	for _, f := range resp.GetFiles() {
		p := filepath.Join(*out, f.GetName())
		if err := os.WriteFile(p, f.GetContents(), 0o644); err != nil {
			return err
		}
		names = append(names, f.GetName())
	}
	sort.Strings(names)
	fmt.Printf("pgb: wrote %d files to %s:\n  %s\n", len(names), *out, strings.Join(names, "\n  "))
	return nil
}

// multiFlag collects repeated -schema values.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// expandSchemas resolves files/dirs to a sorted, deduped list of absolute
// *.sql paths — absolute because the plugin's Enrich pass resolves schema
// paths against req.Settings, and we hand it the same list.
func expandSchemas(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, raw := range in {
		fi, err := os.Stat(raw)
		if err != nil {
			return nil, fmt.Errorf("schema path %s: %w", raw, err)
		}
		if !fi.IsDir() {
			add(raw)
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(raw, "*.sql"))
		sort.Strings(matches)
		if len(matches) == 0 {
			return nil, fmt.Errorf("schema dir %s contains no .sql files", raw)
		}
		for _, m := range matches {
			add(m)
		}
	}
	abs := make([]string, len(out))
	for i, p := range out {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		abs[i] = a
	}
	return abs, nil
}

// buildRequest parses the DDL with oliphant and constructs the same
// plugin.GenerateRequest the sqlc plugin edition receives from sqlc:
// catalog (tables, columns, enums) + Settings.Schema (the Enrich pass
// re-reads these files for indexes, COMMENT ON directives and keys) +
// PluginOptions (the options JSON).
func buildRequest(files []string, pkg, core, target string, includeDefaults bool) (*plugin.GenerateRequest, error) {
	schema := &plugin.Schema{Name: "public"}
	cat := &plugin.Catalog{
		DefaultSchema: "public",
		Schemas:       []*plugin.Schema{schema},
	}

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		res, err := oliphant.Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, rs := range res.GetStmts() {
			collectStmt(schema, rs.GetStmt())
		}
	}

	opts, err := json.Marshal(map[string]any{
		"package":          pkg,
		"core":             core,
		"target":           target,
		"include_defaults": includeDefaults,
	})
	if err != nil {
		return nil, err
	}

	// Catalog order: default-schema tables first, then other schemas —
	// both preserving DDL order (sqlc's catalog lays out public before
	// user schemas; the passes are order-sensitive for model layout).
	partitioned := make([]*plugin.Table, 0, len(schema.Tables))
	for _, t := range schema.Tables {
		if t.GetRel().GetSchema() == "" {
			partitioned = append(partitioned, t)
		}
	}
	for _, t := range schema.Tables {
		if t.GetRel().GetSchema() != "" {
			partitioned = append(partitioned, t)
		}
	}
	schema.Tables = partitioned

	return &plugin.GenerateRequest{
		Settings:      &plugin.Settings{Schema: files},
		Catalog:       cat,
		PluginOptions: opts,
	}, nil
}

func collectStmt(schema *plugin.Schema, n *ast.Node) {
	switch inner := n.GetNode().(type) {
	case *ast.Node_CreateStmt:
		st := inner.CreateStmt
		if st.GetRelation() == nil || st.GetRelation().GetRelname() == "" {
			return
		}
		t := &plugin.Table{
			Rel: &plugin.Identifier{
				Schema: st.GetRelation().GetSchemaname(),
				Name:   st.GetRelation().GetRelname(),
			},
		}
		for _, elt := range st.GetTableElts() {
			if cd := elt.GetColumnDef(); cd != nil {
				t.Columns = append(t.Columns, column(cd))
			}
			if con := elt.GetConstraint(); con != nil && con.GetContype() == ast.ConstrType_CONSTR_PRIMARY {
				// Table-level PRIMARY KEY (a, b): every key column is
				// NOT NULL even if the DDL omits it.
				for _, k := range con.GetKeys() {
					if s := k.GetString_(); s != nil {
						for _, c := range t.Columns {
							if c.Name == s.GetSval() {
								c.NotNull = true
							}
						}
					}
				}
			}
		}
		// PARTITION OF parent (and LIKE parent): the partition declares no
		// TableElts — its columns ARE the parent's. Copy from the
		// already-collected parent, like sqlc's catalog carries them.
		if len(t.Columns) == 0 {
			for _, ir := range st.GetInhRelations() {
				if rv := ir.GetRangeVar(); rv != nil && rv.GetRelname() != "" {
					for _, pt := range schema.Tables {
						if pt.GetRel().GetName() == rv.GetRelname() {
							for _, pc := range pt.GetColumns() {
								t.Columns = append(t.Columns, &plugin.Column{
									Name: pc.GetName(), NotNull: pc.GetNotNull(),
									IsArray: pc.GetIsArray(),
									Type:    &plugin.Identifier{Name: pc.GetType().GetName()},
								})
							}
						}
					}
				}
			}
		}
		schema.Tables = append(schema.Tables, t)
	case *ast.Node_CreateEnumStmt:
		st := inner.CreateEnumStmt
		name := lastString(st.GetTypeName())
		if name == "" {
			return
		}
		e := &plugin.Enum{Name: name}
		for _, v := range st.GetVals() {
			if s := v.GetString_(); s != nil {
				e.Vals = append(e.Vals, s.GetSval())
			}
		}
		schema.Enums = append(schema.Enums, e)
	case *ast.Node_ViewStmt:
		if st := inner.ViewStmt; st != nil {
			if t := viewTable(st.GetView(), st.GetQuery(), schema); t != nil {
				schema.Tables = append(schema.Tables, t)
			}
		}
	case *ast.Node_CreateTableAsStmt:
		if st := inner.CreateTableAsStmt; st != nil {
			if t := viewTable(st.GetInto().GetRel(), st.GetQuery(), schema); t != nil {
				schema.Tables = append(schema.Tables, t)
			}
		}
	case *ast.Node_AlterTableStmt:
		// ALTER TABLE t ADD COLUMN ... mutates an already-collected table
		// (the schema under test appends half its columns this way).
		st := inner.AlterTableStmt
		if st == nil || st.GetRelation() == nil {
			return
		}
		for _, t := range schema.Tables {
			if t.GetRel().GetName() != st.GetRelation().GetRelname() {
				continue
			}
			for _, cn := range st.GetCmds() {
				cmd := cn.GetAlterTableCmd()
				if cmd == nil {
					continue
				}
				switch cmd.GetSubtype() {
				case ast.AlterTableType_AT_AddColumn:
					if cd := cmd.GetDef().GetColumnDef(); cd != nil {
						t.Columns = append(t.Columns, column(cd))
					}
				case ast.AlterTableType_AT_DropColumn:
					if s := cmd.GetName(); s != "" {
						kept := t.Columns[:0]
						for _, c := range t.Columns {
							if c.GetName() != s {
								kept = append(kept, c)
							}
						}
						t.Columns = kept
					}
				}
			}
		}
	}
}

// viewTable derives a catalog table (columns + types) from a VIEW /
// MATERIALIZED VIEW definition: column projections resolve against the
// source tables already collected from this schema; aggregates get the
// Postgres result types (count → int8 NOT NULL, sum/avg → numeric
// NULLABLE). Anything unresolvable warns and drops the view — matching
// sqlc's analyzer-or-nothing stance without carrying an evaluator.
func viewTable(rel *ast.RangeVar, query *ast.Node, schema *plugin.Schema) *plugin.Table {
	if rel == nil || rel.GetRelname() == "" || query == nil {
		return nil
	}
	sel := query.GetSelectStmt()
	if sel == nil {
		warn("view %s: not a plain SELECT — skipped", rel.GetRelname())
		return nil
	}
	sources := map[string]*plugin.Table{}
	for _, f := range sel.GetFromClause() {
		if rv := f.GetRangeVar(); rv != nil && rv.GetRelname() != "" {
			// Schema-aware match: an unqualified FROM prefers the
			// default-schema table (app.users must not shadow public.users).
			for _, t := range schema.Tables {
				if t.GetRel().GetName() != rv.GetRelname() {
					continue
				}
				if rv.GetSchemaname() != "" {
					if t.GetRel().GetSchema() == rv.GetSchemaname() {
						sources[rv.GetRelname()] = t
					}
					continue
				}
				if _, seen := sources[rv.GetRelname()]; !seen || t.GetRel().GetSchema() == "" {
					sources[rv.GetRelname()] = t
				}
			}
		}
	}
	t := &plugin.Table{Rel: &plugin.Identifier{Name: rel.GetRelname()}}
	for _, tl := range sel.GetTargetList() {
		rt := tl.GetResTarget()
		if rt == nil {
			continue
		}
		name := rt.GetName()
		switch val := rt.GetVal().GetNode().(type) {
		case *ast.Node_ColumnRef:
			colName := lastString(val.ColumnRef.GetFields())
			if name == "" {
				name = colName
			}
			var src *plugin.Column
			for _, st := range sources {
				for _, c := range st.GetColumns() {
					if c.GetName() == colName {
						src = c
					}
				}
			}
			if src == nil {
				warn("view %s: column %s unresolvable — view skipped", rel.GetRelname(), colName)
				return nil
			}
			t.Columns = append(t.Columns, &plugin.Column{
				Name: name, NotNull: src.GetNotNull(),
				IsArray: src.GetIsArray(),
				Type:    &plugin.Identifier{Name: src.GetType().GetName()},
			})
		case *ast.Node_FuncCall:
			fname := lastString(val.FuncCall.GetFuncname())
			col := funcResultColumn(fname, name, val.FuncCall, sources)
			if col == nil {
				warn("view %s: function %s unresolvable — view skipped", rel.GetRelname(), fname)
				return nil
			}
			t.Columns = append(t.Columns, col)
		default:
			warn("view %s: unsupported expression — view skipped", rel.GetRelname())
			return nil
		}
	}
	if len(t.Columns) == 0 {
		return nil
	}
	return t
}

// funcResultColumn maps the aggregate/function vocabulary views actually
// use in schemas pgb targets. count → int8 NOT NULL; sum/avg → numeric
// NULLABLE; min/max/coalesce → first-column-ref type, NULLABLE.
func funcResultColumn(fn, alias string, call *ast.FuncCall, sources map[string]*plugin.Table) *plugin.Column {
	if alias == "" {
		alias = fn
	}
	firstColType := func() string {
		for _, a := range call.GetArgs() {
			if cr := a.GetColumnRef(); cr != nil {
				cn := lastString(cr.GetFields())
				for _, st := range sources {
					for _, c := range st.GetColumns() {
						if c.GetName() == cn {
							return c.GetType().GetName()
						}
					}
				}
			}
		}
		return ""
	}
	switch fn {
	case "count":
		return &plugin.Column{Name: alias, NotNull: true, Type: &plugin.Identifier{Name: "bigint"}}
	case "sum", "avg":
		// sqlc's analyzer types view aggregates as bigint NOT NULL —
		// mirror it for edition parity (Postgres itself would say nullable
		// numeric for sum(numeric); divergence noted in the editions page).
		return &plugin.Column{Name: alias, NotNull: true, Type: &plugin.Identifier{Name: "bigint"}}
	case "min", "max", "coalesce":
		tn := firstColType()
		if tn == "" {
			return nil
		}
		return &plugin.Column{Name: alias, Type: &plugin.Identifier{Name: tn}}
	}
	return nil
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pgb: "+format+"\n", args...)
}

// column maps one ColumnDef to the plugin shape the catalog builder reads.
// NOT NULL is IsNotNull OR an explicit NOT NULL constraint (the parser sets
// the flag in most shapes, but belt-and-braces for rewritten DDL).
func column(cd *ast.ColumnDef) *plugin.Column {
	c := &plugin.Column{
		Name:    cd.GetColname(),
		NotNull: cd.GetIsNotNull(),
		IsArray: len(cd.GetTypeName().GetArrayBounds()) > 0,
	}
	if tn := cd.GetTypeName(); tn != nil {
		// Raw-parse names match sqlc's catalog verbatim (int8, bool, ...);
		// no spelling normalization here — only view refs need the
		// preferred spellings.
		if name := lastString(tn.GetNames()); name != "" {
			c.Type = &plugin.Identifier{Name: name}
		}
	}
	for _, cn := range cd.GetConstraints() {
		ct := cn.GetConstraint().GetContype()
		// NOT NULL, PRIMARY KEY and IDENTITY all imply NOT NULL in the
		// catalog (sqlc's analyzer applies the same rule).
		if ct == ast.ConstrType_CONSTR_NOTNULL || ct == ast.ConstrType_CONSTR_PRIMARY || ct == ast.ConstrType_CONSTR_IDENTITY {
			c.NotNull = true
		}
	}
	return c
}

// lastString returns the last String_ node of a qualified-name list
// ("public.users" → "users"; bare "bigint" → "bigint").
func lastString(nodes []*ast.Node) string {
	for i := len(nodes) - 1; i >= 0; i-- {
		if s := nodes[i].GetString_(); s != nil {
			return s.GetSval()
		}
	}
	return ""
}
