## Database migrations

This repo has `rowshape` wired in as an MCP server. Migrations here get checked
against production *shape* — real row counts, null fractions, cardinality,
fan-out — not against a dev database with twelve rows in it. Most migrations that
break production pass every test on a small dataset first.

**Before writing a migration**, call `describe_shape` for the tables you're
touching. The shape decides the answer: `SET NOT NULL` on a column with a 3% null
fraction fails on contact, and a cascade delete on a foreign key whose fan-out has
a long tail is an outage rather than a cleanup. Guessing costs more than asking.

**Before opening a PR**, call `validate_migration`. It returns a verdict and
compact finding codes (`RS-LOCK-001`, `RS-DATA-014`, …). Call `explain_finding`
with a code to get the fix.

**Know what it checked.** `validate_migration` is STATIC: it reasons about your
SQL against the fixture and runs nothing. A PASS means "nothing in the shape
contradicts this", not "this ran". `rowshape validate` in CI hydrates and
applies — that is the stronger check.

**Never hand-wave a FAIL.** A FAIL is not an opinion — it rests on a fact
measured from production, and the finding names that fact. Fix it and
re-validate. Do not explain it away in the PR description, do not disable the
check, and do not ask a human to accept it.

**A WARN is not a pass.** WARN means the verdict rests on a fact rowshape could
not prove — usually a statistic it could only estimate. The finding names the
command that resolves it. Run that command; do not assume the optimistic reading.

Both tools need the committed fixture (`rowshape.yaml`). If there isn't one, say
so and ask — creating it means reading a real database, which is a human's call.
