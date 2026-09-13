package gen

import (
	"strings"

	"github.com/khoale-2804/pgb/ir"
)

const (
	// Import paths referenced by mapped types.
	pgtypeImport = "github.com/jackc/pgx/v5/pgtype"
	uuidImport   = "github.com/google/uuid"
	netipImport  = "net/netip"
	netImport    = "net"
)

// mapping is one builtin entry: the Go type for a NOT NULL column and the
// nullable wrapper. The empty notNull type means "any" (no Go type).
type mapping struct {
	notNull  string
	nullable string
}

func m(notNull, nullable string) mapping { return mapping{notNull, nullable} }

// pg family shortcuts.
func pg(t string) mapping { return m(t, t) } // wrapper handles NULL itself
// any_ maps untyped columns to "any". The empty string would leak into
// emitted struct fields as an unnamed (tag-only) field, so resolve here.
func any_() mapping { return m("any", "any") }

// builtinTypes is the frozen CONTRACTS maptype table. Deviations from the
// literal CONTRACTS text, forced by the pinned pgx/v5 (v5.7.5) pgtype set:
//   - pgtype has no Inet/CIDR: inet/cidr map to netip.Prefix (pgx v5's
//     preferred Go type).
//   - pgtype has no Macaddr/Macaddr8: both map to net.HardwareAddr.
//   - pgtype has no Timetz: timetz maps to pgtype.Time (same codec family).
//   - pgtype has no OID: oid maps to uint32 / pgtype.Uint32.
//   - pgtype.Multirange[T] constrains T to RangeValuer (i.e. a Range), so
//     every multirange maps to Multirange[Range[E]], not Multirange[E].
var builtinTypes = map[string]mapping{
	// text family
	"text":    m("string", "pgtype.Text"),
	"varchar": m("string", "pgtype.Text"),
	"bpchar":  m("string", "pgtype.Text"),
	"citext":  m("string", "pgtype.Text"),
	"name":    m("string", "pgtype.Text"),
	"xml":     m("string", "pgtype.Text"),
	// booleans
	"bool":    m("bool", "pgtype.Bool"),
	"boolean": m("bool", "pgtype.Bool"),
	// integers
	"int2":      m("int16", "pgtype.Int2"),
	"smallint":  m("int16", "pgtype.Int2"),
	"int4":      m("int32", "pgtype.Int4"),
	"integer":   m("int32", "pgtype.Int4"),
	"int":       m("int32", "pgtype.Int4"),
	"serial":    m("int32", "pgtype.Int4"),
	"int8":      m("int64", "pgtype.Int8"),
	"bigint":    m("int64", "pgtype.Int8"),
	"bigserial": m("int64", "pgtype.Int8"),
	// floats
	"float4":           m("float32", "pgtype.Float4"),
	"real":             m("float32", "pgtype.Float4"),
	"float8":           m("float64", "pgtype.Float8"),
	"double precision": m("float64", "pgtype.Float8"),
	// exact numerics + money
	"numeric": pg("pgtype.Numeric"),
	"decimal": pg("pgtype.Numeric"),
	"money":   pg("pgtype.Numeric"),
	// uuid
	"uuid": m("uuid.UUID", "uuid.NullUUID"),
	// binary + json
	"bytea": m("[]byte", "[]byte"),
	"json":  m("[]byte", "[]byte"),
	"jsonb": m("[]byte", "[]byte"),
	// dates and times (wrapper types keep pgtype.X even when NOT NULL,
	// matching sqlc-gen-go)
	"timestamptz": pg("pgtype.Timestamptz"),
	"timestamp":   pg("pgtype.Timestamptz"),
	"date":        pg("pgtype.Date"),
	"time":        pg("pgtype.Time"),
	"timetz":      pg("pgtype.Time"), // deviation: no pgtype.Timetz in pgx/v5
	"interval":    pg("pgtype.Interval"),
	// network types (deviation: no pgtype.Inet/CIDR/Macaddr/Macaddr8)
	"inet":     m("netip.Prefix", "netip.Prefix"),
	"cidr":     m("netip.Prefix", "netip.Prefix"),
	"macaddr":  m("net.HardwareAddr", "net.HardwareAddr"),
	"macaddr8": m("net.HardwareAddr", "net.HardwareAddr"),
	// identifiers and geometric types
	"xid8":    pg("pgtype.Uint64"),
	"tid":     pg("pgtype.TID"),
	"point":   pg("pgtype.Point"),
	"line":    pg("pgtype.Line"),
	"lseg":    pg("pgtype.Lseg"),
	"box":     pg("pgtype.Box"),
	"path":    pg("pgtype.Path"),
	"polygon": pg("pgtype.Polygon"),
	"circle":  pg("pgtype.Circle"),
	// untyped columns
	"tsvector": any_(),
	"tsquery":  any_(),
	"pg_lsn":   any_(),
	// ranges
	"int4range": pg("pgtype.Range[pgtype.Int4]"),
	"int8range": pg("pgtype.Range[pgtype.Int8]"),
	"numrange":  pg("pgtype.Range[pgtype.Numeric]"),
	"daterange": pg("pgtype.Range[pgtype.Date]"),
	"tsrange":   pg("pgtype.Range[pgtype.Timestamp]"),
	"tstzrange": pg("pgtype.Range[pgtype.Timestamptz]"),
	// multiranges
	"int4multirange": pg("pgtype.Multirange[pgtype.Range[pgtype.Int4]]"),
	"int8multirange": pg("pgtype.Multirange[pgtype.Range[pgtype.Int8]]"),
	"nummultirange":  pg("pgtype.Multirange[pgtype.Range[pgtype.Numeric]]"),
	"datemultirange": pg("pgtype.Multirange[pgtype.Range[pgtype.Date]]"),
	"tsmultirange":   pg("pgtype.Multirange[pgtype.Range[pgtype.Timestamp]]"),
	"tstzmultirange": pg("pgtype.Multirange[pgtype.Range[pgtype.Timestamptz]]"),
}

