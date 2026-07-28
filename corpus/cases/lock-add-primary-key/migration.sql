-- ADD PRIMARY KEY builds a unique index over every row under ACCESS
-- EXCLUSIVE. There is no CONCURRENTLY form, so it cannot be made online
-- directly — the index has to be built first and then adopted.
ALTER TABLE events ADD PRIMARY KEY (id);
