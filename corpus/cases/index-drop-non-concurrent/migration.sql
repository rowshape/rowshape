-- A non-concurrent DROP INDEX takes ACCESS EXCLUSIVE on the TABLE, not
-- just the index, so every read and write queues behind it. The drop
-- itself is fast, which is exactly why it looks harmless in a sandbox.
DROP INDEX events_note_idx;
