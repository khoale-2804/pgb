// Package ir defines pgb's SchemaIR — the intermediate representation every
// frontend (sqlc catalog, oliphant DDL parse) builds and every codegen pass
// consumes. See DESIGN.md §2.
package ir

// Schema is the resolved model of a database schema.
type Schema struct {
	DefaultSchema string
	Tables        []Table
	Enums         []Enum
	Composites    []Composite
	Domains       map[string]string // domain name -> base type
}

// Table is one table (or view) in the schema.
type Table struct {
	Schema     string
	Name       string
	Comment    string
	Columns    []Column
	PrimaryKey []string
	Uniques    [][]string
	Search     *SearchIndex // non-nil iff a USING paradedb/bm25 index exists
	Skip       bool         // pgb:skip directive — emit nothing
	View       bool         // view or materialized view (read-only)
}

// Column is one column of a table.
type Column struct {
	Name       string
	PGType     string // resolved base type; domain unwrapped
	NotNull    bool
	IsArray    bool
	HasDefault bool
	// Generated is "stored" or "virtual" for generated columns ("" otherwise).
	// Generated and default-bearing columns are excluded from INSERT/UPDATE.
	Generated string
	Comment   string
}

// Enum is a PostgreSQL enum type.
type Enum struct {
	Schema string
	Name   string
	Values []string
}

// Composite is a PostgreSQL composite type.
type Composite struct {
	Schema string
	Name   string
	Fields []Column
}

// SearchIndex models one `CREATE INDEX ... USING paradedb|bm25` definition.
// Filled by the DDL extraction pass (oliphant), not by the catalog.
type SearchIndex struct {
	Name     string
	Using    string // "paradedb" | "bm25"
	KeyField string
	Fields   []SearchField
	Options  map[string]string
}

// SearchField is one column (or JSON path) covered by the search index.
type SearchField struct {
	Column    string
	Path      string // e.g. "metadata->'color'" for JSON-path fields
	Alias     string // alias=json_color
	Tokenizer string // icu, simple(stemmer=english), literal, ngram(3,3), ...
	PGType    string
}
