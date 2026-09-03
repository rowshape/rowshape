-- The migration names a table that does not exist: `usres`, not `users`. Nothing
-- about the DATA is wrong — this is the migration failing to match the schema it
-- was written against, which is the most common real failure there is.
--
-- No hazard analyzer has anything to say about a statement that never ran, so
-- this is the case RS-APPLY exists for: without it the verdict was FAIL with an
-- empty findings list, and an agent reading the machine-readable verdict was told
-- the migration failed and given nothing at all to act on.
ALTER TABLE public.usres ADD COLUMN nickname text;
