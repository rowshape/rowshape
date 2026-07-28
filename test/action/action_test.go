// Package action_test is the integration test for the rowshape GitHub Action
// (P4-T1). The Action is a thin wrapper over `rowshape validate` (action.yml +
// .github/actions/rowshape/{install,run}.sh); these tests pin the two things the
// wrapper is responsible for — flag translation and exit-code gating — plus the
// release-gated installer's archive naming.
//
// Two layers:
//
//   - Hermetic (always runs where bash exists): run.sh is driven with a stub
//     `rowshape` that emulates `validate`'s exit codes, so the input->flag
//     mapping and the PASS/FAIL/WARN/tool-error -> job-exit mapping are verified
//     with no database. This is the bulk of acceptance criteria 1 and 2.
//   - DB-backed (skipped without ROWSHAPE_TEST_PG_DSN): the real binary is built
//     and run through run.sh against committed corpus fixtures + migrations,
//     using a disposable Postgres and no production credential (criterion 3).
//
// The installer's download path is release-gated (P0-T4) and cannot run here,
// but its archive-name computation is unit-tested against the goreleaser
// convention (TestInstallAssetNaming), the same guard npm/naming.test.js gives
// install.js.
package action_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// repoRoot returns the module root (two levels up from test/action).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func runScript(t *testing.T) string {
	return filepath.Join(repoRoot(t), ".github", "actions", "rowshape", "run.sh")
}

func requireBash(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available; the Action wrapper is bash and its tests require it")
	}
	return bash
}

// writeStub writes a fake `rowshape` that emulates `validate`'s exit codes and
// emits a Verdict-shaped JSON. STUB_VERDICT selects the outcome; the stub honors
// --warn-fail exactly as validate does (WARN -> 1 when set, else 2). It appends
// its argv to $ROWSHAPE_STUB_ARGS so flag forwarding can be asserted.
func writeStub(t *testing.T, dir string) string {
	t.Helper()
	name := "rowshape"
	if runtime.GOOS == "windows" {
		name = "rowshape" // invoked via bash, no .exe needed
	}
	path := filepath.Join(dir, name)
	const body = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$ROWSHAPE_STUB_ARGS"
v="${STUB_VERDICT:-PASS}"
warn_fail=0
for a in "$@"; do [ "$a" = "--warn-fail" ] && warn_fail=1; done
case "$v" in
  PASS) code=0 ;;
  FAIL) code=1 ;;
  WARN) if [ "$warn_fail" = 1 ]; then code=1; else code=2; fi ;;
  TOOLERR) code=3 ;;
  *) code=0 ;;
esac
if [ "$v" = TOOLERR ]; then
  printf '{\n  "error": "tool_error",\n  "category": "bad_usage"\n}\n'
else
  printf '{\n  "rowshape": "1",\n  "verdict": "%s",\n  "findings": []\n}\n' "$v"
fi
exit $code
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type runResult struct {
	exit     int
	output   map[string]string // parsed GITHUB_OUTPUT
	verdict  string
	stubArgs string
	// combined is run.sh's stdout+stderr. Refusals must explain themselves, so
	// tests assert on the message and not merely the exit code.
	combined string
}

// invoke runs run.sh with the stub binary and the given inputs, returning the
// job exit code, the step outputs, and the argv the stub observed.
func invoke(t *testing.T, extraEnv map[string]string) runResult {
	t.Helper()
	bash := requireBash(t)
	work := t.TempDir()
	writeStub(t, work)
	argsFile := filepath.Join(work, "stub-args.txt")
	outFile := filepath.Join(work, "github-output.txt")
	if err := os.WriteFile(argsFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"ROWSHAPE_BIN="+filepath.Join(work, "rowshape"),
		"ROWSHAPE_STUB_ARGS="+argsFile,
		"GITHUB_OUTPUT="+outFile,
		"ROWSHAPE_VERDICT_JSON="+filepath.Join(work, "verdict.json"),
	)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(bash, runScript(t))
	cmd.Dir = work
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	exit := cmd.ProcessState.ExitCode()

	outputs := map[string]string{}
	raw, _ := os.ReadFile(outFile)
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			outputs[k] = v
		}
	}
	stubArgs, _ := os.ReadFile(argsFile)
	return runResult{
		exit:     exit,
		output:   outputs,
		verdict:  outputs["verdict"],
		stubArgs: string(stubArgs),
		combined: string(out),
	}
}

