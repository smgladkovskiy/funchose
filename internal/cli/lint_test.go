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
	fOfText  = "CREATE OR REPLACE FUNCTION f(a text) RETURNS int LANGUAGE sql AS 'SELECT 1';\n"
	gone     = "is gone from the file"
)

const okRoutine = "CREATE OR REPLACE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT %d';\n"

// lintCase is a branch history and what lint says about it.
type lintCase struct {
	name string
	// main is committed on main, mine on the branch after it forked,
	// theirs on main after the fork. Keys are paths under routines/.
	main, mine, theirs map[string]string
	merged             bool // check out the merge of the branch into main, like a merged results pipeline
	head               bool // pass the branch head with -head
	gitlab             bool // set the variables of a GitLab merged results pipeline
	target             bool // compare with origin/main
	code               int
	out                []string
}

//nolint:paralleltest // lintRepo changes the working directory and the environment (t.Chdir, t.Setenv)
func TestLint(t *testing.T) {
	tests := []lintCase{
		{
			name: "files only, outside a merge request",
			main: map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1)},
			code: 0, out: []string{"routine files are fine (no target branch"},
		},
		{
			name: "a file that does not belong",
			main: map[string]string{fFile: "DROP FUNCTION f();\n"},
			code: 1, out: []string{"routines/s/f.sql:1", "DROP FUNCTION does not belong"},
		},
		{
			name:   "a changed argument list fails",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(a int, b int DEFAULT 0) RETURNS int LANGUAGE sql AS 'SELECT a + b';\n"},
			target: true,
			code:   1, out: []string{fRoutine, "f(a int) returns int " + gone, "f_v2.sql"},
		},
		{
			name:   "a changed result type fails",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(a int) RETURNS bigint LANGUAGE sql AS 'SELECT a';\n"},
			target: true,
			code:   1, out: []string{gone},
		},
		{
			name:   "a reformatted header and a new body pass",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: "create or replace function s.f(\n    a INT\n)\n    returns INT\n    language sql\nas 'SELECT a + 1';\n"},
			target: true,
			code:   0, out: []string{"no signature is gone since the branch forked from origin/main"},
		},
		{
			// git merges edits of different lines; the deploy runs what comes out.
			name:   "the same routine changed in the branch and in the target passes",
			main:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 1)},
			mine:   map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 2)},
			theirs: map[string]string{fFile: fmt.Sprintf(okRoutine, "f", 3)},
			target: true,
			code:   0,
		},
		{
			name:   "a changed signature in a merged results pipeline",
			main:   map[string]string{fFile: fOfA},
			mine:   map[string]string{fFile: fOfText},
			merged: true, gitlab: true,
			code: 1, out: []string{gone},
		},
		{
			// The merge commit has the new signature from the target: lint
			// compares the branch head, which does not.
			name:   "a signature the target changed is not the branch's, merged results pipeline",
			main:   map[string]string{fFile: fOfA, gFile: fmt.Sprintf(okRoutine, "g", 1)},
			mine:   map[string]string{gFile: fmt.Sprintf(okRoutine, "g", 2)},
			theirs: map[string]string{fFile: fOfText},
			merged: true, gitlab: true,
			code: 0,
		},
		{
			name:   "a changed signature in a merge commit checkout with the branch head",
			main:   map[string]string{fFile: fOfA, gFile: fmt.Sprintf(okRoutine, "g", 1)},
			mine:   map[string]string{fFile: fOfText},
			theirs: map[string]string{gFile: fmt.Sprintf(okRoutine, "g", 2)},
			merged: true, head: true, target: true,
			code: 1, out: []string{gone},
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

	runGit(t, "checkout", "-q", "main")

	for p := range tt.theirs {
		commit(t, map[string]string{p: tt.theirs[p]}, "theirs on "+filepath.Base(p[:len(p)-4]))
	}

	runGit(t, "update-ref", "refs/remotes/origin/main", "main")
	runGit(t, "checkout", "-q", "branch")

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
