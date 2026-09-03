# RFC-0001 — Rowshape Fixture Spec v1

Status: Draft v0.3 · Author: Dan Pearson · Format version `1`
Repo: `rowshape/fixture-spec` (proposed, separate from the CLI)

---

## 1. Summary

A **fixture** is a portable, committable description of what a production
database *looks like* — structure plus statistical shape — containing no
production row values.

It exists so a migration can be tested against realistic data by someone with no
access to the real data. That someone is increasingly not a person.

Two operations define the format:

- **Profile:** production database → `rowshape.yaml`
- **Hydrate:** `rowshape.yaml` + seed → a local database with realistic rows

Every fact in a fixture carries a **confidence** (§7). This is the load-bearing
idea in the format: a validator's verdict is capped by the confidence of the
facts it relied on, so a cheap fixture yields honest warnings rather than
confident lies.

## 2. Motivation

Migrations break on the shape of data, not its content. The nine-second lock, the
NOT NULL that fails on 400k legacy rows, the unique index that can't build — all
are functions of row counts, null ratios, cardinality, and fan-out. None require
a single real value.

Yet the industry's answer to "test against realistic data" is still to copy
production and scrub it. That is expensive, legally fraught, leaks through join
keys, and produces artifacts that cannot live in version control.

For the agent use case it's worse than expensive — it's useless. A coding agent
cannot read a 40GB dump. It *can* read four kilobytes of YAML saying `email: 3.2%
null, unique (exact), 400k rows` and reason correctly before writing a line of
SQL.

A fixture is a lossy compression of a database, tuned to preserve exactly the
properties that break migrations and discard everything else. The discarding is
the feature.

## 3. Design principles

1. **A fixture contains no row values by default.** It contains statistics
   derived from them, and some statistics reveal extremes. §8 states precisely
   which, and how to turn them off. The format's credibility depends on making
   this claim narrow and true rather than broad and false.
2. **Every fact carries its confidence.** A fixture that doesn't know something is
   required to say so. §7.
3. **Small enough to commit.** Under 320KB for a 200-table schema — about 1.2KB
   per table. If it doesn't fit in a git diff, the format has failed.

   This said 100KB in earlier drafts, which the emitter never met: a real
   200-table pull is ~246KB, and the only test guarding the claim built its
   fixture by hand with deliberately sparse facts. The number was corrected
   rather than the emitter slimmed, because the weight is spread evenly across
   `range`, `distinct`, `unique` and their confidences — every one load-bearing,
   with no fat to cut that would not cost information the format exists to
   carry. The PRINCIPLE is unchanged and is what the number serves: 320KB of
   YAML is still a file a reviewer will read in a diff.
4. **Readable by a human and an agent.** YAML, flat where possible, no
   compression, no binary blobs, no indirection the reader must resolve.
5. **Deterministic.** Fixture + seed → identical database on any conformant
   hydrator.
6. **Declares truth about production, not about the hydration.** The fixture says
   `users` has 1.2M rows even when you hydrate 12k. §9.
7. **Boring.** No novel serialization, no schema DSL, no attempt to be a migration
   format. It describes; it does not instruct.

## 4. Non-goals

- Not a schema migration format. It cannot be applied.
- Not a backup or replication artifact. Lossy by design; cannot reconstruct
  production.
- Not a data catalog, lineage tool, or docs generator, though it will be mistaken
  for all three.
- Not a general statistics format. Every field earns its place by affecting
  migration outcomes.
- v1 describes Postgres. The structure anticipates other engines; the vocabulary
  does not yet cover them.

## 5. Document structure

```yaml
rowshape_fixture: "1"

meta:
  id: prod@2026-07-14
  generated_at: 2026-07-14T09:12:44Z
  generator: rowshape/0.1.0
  engine: { name: postgres, version: "16.3" }
  privacy: standard            # strict | standard | permissive  (§8)
  source: sha256:41b0...       # salted hash of host, never the hostname (§8.4)
  profile:
    mode: fast                 # fast | exact | targeted   (§7.3)
    scanned_at: 2026-07-14T09:12:44Z
    escalated: [public.users.email, public.orders.external_ref]
  digest: sha256:9f2c...       # canonical form, excluding this field (§11)

tables:
  public.users:
    rows: { value: 1200000, confidence: exact }
    bytes: 890000000
    columns: { ... }           # §6
    constraints: [ ... ]       # §6.4
    indexes: [ ... ]           # §6.5
    references: [ ... ]        # §6.6

types:
  public.status: { ... }       # §6.7 (user-defined types, optional)

extensions:
  citext: { schema: public }   # §6.8 (required extensions, optional)
```

`tables` is a map keyed by qualified name, not a list. Maps diff cleanly when a
table is added; lists do not.

