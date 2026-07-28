-- The most common migration outage there is, and the catalog was silent on it.
-- This applies instantly and cleanly; the database is left in a perfectly good
-- state. Every app instance still running the previous release then fails on
-- `column "email" does not exist`. No amount of executing this against a
-- hydrated database surfaces it, which is why RS-DEPLOY-001 is a static rule.
ALTER TABLE users RENAME COLUMN email TO email_address;
