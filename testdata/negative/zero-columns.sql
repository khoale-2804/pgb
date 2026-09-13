-- pgb negative fixture: zero-column table
-- Legal PostgreSQL, degenerate for codegen. Expected behavior (M1 decision):
-- either an empty model struct + no statics, or a clean refusal naming the
-- table. Must NOT panic or emit a partial file.
CREATE TABLE zero_col ();