## 6. The column profile

### 6.1 Universal fields

```yaml
    columns:
      id:
        type: bigint
        nullable: false
        null_fraction: { value: 0.0, confidence: exact }
        distinct: { value: 1200000, confidence: exact, via: unique_index }
        unique: { value: true, confidence: exact, via: constraint }
        generated: identity
        identity: by_default          # by_default | always

      email:
        type: text
        nullable: true
        null_fraction: { value: 0.032, confidence: exact }
        distinct: { value: 1161600, confidence: measured, via: hll, error: 0.02 }
        unique: { value: false, confidence: exact, via: scan }   # §7.2
        format: email
        length: { min: 6, max: 254, mean: 24.1, p95: 38 }

      status:
        type: text
        nullable: false
        null_fraction: { value: 0.0, confidence: exact }
        distinct: { value: 4, confidence: exact }
        format: enum_like
        values: [active, trialing, past_due, canceled]   # privacy: permissive only
        frequencies: [0.71, 0.11, 0.06, 0.12]

      created_at:
        type: timestamptz
        nullable: false
        null_fraction: { value: 0.0, confidence: estimated }
        distinct: { value: 1198412, confidence: estimated, via: pg_stats }
        range: { min: 2021-03-01T00:00:00Z, max: 2026-07-14T08:59:12Z }
```

Scalar facts are `{ value, confidence, via }` objects rather than bare scalars.
This is verbose and it is the point: a reader that wants to ignore confidence
must do so deliberately. Readers MAY accept a bare scalar as shorthand for
`confidence: estimated` — the weakest reading, never the strongest.

`nullable` is structural (the DDL, always exact). `null_fraction` is empirical.
Both matter and they are not the same fact: a column that is nullable but 0% null
is the single most common source of a migration that passes staging and fails
prod three weeks later, when the first null arrives.

`default` is the column's DEFAULT expression, verbatim. It is what makes a NOT
NULL column insertable without naming it, so a consumer that drops it produces a
target which REJECTS ordinary inserts production accepts. It bears on the
canonical migration-safety question too: whether `ADD COLUMN ... NOT NULL
DEFAULT <expr>` rewrites the table turns on the default being non-volatile, and
without this field nothing can say what defaults a table already has.

A `default` naming a sequence through `nextval(...)` — how `serial` is spelled
in the catalog — obliges a consumer to create that sequence, since it is a
separate object the fixture does not otherwise carry. A generated or identity
column MUST NOT record one: `generated` already says how the value arrives, and
`DEFAULT` is not legal alongside `GENERATED`.

Like a `CHECK` expression (§6.4), `default` is DDL: verbatim at `standard`, the
literal `opaque` under `privacy:strict`. A consumer MUST NOT invent a
replacement, and SHOULD report a withheld default on a NOT NULL column, since the
target is then stricter than production.

`generated` marks a column the database computes: `identity` or `stored`.

For an identity column, `identity` records `always` or `by_default`. The
difference is not cosmetic: an `ALWAYS` column REJECTS an explicit value in an
INSERT unless the statement says `OVERRIDING SYSTEM VALUE`, so a migration that
supplies one behaves differently on each. A consumer meeting `generated:
identity` with no `identity` field SHOULD assume `by_default`, the permissive
reading.

For a `stored` column, `generated_expression` carries the generation expression
verbatim. Without it the fixture says a column is computed but not from what, so
a consumer rebuilds it as an ordinary column and the target then ACCEPTS an
UPDATE production rejects with `column "total" can only be updated to DEFAULT`.
It is DDL — the same class of information as a `CHECK` expression (§6.4) — and
takes the same privacy treatment: verbatim at `standard`, the literal `opaque`
under `privacy:strict`. A consumer MUST NOT invent a replacement expression: an
invented one computes values production never held. Where the expression is
`opaque` or absent, a consumer SHOULD create an ordinary column and REPORT that
it did, since the target is then more permissive than production.

A consumer that loads rows into an identity column with explicit values MUST
advance that column's sequence past them. An identity sequence does not observe
explicit inserts, so left alone it collides with the loaded rows on the first
INSERT that omits the column — the ordinary way to insert into such a table.

**Text columns MUST NOT emit `range`.** The min of a text column is a real value,
verbatim. Only `length` statistics are permitted. The same applies to `bytea`.
This is a MUST because it is the leak an emitter author will otherwise ship by
accident, having correctly reasoned that min/max is "just a statistic."

### 6.2 Numeric and temporal

```yaml
        range: { min: 0, max: 4999, mean: 82.3, confidence: exact }
        histogram:                      # optional; privacy: standard+
          buckets: 16
          bounds: [0, 1, 2, 5, 12, ...]
```

