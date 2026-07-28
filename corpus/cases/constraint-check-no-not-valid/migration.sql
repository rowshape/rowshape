-- The data is entirely VALID: amount's profiled range is 1..100000, so every row
-- satisfies the predicate and the statement succeeds. That is precisely why the
-- catalog was silent — RS-CONSTRAINT-010 only fires when the range CONTRADICTS
-- the predicate, i.e. when it would FAIL.
--
-- It still scans 200M rows under ACCESS EXCLUSIVE, blocking every read and write
-- on the table for the duration. A successful outage.
ALTER TABLE orders ADD CONSTRAINT orders_amount_positive CHECK (amount > 0);
