package gen

import (
	"strings"

	"github.com/khoale-2804/pgb/ir"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// Directives holds the pgb:* comment directives attached to one table or
// one column. They live outside ir.Table/ir.Column (the IR carries only the
// human-readable comment); codegen passes look them up in a DirectiveSet.
//
// Parsed from catalog comments (sqlc drops COMMENT ON DDL, but table/column
// comments that survive the analyzer land in the proto Comment fields):
//
//	COMMENT ON TABLE  users          IS 'pgb:skip';
//	COMMENT ON COLUMN users.password IS 'pgb:no_filter; pgb:no_patch';
//	COMMENT ON COLUMN users.metadata IS 'pgb:type=map[string]any';
type Directives struct {
	Skip         bool   // pgb:skip       — emit nothing for the table
	NoFilter     bool   // pgb:no_filter  — not exposed as a predicate
	NoPatch      bool   // pgb:no_patch   — not exposed as a SET clause
	TypeOverride string // pgb:type=<go type> — verbatim Go type expression
}

// Has reports whether any directive is set.
func (d Directives) Has() bool {
	return d != Directives{}
}

// DirectiveSet indexes directives by catalog key: tables under
// "schema.table", columns under "schema.table.column". Missing entries read
// as zero Directives.
type DirectiveSet struct {
	Tables  map[string]Directives
	Columns map[string]Directives
}

// Table returns the directives for one table (zero value if none).
func (d DirectiveSet) Table(schema, name string) Directives {
	if d.Tables == nil {
		return Directives{}
	}
	return d.Tables[catalogKey(schema, name)]
}

// Column returns the directives for one column (zero value if none).
func (d DirectiveSet) Column(schema, table, col string) Directives {
	if d.Columns == nil {
		return Directives{}
	}
	return d.Columns[catalogKey(schema, table, col)]
}

func catalogKey(parts ...string) string {
	return strings.Join(parts, ".")
}

// Build walks req.Catalog (schemas -> tables -> columns, in proto order) and
// resolves it into the SchemaIR, parsing pgb:* directives from table and
// column comments into the returned DirectiveSet.
//
// Deviations forced by the plugin proto (plugin-sdk-go v1.23.0):
//   - proto Table has no view/materialized-view flag, so ir.Table.View stays
//     false for every table; a later proto (or the oliphant pass) must set it.
//   - the proto carries no primary-key, unique, default, or generated-column
//     information, so PrimaryKey/Uniques stay nil and HasDefault/Generated
//     stay zero — the DDL extraction pass fills them.
//   - proto CompositeType carries no fields, so ir.Composite.Fields is empty.
//   - the proto carries no domain list, so ir.Schema.Domains stays nil
//     (column types arrive pre-resolved by sqlc).
func Build(req *plugin.GenerateRequest, opts Options) (ir.Schema, DirectiveSet, error) {
	var sch ir.Schema
	set := DirectiveSet{
		Tables:  map[string]Directives{},
		Columns: map[string]Directives{},
	}
	if req == nil || req.Catalog == nil {
		return sch, set, nil
	}
	cat := req.Catalog
	sch.DefaultSchema = cat.GetDefaultSchema()
	if sch.DefaultSchema == "" {
		sch.DefaultSchema = "public"
	}
	for _, s := range cat.GetSchemas() {
		if s == nil {
			continue
		}
		for _, e := range s.GetEnums() {
			sch.Enums = append(sch.Enums, ir.Enum{
				Schema: s.GetName(),
				Name:   e.GetName(),
				Values: append([]string(nil), e.GetVals()...),
			})
		}
		for _, ct := range s.GetCompositeTypes() {
			sch.Composites = append(sch.Composites, ir.Composite{
				Schema: s.GetName(),
				Name:   ct.GetName(),
			})
		}
		for _, t := range s.GetTables() {
			if t == nil || t.GetRel() == nil || t.GetRel().GetName() == "" {
				continue
			}
			schemaName := t.GetRel().GetSchema()
			if schemaName == "" {
				schemaName = s.GetName()
			}
			name := t.GetRel().GetName()
			tbl := ir.Table{
				Schema: schemaName,
				Name:   name,
			}
			td, plain := parseDirectives(t.GetComment())
			tbl.Comment = plain
			tbl.Skip = td.Skip
			if td.Has() {
				set.Tables[catalogKey(schemaName, name)] = td
			}
			for _, c := range t.GetColumns() {
				col := ir.Column{
					Name:    c.GetName(),
					PGType:  baseTypeName(c.GetType().GetName()),
					NotNull: c.GetNotNull(),
					IsArray: c.GetIsArray(),
				}
				cd, colPlain := parseDirectives(c.GetComment())
				col.Comment = colPlain
				tbl.Columns = append(tbl.Columns, col)
				if cd.Has() {
					set.Columns[catalogKey(schemaName, name, col.Name)] = cd
				}
			}
			sch.Tables = append(sch.Tables, tbl)
		}
	}
	return sch, set, nil
}

// baseTypeName strips a schema qualifier from a proto type name:
// "public.int4" -> "int4", "int4" -> "int4".
func baseTypeName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// parseDirectives splits a catalog comment on ";" and reads the "pgb:"
// tokens. Non-directive text is returned joined as the human-readable
// comment; unknown pgb:* tokens are ignored.
func parseDirectives(comment string) (Directives, string) {
	var d Directives
	if strings.TrimSpace(comment) == "" {
		return d, ""
	}
	var plain []string
	for _, tok := range strings.Split(comment, ";") {
		t := strings.TrimSpace(tok)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "pgb:") {
			plain = append(plain, t)
			continue
		}
		rest := strings.TrimPrefix(t, "pgb:")
		switch {
		case rest == "skip":
			d.Skip = true
		case rest == "no_filter":
			d.NoFilter = true
		case rest == "no_patch":
			d.NoPatch = true
		case strings.HasPrefix(rest, "type="):
			d.TypeOverride = strings.TrimPrefix(rest, "type=")
		}
	}
	return d, strings.Join(plain, "; ")
}
