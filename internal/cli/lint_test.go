package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smgladkovskiy/funchose/internal/cli"
)

// Routine files of the cases below.
const (
	fFile    = "s/f.sql"
	gFile    = "s/g.sql"
	fRoutine = "routines/s/f.sql"
	fOfA     = "CREATE OR REPLACE FUNCTION f(a int) RETURNS int LANGUAGE sql AS 'SELECT a';\n"
)

const okRoutine = "CREATE OR REPLACE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT %d';\n"

// lintCase is a branch history and what lint says about it.
type lintCase struct {
	name string
	// main is committed on main, mine on the branch after it forked,
	// theirs on main after the fork. Keys are paths under routines/.
	main, mine, theirs map[string]string
	remove             []string // paths under routines/ the branch deletes
	rebase             bool
	merged             bool // check out the merge of the branch into main, like a merged results pipeline
	head               bool // pass the branch head with -head
	gitlab             bool // set the variables of a GitLab merged results pipeline
	target             bool // compare with origin/main
	code               int
	out                []string
	notOut             []string
}

//nolint:paralleltest // lintRepo changes the working directory and the environment (t.Chdir, t.Setenv)
func TestLint(t *testing.T) {
	tests := []lintCase{
		{
			name: "files only, outside a merge request",
			main: map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1)},
			code: 0, out: []string{"routine files are fine"},
		},
		{
			name: "a file that does not belong",
			main: map[string]string{fFile: "DROP FUNCTION f();\n"},
			code: 1, out: []string{"routines/s/f.sql:1", "DROP FUNCTION does not belong"},
		},
		{
			name:   "the branch changes a routine the target changed after the fork",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1)},
			mine:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 2)},
			theirs: map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 3)},
			target: true,
			code:   1, out: []string{"1 routine changed both in this branch and in origin/main", fRoutine, "theirs on f", "git rebase origin/main"},
		},
		{
			name:   "the branch and the target change different routines",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1), gFile: fmt.Sprintf(okRoutine, "g", 1)},
			mine:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 2)},
			theirs: map[string]string{gFile: fmt.Sprintf(okRoutine, "g", 2)},
			target: true,
			code:   0, out: []string{"no routine this branch changes was changed in origin/main"},
		},
		{
			name:   "a GitLab merged results pipeline",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1), gFile: fmt.Sprintf(okRoutine, "g", 1)},
			mine:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 2)},
			theirs: map[string]string{gFile: fmt.Sprintf(okRoutine, "g", 2)},
			merged: true, gitlab: true,
			code: 0,
		},
		{
			name:   "a GitLab merged results pipeline, the same routine changed on both sides",
			main:   map[string]string{fFile: "-- f\n" + fmt.Sprintf(okRoutine, "f", 1) + "-- end\n"},
			mine:   map[string]string{fFile: "-- f\n" + fmt.Sprintf(okRoutine, "f", 1) + "-- end, changed in the branch\n"},
			theirs: map[string]string{fFile: "-- f, changed in main\n" + fmt.Sprintf(okRoutine, "f", 1) + "-- end\n"},
			merged: true, gitlab: true,
			code: 1, out: []string{fRoutine},
		},
		{
			name:   "a merge commit checkout with the branch head",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1), gFile: fmt.Sprintf(okRoutine, "g", 1)},
			mine:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 2)},
			theirs: map[string]string{gFile: fmt.Sprintf(okRoutine, "g", 2)},
			merged: true, head: true, target: true,
			code: 0,
		},
		{
			name:   "a merge commit checkout, the same routine changed on both sides",
			main:   map[string]string{fFile: "-- f\n" + fmt.Sprintf(okRoutine, "f", 1) + "-- end\n"},
			mine:   map[string]string{fFile: "-- f\n" + fmt.Sprintf(okRoutine, "f", 1) + "-- end, changed in the branch\n"},
			theirs: map[string]string{fFile: "-- f, changed in main\n" + fmt.Sprintf(okRoutine, "f", 1) + "-- end\n"},
			merged: true, head: true, target: true,
			code: 1, out: []string{fRoutine},
		},
		{
			name:   "removed routine files are listed for review",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1), gFile: fmt.Sprintf(okRoutine, "g", 1)},
			remove: []string{gFile},
			target: true,
			code:   0, out: []string{"this branch removes 1 routine file", "routines/s/g.sql"}, notOut: []string{"still mentioned"},
		},
		{
			name: "a removed routine that another file still calls",
			main: map[string]string{
				fFile:     fmt.Sprintf(okRoutine, "f", 1),
				gFile:     "CREATE OR REPLACE FUNCTION g() RETURNS int LANGUAGE sql AS 'SELECT s.f() + 1';\n",
				"s/h.sql": "CREATE OR REPLACE FUNCTION h() RETURNS int LANGUAGE sql AS 'SELECT ff()';\n",
			},
			remove: []string{fFile},
			target: true,
			code:   0, out: []string{fRoutine, "still mentioned in routines/s/g.sql"}, notOut: []string{"routines/s/h.sql"},
		},
		{
			name:   "a file moved to another schema is a removal",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1)},
			mine:   map[string]string{"t/f.sql": fmt.Sprintf(okRoutine, "f", 1)},
			remove: []string{fFile},
			target: true,
			code:   0, out: []string{"this branch removes 1 routine file", fRoutine},
		},
		{
			name:   "a changed argument list fails",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(a int, b int DEFAULT 0) RETURNS int LANGUAGE sql AS 'SELECT a + b';\n"},
			target: true,
			code:   1, out: []string{fRoutine, "f(a int) returns int is gone from the file", "f_v2.sql"},
		},
		{
			name:   "a changed result type fails",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(a int) RETURNS bigint LANGUAGE sql AS 'SELECT a';\n"},
			target: true,
			code:   1, out: []string{"is gone from the file"},
		},
		{
			name:   "a reformatted header and a new body pass",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "create or replace function s.f(\n    a INT\n)\n    returns INT\n    language sql\nas 'SELECT a + 1';\n"},
			target: true,
			code:   0,
		},
		{
			name:   "a changed signature in a merged results pipeline",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(a text) RETURNS int LANGUAGE sql AS 'SELECT 1';\n"},
			merged: true, gitlab: true,
			code: 1, out: []string{"is gone from the file"},
		},
		{
			name:   "a rebased branch",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1)},
			mine:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 2)},
			theirs: map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 3)},
			rebase: true, target: true,
			code: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := lintRepo(t, tt)

			var buf bytes.Buffer

			code := cli.Run(t.Context(), args, &buf, &buf)
			assert.Equal(t, tt.code, code, buf.String())

			for _, want := range tt.out {
				assert.Contains(t, buf.String(), want)
			}

			for _, unwanted := range tt.notOut {
				assert.NotContains(t, buf.String(), unwanted)
			}
		})
	}
}