// arrayAny lists the base types whose array form has no pgx codec; CONTRACTS
// calls this "geo array skip" — the models pass must still emit a field, so
// it degrades to "any".
var arrayAny = map[string]bool{
	"tsvector": true,
	"tsquery":  true,
	"pg_lsn":   true,
	"tid":      true,
	"xid8":     true,
	"point":    true,
	"line":     true,
	"lseg":     true,
	"box":      true,
	"path":     true,
	"polygon":  true,
	"circle":   true,
}

// GoTypeFor maps one column of a KNOWN table to its Go type — the preferred
// entry point wherever the table is at hand. Column-keyed overrides resolve
// by exact match against "schema.table.column", then "table.column", then a
// bare "column" (first config-order win within a tier); a table-qualified
// key therefore never leaks onto a different table's same-named column —
// the suffix-match flaw in GoType (AUDIT.md P1 #4). DBType overrides, the
// builtin table, and enums behave exactly as in GoType.
func GoTypeFor(t ir.Table, c ir.Column, opts Options) (string, []string) {
	keys := []string{c.Name}
	if t.Name != "" {
		keys = []string{t.Name + "." + c.Name, c.Name}
		if t.Schema != "" {
			keys = []string{t.Schema + "." + t.Name + "." + c.Name, t.Name + "." + c.Name, c.Name}
		}
	}
	for _, key := range keys {
		for _, o := range opts.Overrides {
			if o.Column == key {
				return applyOverride(o)
			}
		}
	}
	return goTypeBase(c, opts)
}

// GoType maps one column to its Go type per the CONTRACTS table, returning
// the type expression and the import paths it needs. Resolution order:
//
//  1. config overrides with a Column match — matched by name suffix because
//     ir.Column alone carries no table context ("products.embedding" matches
//     EVERY table's embedding column); callers holding the table must use
//     GoTypeFor instead,
//  2. config overrides with a DBType match,
//  3. the builtin table above (NOT NULL strips the pgtype wrapper where a
//     bare value type exists),
//  4. generated enum types (opts.Enums, built by EnumTypes),
//  5. "any" for everything else.
func GoType(c ir.Column, opts Options) (string, []string) {
	for _, o := range opts.Overrides {
		if o.Column != "" && overrideMatchesColumn(o.Column, c.Name) {
			return applyOverride(o)
		}
	}
	return goTypeBase(c, opts)
}

// goTypeBase is the table-agnostic half of type resolution: DBType
// overrides, then the builtin table, then enums, then "any".
func goTypeBase(c ir.Column, opts Options) (string, []string) {
	for _, o := range opts.Overrides {
		if o.DBType != "" && strings.EqualFold(o.DBType, c.PGType) {
			return applyOverride(o)
		}
	}
	base := strings.ToLower(c.PGType)
	if c.IsArray {
		return arrayGoType(base, opts)
	}
	if mp, ok := builtinTypes[base]; ok {
		typ := mp.notNull
		if !c.NotNull {
			typ = mp.nullable
		}
		return withImports(typ)
	}
	if opts.Enums != nil {
		if g, ok := opts.Enums[base]; ok {
			return g, nil // same generated package; no import
		}
	}
	return "any", nil
}

// Nullable reports whether the column can carry SQL NULL. Overrides do not
// change PG nullability, so this is simply !c.NotNull.
func Nullable(c ir.Column) bool {
	return !c.NotNull
}

// EnumTypes builds the enum-name -> generated-Go-type map consumed by
// GoType (via Options.Enums). Keys are lowercased base type names.
func EnumTypes(sch ir.Schema) map[string]string {
	enums := make(map[string]string, len(sch.Enums))
	for _, e := range sch.Enums {
		enums[strings.ToLower(e.Name)] = pascalIdent(e.Name)
	}
	return enums
}

// arrayGoType maps base -> slice type. Arrays are never wrapped (a NULL
// array scans as a nil slice); the element type is the NOT NULL mapping.
func arrayGoType(base string, opts Options) (string, []string) {
	if arrayAny[base] {
		return "any", nil
	}
	if mp, ok := builtinTypes[base]; ok {
		return withImports("[]" + mp.notNull)
	}
	if opts.Enums != nil {
		if g, ok := opts.Enums[base]; ok {
			return "[]" + g, nil
		}
	}
	return "any", nil
}

// overrideMatchesColumn reports whether an override keyed "table.column" or
// "schema.table.column" (or a bare column name) applies to col.
func overrideMatchesColumn(key, col string) bool {
	return key == col || strings.HasSuffix(key, "."+col)
}

// applyOverride renders an override as (type expression, imports).
func applyOverride(o TypeOverride) (string, []string) {
	if o.Import == "" {
		return o.Type, nil // bare expression: map[string]any, time.Time, ...
	}
	q := o.Package
	if q == "" {
		q = importQualifier(o.Import)
	}
	return q + "." + o.Type, []string{o.Import}
}

// withImports splits a type expression's known import paths.
func withImports(typ string) (string, []string) {
	var imports []string
	for _, imp := range []string{pgtypeImport, uuidImport, netipImport, netImport} {
		if strings.Contains(typ, importQualifier(imp)+".") {
			imports = append(imports, imp)
		}
	}
	return typ, imports
}
