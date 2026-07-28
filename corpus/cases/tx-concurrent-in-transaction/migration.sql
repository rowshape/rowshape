-- The sharpest false negative the catalog had: this CANNOT succeed in
-- production (SQLSTATE 25001), yet it applied cleanly and returned PASS.
--
-- The cause is that rowshape's sandbox does not execute the migration's own
-- BEGIN/COMMIT — each statement runs in its own transaction so locks can be
-- inspected — and CREATE INDEX CONCURRENTLY is hoisted out of any transaction
-- entirely. The sandbox's convenience erased a guaranteed production failure,
-- which is why RS-TX-001 reasons about the migration TEXT rather than about
-- what happened when it ran.
BEGIN;
CREATE INDEX CONCURRENTLY idx_orders_user_id ON orders (user_id);
COMMIT;
