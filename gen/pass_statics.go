package gen

import (
	"fmt"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/khoale-2804/pgb/core"
	"github.com/khoale-2804/pgb/ir"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// PassStatics emits pass C: "<table>_repo.gen.go" per non-skip table —
// filter/set/params structs, the positional scan func, and the static CRUD
// layer — plus one shared "pgb_helpers.gen.go" with the three-state Set[T]
// helper. Statics compile their filters down to the same core expression
// tree as the builders (no private SQL path); single-row lookups map
// pgx.ErrNoRows to pgb.ErrNotFound, and bulk mutations refuse an empty
// where slice with pgb.ErrNoWhere.
//
// Frozen-scope deviations from docs/generated-code/{statics,pagination}:
//   - core ships no Set/ListOpt types, so Set[T]/SetOf/SetNull/Opt are
//     emitted into the shared helpers file instead of core;
//   - ListUsers takes opts ...pgb.ListOpt (Limit/Offset, zero = no clause);
//   - keyset Page functions are not generated yet (M1);
//   - UpsertUser is generated only for a single-column key (primary key or
//     unique, resolved by heuristicPK from the DDL extraction pass) and
//     takes the key value explicitly: ON CONFLICT (key) DO UPDATE SET
//     <set cols>;
//   - CopyFromUsers (COPY protocol) is not generated yet;
//   - InsertUsers is skipped when an insertable column's Go type is "any"
//     or a generated enum (pgx has no array codec for those element types).
func PassStatics(sch ir.Schema, opts Options, dir DirectiveSet) ([]plugin.File, error) {
	opts = normalizeOptions(sch, opts)
	var files []plugin.File
	any := false
	for _, t := range sch.Tables {
		if t.Skip || dir.Table(t.Schema, t.Name).Skip || !genSchema(t.Schema) {
			continue
		}
		any = true
		src, err := staticsFile(sch, t, opts, dir)
		if err != nil {
			return nil, fmt.Errorf("pgb: statics for %s: %w", t.Name, err)
		}
		_, _, fb := goTableNames(sch, t)
		files = append(files, plugin.File{Name: fb + "_repo.gen.go", Contents: src})
	}
	if any {
		src, err := helpersFile(opts)
		if err != nil {
			return nil, fmt.Errorf("pgb: helpers: %w", err)
		}
		files = append(files, plugin.File{Name: "pgb_helpers.gen.go", Contents: src})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// helpersFile emits the shared three-state Set helper and the filter Opt
// pointer helper. core has neither (the runtime ships SetClause only), so
// generated code defines them once per package.
func helpersFile(opts Options) ([]byte, error) {
	body := `// Set is the three-state field wrapper for generated Set structs: the
// zero value skips the column entirely, SetOf writes a value, SetNull
// writes NULL.
type Set[T any] struct {
	V     T
	Valid bool
	Null  bool
}

// SetOf returns a Set that writes v.
func SetOf[T any](v T) Set[T] { return Set[T]{V: v, Valid: true} }

// SetNull returns a Set that writes NULL.
func SetNull[T any]() Set[T] { return Set[T]{Valid: true, Null: true} }

// Opt returns a pointer to v — the "turn this filter on" helper for the
// generated Filter structs.
func Opt[T any](v T) *T { return &v }
`
	return assembleGoFile(opts, nil, body)
}

// heuristicPK resolves the single-column key the single-row statics key on:
// an explicit single-column primary key first, then an explicit single-column
// unique constraint — both filled by the DDL extraction pass (load_ddl.go).
// A composite key, or none at all, yields nil (no Get/Update/Upsert/Delete
// and no upsert). Tables carrying no explicit key data whatsoever (hand-built
// IRs — unit tests — or a schema file that failed to parse) keep the v0.1
// fallback: a single NOT NULL "id" column.
func heuristicPK(t ir.Table) []string {
	if len(t.PrimaryKey) == 1 && t.PrimaryKey[0] != "" {
		return []string{t.PrimaryKey[0]}
	}
	for _, u := range t.Uniques {
		if len(u) == 1 && u[0] != "" {
			return []string{u[0]}
		}
	}
	if len(t.PrimaryKey) == 0 && len(t.Uniques) == 0 {
		found := false
		unique := true
		for _, c := range t.Columns {
			if c.Name != "id" {
				continue
			}
			if found || !c.NotNull {
				unique = false
				break
			}
			found = true
		}
		if found && unique {
			return []string{"id"}
		}
	}
	return nil
}

// goParamName renders a column name as a safe unexported Go parameter name:
// Go keywords get a trailing underscore ("select" -> "select_"); names that
// cannot be identifiers at all fall back to "id".
func goParamName(col string) string {
	if token.IsIdentifier(col) {
		return col
	}
	if token.IsKeyword(col) {
		return col + "_"
	}
	return "id"
}

// isSerial reports whether the column is a serial/bigserial shorthand.
func isSerial(c ir.Column) bool {
	switch strings.ToLower(c.PGType) {
	case "serial", "smallserial", "bigserial":
		return true
	}
	return false
}

// insertableIdx returns the indexes of INSERT-list columns: generated
// columns and serials are always out; default-bearing columns join only
// when opts.IncludeDefaults.
func insertableIdx(t ir.Table, opts Options) []int {
	var out []int
	for i, c := range t.Columns {
		if c.Generated != "" || isSerial(c) {
			continue
		}
		if c.HasDefault && !opts.IncludeDefaults {
			continue
		}
		out = append(out, i)
	}
	return out
}

// settableIdx returns the indexes of SET-list columns: not pgb:no_patch,
// not generated, not serial, and not the primary key.
func settableIdx(t ir.Table, opts Options, dir DirectiveSet, pkName string) []int {
	var out []int
	for i, c := range t.Columns {
		if c.Generated != "" || isSerial(c) || c.Name == pkName {
			continue
		}
		if dir.Column(t.Schema, t.Name, c.Name).NoPatch {
			continue
		}
		out = append(out, i)
	}
	return out
}

// lowerFirst lowercases the first rune ("Users" -> "users") for unexported
// generated helper names.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] = r[0] - 'A' + 'a'
	}
	return string(r)
}

// staticsFile emits the repo file for one table.
func staticsFile(sch ir.Schema, t ir.Table, opts Options, dir DirectiveSet) ([]byte, error) {
	tableVar, singular, _ := goTableNames(sch, t)
	model := singular
	qname := qualifiedTable(t)
	names := fieldNames(t.Columns)
	bulk := ""
	if singular == tableVar {
		bulk = "Bulk" // singular == plural ("series"): bulk names need a suffix
	}

	pkName := ""
	if pk := heuristicPK(t); pk != nil {
		pkName = pk[0]
	}
	// The single-row statics take the key value as a named parameter — the
	// column's own name ("id" for the common case), keyword-sanitized.
	pkParam := goParamName(pkName)
	pkIdx := -1
	pkType := ""
	for i, c := range t.Columns {
		if c.Name == pkName {
			pkIdx = i
			pkType, _ = resolveColType(t, c, opts, dir)
			break
		}
	}
	writable := !t.View
	var insIdx, setIdx []int
	if writable {
		insIdx = insertableIdx(t, opts)
		setIdx = settableIdx(t, opts, dir, pkName)
	}

	imports := []string{"context", opts.Core, pgxImport}
	for _, c := range t.Columns {
		_, imp := resolveColType(t, c, opts, dir)
		imports = append(imports, imp...)
	}
	if pkIdx >= 0 {
		imports = append(imports, "errors") // GetUser's ErrNoRows mapping
	}

	// Reusable emission lists over all columns (catalog order).
	retExprs := make([]string, len(t.Columns)) // RETURNING exprs, raw Col literals
	for i := range t.Columns {
		retExprs[i] = colLiteral(t, t.Columns[i])
	}
	retArgs := strings.Join(retExprs, ", ")
	scanArgs := make([]string, len(t.Columns)) // &m.Field list for row.Scan
	for i := range t.Columns {
		scanArgs[i] = "&m." + names[i]
	}
	scanList := strings.Join(scanArgs, ", ")
	retSQL := make([]string, len(t.Columns)) // quoted col list for raw SQL consts
	for i := range t.Columns {
		retSQL[i] = core.QuoteIdent(t.Columns[i].Name)
	}
	retCols := strings.Join(retSQL, ", ")

	var b strings.Builder

	// ---- filter struct + predicate compiler ----
	b.WriteString("// " + model + "Filter narrows List" + tableVar + " and Count" + tableVar + ":\n")
	b.WriteString("// nil / zero fields are ignored; Extra composes raw predicates.\n")
	b.WriteString("type " + model + "Filter struct {\n")
	var w strings.Builder
	w.WriteString("\tvar w []pgb.Expr\n")
	seenF := map[string]int{}
	uniqF := func(base string) string {
		n := base
		if k := seenF[strings.ToLower(base)]; k > 0 {
			n = base + strconv.Itoa(k+1)
		}
		seenF[strings.ToLower(base)]++
		return n
	}
	for i, c := range t.Columns {
		if dir.Column(t.Schema, t.Name, c.Name).NoFilter {
			continue
		}
		typ, _ := resolveColType(t, c, opts, dir)
		caps := capsFor(c, typ, opts)
		if !caps.eq && !caps.in && !caps.text && !caps.ordered {
			continue // enum arrays / any-typed arrays: nothing to filter with
		}
		col := tableVar + "." + accessorName(names[i]) + "()"
		if caps.eq {
			eq := uniqF(names[i])
			b.WriteString("\t" + eq + " *" + typ + "\n")
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.Eq(*f.%s))\n\t}\n", eq, col, eq)
		}
		if caps.in {
			in := uniqF(names[i] + "In")
			b.WriteString("\t" + in + " []" + typ + "\n")
			fmt.Fprintf(&w, "\tif len(f.%s) > 0 {\n\t\tw = append(w, %s.In(f.%s...))\n\t}\n", in, col, in)
		}
		if caps.text {
			like := uniqF(names[i] + "Like")
			ilike := uniqF(names[i] + "ILike")
			b.WriteString("\t" + like + " *string\n\t" + ilike + " *string\n")
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.Like(*f.%s))\n\t}\n", like, col, like)
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.ILike(*f.%s))\n\t}\n", ilike, col, ilike)
		}
		if caps.ordered {
			gt := uniqF(names[i] + "Gt")
			lt := uniqF(names[i] + "Lt")
			gte := uniqF(names[i] + "Gte")
			lte := uniqF(names[i] + "Lte")
			b.WriteString("\t" + gt + " *" + typ + "\n\t" + lt + " *" + typ + "\n\t" + gte + " *" + typ + "\n\t" + lte + " *" + typ + "\n")
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.Gt(*f.%s))\n\t}\n", gt, col, gt)
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.Lt(*f.%s))\n\t}\n", lt, col, lt)
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.Gte(*f.%s))\n\t}\n", gte, col, gte)
			fmt.Fprintf(&w, "\tif f.%s != nil {\n\t\tw = append(w, %s.Lte(*f.%s))\n\t}\n", lte, col, lte)
		}
	}
	extra := uniqF("Extra")
	b.WriteString("\t" + extra + " []pgb.Expr\n}\n\n")
	fmt.Fprintf(&w, "\tw = append(w, f.%s...)\n\treturn w\n}\n\n", extra)

	lw := lowerFirst(tableVar) + "FilterWhere"
	b.WriteString("// " + lw + " compiles f into the ANDed predicate list; an empty\n// filter yields an empty list (no WHERE).\n")
	b.WriteString("func " + lw + "(f " + model + "Filter) []pgb.Expr {\n")
	b.WriteString(w.String())

	// ---- set struct + SET-clause lines ----
	var setLines strings.Builder
	if len(setIdx) > 0 {
		b.WriteString("// " + model + "Set is the three-state UPDATE payload: a zero Set field\n")
		b.WriteString("// skips its column, SetOf writes a value, SetNull writes NULL.\n")
		b.WriteString("type " + model + "Set struct {\n")
		seenS := map[string]int{}
		uniqS := func(base string) string {
			n := base
			if k := seenS[strings.ToLower(base)]; k > 0 {
				n = base + strconv.Itoa(k+1)
			}
			seenS[strings.ToLower(base)]++
			return n
		}
		for _, i := range setIdx {
			typ, _ := resolveColType(t, t.Columns[i], opts, dir)
			f := uniqS(names[i])
			b.WriteString("\t" + f + " Set[" + typ + "]\n")
			fmt.Fprintf(&setLines, "\tif s.%s.Valid {\n\t\tn++\n\t\tif s.%s.Null {\n\t\t\tu.Set(%s, pgb.Lit{V: nil})\n\t\t} else {\n\t\t\tu.Set(%s, %s)\n\t\t}\n\t}\n",
				f, f, strconv.Quote(t.Columns[i].Name), strconv.Quote(t.Columns[i].Name),
				litExpr(t.Columns[i], "s."+f+".V", opts))
		}
		b.WriteString("}\n\n")
	}

	// ---- insert params struct ----
	type paramCol struct {
		idx   int
		field string
	}
	var params []paramCol
	b.WriteString("// Insert" + singular + "Params carries the INSERT column values: generated\n")
	b.WriteString("// and serial columns are out, default-bearing columns join only with\n// the include_defaults option.\n")
	b.WriteString("type Insert" + singular + "Params struct {\n")
	seenP := map[string]int{}
	uniqP := func(base string) string {
		n := base
		if k := seenP[strings.ToLower(base)]; k > 0 {
			n = base + strconv.Itoa(k+1)
		}
		seenP[strings.ToLower(base)]++
		return n
	}
	for _, i := range insIdx {
		typ, _ := resolveColType(t, t.Columns[i], opts, dir)
		f := uniqP(names[i])
		b.WriteString("\t" + f + " " + typ + "\n")
		params = append(params, paramCol{idx: i, field: f})
	}
	b.WriteString("}\n\n")

	// ---- positional scan func ----
	b.WriteString("// scan" + singular + " scans one row positionally over every column in\n// catalog order — the single scan path for all generated reads.\n")
	b.WriteString("func scan" + singular + "(row pgx.CollectableRow) (" + model + ", error) {\n")
	b.WriteString("\tvar m " + model + "\n")
	if len(t.Columns) > 0 {
		fmt.Fprintf(&b, "\tif err := row.Scan(%s); err != nil {\n\t\treturn %s{}, err\n\t}\n", scanList, model)
	}
	b.WriteString("\treturn m, nil\n}\n\n")

	// collectSingle is the shared "one row back or ErrNotFound" tail for the
	// RETURNING-based writes.
	collect := func() {
		b.WriteString("\tus, err := pgx.CollectRows(rows, scan" + singular + ")\n")
		b.WriteString("\tif err != nil {\n\t\treturn " + model + "{}, err\n\t}\n")
		b.WriteString("\tif len(us) == 0 {\n\t\treturn " + model + "{}, pgb.ErrNotFound\n\t}\n")
		b.WriteString("\treturn us[0], nil\n}\n\n")
	}

	// ---- Get (single-column PK/unique only) ----
	if pkIdx >= 0 {
		b.WriteString("// Get" + singular + " returns one row by " + pkName + "; pgb.ErrNotFound when absent\n// (pgx.ErrNoRows mapped — errors.Is keeps working for both).\n")
		b.WriteString("func Get" + singular + "(ctx context.Context, exec pgb.DBTX, " + pkParam + " " + pkType + ") (" + model + ", error) {\n")
		fmt.Fprintf(&b, "\tsql, args := %s.Select().Where(%s.%s().Eq(%s)).SQL()\n", tableVar, tableVar, accessorName(names[pkIdx]), pkParam)
		b.WriteString("\tvar m " + model + "\n")
		fmt.Fprintf(&b, "\terr := exec.QueryRow(ctx, sql, args...).Scan(%s)\n", scanList)
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\tif errors.Is(err, pgx.ErrNoRows) {\n\t\t\treturn " + model + "{}, pgb.ErrNotFound\n\t\t}\n")
		b.WriteString("\t\treturn " + model + "{}, err\n\t}\n")
		b.WriteString("\treturn m, nil\n}\n\n")
	}

	// ---- List / Count ----
	b.WriteString("// List" + tableVar + " returns the rows matching f; limit <= 0 means no LIMIT.\n")
	b.WriteString("func List" + tableVar + "(ctx context.Context, exec pgb.DBTX, f " + model + "Filter, opts ...pgb.ListOpt) ([]" + model + ", error) {\n")
	b.WriteString("\tsel := " + tableVar + ".Select().Where(" + lw + "(f)...).ApplyList(opts...)\n")
	b.WriteString("\trows, err := sel.Run(ctx, exec)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn pgx.CollectRows(rows, scan" + singular + ")\n}\n\n")

	b.WriteString("// Count" + tableVar + " counts the rows matching f.\n")
	b.WriteString("func Count" + tableVar + "(ctx context.Context, exec pgb.DBTX, f " + model + "Filter) (int64, error) {\n")
	fmt.Fprintf(&b, "\tsql, args := pgb.NewSelect(%s, pgb.Raw{SQL: \"count(*)\"}).Where(%s(f)...).SQL()\n", strconv.Quote(qname), lw)
	b.WriteString("\tvar n int64\n")
	b.WriteString("\tif err := exec.QueryRow(ctx, sql, args...).Scan(&n); err != nil {\n\t\treturn 0, err\n\t}\n")
	b.WriteString("\treturn n, nil\n}\n\n")

	if writable {
		// ---- InsertUser ----
		b.WriteString("// Insert" + singular + " inserts one row and returns it (RETURNING every\n// column, defaults included).\n")
		b.WriteString("func Insert" + singular + "(ctx context.Context, exec pgb.DBTX, p Insert" + singular + "Params) (" + model + ", error) {\n")
		if len(params) > 0 {
			cols := make([]string, len(params))
			vals := make([]string, len(params))
			for k, pc := range params {
				cols[k] = strconv.Quote(t.Columns[pc.idx].Name)
				vals[k] = litExpr(t.Columns[pc.idx], "p."+pc.field, opts)
			}
			fmt.Fprintf(&b, "\trows, err := pgb.NewInsert(%s,\n\t\t[]string{%s},\n\t\t[]pgb.Expr{%s},\n\t).Returning(%s).Run(ctx, exec)\n",
				strconv.Quote(qname), strings.Join(cols, ", "), strings.Join(vals, ", "), retArgs)
		} else {
			fmt.Fprintf(&b, "\trows, err := pgb.NewInsert(%s).Returning(%s).Run(ctx, exec)\n", strconv.Quote(qname), retArgs)
		}
		b.WriteString("\tif err != nil {\n\t\treturn " + model + "{}, err\n\t}\n")
		collect()

		// ---- InsertUsers (unnest batch) ----
		// pgx has no codec for []any or []Enum arrays, so the batch form is
		// skipped when an insertable column resolves to either.
		batchable := true
		for _, pc := range params {
			typ, _ := resolveColType(t, t.Columns[pc.idx], opts, dir)
			if typ == "any" || opts.Enums[strings.ToLower(t.Columns[pc.idx].PGType)] != "" {
				batchable = false
				break
			}
		}
		if len(params) > 0 && batchable {
			casts := make([]string, len(params))
			colNames := make([]string, len(params))
			for k, pc := range params {
				casts[k] = "$" + strconv.Itoa(k+1) + "::" + strings.ToLower(t.Columns[pc.idx].PGType) + "[]"
				colNames[k] = core.QuoteIdent(t.Columns[pc.idx].Name)
			}
			sql := "INSERT INTO " + core.QuoteIdent(strings.Split(qname, ".")...) +
				" (" + strings.Join(colNames, ", ") + ") SELECT * FROM unnest(" +
				strings.Join(casts, ", ") + ") RETURNING " + retCols
			fmt.Fprintf(&b, "const insert%[1]s%[2]sSQL = %[3]s\n\n", tableVar, bulk, strconv.Quote(sql))
			b.WriteString("// Insert" + tableVar + bulk + " inserts a whole batch in one round trip via\n// unnest and returns every inserted row.\n")
			b.WriteString("func Insert" + tableVar + bulk + "(ctx context.Context, exec pgb.DBTX, ps []Insert" + singular + "Params) ([]" + model + ", error) {\n")
			b.WriteString("\tif len(ps) == 0 {\n\t\treturn nil, nil\n\t}\n")
			var arrVars []string
			for _, pc := range params {
				typ, _ := resolveColType(t, t.Columns[pc.idx], opts, dir)
				fmt.Fprintf(&b, "\tcol%s := make([]%s, len(ps))\n", pc.field, typ)
				arrVars = append(arrVars, "col"+pc.field)
			}
			b.WriteString("\tfor i, p := range ps {\n")
			for _, pc := range params {
				fmt.Fprintf(&b, "\t\tcol%s[i] = p.%s\n", pc.field, pc.field)
			}
			b.WriteString("\t}\n")
			fmt.Fprintf(&b, "\targs := make([]any, 0, %d*len(ps))\n", len(params))
			b.WriteString("\targs = append(args, " + strings.Join(arrVars, ", ") + ")\n")
			fmt.Fprintf(&b, "\trows, err := exec.Query(ctx, insert%[1]s%[2]sSQL, args...)\n", tableVar, bulk)
			b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
			b.WriteString("\treturn pgx.CollectRows(rows, scan" + singular + ")\n}\n\n")
		}

		// ---- UpdateUser (three-state set, single-column key) ----
		if pkIdx >= 0 && len(setIdx) > 0 {
			b.WriteString("// Update" + singular + " applies the non-zero fields of s to one row and\n// returns the updated row; pgb.ErrNotFound when absent.\n")
			b.WriteString("func Update" + singular + "(ctx context.Context, exec pgb.DBTX, " + pkParam + " " + pkType + ", s " + model + "Set) (" + model + ", error) {\n")
			b.WriteString("\tu := " + tableVar + ".Update()\n\tn := 0\n")
			b.WriteString(setLines.String())
			fmt.Fprintf(&b, "\tif n == 0 {\n\t\treturn Get%s(ctx, exec, %s)\n\t}\n", singular, pkParam)
			fmt.Fprintf(&b, "\tu.Where(%s.%s().Eq(%s)).Returning(%s)\n", tableVar, accessorName(names[pkIdx]), pkParam, retArgs)
			b.WriteString("\trows, err := u.Run(ctx, exec)\n")
			b.WriteString("\tif err != nil {\n\t\treturn " + model + "{}, err\n\t}\n")
			collect()
		}

		// ---- UpdateUsers ----
		if len(setIdx) > 0 {
			b.WriteString("// Update" + tableVar + bulk + " applies s to every row matching where and\n// returns the affected count; an empty where is refused with\n// pgb.ErrNoWhere before any SQL is sent.\n")
			b.WriteString("func Update" + tableVar + bulk + "(ctx context.Context, exec pgb.DBTX, where []pgb.Expr, s " + model + "Set) (int64, error) {\n")
			b.WriteString("\tu := " + tableVar + ".Update()\n\tn := 0\n")
			b.WriteString(setLines.String())
			b.WriteString("\tif n == 0 {\n\t\treturn 0, nil\n\t}\n")
			b.WriteString("\tif len(where) == 0 {\n\t\treturn 0, pgb.ErrNoWhere\n\t}\n")
			b.WriteString("\tu.Where(where...)\n")
			b.WriteString("\ttag, err := u.Exec(ctx, exec)\n")
			b.WriteString("\tif err != nil {\n\t\treturn 0, err\n\t}\n")
			b.WriteString("\treturn tag.RowsAffected(), nil\n}\n\n")
		}

		// ---- UpsertUser (single-column PK/unique target only) ----
		if pkIdx >= 0 && len(params) > 0 {
			ucols := []string{strconv.Quote(pkName)}
			uvals := []string{"pgb.Lit{V: " + pkParam + "}"}
			for _, pc := range params {
				if t.Columns[pc.idx].Name == pkName {
					continue // the key comes from the explicit argument
				}
				ucols = append(ucols, strconv.Quote(t.Columns[pc.idx].Name))
				uvals = append(uvals, litExpr(t.Columns[pc.idx], "p."+pc.field, opts))
			}
			b.WriteString("// Upsert" + singular + " inserts p under " + pkName + ", or on conflict updates\n// every settable column from the proposed row (EXCLUDED.*) and returns\n// the resulting row; pgb.ErrNotFound when DO NOTHING matched.\n")
			b.WriteString("func Upsert" + singular + "(ctx context.Context, exec pgb.DBTX, " + pkParam + " " + pkType + ", p Insert" + singular + "Params) (" + model + ", error) {\n")
			fmt.Fprintf(&b, "\trows, err := pgb.NewInsert(%s,\n\t\t[]string{%s},\n\t\t[]pgb.Expr{%s},\n\t).OnConflict(pgb.OnConflict{\n",
				strconv.Quote(qname), strings.Join(ucols, ", "), strings.Join(uvals, ", "))
			fmt.Fprintf(&b, "\t\tTarget: []string{%s},\n", strconv.Quote(pkName))
			if len(setIdx) > 0 {
				b.WriteString("\t\tSets: []pgb.SetClause{\n")
				for _, i := range setIdx {
					fmt.Fprintf(&b, "\t\t\t{Col: %s, E: pgb.Col{Table: \"excluded\", Name: %s}},\n",
						strconv.Quote(t.Columns[i].Name), strconv.Quote(t.Columns[i].Name))
				}
				b.WriteString("\t\t},\n")
			} else {
				b.WriteString("\t\tDoNothing: true,\n")
			}
			fmt.Fprintf(&b, "\t}).Returning(%s).Run(ctx, exec)\n", retArgs)
			b.WriteString("\tif err != nil {\n\t\treturn " + model + "{}, err\n\t}\n")
			collect()
		}

		// ---- DeleteUser / DeleteUsers ----
		if pkIdx >= 0 {
			b.WriteString("// Delete" + singular + " removes one row by " + pkName + ".\n")
			b.WriteString("func Delete" + singular + "(ctx context.Context, exec pgb.DBTX, " + pkParam + " " + pkType + ") error {\n")
			fmt.Fprintf(&b, "\t_, err := %s.Delete().Where(%s.%s().Eq(%s)).Exec(ctx, exec)\n\treturn err\n}\n\n", tableVar, tableVar, accessorName(names[pkIdx]), pkParam)
		}
		b.WriteString("// Delete" + tableVar + bulk + " removes every row matching where and returns the\n// affected count; an empty where is refused with pgb.ErrNoWhere.\n")
		b.WriteString("func Delete" + tableVar + bulk + "(ctx context.Context, exec pgb.DBTX, where []pgb.Expr) (int64, error) {\n")
		b.WriteString("\tif len(where) == 0 {\n\t\treturn 0, pgb.ErrNoWhere\n\t}\n")
		fmt.Fprintf(&b, "\ttag, err := %s.Delete().Where(where...).Exec(ctx, exec)\n", tableVar)
		b.WriteString("\tif err != nil {\n\t\treturn 0, err\n\t}\n")
		b.WriteString("\treturn tag.RowsAffected(), nil\n}\n")
	}

	return assembleGoFile(opts, imports, b.String())
}
