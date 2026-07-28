-- The canonical unbatched backfill. RS-PERF-002 fires only on statements with NO
-- WHERE clause, so this was exempt BY CONSTRUCTION and returned PASS — while the
-- real-world hazard always carries a WHERE.
--
-- null_fraction is exactly the selectivity of an IS NULL predicate, so the
-- fixture can size this from a fact it already profiles: 0.9 x 50M = ~45M rows,
-- in one statement, in one transaction.
UPDATE users SET normalized = lower(email) WHERE normalized IS NULL;
