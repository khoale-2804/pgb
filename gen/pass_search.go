package gen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/khoale-2804/pgb/core"
	"github.com/khoale-2804/pgb/ir"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// PassSearch emits the pg_search layer (pass D): one "<table>_search.gen.go"
// per table carrying a `USING paradedb|bm25` index (ir.Table.Search, filled
// by the DDL extraction pass). The file holds:
//
//   - search predicate methods on the indexed columns' typed column types
//     (the pass-B <Table><Field>Col structs — methods may live in a second
//     file of the same package): Match/MatchAll/Phrase/Exact/Fuzzy/Regex/
//     Parse via the core/search.go constructors, MatchB as pgb.Boost
//     composition, ExactAny on text fields (array right-hand side),
//     RangeTerm on range-castable fields, and the Snippet/Snippets/Highlight
//     projections;
//   - a wrapper type per indexed JSON path with the path re-emitted exactly
//     as indexed, through pgb.Raw (no Boost composition or snippets there);
//   - the table-level Score() (pdb.score over the index key field) and the
//     Search<Table> static: WHERE key @@@ pdb.parse($1), selected and ordered
//     by pdb.score(key) DESC, key ASC, always LIMIT-bounded (Top-K pushdown).
//
// Only indexed columns get methods (SearchField.Column/Path), in index
// declaration order, deduped by (column, path) — a column double-indexed
// under two tokenizers (fixture docs.title) yields one wrapper. pgb:skip
// tables and non-app schemas emit nothing. Deterministic; go/format-clean.
func PassSearch(sch ir.Schema, opts Options, dir DirectiveSet) ([]plugin.File, error) {
	opts = normalizeOptions(sch, opts)
	var files []plugin.File
	for _, t := range sch.Tables {
		if t.Search == nil {
			continue
		}
		if t.Skip || dir.Table(t.Schema, t.Name).Skip || !genSchema(t.Schema) {
			continue
		}
		src, err := searchFile(sch, t, opts)
		if err != nil {
			return nil, fmt.Errorf("pgb: search for %s: %w", t.Name, err)
		}
		_, _, fb := goTableNames(sch, t)
		files = append(files, plugin.File{Name: fb + "_search.gen.go", Contents: src})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// dedupeSearchFields collapses duplicate (column, path) fields, keeping the
// first occurrence.
func dedupeSearchFields(fields []ir.SearchField) []ir.SearchField {
	seen := map[[2]string]bool{}
	out := make([]ir.SearchField, 0, len(fields))
	for _, f := range fields {
		k := [2]string{f.Column, f.Path}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// colIndexByName resolves a search field's column in the catalog list
// (case-insensitive fallback, like the DDL pass). -1 when absent.
func colIndexByName(t ir.Table, name string) int {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return i
		}
	}
	for i := range t.Columns {
		if strings.EqualFold(t.Columns[i].Name, name) {
			return i
		}
	}
	return -1
}

// rangeCastFor maps an indexed column's PG type onto the pdb.range_term
// cast (docs/paradedb/predicates.mdx); "" when the type has no range cast.
func rangeCastFor(pgType string) string {
	switch strings.ToLower(pgType) {
	case "int2", "smallint", "int4", "integer", "int", "serial", "smallserial":
		return "int4range"
	case "int8", "bigint", "bigserial":
		return "int8range"
	case "numeric", "decimal", "money", "float4", "real", "float8", "double precision":
		return "numrange"
	case "timestamp":
		return "tsrange"
	case "timestamptz":
		return "tstzrange"
	case "date":
		return "daterange"
	}
	return ""
}

// searchFile emits the search file for one indexed table.
func searchFile(sch ir.Schema, t ir.Table, opts Options) ([]byte, error) {
	tableVar, _, _ := goTableNames(sch, t)
	tableType := singularize(tableVar) + "Table"
	base := strings.TrimSuffix(tableType, "Table")
	qname := qualifiedTable(t)
	si := t.Search
	fields := dedupeSearchFields(si.Fields)
	names := fieldNames(t.Columns)

	// The pass-B column types the methods attach to, and the Score accessor
	// guard (a column literally named "score" already owns that method).
	taken := map[string]bool{}
	for _, n := range names {
		taken[base+n+"Col"] = true
	}
	scoreTaken := false
	for _, n := range names {
		if accessorName(n) == "Score" {
			scoreTaken = true
		}
	}

	hasStatic := si.KeyField != "" && len(t.Columns) > 0
	imports := []string{opts.Core}
	if hasStatic {
		imports = append(imports, "context", pgxImport, "github.com/jackc/pgx/v5/pgtype")
	}

	keyField := si.KeyField
	keyCol := "pgb.Col{Table: " + strconv.Quote(t.Name) + ", Name: " + strconv.Quote(keyField) + "}"

	var b strings.Builder
	fmt.Fprintf(&b, "// Search surface for %s (index %s, key field %s): predicate methods only\n// on the columns the USING %s index covers, plus the scored document\n// query. Every value stays a bound parameter; operator shapes live in\n// core/search.go (the pg_search canon).\n",
		qname, si.Name, keyField, si.Using)

	if keyField != "" && !scoreTaken {
		fmt.Fprintf(&b, "// Score ranks by the index key field: pdb.score(%s.%s).\n", t.Name, keyField)
		fmt.Fprintf(&b, "func (t %s) Score() pgb.Expr {\n\treturn pgb.Score(%s)\n}\n\n", tableType, keyCol)
	}

	for _, f := range fields {
		if f.Path != "" {
			emitPathSearch(&b, t, f, base, taken)
			continue
		}
		i := colIndexByName(t, f.Column)
		if i < 0 {
			continue // index field without a catalog column: no wrapper
		}
		emitColSearch(&b, base+names[i]+"Col", f)
	}

	if hasStatic {
		emitSearchStatic(&b, t, tableVar, base, tableType, keyField, keyCol, qname, names)
	}

	return assembleGoFile(opts, imports, b.String())
}

// emitColSearch writes the pg_search predicate methods onto one pass-B
// column type. ExactAny rides the text-family gate ("text[]" right-hand
// side), RangeTerm the range-cast gate (pdb.range_term); the rest of the
// operator vocabulary applies to any indexed field.
func emitColSearch(b *strings.Builder, colType string, f ir.SearchField) {
	m := func(sig, body string) {
		fmt.Fprintf(b, "func (c %s) %s {\n\treturn %s\n}\n\n", colType, sig, body)
	}
	m("Match(q string) pgb.Expr", "pgb.Match(c.Col, q)")
	m("MatchAll(q string) pgb.Expr", "pgb.MatchAll(c.Col, q)")
	m("Phrase(q string, slop int) pgb.Expr", "pgb.Phrase(c.Col, q, slop)")
	m("Exact(v any) pgb.Expr", "pgb.Exact(c.Col, v)")
	if textTypes[strings.ToLower(f.PGType)] {
		m("ExactAny(vs []string) pgb.Expr", `pgb.ExactAny(c.Col, vs, "text")`)
	}
	m("Fuzzy(q string, dist int, prefix bool) pgb.Expr", "pgb.Fuzzy(c.Col, q, dist, prefix)")
	m("Regex(pattern string) pgb.Expr", "pgb.Regex(c.Col, pattern)")
	m("Parse(q string) pgb.Expr", "pgb.Parse(c.Col, q, false)")
	m("MatchB(q string, b float64) pgb.Expr", "pgb.Boost(pgb.Match(c.Col, q), b)")
	if cast := rangeCastFor(f.PGType); cast != "" {
		m("RangeTerm(r any, mode string) pgb.Expr", fmt.Sprintf("pgb.RangeTerm(c.Col, r, %q, mode)", cast))
	}
	m("Snippet(startTag, endTag string, maxChars int) pgb.Expr", "pgb.Snippet(c.Col, startTag, endTag, maxChars)")
	m("Snippets(limitN, offsetN int, sortBy string) pgb.Expr", "pgb.Snippets(c.Col, limitN, offsetN, sortBy)")
	m("Highlight() pgb.Expr", "pgb.Highlight(c.Col)")
}

// pathWrapperName derives the wrapper type for an indexed JSON path: from
// the path segments first (metadata->'color' -> ProductMetadataColorCol),
// then the alias, then a Path suffix — never colliding with a pass-B
// column type (taken is updated in place).
func pathWrapperName(base string, f ir.SearchField, taken map[string]bool) string {
	cands := []string{base + pascalIdent(f.Path) + "Col"}
	if f.Alias != "" {
		cands = append(cands, base+pascalIdent(f.Alias)+"Col")
	}
	cands = append(cands, base+pascalIdent(f.Path)+"PathCol")
	for _, c := range cands {
		if !taken[c] {
			taken[c] = true
			return c
		}
	}
	return cands[len(cands)-1]
}

// emitPathSearch writes the wrapper type and predicate methods for an
// indexed JSON path. The path expression is re-emitted exactly as indexed
// (qualified by the table name) via pgb.Raw, so predicates match what the
// index covers. Boost composition (MatchB) and snippet projections are not
// available on path fields.
func emitPathSearch(b *strings.Builder, t ir.Table, f ir.SearchField, base string, taken map[string]bool) {
	colType := pathWrapperName(base, f, taken)
	qualified := core.QuoteIdent(t.Name) + "." + core.QuoteIdent(f.Column) + strings.TrimPrefix(f.Path, f.Column)
	// Aliased index fields resolve ONLY through their literal cast — a bare
	// path errors "field ... is not part of the pg_search index" (verified
	// on pg_search 0.25.9), so the predicate re-emits the cast too.
	if f.Alias != "" {
		qualified = "(" + qualified + ")::pdb.literal('alias=" +
			strings.ReplaceAll(f.Alias, "'", "''") + "')"
	}
	if f.Alias != "" {
		fmt.Fprintf(b, "// %s carries the pg_search surface for the indexed JSON path\n// %s (index alias %s): the path is re-emitted exactly as indexed,\n// through pgb.Raw. No Boost composition (MatchB) or snippets on path\n// fields.\n", colType, qualified, f.Alias)
	} else {
		fmt.Fprintf(b, "// %s carries the pg_search surface for the indexed JSON path\n// %s: the path is re-emitted exactly as indexed, through pgb.Raw.\n// No Boost composition (MatchB) or snippets on path fields.\n", colType, qualified)
	}
	fmt.Fprintf(b, "type %s struct{ pgb.Col }\n\n", colType)
	raw := func(sig, op, arg string) {
		fmt.Fprintf(b, "func (c %[1]s) %[2]s {\n\treturn pgb.Raw{SQL: %[3]s, Args: []any{%[4]s}}\n}\n\n",
			colType, sig, strconv.Quote(qualified+op), arg)
	}
	raw("Match(q string) pgb.Expr", " ||| ?", "q")
	raw("MatchAll(q string) pgb.Expr", " &&& ?", "q")
	raw("Exact(v any) pgb.Expr", " === ?", "v")
	raw("ExactAny(vs []string) pgb.Expr", " === ?::text[]", "vs")
	raw("Regex(pattern string) pgb.Expr", " @@@ pdb.regex(?)", "pattern")
	raw("Parse(q string) pgb.Expr", " @@@ pdb.parse(?)", "q")
}

// emitSearchStatic writes SearchProductsOpts, the hit type, the positional
// scan func and the Search<Table> static: the generic entry point queries
// the index key field with pdb.parse (the full query-string syntax),
// projects every plain column plus pdb.score(key) (and optionally one
// pdb.snippet fragment), and orders pdb.score(key) DESC, key ASC with a
// mandatory LIMIT so ParadeDB can push Top-K down.
func emitSearchStatic(b *strings.Builder, t ir.Table, tableVar, model, tableType, keyField, keyCol, qname string, names []string) {
	fmt.Fprintf(b, "// Search%sOpts keeps the search entry surface small: Limit caps the\n// result (<= 0 falls back to 20 — every generated search ends in an\n// ordered LIMIT so ParadeDB can push Top-K down); SnippetCol optionally\n// names one indexed column to project a pdb.snippet fragment for.\n", tableVar)
	fmt.Fprintf(b, "type Search%sOpts struct {\n\tLimit      int\n\tSnippetCol string\n}\n\n", tableVar)

	fmt.Fprintf(b, "// %sHit is one Search%s result row: the full model, the BM25 score,\n// and the snippet fragment (populated only when Search%sOpts.SnippetCol\n// selected one, NULL/zero otherwise).\n", model, tableVar, tableVar)
	fmt.Fprintf(b, "type %sHit struct {\n\tProduct %s\n\tScore   float64\n\tSnippet pgtype.Text\n}\n\n", model, model)

	fmt.Fprintf(b, "// Scan%s scans one Search%s row positionally: every %s column\n// in catalog order, then the score. Rows requested with a snippet\n// projection carry one extra trailing column — Search%s scans those\n// rows itself.\n", tableVar, tableVar, t.Name, tableVar)
	fmt.Fprintf(b, "func Scan%s(row pgx.CollectableRow) (%sHit, error) {\n\tvar h %sHit\n\tvar p %s\n", tableVar, model, model, model)
	dests := make([]string, 0, len(t.Columns)+1)
	for _, n := range names {
		dests = append(dests, "&p."+n)
	}
	dests = append(dests, "&h.Score")
	fmt.Fprintf(b, "\tif err := row.Scan(%s); err != nil {\n\t\treturn %sHit{}, err\n\t}\n\th.Product = p\n\treturn h, nil\n}\n\n", strings.Join(dests, ", "), model)

	fmt.Fprintf(b, "// Search%s runs the generic document query against the index key\n// field: WHERE %s @@@ pdb.parse($1) — pdb.parse carries ParadeDB's full\n// query-string syntax — selecting every column plus pdb.score(%s),\n// optionally one pdb.snippet fragment, ordered pdb.score(%s) DESC,\n// %s ASC and LIMIT-bounded.\n", tableVar, keyField, keyField, keyField, keyField)
	fmt.Fprintf(b, "func Search%s(ctx context.Context, exec pgb.DBTX, q string, o Search%sOpts) ([]%sHit, error) {\n", tableVar, tableVar, model)
	b.WriteString("\tlimit := o.Limit\n\tif limit <= 0 {\n\t\tlimit = 20\n\t}\n")
	fmt.Fprintf(b, "\tkey := %s\n", keyCol)
	cols := make([]string, 0, len(t.Columns)+2)
	for i := range t.Columns {
		cols = append(cols, colLiteral(t, t.Columns[i]))
	}
	cols = append(cols, "pgb.Score(key)")
	fmt.Fprintf(b, "\tcols := []pgb.Expr{%s}\n", strings.Join(cols, ", "))
	b.WriteString("\tif o.SnippetCol != \"\" {\n")
	b.WriteString("\t\tcols = append(cols, pgb.Snippet(pgb.Col{Table: " + strconv.Quote(t.Name) + ", Name: o.SnippetCol}, \"\", \"\", 0))\n\t}\n")
	fmt.Fprintf(b, "\trows, err := pgb.NewSelect(%s, cols...).\n", strconv.Quote(qname))
	b.WriteString("\t\tWhereExpr(pgb.Parse(key, q, false)).\n")
	b.WriteString("\t\tOrderBy(pgb.Desc(pgb.Score(key)), pgb.Asc(key)).\n")
	b.WriteString("\t\tLimit(limit).\n\t\tRun(ctx, exec)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\tif o.SnippetCol == \"\" {\n")
	fmt.Fprintf(b, "\t\treturn pgx.CollectRows(rows, Scan%s)\n\t}\n", tableVar)
	fmt.Fprintf(b, "\treturn pgx.CollectRows(rows, func(row pgx.CollectableRow) (%sHit, error) {\n", model)
	b.WriteString("\t\tvar h " + model + "Hit\n\t\tvar p " + model + "\n")
	snipDests := make([]string, 0, len(t.Columns)+2)
	for _, n := range names {
		snipDests = append(snipDests, "&p."+n)
	}
	snipDests = append(snipDests, "&h.Score", "&h.Snippet")
	fmt.Fprintf(b, "\t\tif err := row.Scan(%s); err != nil {\n\t\t\treturn %sHit{}, err\n\t\t}\n", strings.Join(snipDests, ", "), model)
	b.WriteString("\t\th.Product = p\n\t\treturn h, nil\n\t})\n}\n")
}