`range` carries a `confidence` describing how well the EXTREMES are known:
`exact` when min/max were read over the whole column, `estimated` when they came
from a sample. Emitters MUST record it, because a sampled range is a LOWER BOUND
on the real spread, not the spread — a column whose true maximum was 60,000 was
recorded from a sample as 59,773.

This is the one place where a weak fact does damage that §7.4 capping cannot
undo. Capping caps findings that EXIST; a sampled range makes a finding fail to
EXIST — `ADD CONSTRAINT CHECK (customer_id <= 59900)` looked satisfiable against
the sampled maximum, no conflict was reported, and the verdict was PASS while
the engine refused the statement outright. A missing finding is a PASS nothing
downstream can reach.

So a consumer MUST NOT treat the ABSENCE of a range conflict as proof of safety
when the range is `estimated`. It MUST report the uncertainty and name the
command that resolves it. There is no threshold below which a bound is "far
enough" outside a sampled range to be safe: a sample gives no bound on how far
past its extremes the real data lies.

Histograms exist for one reason: skew. A `tenant_id` where one tenant owns 80% of
rows behaves completely differently under a partitioning migration than a uniform
one, and no summary statistic captures that.

### 6.3 Format classes

A closed vocabulary. Emitters MUST use one of:

`uuid` · `email` · `url` · `hostname` · `ipv4` · `ipv6` · `phone` · `json` ·
`jsonb_shape` · `base64` · `hex` · `slug` · `iso_date` · `numeric_string` ·
`enum_like` · `free_text` · `opaque`

Classification is inferred from a sample and is a *hint to the hydrator*, not an
assertion about semantics. `opaque` is always legal and MUST be the fallback. An
emitter that is unsure MUST emit `opaque` rather than guess — a wrong class
produces confidently wrong synthetic data, which is worse than obviously fake
data.

`jsonb_shape` carries a key skeleton (key names, depth, leaf types) but never leaf
values. JSON columns are the richest leak vector in this format; treat them with
suspicion.

### 6.4 Constraints

```yaml
    constraints:
      - name: users_pkey
        kind: primary_key
        columns: [id]
      - name: users_email_key
        kind: unique
        columns: [email]
        nulls_distinct: true
      - name: users_status_check
        kind: check
        expression: "status = ANY (ARRAY['active'::text, ...])"
        validated: true
```

Check expressions are emitted verbatim. **This is a deliberate exception to §3.1**
and it leaks: a CHECK can contain literal values from your domain. It's emitted
anyway because a migration that violates a CHECK is exactly what this tool exists
to catch, and the expression is DDL — it's already in your repository. Under
`privacy: strict`, expressions become `opaque` and the fixture is weaker. Say this
in the docs, loudly.

`validated: false` (a `NOT VALID` constraint) MUST be preserved. It changes what
the migration is allowed to do.

An existing unique constraint or unique index is a **proof** of uniqueness and
sets `unique.confidence: exact, via: constraint` for free. Most of the time the
expensive question in §7.2 is already answered by the DDL.

### 6.5 Indexes

```yaml
    indexes:
      - name: users_email_idx
        method: btree
        columns: [email]
        unique: true
        partial: "deleted_at IS NULL"
        bytes: 48000000
        bloat_estimate: 0.12
      - name: users_lower_email_key      # an EXPRESSION index
        method: btree
        keys: ["lower(email)"]
        unique: true
      - name: users_cust_created         # a COVERING index
        method: btree
        columns: [customer_id, created_at]
        include: [total]                 # payload, NOT part of the key
        unique: true
```

`bytes` and `bloat_estimate` exist because index rebuild time is a first-order
input to lock duration, and lock duration is the finding people actually care
about.

`keys` carries the index's key expressions as SQL text, in order, and MUST be
present when any key is an EXPRESSION rather than a plain column. `columns`
cannot describe such an index — a unique index on `lower(email)` has no column
key at all — so an emitter that records only `columns` describes it as having no
keys, and a consumer rebuilding the schema drops it. That is not a lost
statistic: a UNIQUE index present in production and absent from the target is a
constraint no longer enforced, so a migration that violates it can be reported
as safe.

`keys` MUST also be present when a key carries anything a bare column name
cannot express — an ordering (`DESC`, `NULLS FIRST`), a collation, or a
non-default operator class. These are not decoration. An index declared
`USING gin (email gin_trgm_ops)` and rebuilt from the column name alone does not
build at all (`data type text has no default operator class for access method
"gin"`), so the target silently lacks an index production has. For an index whose
keys are plain ascending columns on their default operator class, `keys` SHOULD
be omitted, since it would merely restate `columns`.

