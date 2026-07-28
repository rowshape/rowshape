#!/usr/bin/env bash
# rowshape GitHub Action — run `rowshape validate` and map its verdict onto a CI
# job outcome.
#
# This is a THIN WRAPPER over the released binary (P0-T4), not a reimplementation
# (PRD §13). It adds no finding logic and reveals no new facts: everything comes
# from `rowshape validate`, which renders the SAME Verdict struct as the CLI and
# the MCP server — one struct, two marshalers (PRD §10, INV-VERDICT-SHAPE). This
# script only (1) translates Action inputs to `validate` flags and (2) maps the
# process exit code onto a CI gate:
#
#   0 PASS        -> job passes
#   1 FAIL        -> job fails
#   2 WARN-only   -> job passes by default; fails when warn-as-fail is set
#   3 tool error  -> job fails (could not produce a verdict)
#
# The "configurable WARN" knob (PRD §10) is honored in one place: when
# warn-as-fail is true we pass `--validate --warn-fail`, so `validate` itself
# returns 1 for a WARN-only verdict; when it is false a raw exit 2 is remapped to
# 0 here so a WARN informs review without blocking the merge.
set -u

# EXIT_TOOL_ERROR mirrors internal/exitcode: "the tool could not produce a
# verdict", which is distinct from FAIL and must never be confused with it.
EXIT_TOOL_ERROR=3

# rowshape_bool <value> <input-name> -> echoes "true"/"false", or fails.
#
# These inputs gate CI strictness, so an unrecognized value must NOT quietly
# become the permissive branch. A literal `= "true"` test meant True, TRUE, yes
# and 1 all silently read as false — i.e. a user asking for MORE strictness got
# less, with no diagnostic. Accept the common spellings, reject anything else.
rowshape_bool() {
  local v="$1" name="$2"
  case "$(printf '%s' "$v" | tr '[:upper:]' '[:lower:]')" in
    true | yes | 1 | on) echo true ;;
    false | no | 0 | off | "") echo false ;;
    *)
      echo "rowshape: input '${name}' must be true or false, got '${v}'" >&2
      exit "$EXIT_TOOL_ERROR"
      ;;
  esac
}

BIN="${ROWSHAPE_BIN:-${INPUT_BINARY:-rowshape}}"

# A missing binary is a TOOL ERROR, not a verdict. Without this the launcher
# failure escaped as bash's 127, which the exit mapping below does not remap —
# so the job failed with a code outside the documented 0/1/2/3 contract.
# npm/bin/rowshape.js already exits 3 for the same case; the two wrappers must
# not disagree about it.
if ! command -v "$BIN" >/dev/null 2>&1 && [ ! -x "$BIN" ]; then
  echo "rowshape: could not find the rowshape binary at '${BIN}'" >&2
  echo "rowshape: the install step may have failed, or 'binary' points at nothing executable" >&2
  exit "$EXIT_TOOL_ERROR"
fi

# `target` and `ephemeral` are documented mutually exclusive. They used to be
# appended together and silently resolved in validate's favour — meaning the
# mode that writes to a LIVE database won a conflict the user never saw. Refuse
# instead: an ambiguous request about which database gets written to is not
# something to guess at.
if [ -n "${INPUT_TARGET:-}" ] && [ -n "${INPUT_EPHEMERAL:-}" ]; then
  echo "rowshape: 'target' and 'ephemeral' are mutually exclusive; set exactly one" >&2
  echo "rowshape: 'target' validates against a live database, 'ephemeral' against a disposable one" >&2
  exit "$EXIT_TOOL_ERROR"
fi

# `|| exit $?` is load-bearing: rowshape_bool runs in a command substitution, so
# its `exit` ends only that SUBSHELL. Without propagating the status the refusal
# would be swallowed, the variable would be set to the empty string, and the run
# would continue with the permissive branch — reintroducing the fail-open this
# is meant to remove.
warn_as_fail=$(rowshape_bool "${INPUT_WARN_AS_FAIL:-false}" warn-as-fail) || exit $?
json=$(rowshape_bool "${INPUT_JSON:-true}" json) || exit $?

args=(validate)
[ -n "${INPUT_FIXTURE:-}" ] && args+=("${INPUT_FIXTURE}")
[ -n "${INPUT_MIGRATIONS:-}" ] && args+=(--migrations "${INPUT_MIGRATIONS}")
[ -n "${INPUT_TARGET:-}" ] && args+=(--target "${INPUT_TARGET}")
[ -n "${INPUT_EPHEMERAL:-}" ] && args+=(--ephemeral "${INPUT_EPHEMERAL}")
[ -n "${INPUT_RUNNER:-}" ] && args+=(--runner "${INPUT_RUNNER}")
[ -n "${INPUT_SEED:-}" ] && args+=(--seed "${INPUT_SEED}")
[ -n "${INPUT_SCALE:-}" ] && args+=(--scale "${INPUT_SCALE}")
[ "$warn_as_fail" = "true" ] && args+=(--warn-fail)

# INPUT_ARGS is an optional space-separated passthrough of extra `validate` flags.
if [ -n "${INPUT_ARGS:-}" ]; then
  # shellcheck disable=SC2206  # intentional word-splitting of extra flags
  extra=(${INPUT_ARGS})
  args+=("${extra[@]}")
fi

out="${ROWSHAPE_VERDICT_JSON:-rowshape-verdict.json}"

# --json puts the machine-readable Verdict (or a tool-error payload) on stdout.
# Capture it to a file so a downstream step (P4-T2) can render PR annotations
# from the same struct, then echo it into the job log.
if [ "$json" = "true" ]; then
  args+=(--json)
  "$BIN" "${args[@]}" >"$out"
  code=$?
  cat "$out"
else
  "$BIN" "${args[@]}"
  code=$?
fi

# Extract the verdict string for the step output. The JSON is pretty-printed, so
# "verdict": "FAIL" sits on its own line; a tool error has no verdict field.
verdict=""
if [ "$json" = "true" ] && [ -f "$out" ]; then
  verdict=$(sed -n 's/.*"verdict"[[:space:]]*:[[:space:]]*"\([A-Za-z]*\)".*/\1/p' "$out" | head -n1)
fi

# Map the validate exit code onto the CI gate. Only a WARN-only (2) is softened,
# and only because warn-as-fail=false means "do not block on WARN"; FAIL (1) and
# tool error (3) always fail the job.
job=$code
if [ "$code" -eq 2 ]; then
  job=0
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "verdict=${verdict}"
    echo "exit-code=${code}"
    # Only advertise the JSON path when the file actually exists. It used to be
    # set unconditionally while being written only under json=true, so a
    # downstream step consuming the documented output got a path to a file that
    # was never created.
    if [ "$json" = "true" ] && [ -f "$out" ]; then
      echo "verdict-json=${out}"
    else
      echo "verdict-json="
    fi
  } >>"$GITHUB_OUTPUT"
fi

echo "rowshape: verdict=${verdict:-<none>} (validate exit ${code}; job exit ${job})" >&2
exit "$job"
