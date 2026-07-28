-- Instant and safe FOR THE DATABASE. The hazard is the contract: code,
-- ORM models and downstream schemas were written against a column that
-- could never be null, and none of them are re-checked here.
ALTER TABLE events ALTER COLUMN note DROP NOT NULL;