`include` carries a covering index's INCLUDE payload — columns stored in the
index but NOT part of its key. It MUST be recorded separately from `columns`,
and a consumer MUST NOT fold it into the key. The key is what a UNIQUE index
ENFORCES: recording `UNIQUE (customer_id, created_at) INCLUDE (total)` as unique
on all three describes a strictly weaker constraint, so the target accepts rows
production rejects and a migration that violates the real two-column uniqueness
is reported as safe.

`partial` is the predicate alone, without the leading `WHERE`. A consumer MUST
reproduce a partial index WITH its predicate: the predicate is what gives the
index its scope, and a partial unique index (`UNIQUE (email) WHERE deleted_at IS
NULL`, the standard soft-delete pattern) enforces something a plain one does not.
Where `privacy:strict` has replaced the predicate with `opaque` (§6.4), a
consumer MUST NOT invent one — a guessed predicate enforces a different rule than
production — and SHOULD report the index as unreproducible instead.

### 6.6 References and fan-out

```yaml
    references:
      - column: user_id
        to: public.users.id
        name: orders_user_id_fkey
        on_delete: cascade
        on_update: no_action
        fanout: { mean: 8.4, p50: 3, p95: 41, max: 12902, confidence: measured }
        orphan_fraction: { value: 0.0, confidence: exact, via: scan }
```

`fanout` is the most important field in the format and the one no other tool
captures. "Orders per user" being a long tail rather than a uniform 8.4 is what
turns a cascade delete into an outage. A hydrator MUST reproduce the fan-out
distribution, not merely the mean.

`orphan_fraction` records FK violations existing in production despite the
constraint — common where constraints were added `NOT VALID`, or dropped and
never restored. If nonzero, a migration adding `FOREIGN KEY ... VALIDATE` will
fail, and the fixture is the only thing that knows.

`name` is the constraint's own name. A COMPOSITE foreign key is recorded as one
entry per column pair — so `fanout` stays per-column — and the shared `name` is
what lets a consumer group them back into the single constraint they came from.
Without it, `FOREIGN KEY (a, b) REFERENCES t (x, y)` is indistinguishable from
two independent single-column keys, which constrain something else entirely. A
migration naming the constraint (`DROP CONSTRAINT orders_user_id_fkey`) also has
something to match against.

`on_update` is recorded for the same reason `on_delete` is: a migration that
rewrites parent keys behaves differently under `CASCADE` than under `RESTRICT`.
`validated: false` marks a `NOT VALID` key.

A consumer MUST recreate the recorded references as real foreign keys, with
their referential actions, and MUST NOT validate one recorded `validated:
false` — such a key is not enforced against existing rows, so validating it
makes the target reject data production holds. A reference the target does not
enforce is a rule a migration can break and be certified for: an insert of a row
whose parent does not exist was reported as safe while the source database
refused it. Constraints SHOULD be added AFTER rows are loaded, which removes any
dependency on table load order and is what makes self-referencing keys and
cycles between tables work.

### 6.7 User-defined types

```yaml
types:
  public.order_status:
    kind: enum
    labels: [pending, paid, shipped]   # declaration order is significant
    label_count: 3
  public.positive_int:
    kind: domain
    base: integer
    not_null: false
    check: (VALUE > 0)                  # verbatim; opaque under privacy:strict
```

`types` is an OPTIONAL top-level map, keyed by the schema-qualified type name
exactly as it appears in a column's `type` — so a column and its definition join
by string equality. A fixture whose columns use only built-in types omits the
section entirely.

It exists because a column's `type` is only a NAME. For `integer` that is
sufficient: every engine already has one. For `public.order_status` it is not,
and a consumer reconstructing a database from the fixture has nothing to create.
An emitter MUST define every enum and domain its columns reference; a consumer
that cannot resolve a column's type MUST fail rather than substitute one, because
a substituted type accepts values the real column would have rejected.

`kind` is `enum` or `domain`.

For an enum, `labels` is the full ordered vocabulary and `label_count` its size.
The ORDER is normative: engines order an enum by declaration, so a migration that
compares or sorts the column depends on it. `label_count` MUST be present even
when `labels` is not, because cardinality is shape: a consumer that knows only
the count can still build a type of the right size, whereas one with neither
cannot build the type at all.

For a domain, `base` is the underlying type — itself possibly another domain,
which resolves transitively — with `not_null` and a verbatim `check` over the
keyword `VALUE`.

Labels and a domain's `check` are DDL, the same class of information as a column
name or a table `CHECK` (§6.4), and are emitted at `standard`. Under
`privacy:strict` they are withheld: `labels` is dropped (leaving `label_count`)
and `check` becomes `opaque`, exactly as a table CHECK does. A consumer MUST NOT
invent a predicate to replace an opaque `check` — an invented constraint would
reject data production accepted.

