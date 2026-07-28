-- uuid_generate_v7() is volatile, but it is not on rowshape's known-volatile
-- list — and the list cannot be exhaustive, since a user can define their own.
--
-- The two-state check fell through to "not volatile", which on PG 11+ means the
-- catalog fast-path and therefore NO FINDING AT ALL: a full rewrite of 100M rows
-- under ACCESS EXCLUSIVE, certified safe. Fail-open on the single hazard this
-- tool is best known for.
ALTER TABLE users ADD COLUMN token uuid DEFAULT uuid_generate_v7();
