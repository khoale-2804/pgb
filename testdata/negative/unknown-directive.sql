-- pgb negative fixture: unknown directive
-- Expected behavior: codegen proceeds, the unknown directive is reported as a
-- warning on stderr (never silently ignored, never a hard error).
CREATE TABLE widget (
  id    bigserial PRIMARY KEY,
  color text
);

COMMENT ON COLUMN widget.color IS 'pgb:bogus_directive=true';