Types that are neither enum nor domain (composite, range, and engine-specific
types) are outside this version. An emitter MUST NOT guess a definition for one,
and a consumer meeting an unresolvable type MUST report it by name.

### 6.8 Required extensions

```yaml
extensions:
  citext:  { schema: public }
  pg_trgm: { schema: public }
  vector:  { schema: ext }
```

§6.7 covers a type the database itself defines. This covers the other case: a
type that arrives with an ENGINE EXTENSION and cannot be reconstructed from the
catalog at all. A `citext` column names something a freshly created disposable
database has never heard of, so the DDL fails with `type "citext" does not
exist` and validation cannot run on the schema at all — the same for `hstore`,
`ltree`, PostGIS `geometry`, and pgvector's `vector`.

An extension requirement can also hide where no column type mentions it: an
index declared `USING gin (email gin_trgm_ops)` needs `pg_trgm` because of its
OPERATOR CLASS (§6.5), not because of any column's type.

An emitter MUST record every extension the read schema actually references —
resolved from the types its columns carry and the operator classes its indexes
use — and MUST NOT record extensions merely INSTALLED on the source. Recording
all of them would make a fixture demand PostGIS of a disposable server for a
schema that never touches it. An extension present in every database from
initialization (`plpgsql`) carries no information and SHOULD be omitted.

`schema` is where the extension's objects live, and matters because a type name
in a column can be schema-qualified: `ext.citext` and `public.citext` are
different strings, and only one resolves.

No VERSION is recorded. The claim is "this schema needs citext", not "it needs
citext 1.6" — pinning would make a fixture refuse to hydrate on a server that
ships a different version, for no gain, since the target only has to provide the
type.

A consumer MUST install the recorded extensions before creating types or tables,
and MUST fail loudly, naming the extension, when one is unavailable. It MUST NOT
substitute a built-in type for a missing extension type: `citext` is
case-insensitive and `text` is not, so the substitution changes what the target
enforces and turns a loud failure into a wrong verdict.

An extension name is schema, not data, so `extensions` is emitted at every
privacy level and is not a `--leaks` finding (§8.3).

### 6.9 Partitioned tables

```yaml
  public.events:
    rows: { value: 412000000, confidence: estimated }
    bytes: 431000000000        # the WHOLE tree, not the parent's own storage
    partitions:
      count: 90
      strategy: range          # range | list | hash
      key: occurred_at
      skew: 0.31
```

A partitioned parent declares its partitioning; individual partitions are NOT
recorded as tables of their own. Ninety partitions would be ninety near-identical
table entries, and what changes a migration's behaviour is the count, the
strategy, and the skew — not each partition's bounds.

`key` is the partition key: the column list or expression inside `PARTITION BY
<strategy> (...)`, without the strategy word. `count`, `strategy` and `skew`
DESCRIBE a partitioned table; `key` is what lets one be REBUILT. Without it a
consumer has nothing to declare `PARTITION BY` with, so it recreates the parent
as an ordinary table — and an ordinary table accepts `CREATE INDEX
CONCURRENTLY`, which Postgres refuses outright on a partitioned parent. A
migration that cannot run at all in production is then reported as safe.

`bytes` on a partitioned parent MUST be the total across the whole partition
tree. The engine's own relation-size function measures the parent's storage,
which is zero, so a naive emitter reports `bytes: 0` for a 400GB table and every
size-driven duration estimate (§9.1) extrapolates from nothing — understating
precisely the migrations whose cost matters most. `rows` follows the same rule
for the same reason.

A consumer MUST recreate a partitioned parent as a partitioned table. It is NOT
required to reproduce production's bounds: bounds are not recorded, and the
behaviour that matters does not depend on them. It SHOULD reproduce the `count`
where the strategy allows this mechanically — `hash` does, through modulus and
remainder — and where it cannot, it MUST report the difference, because a DDL
statement on a parent takes locks on every partition and a target standing one
partition in for ninety understates that. Whatever partitions it creates MUST
span the key space, since a row matching no partition fails to load at all.

## 7. Confidence

This section exists because of one failure mode: **a wrong PASS.**

If a fixture says `email` is unique when it isn't, an agent concludes
`ADD CONSTRAINT UNIQUE` is safe, `validate` returns PASS, and production fails.
That is the only outcome that kills the project outright — a tool that misses a
problem is disappointing, but a tool that actively certifies a broken migration
is worse than no tool. Every other design decision here is negotiable. This one
isn't.

### 7.1 The four levels