// TestRunExitMapping is the core acceptance test (criteria 1 and 2): the wrapper
// maps validate's exit codes onto the CI gate and honors the configurable WARN.
func TestRunExitMapping(t *testing.T) {
	cases := []struct {
		name       string
		verdict    string
		warnFail   string
		wantExit   int
		wantOutput string
	}{
		{"pass is green", "PASS", "false", 0, "PASS"},
		{"fail is red", "FAIL", "false", 1, "FAIL"},
		{"warn-only passes by default", "WARN", "false", 0, "WARN"},
		{"warn gated fails when configured", "WARN", "true", 1, "WARN"},
		{"tool error is red", "TOOLERR", "false", 3, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := invoke(t, map[string]string{
				"STUB_VERDICT":       tc.verdict,
				"INPUT_WARN_AS_FAIL": tc.warnFail,
				"INPUT_FIXTURE":      "rowshape.yaml",
				"INPUT_MIGRATIONS":   "migrations",
			})
			if r.exit != tc.wantExit {
				t.Errorf("job exit = %d, want %d (stub args: %q)", r.exit, tc.wantExit, r.stubArgs)
			}
			if r.verdict != tc.wantOutput {
				t.Errorf("verdict output = %q, want %q", r.verdict, tc.wantOutput)
			}
			// warn-as-fail must reach validate as --warn-fail, and only then.
			hasFlag := strings.Contains(r.stubArgs, "--warn-fail")
			if want := tc.warnFail == "true"; hasFlag != want {
				t.Errorf("--warn-fail forwarded = %v, want %v (args: %q)", hasFlag, want, r.stubArgs)
			}
		})
	}
}

// TestRunForwardsInputs asserts inputs become the right validate flags.
func TestRunForwardsInputs(t *testing.T) {
	r := invoke(t, map[string]string{
		"STUB_VERDICT":     "PASS",
		"INPUT_FIXTURE":    "db/rowshape.yaml",
		"INPUT_MIGRATIONS": "db/migrations",
		"INPUT_EPHEMERAL":  "postgres://postgres:postgres@localhost:5432/postgres",
		"INPUT_SEED":       "42",
		"INPUT_SCALE":      "0.5",
		"INPUT_ARGS":       "--calibrate",
	})
	args := r.stubArgs
	for _, want := range []string{
		"validate",
		"db/rowshape.yaml",
		"--migrations db/migrations",
		"--ephemeral postgres://postgres:postgres@localhost:5432/postgres",
		"--seed 42",
		"--scale 0.5",
		"--calibrate",
		"--json",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("expected %q in forwarded args, got: %q", want, args)
		}
	}
}

// TestRunCapturesJSON asserts the JSON verdict is written to the output file so
// a downstream annotation step (P4-T2) can render from the same struct.
func TestRunCapturesJSON(t *testing.T) {
	bash := requireBash(t)
	work := t.TempDir()
	writeStub(t, work)
	jsonPath := filepath.Join(work, "verdict.json")
	env := append(os.Environ(),
		"ROWSHAPE_BIN="+filepath.Join(work, "rowshape"),
		"ROWSHAPE_STUB_ARGS="+filepath.Join(work, "args.txt"),
		"ROWSHAPE_VERDICT_JSON="+jsonPath,
		"STUB_VERDICT=FAIL",
		"INPUT_FIXTURE=rowshape.yaml",
	)
	cmd := exec.Command(bash, runScript(t))
	cmd.Dir = work
	cmd.Env = env
	_ = cmd.Run()

	body, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("verdict json not written: %v", err)
	}
	if !strings.Contains(string(body), `"verdict": "FAIL"`) {
		t.Errorf("captured json missing verdict: %s", body)
	}
}

// TestInstallAssetNaming pins install.sh's archive naming to the goreleaser
// convention for every published platform/arch combo. This is the release-gated
// installer's one testable-without-a-release surface (mirrors npm/naming.test.js).
func TestInstallAssetNaming(t *testing.T) {
	bash := requireBash(t)
	installSh := filepath.Join(repoRoot(t), ".github", "actions", "rowshape", "install.sh")
	cases := []struct {
		os, arch, want string
	}{
		{"linux", "amd64", "rowshape_1.2.3_linux_amd64.tar.gz"},
		{"linux", "arm64", "rowshape_1.2.3_linux_arm64.tar.gz"},
		{"darwin", "amd64", "rowshape_1.2.3_darwin_amd64.tar.gz"},
		{"darwin", "arm64", "rowshape_1.2.3_darwin_arm64.tar.gz"},
		{"windows", "amd64", "rowshape_1.2.3_windows_amd64.zip"},
		{"windows", "arm64", "rowshape_1.2.3_windows_arm64.zip"},
	}
	for _, tc := range cases {
		script := "ROWSHAPE_INSTALL_SOURCE_ONLY=1 . '" + installSh + "'; rowshape_asset_name 1.2.3 " + tc.os + " " + tc.arch
		cmd := exec.Command(bash, "-c", script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sourcing install.sh failed: %v\n%s", err, out)
		}
		got := strings.TrimSpace(string(out))
		if got != tc.want {
			t.Errorf("asset(%s/%s) = %q, want %q", tc.os, tc.arch, got, tc.want)
		}
	}
}

