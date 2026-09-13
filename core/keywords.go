// Code generated from pg_get_keywords() on the pinned test server
// (paradedb/paradedb:0.25.9, PostgreSQL 18). DO NOT EDIT BY HAND —
// regenerate with:
//
//   docker exec pgb-paradedb psql -U postgres -tA \
//     -c "SELECT word FROM pg_get_keywords() ORDER BY word" > /tmp/kw.txt
//   # then reformat into pgKeywordList below

package core

// pgKeywordList is every keyword Postgres knows — reserved, unreserved,
// col_name and type_func_name classes alike. Only reserved classes truly
// require quoting, but an identifier that collides with any keyword is
// quoted defensively: over-quoting is always safe, under-quoting is a
// syntax error the generated code cannot recover from (AUDIT.md P1 #3).
var pgKeywordList = []string{
	"abort", "absent", "absolute", "access", "action", "add", "admin", "after",
	"aggregate", "all", "also", "alter", "always", "analyse", "analyze", "and",
	"any", "array", "as", "asc", "asensitive", "assertion", "assignment", "asymmetric",
	"at", "atomic", "attach", "attribute", "authorization", "backward", "before", "begin",
	"between", "bigint", "binary", "bit", "boolean", "both", "breadth", "by",
	"cache", "call", "called", "cascade", "cascaded", "case", "cast", "catalog",
	"chain", "char", "character", "characteristics", "check", "checkpoint", "class", "close",
	"cluster", "coalesce", "collate", "collation", "column", "columns", "comment", "comments",
	"commit", "committed", "compression", "concurrently", "conditional", "configuration", "conflict", "connection",
	"constraint", "constraints", "content", "continue", "conversion", "copy", "cost", "create",
	"cross", "csv", "cube", "current", "current_catalog", "current_date", "current_role", "current_schema",
	"current_time", "current_timestamp", "current_user", "cursor", "cycle", "data", "database", "day",
	"deallocate", "dec", "decimal", "declare", "default", "defaults", "deferrable", "deferred",
	"definer", "delete", "delimiter", "delimiters", "depends", "depth", "desc", "detach",
	"dictionary", "disable", "discard", "distinct", "do", "document", "domain", "double",
	"drop", "each", "else", "empty", "enable", "encoding", "encrypted", "end",
	"enforced", "enum", "error", "escape", "event", "except", "exclude", "excluding",
	"exclusive", "execute", "exists", "explain", "expression", "extension", "external", "extract",
	"false", "family", "fetch", "filter", "finalize", "first", "float", "following",
	"for", "force", "foreign", "format", "forward", "freeze", "from", "full",
	"function", "functions", "generated", "global", "grant", "granted", "greatest", "group",
	"grouping", "groups", "handler", "having", "header", "hold", "hour", "identity",
	"if", "ilike", "immediate", "immutable", "implicit", "import", "in", "include",
	"including", "increment", "indent", "index", "indexes", "inherit", "inherits", "initially",
	"inline", "inner", "inout", "input", "insensitive", "insert", "instead", "int",
	"integer", "intersect", "interval", "into", "invoker", "is", "isnull", "isolation",
	"join", "json", "json_array", "json_arrayagg", "json_exists", "json_object", "json_objectagg", "json_query",
	"json_scalar", "json_serialize", "json_table", "json_value", "keep", "key", "keys", "label",
	"language", "large", "last", "lateral", "leading", "leakproof", "least", "left",
	"level", "like", "limit", "listen", "load", "local", "localtime", "localtimestamp",
	"location", "lock", "locked", "logged", "mapping", "match", "matched", "materialized",
	"maxvalue", "merge", "merge_action", "method", "minute", "minvalue", "mode", "month",
	"move", "name", "names", "national", "natural", "nchar", "nested", "new",
	"next", "nfc", "nfd", "nfkc", "nfkd", "no", "none", "normalize",
	"normalized", "not", "nothing", "notify", "notnull", "nowait", "null", "nullif",
	"nulls", "numeric", "object", "objects", "of", "off", "offset", "oids",
	"old", "omit", "on", "only", "operator", "option", "options", "or",
	"order", "ordinality", "others", "out", "outer", "over", "overlaps", "overlay",
	"overriding", "owned", "owner", "parallel", "parameter", "parser", "partial", "partition",
	"passing", "password", "path", "period", "placing", "plan", "plans", "policy",
	"position", "preceding", "precision", "prepare", "prepared", "preserve", "primary", "prior",
	"privileges", "procedural", "procedure", "procedures", "program", "publication", "quote", "quotes",
	"range", "read", "real", "reassign", "recursive", "ref", "references", "referencing",
	"refresh", "reindex", "relative", "release", "rename", "repeatable", "replace", "replica",
	"reset", "restart", "restrict", "return", "returning", "returns", "revoke", "right",
	"role", "rollback", "rollup", "routine", "routines", "row", "rows", "rule",
	"savepoint", "scalar", "schema", "schemas", "scroll", "search", "second", "security",
	"select", "sequence", "sequences", "serializable", "server", "session", "session_user", "set",
	"setof", "sets", "share", "show", "similar", "simple", "skip", "smallint",
	"snapshot", "some", "source", "sql", "stable", "standalone", "start", "statement",
	"statistics", "stdin", "stdout", "storage", "stored", "strict", "string", "strip",
	"subscription", "substring", "support", "symmetric", "sysid", "system", "system_user", "table",
	"tables", "tablesample", "tablespace", "target", "temp", "template", "temporary", "text",
	"then", "ties", "time", "timestamp", "to", "trailing", "transaction", "transform",
	"treat", "trigger", "trim", "true", "truncate", "trusted", "type", "types",
	"uescape", "unbounded", "uncommitted", "unconditional", "unencrypted", "union", "unique", "unknown",
	"unlisten", "unlogged", "until", "update", "user", "using", "vacuum", "valid",
	"validate", "validator", "value", "values", "varchar", "variadic", "varying", "verbose",
	"version", "view", "views", "virtual", "volatile", "when", "where", "whitespace",
	"window", "with", "within", "without", "work", "wrapper", "write", "xml",
	"xmlattributes", "xmlconcat", "xmlelement", "xmlexists", "xmlforest", "xmlnamespaces", "xmlparse", "xmlpi",
	"xmlroot", "xmlserialize", "xmltable", "year", "yes", "zone",
}

// pgKeywords indexes pgKeywordList for needsQuote.
var pgKeywords = func() map[string]bool {
	m := make(map[string]bool, len(pgKeywordList))
	for _, w := range pgKeywordList {
		m[w] = true
	}
	return m
}()