| Level | Meaning | Example source |
|---|---|---|
| `exact` | Proven. A scan, a count, or a structural guarantee. | `count(*)`, a unique index, the DDL |
| `measured` | Full pass over the data, bounded error. | HyperLogLog distinct (±2%), streamed quantiles |
| `estimated` | Extrapolated from a sample or the planner's stats. | `pg_stats.n_distinct`, reservoir sample |
| `declared` | Asserted by a human in the file. Not verified. | hand-edited fixture |

Ordering: `exact > measured > estimated > declared`.

### 7.2 Uniqueness is never estimated

`unique` MUST be `exact` or absent. There is no middle.

Sampling cannot establish uniqueness — a sample of 100k from 1.2M rows will
happily show zero duplicates when 400 exist. An emitter MUST NOT infer
`unique: true` from a sample under any circumstances.

Three legal ways to reach `exact`:

1. **A unique constraint or index exists.** Free, from the catalog. Covers most
   columns anyone cares about.
2. **An existence probe.** `SELECT EXISTS (SELECT 1 FROM t WHERE c IS NOT NULL
   GROUP BY c HAVING count(*) > 1)` — returns one boolean, no values leave, and
   short-circuits on the first duplicate found. On a column that *isn't* unique
   this is usually fast. On one that is, it's a full scan.
3. **A count comparison.** `SELECT count(*) - count(DISTINCT c) FROM t` — one
   integer out, always a full scan, gives duplicate *count* rather than just
   existence.

If none is affordable, the emitter omits `unique` entirely. §7.4 makes that safe.

### 7.3 Profile modes

Cost control lives in the emitter, not in the format.

- **`fast`** (default) — catalog + `pg_stats` + a reservoir sample. Seconds. Most
  facts land at `estimated`. Free uniqueness proofs from existing constraints.
- **`exact`** — full streaming pass per table: HyperLogLog for distinct, exact
  null counts, exact uniqueness probes. Minutes to hours. Reads values into the
  emitter's memory; emits only aggregates.
- **`targeted`** — `fast` everywhere, plus escalation to a full pass on specific
  columns. `meta.profile.escalated` records which.

**Auto-escalation is the default behavior and the interesting part.** In `fast`
mode an emitter SHOULD automatically escalate any column where
`n_distinct / rows > 0.95` and no unique constraint exists.

That predicate selects exactly the dangerous columns: ones that *look* unique but
aren't proven. It's a small set in practice — usually a handful of
`email`/`slug`/`external_ref` columns — so a `fast` pull stays fast while the
columns where a wrong answer costs an outage get the expensive treatment. The
cheap default is safe by construction rather than by the operator remembering a
flag.

HyperLogLog runs client-side in the emitter: stream the column with a server-side
cursor, hash each value, discard it. Bounded memory, ~2% error, no values
retained. No server extension required — `postgresql-hll` is not installable on
most managed Postgres, so depending on it would gate the feature behind exactly
the infrastructure that most needs it.

### 7.4 Verdict capping (the payoff)

Each finding declares which fixture facts it depends on. **The verdict a finding
can produce is capped by the minimum confidence across its dependencies.**

| Min dependency confidence | Strongest verdict allowed |
|---|---|
| `exact` | PASS |
| `measured` | PASS |
| `estimated` | WARN |
| `declared` / absent | WARN |

So: a migration adding `UNIQUE (email)` against a fixture where uniqueness is
unproven cannot return PASS. It returns:

```
RS-DATA-014  warn  Cannot confirm uniqueness of public.users.email
  This fixture profiles email at 'estimated' confidence (via pg_stats).
  ADD CONSTRAINT UNIQUE may fail on production data.
  Resolve: rowshape pull --exact public.users.email
```

A wrong PASS becomes a loud, actionable WARN naming the exact command that fixes
it. That single mapping is the difference between a tool people trust with
production and a toy.

It also means `fast` mode is safe to default. A cheap fixture doesn't lie; it
declines to certify.

Findings MUST NOT downgrade a dependency's confidence to reach a stronger
verdict. Yes, this needs saying — the pressure to make the demo green is real,
and it will come from the maintainer, not from an attacker.

## 8. Privacy

### 8.1 What actually leaks

The claim "no production values leave the profiler" is *almost* true, and the
spec is honest about where it isn't:

- `range.min` on `salary` reveals someone's salary.
- `range.min` on `birth_date` reveals the oldest customer's birth year.
- `values` on a low-cardinality column reveals the value set.
- Histogram bounds are, literally, real values from your data.
- CHECK expressions contain domain literals (§6.4).
- `length.max` on `free_text` is weakly identifying in a small table.

The defensible claim, and the one the README MUST use:

> A fixture contains no rows from your database. It contains statistics computed
> from them. At `--privacy standard`, some of those statistics reveal the extremes
> of numeric and date columns. At `--privacy strict`, none do.

### 8.2 Levels

