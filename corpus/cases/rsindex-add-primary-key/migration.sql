-- Adding a PRIMARY KEY over existing data scans the column for NULLs and builds a
-- unique index while holding ACCESS EXCLUSIVE — reads AND writes block for the
-- whole O(n log n) build. On a 50M-row table that is a full outage, and no
-- analyzer flagged it before RS-INDEX-002 (rsLock excludes ADD PRIMARY, rsData
-- keys on UNIQUE). The build applies fast against the small hydrated fixture, so
-- only extrapolation to declared rows exposes the production cost.
ALTER TABLE public.events ADD CONSTRAINT events_pkey PRIMARY KEY (id);
