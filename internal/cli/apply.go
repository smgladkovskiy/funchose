package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/smgladkovskiy/funchose"
)

// applyFlags are the flags of up, plan and status.
type applyFlags struct {
	conn

	build                                int64
	local                                bool
	downgrade, drop, overwrite, replicas bool
	retireLimit                          int64
	removedSchemas, failOn               string
	lockTimeout                          time.Duration
}

const (
	defaultRetireLimit = 10
	defaultLockTimeout = 5 * time.Minute
)

func (f *applyFlags) register(flags *flag.FlagSet, env *envReader) {
	f.conn.register(flags, env)
	flags.Int64Var(&f.build, "build", env.number("FUNCHOSE_BUILD", 0),
		"number of the build being deployed, growing over time, e.g. $CI_PIPELINE_IID (env FUNCHOSE_BUILD); "+
			"up and plan need it, or -local")
	flags.BoolVar(&f.local, "local", false,
		"run as build 0 against a developer database; never overwrites what CI applied")
	flags.BoolVar(&f.downgrade, "allow-downgrade", env.boolean("FUNCHOSE_ALLOW_DOWNGRADE"),
		"let an older build roll routines back to its versions (env FUNCHOSE_ALLOW_DOWNGRADE)")
	flags.BoolVar(&f.drop, "allow-drop", env.boolean("FUNCHOSE_ALLOW_DROP"),
		"drop routines funchose cannot prove unused: created by a rolled-back build, "+
			"or retired with uncounted calls (env FUNCHOSE_ALLOW_DROP)")
	flags.BoolVar(&f.overwrite, "overwrite-drift", env.boolean("FUNCHOSE_OVERWRITE_DRIFT"),
		"replace definitions funchose did not apply: changed by hand, or changed on adoption "+
			"(env FUNCHOSE_OVERWRITE_DRIFT)")
	flags.BoolVar(&f.replicas, "replicas", env.boolean("FUNCHOSE_REPLICAS"),
		"routines are also called on read replicas, so the call counter cannot prove a routine unused "+
			"(env FUNCHOSE_REPLICAS)")
	flags.Int64Var(&f.retireLimit, "retire-limit", env.number("FUNCHOSE_RETIRE_LIMIT", defaultRetireLimit),
		"most routines one run may retire, 0 for no limit (env FUNCHOSE_RETIRE_LIMIT)")
	flags.StringVar(&f.removedSchemas, "removed-schemas", env.text("FUNCHOSE_REMOVED_SCHEMAS", ""),
		"comma-separated schemas whose directory was removed on purpose (env FUNCHOSE_REMOVED_SCHEMAS)")
	flags.StringVar(&f.failOn, "fail-on", env.text("FUNCHOSE_FAIL_ON", string(funchose.ActionStale)),
		"comma-separated warnings that fail up and plan: drift, stale, blocked, kept, or none (env FUNCHOSE_FAIL_ON)")
	flags.DurationVar(&f.lockTimeout, "lock-timeout", env.duration("FUNCHOSE_LOCK_TIMEOUT", defaultLockTimeout),
		"how long to wait for another run on the same history table (env FUNCHOSE_LOCK_TIMEOUT)")
}

func (f *applyFlags) options() ([]funchose.Option, error) {
	failOn, err := parseFailOn(f.failOn)
	if err != nil {
		return nil, err
	}

	opts := []funchose.Option{
		funchose.WithBuild(f.build),
		funchose.WithAllowDowngrade(f.downgrade),
		funchose.WithAllowDrop(f.drop),
		funchose.WithOverwriteDrift(f.overwrite),
		funchose.WithReplicas(f.replicas),
		funchose.WithRetireLimit(int(f.retireLimit)),
		funchose.WithRemovedSchemas(list(f.removedSchemas)...),
		funchose.WithFailOn(failOn...),
		funchose.WithLockTimeout(f.lockTimeout),
		funchose.WithLogger(nil),
	}
	if f.table != "" {
		opts = append(opts, funchose.WithTable(f.table))
	}

	if f.local {
		opts = append(opts, funchose.WithLocal())
	}

	return opts, nil
}

// parseFailOn reads "stale,drift" into actions; "none" or "" is no action.
func parseFailOn(names string) ([]funchose.Action, error) {
	var out []funchose.Action

	for _, name := range list(names) {
		if name == "none" {
			continue
		}

		action := funchose.Action(name)
		if !action.Warns() {
			return nil, fmt.Errorf("%w, not %q", errFailOn, name)
		}

		out = append(out, action)
	}

	return out, nil
}

func list(items string) []string {
	var out []string

	for item := range strings.SplitSeq(items, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}

	return out
}

// apply runs up, plan or status.
func apply(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer, colors palette) int {
	opts, options, code := parseApply(cmd, args, stderr)
	if opts == nil {
		return code
	}

	db, err := opts.open()
	if err != nil {
		fmt.Fprintln(stderr, "funchose:", err)

		return ExitNoStart
	}
	defer db.Close()

	provider, err := funchose.NewProvider(db, os.DirFS(opts.dir), options...)
	if err != nil {
		fmt.Fprintln(stderr, colors.red(err.Error()))

		if errors.Is(err, funchose.ErrInvalidFiles) {
			return ExitFailed
		}

		return ExitNoStart
	}

	var rep funchose.Report

	switch cmd {
	case cmdUp:
		rep, err = provider.Up(ctx)
	case cmdPlan:
		rep, err = provider.Plan(ctx)
	default:
		rep, err = provider.Status(ctx)
	}

	return finish(cmd, rep, err, stdout, stderr, colors)
}

// parseApply reads the flags and the environment. On failure it returns nil
// and the exit code.
func parseApply(cmd string, args []string, stderr io.Writer) (*applyFlags, []funchose.Option, int) {
	flags := flag.NewFlagSet("funchose "+cmd, flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		env  envReader
		opts applyFlags
	)

	opts.register(flags, &env)

	if err := env.err(); err != nil {
		fmt.Fprintln(stderr, "funchose:", err)

		return nil, nil, ExitNoStart
	}

	if err := flags.Parse(args); err != nil {
		return nil, nil, ExitNoStart
	}

	options, err := opts.options()
	if err != nil {
		fmt.Fprintln(stderr, "funchose:", err)

		return nil, nil, ExitNoStart
	}

	return &opts, options, ExitOK
}

// finish prints the outcome of a run and returns the exit code.
func finish(cmd string, rep funchose.Report, err error, stdout, stderr io.Writer, colors palette) int {
	switch {
	case errors.Is(err, funchose.ErrBuildRequired):
		fmt.Fprintln(stderr, err)

		return ExitNoStart
	case errors.Is(err, funchose.ErrFailOn):
		printReport(stdout, cmd, rep, colors)
		fmt.Fprintln(stderr, colors.red(err.Error()))

		return ExitFailed
	case err != nil:
		fmt.Fprintln(stderr, colors.red(err.Error()))

		return ExitFailed
	default:
		printReport(stdout, cmd, rep, colors)

		return ExitOK
	}
}