| | `strict` | `standard` (default) | `permissive` |
|---|---|---|---|
| Row counts, null fractions, distinct counts | ✓ | ✓ | ✓ |
| Format classes, length stats | ✓ | ✓ | ✓ |
| Fan-out distributions | ✓ | ✓ | ✓ |
| Numeric/temporal `range` | — | ✓ | ✓ |
| Histograms | — | ✓ | ✓ |
| `values` / `frequencies` | — | — | ✓ |
| CHECK expressions verbatim | — | ✓ | ✓ |

Under `permissive`, value sets materialize only when **both** `distinct <= 50`
**and** every value occurs at least `k` times (default `k=20`). The k-threshold
isn't decoration: a `status` column with 999,999 `active` and one
`pending_legal_hold_case_4471` would otherwise publish a fact about exactly one
person.

Emitters MUST support `strict`. Emitters MUST NOT default to `permissive`.

Per-column overrides always win:

```yaml
      salary:
        redact: [range, histogram]     # keep the shape, drop the numbers
      internal_notes:
        redact: all                    # opaque free_text only
```

### 8.3 Self-audit

`rowshape inspect --leaks fixture.yaml` MUST enumerate every field in a given
fixture that is derived from row values, with its source column and privacy level.

This is a trust feature, not a compliance checkbox. Priya's security team will
find these fields in an afternoon whether or not we point at them. Pointing at
them first — with a command that prints the complete list — is the difference
between "documented tradeoff" and "undisclosed leak." Ship it in v1.

### 8.4 Source identity

`meta.source` is a salted hash of the source host, never the hostname. The PRD's
safety check ("refuse to validate against the fixture's source host") works fine
against a hash, and a committed file shouldn't publish internal infrastructure
names. Salt is per-fixture and stored alongside; this defends against casual
disclosure, not against a determined attacker with a host list, and the docs
should not overclaim it.

## 9. Declared rows vs. hydrated rows

The fixture declares `rows: 1200000`. A developer runs `rowshape hydrate --scale
0.01` and gets 12,000, because nobody waits four minutes to check a migration.

These serve different purposes and MUST NOT be conflated:

- **Hydrated rows** test *correctness*: does the constraint hold, does the
  backfill compute the right values, does the unique index build.
- **Declared rows** drive *extrapolation*: a validator computes lock duration and
  rewrite cost from `rows`, not from what it hydrated.

So a lock finding reports "≈9s, 1.2M rows rewritten" having physically rewritten
12,000 rows in 90ms.

### 9.1 Extrapolation is model-based, not linear

Naive linear scaling is wrong and will be caught. Instead, each operation class
carries a known cost model, and the validator extrapolates along it:

| Operation | Model |
|---|---|
| Table rewrite (`ADD COLUMN` w/ volatile default, type change) | `O(n)` |
| Constraint validation scan | `O(n)` |
| B-tree index build | `O(n log n)` |
| `CREATE INDEX CONCURRENTLY` | `O(n log n)`, two passes, no exclusive lock |
| Catalog-only change (`RENAME`, `DROP` w/o rewrite) | `O(1)` |

The model comes from the operation, which is known statically. This costs nothing
and is right far more often than linear.

**Cost models are engine-version-conditional**, which is why `meta.engine.version`
is mandatory. PG11+ fast-paths `ADD COLUMN ... DEFAULT` into the catalog only when
the default is non-volatile — a volatile default still rewrites `O(n)`, and
`SET NOT NULL` on an existing column still full-scans regardless. A validator that
applies one table of models across all versions will be confidently wrong on the
older databases most likely to have the scary migrations. A validator MUST refuse
to extrapolate when `engine.version` is absent rather than assume a recent
default.

### 9.2 Estimates are reported as estimates

Beyond the model, reality is non-linear in ways a fixture cannot capture: buffer
cache cliffs, autovacuum, concurrent load, disk contention. An extrapolated
duration is an order-of-magnitude signal, not a stopwatch.

Findings therefore report **buckets, not point estimates**:

`instant` (<100ms) · `fast` (<1s) · `noticeable` (1–10s) · `slow` (10–60s) ·
`outage` (>60s)

with the basis attached:

```yaml
estimate:
  bucket: slow
  model: n_log_n
  basis_rows: 12000
  basis_ms: 91
  declared_rows: 1200000
  confidence: estimated       # never exceeds the confidence of `rows` (§7.4)
```

A tool that says "9.2 seconds" invites someone to time it and blog about the
discrepancy. A tool that says "10–60s, extrapolated from 12k rows on an n log n
model" is making a claim it can defend. Take the smaller claim.

`rowshape validate --calibrate` hydrates at two scale factors and fits the actual
curve, upgrading the estimate to `measured`. It's slower and it's the honest
option for the one migration you're genuinely nervous about.

