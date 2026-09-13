# Negative fixtures

Each file here must produce the documented behavior in the error/warning test
suite — the generator is judged as much on how it fails as on what it emits.

| file | expected behavior |
|---|---|
| `zero-columns.sql` | empty model or clean refusal naming `zero_col`; never a partial file or panic |
| `unknown-directive.sql` | codegen succeeds; stderr warning names the directive and column |
| `duplicate-table.sql` | hard error naming the table and both definition sites |
