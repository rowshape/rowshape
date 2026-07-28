package runner

import (
	"context"
	"os/exec"
)

// drizzleRunner drives Drizzle Kit. Signature: a `drizzle.config.{ts,js,mjs}` at
// the project root (PRD §13).
type drizzleRunner struct {
	root string
}

func (d *drizzleRunner) Kind() Kind { return Drizzle }

// Binary is the executable ApplyCmd shells out to. On Windows this resolves to
// drizzle-kit.CMD via PATHEXT, which exec.LookPath honors.
func (d *drizzleRunner) Binary() string { return "drizzle-kit" }

// ApplyCmd runs `drizzle-kit migrate`, which applies pending migrations. The
// target DSN is provided via DATABASE_URL, which the drizzle config resolves.
func (d *drizzleRunner) ApplyCmd(ctx context.Context, dsn string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "drizzle-kit", "migrate")
	cmd.Dir = d.root
	cmd.Env = envWithDSN(dsn)
	return cmd
}

// drizzleConfigs are the config filenames drizzle-kit accepts, in the order it
// resolves them.
//
// CR-T20: .cjs and .json were missing, so a project using either was silently
// reported as "no runner detected" — the failure is quiet and looks like the
// project simply is not a Drizzle project, which is the hardest kind of gap to
// notice. .cjs is what a CommonJS project needs when package.json sets
// "type": "module"; .json is the config-only form drizzle-kit supports for
// projects that would rather not execute a config file at all.
var drizzleConfigs = []string{
	"drizzle.config.ts",
	"drizzle.config.js",
	"drizzle.config.mjs",
	"drizzle.config.cjs",
	"drizzle.config.json",
}

// detectDrizzle recognizes a Drizzle project by its config file. Precedence is
// the order of drizzleConfigs, so a project carrying more than one resolves
// deterministically rather than by directory-listing order.
func detectDrizzle(dir string) (Runner, bool) {
	if firstExisting(dir, drizzleConfigs...) != "" {
		return &drizzleRunner{root: dir}, true
	}
	return nil, false
}
