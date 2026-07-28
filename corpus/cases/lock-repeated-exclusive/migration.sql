-- Each acquisition queues independently, so the table is unavailable
-- across the whole SEQUENCE rather than for the longest single statement.
-- lock_timeout is set, so RS-LOCK-010 stays silent and this case isolates
-- the repetition itself.
SET lock_timeout = '3s';
ALTER TABLE events ALTER COLUMN note TYPE varchar(500);
ALTER TABLE events ADD CONSTRAINT events_note_chk CHECK (note IS NOT NULL);
