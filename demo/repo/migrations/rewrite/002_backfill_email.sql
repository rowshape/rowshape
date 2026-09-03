-- Step 2 of 3: backfill the new column in BOUNDED BATCHES.
--
-- A single `UPDATE users SET email = ... WHERE email IS NULL` is the canonical
-- unbatched backfill: one statement over every row that needs it, row locks held
-- for its whole duration, table bloat, and a replication lag spike. rowshape
-- reports it as RS-PERF-010 and is right to — the users table declares 5,000,000
-- rows. Each statement here is bounded on BOTH sides of the primary key, so no
-- single statement touches more than `batch` rows; a one-sided `id >= lo` would
-- be a prefix of the table, not a batch.
--
-- WHAT THIS DEMO CANNOT SHOW: the other half of batching is committing each
-- batch, so locks are released between them. That COMMIT cannot appear here.
-- rowshape's runner applies a migration inside one transaction, and a COMMIT
-- inside a DO block or a called procedure is rejected outright (SQLSTATE 2D000,
-- invalid transaction termination) — so the committing form is not something
-- rowshape can validate today. In production this loop belongs in a task run
-- OUTSIDE the migration, exactly as `rowshape explain RS-PERF-010` says.
DO $backfill$
DECLARE
  lo bigint := 0;
  hi bigint;
  batch constant bigint := 50000;
BEGIN
  SELECT COALESCE(max(id), 0) INTO hi FROM public.users;
  WHILE lo <= hi LOOP
    UPDATE public.users
       SET email = 'user_' || id || '@users.invalid'
     WHERE id >= lo AND id < lo + batch
       AND email IS NULL;
    lo := lo + batch;
  END LOOP;
END
$backfill$;
