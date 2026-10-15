// Package cli is the funchose command line: up, plan, status, lint and export.
// It lives apart from the library so that the library does not depend on a
// PostgreSQL driver.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
)

// Exit codes of Run.
const (
	ExitOK      = 0 // done, warnings included
	ExitFailed  = 1 // the run failed or lint found problems
	ExitNoStart = 2 // funchose could not start: flags, environment, configuration
)

// Commands.
const (
	cmdUp      = "up"
	cmdPlan    = "plan"
	cmdStatus  = "status"
	cmdLint    = "lint"
	cmdExport  = "export"
	cmdVersion = "version"
)

var (
	errNoDatabase = errors.New("no database: pass -dsn or set FUNCHOSE_DSN")
	errEnv        = errors.New("invalid environment variable")
	errFailOn     = errors.New("-fail-on takes drift, stale, blocked, kept or none")
	errGit        = errors.New("git")
)

const usage = `usage: funchose <command> [flags]

Commands:
  up       apply the routines that changed
  plan     do what up does, print the diff of every routine, roll everything back
  status   compare files, history and database without changing anything
  lint     check routine files; in a merge request also check that the branch
           does not change a routine the target branch changed after the fork
  export   write the routines of a schema from the database into files
  version  print the version

Run funchose <command> -h for the flags of a command.`

// Run executes one command and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, usage)

		return ExitNoStart
	}

	cmd, args := args[0], args[1:]
	colors := newPalette(colorEnabled(stdout))

	switch cmd {
	case cmdUp, cmdPlan, cmdStatus:
		return apply(ctx, cmd, args, stdout, stderr, colors)
	case cmdLint:
		return lint(ctx, args, stdout, stderr, colors)
	case cmdExport:
		return export(ctx, args, stdout, stderr)
	case cmdVersion:
		fmt.Fprintln(stdout, "funchose", version())

		return ExitOK
	default:
		fmt.Fprintf(stderr, "funchose: unknown command %q\n\n%s\n", cmd, usage)

		return ExitNoStart
	}
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}

	return "(devel)"
}
