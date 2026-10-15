package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/smgladkovskiy/funchose"
)

// lintFlags are the flags of lint.
type lintFlags struct {
	dir, base, since, head string
}

// lint checks the routine files. When it knows the target branch, it also checks
// what the branch did since it forked:
//
//   - a routine changed both in the branch and in the target branch fails: git
//     merges such edits silently when they touch different lines, and nobody
//     reviews or tests the routine that comes out of the merge;
//   - a signature that disappeared from a changed file fails: CREATE OR REPLACE
//     cannot change arguments or the result in place;
//   - removed files are listed, with the other routine files that still mention
//     the removed routine: a later deploy drops it.
func lint(ctx context.Context, args []string, stdout, stderr io.Writer, colors palette) int {
	opts, code := parseLint(args, stderr)
	if opts == nil {
		return code
	}

	routines, problems, err := funchose.Load(os.DirFS(opts.dir))
	if err != nil {
		fmt.Fprintln(stderr, "funchose lint:", err)

		return ExitNoStart
	}

	ref := targetRef(opts.base)

	var (
		changes branch
		clashes []clash
	)

	if ref != "" {
		if changes, clashes, err = compare(ctx, opts, ref); err != nil {
			fmt.Fprintln(stderr, "funchose lint:", err)

			return ExitNoStart
		}

		problems = append(problems, signatureProblems(ctx, opts.dir, changes)...)
	}

	out := lintOutput{out: stdout, colors: colors, dir: opts.dir, annotate: os.Getenv("GITHUB_ACTIONS") == "true"}
	out.problems(problems)
	out.removed(changes.removed, routines)
	out.clashes(clashes, ref)

	switch {
	case len(problems) > 0 || len(clashes) > 0:
		return ExitFailed
	case ref == "":
		fmt.Fprintf(stdout, "%s routine files are fine (no target branch, the branch was not compared)\n",
			colors.green("funchose lint: ok,"))
	default:
		fmt.Fprintf(stdout, "%s no routine this branch changes was changed in %s since the fork\n",
			colors.green("funchose lint: ok,"), ref)
	}

	return ExitOK
}

func parseLint(args []string, stderr io.Writer) (*lintFlags, int) {
	flags := flag.NewFlagSet("funchose lint", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		env  envReader
		opts lintFlags
	)

	flags.StringVar(&opts.dir, "dir", env.text("FUNCHOSE_DIR", "routines"),
		"directory with <schema>/<name>.sql files (env FUNCHOSE_DIR)")
	flags.StringVar(&opts.base, "base", "",
		"git ref of the target branch; defaults to origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME or "+
			"origin/$GITHUB_BASE_REF. Without a target branch only the files are checked")
	flags.StringVar(&opts.since, "since", "",
		"commit the branch forked from; defaults to $CI_MERGE_REQUEST_DIFF_BASE_SHA or git merge-base <base> HEAD")
	flags.StringVar(&opts.head, "head", os.Getenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA"),
		"last commit of the branch, when the checkout is a merge commit (GitLab merged results pipelines set "+
			"$CI_MERGE_REQUEST_SOURCE_BRANCH_SHA); defaults to the working tree")

	if err := flags.Parse(args); err != nil {
		return nil, ExitNoStart
	}

	return &opts, ExitOK
}

// compare finds what the branch did since it forked from ref.
func compare(ctx context.Context, opts *lintFlags, ref string) (branch, []clash, error) {
	fork, err := forkPoint(ctx, opts.since, ref, opts.head)
	if err != nil {
		return branch{}, nil, err
	}

	changes, err := readBranch(ctx, opts.dir, fork, opts.head)
	if err != nil {
		return branch{}, nil, err
	}

	clashes, err := changes.clashes(ctx, opts.dir, ref)

	return changes, clashes, err
}

// lintOutput prints what lint found.
type lintOutput struct {
	out      io.Writer
	colors   palette
	dir      string
	annotate bool // GitHub Actions turns ::error lines into annotations
}

func (l lintOutput) problems(problems []funchose.Problem) {
	if len(problems) == 0 {
		return
	}

	fmt.Fprintf(l.out, "%s\n\n", l.colors.red("funchose lint: "+plural(len(problems), "problem")))

	for _, problem := range problems {
		path := filepath.Join(l.dir, problem.Path)
		if l.annotate {
			fmt.Fprintf(l.out, "::error file=%s,line=%d::%s\n", path, max(problem.Line, 1), problem.Msg)
		}

		loc := path
		if problem.Line > 0 {
			loc = fmt.Sprintf("%s:%d", path, problem.Line)
		}

		fmt.Fprintf(l.out, "  %s\n    %s\n\n", l.colors.bold(loc), problem.Msg)
	}
}

