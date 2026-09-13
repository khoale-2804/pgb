package core

// TableMeta identifies the table behind a generated descriptor. Descriptors
// embed it; Schema and Name are the source of truth for the FROM/INTO/UPDATE
// targets the descriptor's builders emit.
type TableMeta struct {
	Schema string
	Name   string
}

// NewTableMeta builds a TableMeta, e.g. NewTableMeta("public", "users").
func NewTableMeta(schema, name string) TableMeta {
	return TableMeta{Schema: schema, Name: name}
}