// lintRepo builds the git history of a case in a temporary directory, makes it
// the working directory and returns the lint arguments.
func lintRepo(t *testing.T, tt lintCase) []string {
	t.Helper()

	for _, v := range []string{"CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "CI_MERGE_REQUEST_DIFF_BASE_SHA", "GITHUB_BASE_REF",
		"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", "GITHUB_ACTIONS"} {
		t.Setenv(v, "")
	}

	t.Setenv("NO_COLOR", "1")
	// Keep the developer's git config (commit signing, hooks) out of the test.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Chdir(t.TempDir())
	runGit(t, "init", "-q", "-b", "main")
	runGit(t, "config", "user.email", "test@example.com")
	runGit(t, "config", "user.name", "test")
	commit(t, tt.main, "main")
	fork := strings.TrimSpace(gitOut(t, "rev-parse", "HEAD"))
	runGit(t, "checkout", "-q", "-b", "branch")
	commit(t, tt.mine, "mine")

	if len(tt.remove) > 0 {
		for _, p := range tt.remove {
			runGit(t, "rm", "-q", filepath.Join("routines", p))
		}

		runGit(t, "commit", "-q", "-m", "remove")
	}

	runGit(t, "checkout", "-q", "main")

	for p := range tt.theirs {
		commit(t, map[string]string{p: tt.theirs[p]}, "theirs on "+filepath.Base(p[:len(p)-4]))
	}

	runGit(t, "update-ref", "refs/remotes/origin/main", "main")
	runGit(t, "checkout", "-q", "branch")

	if tt.rebase {
		runGit(t, "rebase", "-q", "-X", "ours", "origin/main")
	}

	branchHead := strings.TrimSpace(gitOut(t, "rev-parse", "HEAD"))
	if tt.merged {
		runGit(t, "checkout", "-q", "--detach", "origin/main")
		runGit(t, "merge", "-q", "--no-ff", "-m", "merged result", "branch")
	}

	args := []string{"lint"}

	if tt.gitlab {
		t.Setenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "main")
		t.Setenv("CI_MERGE_REQUEST_DIFF_BASE_SHA", fork)
		t.Setenv("CI_MERGE_REQUEST_SOURCE_BRANCH_SHA", branchHead)
	}

	if tt.head {
		args = append(args, "-head", branchHead)
	}

	if tt.target {
		args = append(args, "-base", "origin/main")
	}

	return args
}

func commit(t *testing.T, files map[string]string, msg string) {
	t.Helper()

	if len(files) == 0 {
		return
	}

	for p, content := range files {
		path := filepath.Join("routines", p)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	runGit(t, "add", "-A")
	runGit(t, "commit", "-q", "-m", msg)
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	gitOut(t, args...)
}

func gitOut(t *testing.T, args ...string) string {
	t.Helper()

	b, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
	require.NoError(t, err, string(b))

	return string(b)
}