## 10. Determinism

`hydrate --seed 42` MUST produce identical output for the same fixture and
hydrator version, across platforms.

- The seed derives per-column PRNG streams via `hash(seed, table, column,
  row_ordinal)`, so adding a column doesn't reshuffle other columns' values and
  `--scale` changes don't reshuffle the prefix.
- Generation order MUST NOT depend on map iteration order (a real Go hazard —
  canonicalize before generating).
- The hydrator version participates in the determinism contract. Changing
  generation output is a breaking change to the hydrator, not to the fixture.

## 11. Canonical form and identity

`meta.digest` is SHA-256 over the canonical form: keys sorted lexicographically,
two-space indent, no anchors or aliases, floats to 6 significant figures, `\n`
line endings, `meta.digest` and `meta.generated_at` excluded.

Two fixtures with the same digest are interchangeable. The digest is what a
verdict cites and what an attestation record binds to. Without it, "the agent
validated against prod shape" is an unfalsifiable claim.

## 12. Extensibility and compatibility

- Unknown fields MUST be ignored by readers, not rejected.
- Vendor extensions use an `x_` prefix and MUST NOT change hydration semantics.
- `rowshape_fixture: "1"` is the major version. Additive fields ship without a
  bump. Removing or reinterpreting a field requires `"2"`.
- A reader encountering an unknown major version MUST refuse rather than
  best-effort. Silent partial understanding is how you get a PASS that means
  nothing.

## 13. Conformance

**A conformant emitter** MUST: read only via catalog views, sampled `SELECT`, or
streamed cursors; refuse to run as superuser absent explicit override; emit
`strict` when asked; never emit a value violating §8.2 for the active level; never
emit `range` on text or bytea (§6.1); never infer `unique: true` from a sample
(§7.2); attach a confidence to every fact in §6; produce a canonical form whose
digest is stable across runs against an unchanged database.

**A conformant hydrator** MUST: honor `null_fraction` within ±0.5%; honor
`unique`; reproduce `fanout` distribution shape, not just its mean; satisfy all
declared constraints unless `orphan_fraction > 0` demands otherwise; be
deterministic per §10. It MAY substitute obviously-fake values for any format
class — realism of *content* is explicitly not required, only realism of *shape*.

**A conformant validator** MUST: cap verdicts per §7.4; report durations as
buckets with basis per §9.2; never downgrade a dependency's confidence to reach a
stronger verdict.

A hydrator generating plausible-looking fake emails is not better than one
generating `user_00417@example.invalid`. It's worse, because someone will
eventually mistake the output for real data.

## 14. Open questions

1. **Multi-column correlation.** `country` and `phone_prefix` are correlated;
   independent generation produces nonsense. Nonsense is usually fine (§13), but
   correlation matters for composite unique constraints, where independent
   generation understates collisions. Defer to v2, or add an optional
   `correlations` block now?
2. **Partitioned tables.** ~~Parent only, or every partition?~~ **Resolved:**
   parent only, and normative in §6.9. The parent declares
   `partitions: { count, strategy, key, skew }` with no per-partition entries.
   `key` was added after the parent-only decision proved insufficient to REBUILD
   a partitioned table — count and strategy describe one, the key is what
   declares it.
3. **Does `bloat_estimate` belong in a portable spec,** or is it Postgres trivia
   that will embarrass us at MySQL?
4. **HLL parameters.** Precision 14 (~1.6% error, 16KB state) vs 16 (~0.4%, 64KB)?
   Per-column state is transient, so memory is cheap — but the error rate lands in
   `distinct.error` and feeds §7.4, so it's a published number. Leaning 14, since
   `measured` only needs to beat `estimated`, not approach `exact`.
5. **Does auto-escalation (§7.3) need a cost ceiling?** A `fast` pull that quietly
   full-scans a 400M-row table because one column looked unique is a bad surprise.
   Options: `--max-escalation-rows`, a timeout that degrades to omitting `unique`,
   or prompting. Leaning: soft cap with a WARN that names what was skipped and
   why.

## 15. Prior art

`pg_dump --schema-only` (structure, no shape) · `pg_stats` (shape, not portable,
not committable) · Atlas HCL (desired structure, no data model) · Synth /
Tonic.ai / Gretel (synthetic data as a product rather than a spec, optimized for
content realism over migration pathology) · JSON Schema (the extensibility and
conformance model here is deliberately borrowed) · DataFusion / Calcite statistics
models (the confidence-tiering idea is a cousin of query-planner stats
confidence).

The gap this fills: nobody publishes a *portable, committable, value-free*
description of data shape. Every tool above either keeps the shape private or
keeps the values.