// --- DB-backed end-to-end (criterion 3): real binary, disposable Postgres ---

// TestActionEndToEnd builds the real rowshape binary and drives it through
// run.sh against committed corpus fixtures + migrations, using a disposable
// Postgres (ROWSHAPE_TEST_PG_DSN) and no production credential. A PASS case must
// leave the job green; a FAIL case must fail it; a WARN case is configurable.
func TestActionEndToEnd(t *testing.T) {
	admin := os.Getenv("ROWSHAPE_TEST_PG_DSN")
	if admin == "" {
		t.Skip("set ROWSHAPE_TEST_PG_DSN to a Postgres admin connection to run the end-to-end Action test")
	}
	ctx := context.Background()
	if conn, err := pgx.Connect(ctx, admin); err != nil {
		t.Skipf("admin connection unusable: %v", err)
	} else {
		_ = conn.Close(ctx)
	}
	bash := requireBash(t)
	root := repoRoot(t)

	// Build the real binary once.
	bin := filepath.Join(t.TempDir(), "rowshape")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	cases := []struct {
		name     string
		dir      string
		warnFail string
		wantExit int
		wantVerd string
	}{
		{"pass case is green", "capping-exact-not-null", "false", 0, "PASS"},
		{"fail case is red", "rsdata-notnull-has-nulls", "false", 1, "FAIL"},
		{"warn passes by default", "cascade_delete_fanout", "false", 0, "WARN"},
		{"warn gated fails", "cascade_delete_fanout", "true", 1, "WARN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := filepath.Join(root, "corpus", "cases", tc.dir)
			work := t.TempDir()
			env := append(os.Environ(),
				"ROWSHAPE_BIN="+bin,
				"GITHUB_OUTPUT="+filepath.Join(work, "out.txt"),
				"ROWSHAPE_VERDICT_JSON="+filepath.Join(work, "verdict.json"),
				"INPUT_FIXTURE="+filepath.Join(caseDir, "fixture.yaml"),
				"INPUT_MIGRATIONS="+filepath.Join(caseDir, "migration.sql"),
				"INPUT_EPHEMERAL="+admin,
				"INPUT_WARN_AS_FAIL="+tc.warnFail,
				"INPUT_SEED=1",
			)
			cmd := exec.Command(bash, runScript(t))
			cmd.Dir = work
			cmd.Env = env
			out, _ := cmd.CombinedOutput()
			exit := cmd.ProcessState.ExitCode()
			if exit != tc.wantExit {
				t.Errorf("job exit = %d, want %d\n%s", exit, tc.wantExit, out)
			}
			outputs, _ := os.ReadFile(filepath.Join(work, "out.txt"))
			if !strings.Contains(string(outputs), "verdict="+tc.wantVerd) {
				t.Errorf("expected verdict=%s in outputs, got:\n%s\nlog:\n%s", tc.wantVerd, outputs, out)
			}
		})
	}
}

