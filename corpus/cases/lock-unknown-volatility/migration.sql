-- A USER-DEFINED volatile function as a column default.
--
-- rowshape's known-volatile list cannot be exhaustive, because a user can define
-- their own functions — which is exactly what this migration does. The two-state
-- check fell through to "not volatile", and on PG 11+ that means the catalog
-- fast-path and therefore NO FINDING AT ALL: a full rewrite of 100M rows under
-- ACCESS EXCLUSIVE, certified safe. Fail-open on the single hazard this tool is
-- best known for.
--
-- The function is DEFINED HERE rather than assumed. An earlier version of this
-- case used uuid_generate_v7(), which does not exist in a stock PostgreSQL at
-- all, so the statement was rejected before any of this could be exercised —
-- the case asserted a volatility verdict on a migration that never ran. Its
-- body uses only md5/random, which are present from PG 10, so the case means
-- the same thing on every major in the matrix.
CREATE FUNCTION app_new_token() RETURNS uuid
  LANGUAGE sql VOLATILE AS $$ SELECT md5(random()::text)::uuid $$;

ALTER TABLE public.users ADD COLUMN token uuid DEFAULT app_new_token();
