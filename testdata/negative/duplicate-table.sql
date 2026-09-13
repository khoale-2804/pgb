-- pgb negative fixture: duplicate table name in one schema
-- Expected behavior: hard error naming the table and both definition sites
-- (sqlc's analyzer also rejects this — the error must come from pgb's IR
-- loader with the same clarity when running standalone).
CREATE TABLE dup (
    id bigserial PRIMARY KEY
);

CREATE TABLE dup (
    id bigserial PRIMARY KEY,
    extra text
);
