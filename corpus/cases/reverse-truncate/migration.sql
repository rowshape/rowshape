-- TRUNCATE with no cascading child: the case that produced ZERO findings and a
-- PASS verdict before RS-REVERSE-004 existed. Total irreversible data loss under
-- ACCESS EXCLUSIVE, and it succeeds against the hydrated table too, so the
-- apply-failure floor never fires.
TRUNCATE events;