func (l lintOutput) removed(removed []string, routines []funchose.Routine) {
	if len(removed) == 0 {
		return
	}

	fmt.Fprintf(l.out, "%s\n", l.colors.yellow(fmt.Sprintf("funchose lint: this branch removes %s; "+
		"the next deploy retires them, a later one drops them unless they are still called:",
		plural(len(removed), "routine file"))))

	for _, file := range removed {
		fmt.Fprintf(l.out, "  %s\n", file)

		for _, other := range mentions(l.dir, routines, file) {
			if l.annotate {
				fmt.Fprintf(l.out, "::warning file=%s::mentions %s, which this branch removes\n", other, routineName(file))
			}

			fmt.Fprintf(l.out, "    %s\n", l.colors.yellow("still mentioned in "+other))
		}
	}

	fmt.Fprintln(l.out)
}

func (l lintOutput) clashes(clashes []clash, ref string) {
	if len(clashes) == 0 {
		return
	}

	fmt.Fprintf(l.out, "%s\n\n", l.colors.red(fmt.Sprintf("funchose lint: %s changed both in this branch and in %s",
		plural(len(clashes), "routine"), ref)))

	for _, found := range clashes {
		if l.annotate {
			fmt.Fprintf(l.out, "::error file=%s::changed in %s after this branch forked (%s); "+
				"rebase and review the merged routine\n", found.path, ref, found.commit)
		}

		fmt.Fprintf(l.out, "  %s\n    changed in %s after this branch forked: %s\n\n",
			l.colors.bold(found.path), ref, found.commit)
	}

	fmt.Fprintf(l.out, "%s\n\n  %s\n", l.colors.yellow("Rebase, then review and test the merged routines before merging:"),
		l.colors.green(fmt.Sprintf("git fetch origin %s && git rebase %s", strings.TrimPrefix(ref, "origin/"), ref)))
}

// signatureProblems reports the files whose signatures at the fork point are
// gone from the branch: a changed argument list or result type, or a removed
// overload. Neither can be done in place.
func signatureProblems(ctx context.Context, dir string, changes branch) []funchose.Problem {
	var problems []funchose.Problem

	for _, path := range changes.changed {
		old, cur, ok := versions(ctx, dir, changes, path)
		if !ok {
			continue
		}

		for _, sig := range old.Signatures() {
			if slices.Contains(cur.Signatures(), sig) {
				continue
			}

			problems = append(problems, funchose.Problem{Path: old.Path, Msg: fmt.Sprintf(
				"%s is gone from the file: a new argument list or result type is a new routine "+
					"(copy the file to %s_v2.sql and change the copy); "+
					"removing an overload needs a schema migration that drops it", sig, old.Name)})
		}
	}

	return problems
}

// versions parses a changed routine file at the fork point and in the branch.
// It reports false for new files and files that do not parse: Load reports
// those.
func versions(ctx context.Context, dir string, changes branch, path string) (funchose.Routine, funchose.Routine, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || filepath.Ext(path) != ".sql" {
		return funchose.Routine{}, funchose.Routine{}, false
	}

	rel = filepath.ToSlash(rel)

	before, err := gitOutput(ctx, "show", changes.fork+":./"+filepath.ToSlash(path))
	if err != nil {
		return funchose.Routine{}, funchose.Routine{}, false // a new file
	}

	after, err := changes.file(ctx, path)
	if err != nil {
		return funchose.Routine{}, funchose.Routine{}, false
	}

	old, oldProblems := funchose.ParseRoutine(rel, []byte(before))
	cur, curProblems := funchose.ParseRoutine(rel, []byte(after))

	return old, cur, len(oldProblems) == 0 && len(curProblems) == 0
}

// mentions returns the routine files that mention the routine of a removed file
// by name.
func mentions(dir string, routines []funchose.Routine, removed string) []string {
	name := routineName(removed)
	word := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_$])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_$])`)

	var out []string

	for _, routine := range routines {
		if routine.Name != name && word.MatchString(routine.SQL) {
			out = append(out, filepath.Join(dir, routine.Path))
		}
	}

	return out
}

func routineName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".sql")
}
