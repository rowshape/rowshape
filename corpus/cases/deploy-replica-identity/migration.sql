-- Changing REPLICA IDENTITY raises no error and does not affect queries, so
-- nothing about applying it reveals a problem. Logical replication
-- subscribers and CDC consumers then start receiving UPDATE and DELETE
-- events they cannot match to a row.
ALTER TABLE events REPLICA IDENTITY NOTHING;
