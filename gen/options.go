package gen

import (
	"encoding/json"
	"strings"

	"github.com/sqlc-dev/plugin-sdk-go/plugin"
)

// Options is the pgb plugin configuration, parsed from req.PluginOptions
// (the `options:` block under the plugin entry in sqlc.yaml).
type Options struct {
	// Package is the name of the generated package. Default "db".
	Package string
	// Core is the import path of the pgb runtime package. Default
	// "github.com/khoale-2804/pgb/core".
	Core string
	// Target is the Postgres emitter dialect gate: "18" or "19". Default "18".
	Target string
	// Overrides remaps Postgres types to Go types (column match wins over
	// DBType match; see maptype.go for the full resolution order).
	Overrides []TypeOverride
	// IncludeDefaults keeps default-bearing columns in INSERT column lists.
	IncludeDefaults bool

	// Enums maps a Postgres enum type name (lowercased base name, e.g.
	// "user_status") to its generated Go type name ("UserStatus"). Populated
	// from the catalog by Build or EnumTypes; readers must nil-check.
	Enums map[string]string

	// SqlcVersion is req.SqlcVersion, captured by ParseOptions so the
	// generated-file versions header stays deterministic.
	SqlcVersion string
}

// TypeOverride remaps one Postgres type to a Go type.
//
// Accepts the sqlc-gen-go option shapes:
//
//	{ "db_type": "vector", "go_type": "github.com/pgvector/pgvector-go.Vector" }
//	{ "column": "products.embedding", "go_type": { "import": "...", "package": "...", "type": "..." } }
//	{ "db_type": "vector", "import": "...", "package": "...", "type": "..." }
type TypeOverride struct {
	// DBType is the Postgres type name, e.g. "vector".
	DBType string
	// Column is the catalog key the override applies to: "table.column" or
	// "schema.table.column". Wins over DBType.
	Column string
	// Import is the Go import path providing the type ("" for bare type
	// expressions like "map[string]any").
	Import string
	// Package is the import qualifier; derived from Import when empty.
	Package string
	// Type is the Go type expression, e.g. "Vector" or "map[string]any".
	Type string
}

// ParseOptions decodes req.PluginOptions (JSON). Unknown or malformed fields
// are ignored; defaults: Package "db", Core "github.com/khoale-2804/pgb/core",
// Target "18".
func ParseOptions(req *plugin.GenerateRequest) Options {
	opts := Options{
		Package: "db",
		Core:    "github.com/khoale-2804/pgb/core",
		Target:  "18",
	}
	if req == nil {
		return opts
	}
	opts.SqlcVersion = req.SqlcVersion
	if len(req.PluginOptions) == 0 {
		return opts
	}
	var raw struct {
		Package         string            `json:"package"`
		Core            string            `json:"core"`
		Target          string            `json:"target"`
		IncludeDefaults bool              `json:"include_defaults"`
		Overrides       []json.RawMessage `json:"overrides"`
	}
	if err := json.Unmarshal(req.PluginOptions, &raw); err != nil {
		return opts
	}
	if raw.Package != "" {
		opts.Package = raw.Package
	}
	if raw.Core != "" {
		opts.Core = raw.Core
	}
	if raw.Target == "18" || raw.Target == "19" {
		opts.Target = raw.Target
	}
	opts.IncludeDefaults = raw.IncludeDefaults
	for _, r := range raw.Overrides {
		if o, ok := parseOverrideJSON(r); ok {
			opts.Overrides = append(opts.Overrides, o)
		}
	}
	return opts
}

// parseOverrideJSON accepts both the flat form (db_type/column plus import,
// package, type, or go_type) and the structured go_type form.
func parseOverrideJSON(raw json.RawMessage) (TypeOverride, bool) {
	var flat struct {
		DBType  string          `json:"db_type"`
		Column  string          `json:"column"`
		GoType  json.RawMessage `json:"go_type"`
		Import  string          `json:"import"`
		Package string          `json:"package"`
		Type    string          `json:"type"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return TypeOverride{}, false
	}
	o := TypeOverride{
		DBType:  flat.DBType,
		Column:  flat.Column,
		Import:  flat.Import,
		Package: flat.Package,
		Type:    flat.Type,
	}
	// Structured go_type form.
	if len(flat.GoType) > 0 && string(flat.GoType) != "null" {
		var gt struct {
			Import  string `json:"import"`
			Package string `json:"package"`
			Type    string `json:"type"`
		}
		if err := json.Unmarshal(flat.GoType, &gt); err == nil && gt.Type != "" {
			o.Import = gt.Import
			o.Package = gt.Package
			o.Type = gt.Type
		}
	}
	// String go_type form ("github.com/pgvector/pgvector-go.Vector").
	if o.Type == "" && len(flat.GoType) > 0 {
		var s string
		if err := json.Unmarshal(flat.GoType, &s); err == nil {
			parsed := parseGoType(s)
			o.Import = parsed.Import
			o.Package = parsed.Package
			o.Type = parsed.Type
		}
	}
	if o.Type == "" {
		return TypeOverride{}, false
	}
	return o, true
}

// parseGoType splits a "path/to/pkg.Type" string into import path, package
// qualifier, and type name. The split point is the LAST DOT — the package
// qualifier is the last import-path segment (pgvector-go.Vector means
// import "github.com/pgvector/pgvector-go" with package name pgvector).
// Bare type expressions ("map[string]any", "time.Time") are kept verbatim
// in Type with no Import.
func parseGoType(s string) TypeOverride {
	i := strings.LastIndex(s, "/")
	j := strings.LastIndex(s, ".")
	if i < 0 || j <= i {
		// No import path, or no type suffix after the last slash: keep the
		// whole expression verbatim ("map[string]any", "example.com/pkg").
		return TypeOverride{Type: s}
	}
	o := TypeOverride{Import: s[:j], Type: s[j+1:]}
	seg := o.Import[strings.LastIndex(o.Import, "/")+1:]
	if isVersionSegment(seg) {
		// Version-suffixed imports (".../bar/v2.Baz"): the qualifier is the
		// segment before /vN.
		trimmed := o.Import[:strings.LastIndex(o.Import, "/")]
		o.Package = trimmed[strings.LastIndex(trimmed, "/")+1:]
		return o
	}
	o.Package = strings.TrimSuffix(seg, "-go") // pgvector-go -> pgvector
	return o
}

// isVersionSegment reports whether s is a Go module major-version suffix
// ("v2", "v10", ...) — never a real package name.
func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// importQualifier derives the import qualifier (package name) for an import
// path: the last path segment, with any major-version suffix and a trailing
// "-go" (pgvector-go) stripped.
func importQualifier(path string) string {
	seg := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		seg = path[i+1:]
	}
	if isVersionSegment(seg) && strings.Contains(path, "/") {
		trimmed := path[:strings.LastIndex(path, "/")]
		if j := strings.LastIndex(trimmed, "/"); j >= 0 {
			seg = trimmed[j+1:]
		} else {
			seg = trimmed
		}
	}
	return strings.TrimSuffix(seg, "-go")
}