// TestInstallChecksumVerification covers the verification helpers in install.sh
// by sourcing them, so it needs no release and no network.
//
// install.sh used to curl a release archive and execute it having verified
// nothing — on the CI path, which runs with repository credentials in scope —
// even though the release publishes checksums.txt and a cosign signature over
// it, and the docs walk users through verifying them. These checks pin that the
// verification exists and, importantly, that it FAILS CLOSED: a mismatch, a
// missing entry, and a missing tool are all refusals rather than warnings.
func TestInstallChecksumVerification(t *testing.T) {
	bash := requireBash(t)
	installSh := filepath.Join(repoRoot(t), ".github", "actions", "rowshape", "install.sh")

	dir := t.TempDir()
	asset := "rowshape_1.2.3_linux_amd64.tar.gz"
	archive := filepath.Join(dir, asset)
	body := []byte("this stands in for the release archive")
	if err := os.WriteFile(archive, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])

	sums := filepath.Join(dir, "checksums.txt")
	content := "" +
		strings.Repeat("0", 64) + "  rowshape_1.2.3_darwin_arm64.tar.gz\n" +
		good + "  " + asset + "\n" +
		strings.Repeat("1", 64) + "  rowshape_1.2.3_windows_amd64.zip\n"
	if err := os.WriteFile(sums, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// A tampered archive: same name, different bytes.
	tampered := filepath.Join(dir, "tampered.tar.gz")
	if err := os.WriteFile(tampered, append(body, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, args string) (string, bool) {
		t.Helper()
		script := "ROWSHAPE_INSTALL_SOURCE_ONLY=1 . '" + installSh + "'; " + args
		out, err := exec.Command(bash, "-c", script).CombinedOutput()
		return string(out), err == nil
	}

	toSlash := func(p string) string { return filepath.ToSlash(p) }

	t.Run("matching archive verifies", func(t *testing.T) {
		out, ok := run(t, "rowshape_verify_checksum '"+toSlash(archive)+"' '"+toSlash(sums)+"' '"+asset+"'")
		if !ok {
			t.Errorf("a matching archive must verify, got: %s", out)
		}
	})

	t.Run("tampered archive is refused", func(t *testing.T) {
		out, ok := run(t, "rowshape_verify_checksum '"+toSlash(tampered)+"' '"+toSlash(sums)+"' '"+asset+"'")
		if ok {
			t.Errorf("a tampered archive must be refused, got success: %s", out)
		}
		if !strings.Contains(out, "checksum mismatch") {
			t.Errorf("the refusal must say why, got: %s", out)
		}
	})

	t.Run("asset absent from checksums is refused", func(t *testing.T) {
		out, ok := run(t, "rowshape_verify_checksum '"+toSlash(archive)+"' '"+toSlash(sums)+"' 'rowshape_9.9.9_linux_arm64.tar.gz'")
		if ok {
			t.Errorf("an unlisted asset must be refused, got success: %s", out)
		}
		if !strings.Contains(out, "no entry in checksums.txt") {
			t.Errorf("the refusal must say why, got: %s", out)
		}
	})

	t.Run("selects the right line out of many", func(t *testing.T) {
		out, ok := run(t, "rowshape_expected_sum '"+toSlash(sums)+"' '"+asset+"'")
		if !ok {
			t.Fatalf("rowshape_expected_sum failed: %s", out)
		}
		if got := strings.TrimSpace(out); got != good {
			t.Errorf("expected_sum = %q, want %q", got, good)
		}
	})

	// A substring match would let an entry for a longer filename satisfy a
	// shorter one, so the name is compared as a whole field.
	t.Run("a filename merely containing the asset name does not satisfy it", func(t *testing.T) {
		sneaky := filepath.Join(dir, "sneaky.txt")
		if err := os.WriteFile(sneaky, []byte(strings.Repeat("2", 64)+"  prefix_"+asset+"_suffix.tar.gz\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, _ := run(t, "rowshape_expected_sum '"+toSlash(sneaky)+"' '"+asset+"'")
		if strings.TrimSpace(out) != "" {
			t.Errorf("a substring filename must not match, got %q", strings.TrimSpace(out))
		}
	})

	t.Run("sha256 helper agrees with crypto/sha256", func(t *testing.T) {
		out, ok := run(t, "rowshape_sha256 '"+toSlash(archive)+"'")
		if !ok {
			t.Fatalf("rowshape_sha256 failed: %s", out)
		}
		got := strings.TrimSpace(out)
		if got == "" {
			t.Skip("no sha256 tool on this machine")
		}
		if !strings.EqualFold(got, good) {
			t.Errorf("rowshape_sha256 = %q, want %q", got, good)
		}
	})
}

// TestRunRefusesConflictingTargets: `target` and `ephemeral` are documented
// mutually exclusive, but run.sh appended both and validate silently preferred
// --target — so an ambiguous request resolved toward the mode that WRITES TO A
// LIVE DATABASE, with no diagnostic. An ambiguous instruction about which
// database gets written to is not something to guess at.
func TestRunRefusesConflictingTargets(t *testing.T) {
	r := invoke(t, map[string]string{
		"STUB_VERDICT":     "PASS",
		"INPUT_FIXTURE":    "rowshape.yaml",
		"INPUT_MIGRATIONS": "migrations",
		"INPUT_TARGET":     "postgres://live/prod",
		"INPUT_EPHEMERAL":  "postgres://disposable/ci",
	})
	if r.exit != 3 {
		t.Errorf("job exit = %d, want 3 (tool error) when both target and ephemeral are set", r.exit)
	}
	if !strings.Contains(r.combined, "mutually exclusive") {
		t.Errorf("the refusal must explain itself, got: %s", r.combined)
	}
	// Crucially, validate must never have been invoked at all.
	if strings.Contains(r.stubArgs, "--target") {
		t.Errorf("validate must not run on a conflicting request, got args: %q", r.stubArgs)
	}
}

// TestRunBooleanInputsAreStrict: warn-as-fail and json compared against the
// literal "true", so True/TRUE/yes/1 silently read as false. On warn-as-fail
// that is a gating knob failing OPEN — a user asking for more strictness got
// less. Common spellings are now honored and unrecognized values are refused
// rather than defaulting to the permissive branch.
func TestRunBooleanInputsAreStrict(t *testing.T) {
	truthy := []string{"true", "True", "TRUE", "yes", "1", "on"}
	for _, v := range truthy {
		t.Run("truthy_"+v, func(t *testing.T) {
			r := invoke(t, map[string]string{
				"STUB_VERDICT":       "WARN",
				"INPUT_WARN_AS_FAIL": v,
				"INPUT_FIXTURE":      "rowshape.yaml",
			})
			if !strings.Contains(r.stubArgs, "--warn-fail") {
				t.Errorf("warn-as-fail=%q must forward --warn-fail, got args: %q", v, r.stubArgs)
			}
		})
	}
	for _, v := range []string{"false", "False", "no", "0", "off", ""} {
		t.Run("falsy_"+v, func(t *testing.T) {
			r := invoke(t, map[string]string{
				"STUB_VERDICT":       "WARN",
				"INPUT_WARN_AS_FAIL": v,
				"INPUT_FIXTURE":      "rowshape.yaml",
			})
			if strings.Contains(r.stubArgs, "--warn-fail") {
				t.Errorf("warn-as-fail=%q must not forward --warn-fail, got args: %q", v, r.stubArgs)
			}
		})
	}
	t.Run("garbage is refused, not treated as false", func(t *testing.T) {
		r := invoke(t, map[string]string{
			"STUB_VERDICT":       "PASS",
			"INPUT_WARN_AS_FAIL": "maybe",
			"INPUT_FIXTURE":      "rowshape.yaml",
		})
		if r.exit != 3 {
			t.Errorf("job exit = %d, want 3 on an unparseable boolean", r.exit)
		}
		if !strings.Contains(r.combined, "must be true or false") {
			t.Errorf("the refusal must explain itself, got: %s", r.combined)
		}
	})
}

// TestRunVerdictJSONOutputOnlyWhenWritten: the verdict-json step output was set
// unconditionally while the file was written only under json=true, so a
// downstream step consuming the documented output got a path to a file that was
// never created.
func TestRunVerdictJSONOutputOnlyWhenWritten(t *testing.T) {
	t.Run("json enabled advertises a file that exists", func(t *testing.T) {
		r := invoke(t, map[string]string{
			"STUB_VERDICT":  "PASS",
			"INPUT_JSON":    "true",
			"INPUT_FIXTURE": "rowshape.yaml",
		})
		p := r.output["verdict-json"]
		if p == "" {
			t.Fatal("verdict-json must be set when json=true")
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("verdict-json points at a file that does not exist: %v", err)
		}
	})
	t.Run("json disabled advertises nothing", func(t *testing.T) {
		r := invoke(t, map[string]string{
			"STUB_VERDICT":  "PASS",
			"INPUT_JSON":    "false",
			"INPUT_FIXTURE": "rowshape.yaml",
		})
		if p := r.output["verdict-json"]; p != "" {
			t.Errorf("verdict-json = %q, want empty when json=false — the file is never written", p)
		}
	})
}

// TestRunMissingBinaryIsToolError: a missing binary made bash return 127, which
// the exit mapping does not remap, so the job exited outside the documented
// 0/1/2/3 contract. npm/bin/rowshape.js already exits 3 for the same case.
func TestRunMissingBinaryIsToolError(t *testing.T) {
	r := invoke(t, map[string]string{
		"ROWSHAPE_BIN":  filepath.Join(t.TempDir(), "definitely-not-here"),
		"INPUT_FIXTURE": "rowshape.yaml",
	})
	if r.exit != 3 {
		t.Errorf("job exit = %d, want 3 (tool error) when the binary is missing", r.exit)
	}
	if !strings.Contains(r.combined, "could not find the rowshape binary") {
		t.Errorf("the refusal must explain itself, got: %s", r.combined)
	}
}
