package functional_test

import (
	"cmp"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/smgladkovskiy/funchose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fn(name, result string) string {
	return fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s(x int DEFAULT 0)
    RETURNS text
    LANGUAGE plpgsql
AS $$
BEGIN
    RETURN '%s';
END
$$;`, name, result)
}

// Routines, files and tables the cases below use again and again.
const (
	fKey, fFile  = "app.f", "app/f"
	gKey, gFile  = "app.g", "app/g"
	aKey, aFile  = "app.a", "app/a"
	kFile        = "app/k"
	keepFile     = "app/keep"
	qKey         = "app.q"
	trgKey       = "app.trg"
	svcB         = "svc_b.b"
	oldG         = "old.g"
	historyTable = "public.history"
	methodPlan   = "plan"
	methodStatus = "status"
)

// step is one run of funchose, like a deploy or a pod start.
type step struct {
	setup     string            // SQL run before the step
	files     map[string]string // "schema/name" -> content; the files of this run
	method    string            // up (default), plan or status
	opts      []funchose.Option
	parallel  int    // run Up this many times at once
	holdLock  bool   // another session holds the funchose lock during the step
	waitCalls string // "schema.name" whose calls, made in setup, must reach the statistics first

	want       map[string]funchose.Action // "schema.name" -> action
	changed    map[string]bool
	notes      map[string]string   // "schema.name" -> substring of the note
	diffs      map[string][]string // "schema.name" -> substrings of the diff
	err        error               // errors.Is
	errText    string              // substring of the error
	results    map[string]string   // "schema.name" -> what SELECT schema.name() returns
	absent     []string            // "schema.name" of routines that must not exist
	check      string              // SQL that must return true after the step
	rolledBack int                 // history records marked as rolled back after the step
}

// providerCase is a sequence of runs against one fresh database.
type providerCase struct {
	name    string
	schemas []string // default app
	track   string   // track_functions, default pl
	table   string   // history table, default <schema>.funchose_history
	steps   []step
}

// b is WithBuild, short for the tables below.
func b(n int64) funchose.Option { return funchose.WithBuild(n) }

// gCallsF is a routine g whose body calls f.
const gCallsF = "CREATE OR REPLACE FUNCTION g() RETURNS text LANGUAGE plpgsql AS $$ BEGIN RETURN app.f(); END $$;"

// keep is another routine of the schema, so that the schema has a directory.
var keep = map[string]string{kFile: fn("k", "k")}

// applyCases: creating, adopting and changing routines, and what stops a run.
var applyCases = []providerCase{
	{
		name: "create then nothing to do",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionCreate}, changed: map[string]bool{fKey: true}, results: map[string]string{fKey: "v1"}},
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionUnchanged}},
		},
	},
	{
		name: "adopt an existing routine that matches the file",
		steps: []step{
			{setup: "SET LOCAL search_path TO app;" + fn("f", "v1"), files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionAdopt}, changed: map[string]bool{fKey: false}, notes: map[string]string{fKey: "matches the database"}},
		},
	},
	{
		name: "adopting a file that changes the routine needs overwriting allowed",
		steps: []step{
			{setup: "SET LOCAL search_path TO app;" + fn("f", "before"), files: map[string]string{fFile: fn("f", "after")}, opts: opts(b(1)),
				err: funchose.ErrDrift, errText: "never applied", results: map[string]string{fKey: "before"}},
			{method: methodPlan, files: map[string]string{fFile: fn("f", "after")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionAdopt}, notes: map[string]string{fKey: "would replace a definition funchose did not apply"},
				diffs: map[string][]string{fKey: {"-    RETURN 'before';", "+    RETURN 'after';"}}, results: map[string]string{fKey: "before"}},
			{files: map[string]string{fFile: fn("f", "after")}, opts: opts(b(1), funchose.WithOverwriteDrift(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionAdopt}, changed: map[string]bool{fKey: true}, results: map[string]string{fKey: "after"}},
		},
	},
	{
		name: "a routine changed by hand is not overwritten silently",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{setup: "SET LOCAL search_path TO app;" + fn("f", "hand"), files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionDrift}, results: map[string]string{fKey: "hand"}},
			{method: methodPlan, files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, notes: map[string]string{fKey: "would replace a definition funchose did not apply"},
				diffs: map[string][]string{fKey: {"+    RETURN 'v2';"}}, results: map[string]string{fKey: "hand"}},
			{files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2)), err: funchose.ErrDrift, errText: "would overwrite that change"},
			{files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2), funchose.WithOverwriteDrift(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, results: map[string]string{fKey: "v2"}},
		},
	},
	{
		name: "a routine dropped by hand is drift, and overwriting recreates it",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{setup: "DROP FUNCTION app.f(int)", files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionDrift}, absent: []string{fKey}},
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1), funchose.WithOverwriteDrift(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, results: map[string]string{fKey: "v1"}},
		},
	},
	{
		name: "a hand change the new version repeats is accepted",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{setup: "SET LOCAL search_path TO app;" + fn("f", "v2"), files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, changed: map[string]bool{fKey: false}},
		},
	},
	{
		name: "a new argument list fails the whole run",
		steps: []step{
			{files: map[string]string{aFile: fn("a", "v1"), fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{aFile: fn("a", "v2"), fFile: strings.Replace(fn("f", "v2"), "x int DEFAULT 0", "x int DEFAULT 0, y int DEFAULT 0", 1)}, opts: opts(b(2)),
				err: funchose.ErrOverload, results: map[string]string{aKey: "v1", fKey: "v1"}},
		},
	},
	{
		name: "a new result type fails",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(x int DEFAULT 0) RETURNS int LANGUAGE sql AS 'SELECT 1';"}, opts: opts(b(2)),
				errText: "app/f.sql:1: ERROR: cannot change return type"},
		},
	},
	{
		name: "plan shows the diff and changes nothing",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{method: methodPlan, files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, diffs: map[string][]string{fKey: {"-    RETURN 'v1';", "+    RETURN 'v2';"}},
				results: map[string]string{fKey: "v1"}},
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)), want: map[string]funchose.Action{fKey: funchose.ActionUnchanged}},
		},
	},
	{
		name: "status writes nothing and needs no build",
		steps: []step{
			{method: methodStatus, files: map[string]string{fFile: fn("f", "v1")},
				want: map[string]funchose.Action{fKey: funchose.ActionCreate}, absent: []string{fKey}, check: "SELECT to_regclass('app.funchose_history') IS NULL"},
		},
	},
	{
		name: "an sql function may call a routine created later in the run",
		steps: []step{
			{files: map[string]string{
				aFile:   "CREATE OR REPLACE FUNCTION a() RETURNS text LANGUAGE sql SET search_path FROM CURRENT AS 'SELECT b(0)';",
				"app/b": fn("b", "from b"),
			}, opts: opts(b(1)),
				want: map[string]funchose.Action{aKey: funchose.ActionCreate, "app.b": funchose.ActionCreate}, results: map[string]string{aKey: "from b"}},
		},
	},
	{
		name: "a syntax error in a plpgsql body fails",
		steps: []step{
			{files: map[string]string{fFile: "CREATE OR REPLACE FUNCTION f() RETURNS text LANGUAGE plpgsql AS $$ BEGIN RETURN 'x' END $$;"}, opts: opts(b(1)),
				errText: "syntax error", absent: []string{fKey}},
		},
	},
	{
		// The file runs statement by statement, exactly as checked: an error
		// names the statement that failed.
		name: "an error names the line of the statement that failed",
		steps: []step{
			{files: map[string]string{fFile: "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';\n\n" +
				"COMMENT ON FUNCTION f(text) IS 'no such overload';\n"}, opts: opts(b(1)),
				errText: "app/f.sql:3: ERROR: function f(text) does not exist", absent: []string{fKey}},
		},
	},
	{
		name: "parameters named begin are fine",
		steps: []step{
			{files: map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(begin int) RETURNS int LANGUAGE sql AS 'SELECT 1';\n" +
				"CREATE OR REPLACE FUNCTION f(begin text) RETURNS int LANGUAGE sql AS 'SELECT 2';\n"}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionCreate}, check: "SELECT app.f(1) = 1 AND app.f('x'::text) = 2"},
		},
	},
	{
		// pg_get_functiondef qualifies types that are not on search_path: read
		// under different paths the same routine would look changed.
		name: "a routine using a type of its schema is not reported as drift",
		steps: []step{
			{setup: "CREATE DOMAIN app.code AS text", files: map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(c code) RETURNS code LANGUAGE sql AS 'SELECT c';"}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionCreate}},
			{files: map[string]string{fFile: "CREATE OR REPLACE FUNCTION f(c code) RETURNS code LANGUAGE sql AS 'SELECT c';"}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionUnchanged}},
		},
	},
	{
		name: "concurrent runs apply once",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)), parallel: 4, results: map[string]string{fKey: "v1"}},
		},
	},
	{
		name: "a run with work waits for the lock no longer than the timeout",
		steps: []step{
			{holdLock: true, files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1), funchose.WithLockTimeout(200*time.Millisecond)),
				err: funchose.ErrLockTimeout, absent: []string{fKey}},
		},
	},
	{
		name: "a run with nothing to do does not wait for the lock",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{holdLock: true, files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1), funchose.WithLockTimeout(200*time.Millisecond)),
				want: map[string]funchose.Action{fKey: funchose.ActionUnchanged}},
		},
	},
	{
		name: "a missing schema fails instead of falling into public",
		steps: []step{
			{setup: "DROP SCHEMA app CASCADE", files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)), err: funchose.ErrMissingSchema},
		},
	},
}

func TestApply(t *testing.T) {
	t.Parallel()
	runCases(t, applyCases)
}

// rollbackCases: older builds, stale pods and deliberate rollbacks.
var rollbackCases = []providerCase{
	{
		name: "an older build is stale until rolling back is allowed",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)), want: map[string]funchose.Action{fKey: funchose.ActionCreate}},
			{files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2)), want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, results: map[string]string{fKey: "v2"}},
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1)),
				want: map[string]funchose.Action{fKey: funchose.ActionStale}, notes: map[string]string{fKey: "build 2 applied a newer version"}, results: map[string]string{fKey: "v2"}},
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1), funchose.WithAllowDowngrade(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionRollback}, results: map[string]string{fKey: "v1"}, rolledBack: 1},
			// A pod of the rolled-back build restarts: it must not undo the rollback.
			{files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionStale}, notes: map[string]string{fKey: "was rolled back"}, results: map[string]string{fKey: "v1"}},
			{files: map[string]string{fFile: fn("f", "v3")}, opts: opts(b(3)), want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, results: map[string]string{fKey: "v3"}},
		},
	},
	{
		name: "rolling back to a version the history never saw",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{fFile: fn("f", "v3")}, opts: opts(b(3))},
			{files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(2), funchose.WithAllowDowngrade(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionRollback}, notes: map[string]string{fKey: "no record of this version"}, results: map[string]string{fKey: "v2"}},
		},
	},
	{
		name:  "rolling back past adoption puts back the untracked definition",
		table: historyTable,
		steps: []step{
			{setup: "SET LOCAL search_path TO app;" + fn("f", "untracked"), files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "adopted")}, opts: opts(b(2), funchose.WithOverwriteDrift(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionAdopt}},
			{files: keep, opts: opts(b(1)), want: map[string]funchose.Action{fKey: funchose.ActionStale}, results: map[string]string{fKey: "adopted"}},
			{files: keep, opts: opts(b(1), funchose.WithAllowDowngrade(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionRollback}, notes: map[string]string{fKey: "no longer tracked"}, results: map[string]string{fKey: "untracked"}},
			// funchose put the old definition back itself: adopting again overwrites nothing foreign.
			{files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "again")}, opts: opts(b(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionAdopt}, results: map[string]string{fKey: "again"}},
		},
	},
	{
		name:  "rolling back a created routine needs permission to drop",
		table: historyTable,
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), gFile: fn("g", "new")}, opts: opts(b(2)), want: map[string]funchose.Action{gKey: funchose.ActionCreate}},
			{files: keep, opts: opts(b(1), funchose.WithAllowDowngrade(true)),
				want: map[string]funchose.Action{gKey: funchose.ActionBlocked}, results: map[string]string{gKey: "new"}},
			{files: keep, opts: opts(b(1), funchose.WithAllowDowngrade(true), funchose.WithAllowDrop(true)),
				want: map[string]funchose.Action{gKey: funchose.ActionDrop}, absent: []string{gKey}, rolledBack: 1},
			{files: map[string]string{kFile: fn("k", "k"), gFile: fn("g", "back")}, opts: opts(b(3)),
				want: map[string]funchose.Action{gKey: funchose.ActionCreate}, results: map[string]string{gKey: "back"}},
		},
	},
	{
		name: "a deliberate rollback brings a dropped routine back",
		steps: []step{
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(2)), want: map[string]funchose.Action{fKey: funchose.ActionRetire}},
			{files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(3)), want: map[string]funchose.Action{fKey: funchose.ActionDrop}, absent: []string{fKey}},
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(1)), want: map[string]funchose.Action{fKey: funchose.ActionStale}, absent: []string{fKey}},
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(1), funchose.WithAllowDowngrade(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionCreate}, results: map[string]string{fKey: "v1"}, rolledBack: 2},
		},
	},
	{
		name: "a local run does not overwrite CI",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "ci")}, opts: opts(b(5))},
			{files: map[string]string{fFile: fn("f", "local")}, opts: opts(funchose.WithLocal()),
				want: map[string]funchose.Action{fKey: funchose.ActionStale}, results: map[string]string{fKey: "ci"}},
		},
	},
}

func TestRollback(t *testing.T) {
	t.Parallel()
	runCases(t, rollbackCases)
}

// removalCases: removed files, retirement and drops.
var removalCases = []providerCase{
	{
		name: "a removed file retires the routine, the next deploy drops it",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(1))},
			{setup: "SET LOCAL search_path TO app;" + fn("u", "manual"), files: map[string]string{gFile: fn("g", "v1")}, opts: opts(b(2)),
				want:    map[string]funchose.Action{fKey: funchose.ActionRetire, gKey: funchose.ActionCreate, "app.u": funchose.ActionUntracked},
				results: map[string]string{fKey: "v1", "app.u": "manual"}},
			// Pods of the same build restart: nothing is dropped.
			{files: map[string]string{gFile: fn("g", "v1")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionRetired}, notes: map[string]string{fKey: "dropped by a newer build"}, results: map[string]string{fKey: "v1"}},
			{files: map[string]string{gFile: fn("g", "v1")}, opts: opts(b(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionDrop}, changed: map[string]bool{fKey: true}, notes: map[string]string{fKey: "not called since"}, absent: []string{fKey}},
		},
	},
	{
		name: "a retired routine that is still called waits for the next deploy",
		steps: []step{
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(2)), want: map[string]funchose.Action{fKey: funchose.ActionRetire}},
			{setup: "SELECT app.f()", waitCalls: fKey, files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionKept}, notes: map[string]string{fKey: "called 1 times since the file was removed"}, results: map[string]string{fKey: "v1"}},
			{files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(4)), want: map[string]funchose.Action{fKey: funchose.ActionDrop}, absent: []string{fKey}},
		},
	},
	{
		name: "a file that comes back ends the retirement",
		steps: []step{
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(2)), want: map[string]funchose.Action{fKey: funchose.ActionRetire}},
			// A pod of the release that still had the file restarts.
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(1)), want: map[string]funchose.Action{fKey: funchose.ActionStale}},
			{files: map[string]string{keepFile: fn("keep", "k"), fFile: fn("f", "v1")}, opts: opts(b(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionUpdate}, notes: map[string]string{fKey: "no longer retired"}},
			{files: map[string]string{keepFile: fn("keep", "k")}, opts: opts(b(4)), want: map[string]funchose.Action{fKey: funchose.ActionRetire}, results: map[string]string{fKey: "v1"}},
		},
	},
	{
		name:  "with track_functions off a retired routine is not dropped without permission",
		track: "none",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionRetire}, notes: map[string]string{fKey: "track_functions is off"}},
			{setup: "SELECT app.f() FROM generate_series(1, 100)", files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionBlocked}, results: map[string]string{fKey: "v1"}},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(4), funchose.WithAllowDrop(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionDrop}, absent: []string{fKey}},
		},
	},
	{
		name:  "a called SQL routine is kept, a silent one waits for permission",
		track: "all",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), "app/q": "CREATE OR REPLACE FUNCTION q() RETURNS int LANGUAGE sql VOLATILE AS $$ SELECT 1; SELECT 2 $$;"}, opts: opts(b(1))},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2)),
				want: map[string]funchose.Action{qKey: funchose.ActionRetire}, notes: map[string]string{qKey: "inlined"}},
			{setup: "SELECT app.q() FROM generate_series(1, 100)", waitCalls: qKey, files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(3)),
				want: map[string]funchose.Action{qKey: funchose.ActionKept}},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(4)), want: map[string]funchose.Action{qKey: funchose.ActionBlocked}},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(5), funchose.WithAllowDrop(true)),
				want: map[string]funchose.Action{qKey: funchose.ActionDrop}, absent: []string{qKey}},
		},
	},
	{
		name: "with read replicas a silent counter does not drop a routine",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "v1")}, opts: opts(b(1))},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2), funchose.WithReplicas(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionRetire}, notes: map[string]string{fKey: "read replicas"}},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(3), funchose.WithReplicas(true)),
				want: map[string]funchose.Action{fKey: funchose.ActionBlocked}, results: map[string]string{fKey: "v1"}},
		},
	},
	{
		name: "a routine a trigger uses is not dropped, and the rest of the run goes on",
		steps: []step{
			{setup: "CREATE TABLE app.t (x int)", opts: opts(b(1)), files: map[string]string{
				"app/trg": "CREATE OR REPLACE FUNCTION trg() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;",
				fFile:     fn("f", "v1")}},
			{setup: "CREATE TRIGGER tr BEFORE INSERT ON app.t FOR EACH ROW EXECUTE FUNCTION app.trg()", files: map[string]string{fFile: fn("f", "v1")}, opts: opts(b(2)),
				want: map[string]funchose.Action{trgKey: funchose.ActionRetire}},
			{files: map[string]string{fFile: fn("f", "v2")}, opts: opts(b(3)),
				want:    map[string]funchose.Action{trgKey: funchose.ActionBlocked, fKey: funchose.ActionUpdate},
				notes:   map[string]string{trgKey: "trigger tr on table app.t"},
				results: map[string]string{fKey: "v2"}, check: "SELECT to_regprocedure('app.trg()') IS NOT NULL"},
		},
	},
	{
		// PostgreSQL records no dependency on a routine that a body calls, and
		// a caller that did not run between two deploys left the counter as it was.
		name: "a routine another routine calls is not dropped until the call is gone",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "v1"), gFile: gCallsF}, opts: opts(b(1))},
			{files: map[string]string{kFile: fn("k", "k"), gFile: gCallsF}, opts: opts(b(2)), want: map[string]funchose.Action{fKey: funchose.ActionRetire}},
			// Another change makes the run apply files, not stop at the first pass.
			{files: map[string]string{kFile: fn("k", "k2"), gFile: gCallsF}, opts: opts(b(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionBlocked}, notes: map[string]string{fKey: "called by app.g()"},
				results: map[string]string{fKey: "v1", gKey: "v1"}},
			{files: map[string]string{kFile: fn("k", "k2"), gFile: gCallsF}, opts: opts(b(4)),
				want: map[string]funchose.Action{fKey: funchose.ActionBlocked}},
			// The bodies are read after the files are applied: the run that
			// removes the call drops the routine, even when blocked fails runs.
			{files: map[string]string{kFile: fn("k", "k2"), gFile: strings.Replace(gCallsF, "app.f()", "'alone'", 1)},
				opts: opts(b(5), funchose.WithFailOn(funchose.ActionBlocked)),
				want: map[string]funchose.Action{fKey: funchose.ActionDrop, gKey: funchose.ActionUpdate}, absent: []string{fKey},
				results: map[string]string{gKey: "alone"}},
		},
	},
	{
		name: "routines that call each other are dropped together",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "v1"), gFile: gCallsF}, opts: opts(b(1))},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2)),
				want: map[string]funchose.Action{fKey: funchose.ActionRetire, gKey: funchose.ActionRetire}},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(3), funchose.WithFailOn(funchose.ActionBlocked)),
				want: map[string]funchose.Action{fKey: funchose.ActionDrop, gKey: funchose.ActionDrop}, absent: []string{fKey, gKey}},
		},
	},
	{
		name: "overloads that use each other are dropped together",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), "app/o": "CREATE OR REPLACE FUNCTION o(x text) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT length(x); END;\n" +
				"CREATE OR REPLACE FUNCTION o(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT o(x::text); END;\n"}, opts: opts(b(1)),
				check: "SELECT app.o(12345) = 5"},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2)), want: map[string]funchose.Action{"app.o": funchose.ActionRetire}},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(3), funchose.WithAllowDrop(true)),
				want: map[string]funchose.Action{"app.o": funchose.ActionDrop}, check: "SELECT count(*) = 0 FROM pg_proc WHERE proname = 'o'"},
		},
	},
}

func TestRemoval(t *testing.T) {
	t.Parallel()
	runCases(t, removalCases)
}

// safetyCases: the guards against losing routines by mistake.
var safetyCases = []providerCase{
	{
		// Two services in one database share a history table by mistake. The
		// second one must not take the routines of the first for removed files.
		name:    "a shared history table fails instead of retiring another service's routines",
		schemas: []string{"svc_a", "svc_b"},
		table:   "public.shared",
		steps: []step{
			{files: map[string]string{"svc_b/b": fn("b", "b")}, opts: opts(b(7))},
			{files: map[string]string{"svc_a/a": fn("a", "a")}, opts: opts(b(120)),
				err: funchose.ErrSchemaWithoutDir, errText: "svc_b", results: map[string]string{svcB: "b"}, absent: []string{"svc_a.a"}},
			{files: map[string]string{"svc_a/a": fn("a", "a")}, opts: opts(b(5)),
				want: map[string]funchose.Action{"svc_a.a": funchose.ActionCreate, svcB: funchose.ActionStale}, results: map[string]string{svcB: "b"}},
		},
	},
	{
		name:  "an empty directory fails instead of retiring everything",
		table: historyTable,
		steps: []step{
			{files: map[string]string{fFile: fn("f", "f"), gFile: fn("g", "g")}, opts: opts(b(1))},
			{opts: opts(b(2)), err: funchose.ErrSchemaWithoutDir, results: map[string]string{fKey: "f", gKey: "g"}},
			{opts: opts(b(3)), err: funchose.ErrSchemaWithoutDir, results: map[string]string{fKey: "f", gKey: "g"}},
		},
	},
	{
		name:    "a schema removed on purpose is retired, then dropped",
		schemas: []string{"app", "old"},
		table:   historyTable,
		steps: []step{
			{files: map[string]string{fFile: fn("f", "f"), "old/g": fn("g", "g")}, opts: opts(b(1))},
			{files: map[string]string{fFile: fn("f", "f")}, opts: opts(b(2), funchose.WithRemovedSchemas("old")),
				want: map[string]funchose.Action{oldG: funchose.ActionRetire}},
			{files: map[string]string{fFile: fn("f", "f")}, opts: opts(b(3), funchose.WithRemovedSchemas("old")),
				want: map[string]funchose.Action{oldG: funchose.ActionDrop}, absent: []string{oldG}},
			{files: map[string]string{fFile: fn("f", "f")}, opts: opts(b(4)), want: map[string]funchose.Action{fKey: funchose.ActionUnchanged}},
		},
	},
	{
		name: "the retire limit stops a mass retirement",
		steps: []step{
			{files: map[string]string{kFile: fn("k", "k"), fFile: fn("f", "f"), gFile: fn("g", "g"), "app/h": fn("h", "h")}, opts: opts(b(1))},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2), funchose.WithRetireLimit(2)),
				err: funchose.ErrRetireLimit, errText: "retire 3 routines, the limit is 2"},
			{files: map[string]string{kFile: fn("k", "k")}, opts: opts(b(2), funchose.WithRetireLimit(3)),
				want: map[string]funchose.Action{fKey: funchose.ActionRetire, gKey: funchose.ActionRetire, "app.h": funchose.ActionRetire}},
		},
	},
	{
		name: "a warning named in fail-on fails the run and applies nothing",
		steps: []step{
			{files: map[string]string{fFile: fn("f", "v1"), gFile: fn("g", "v1")}, opts: opts(b(1))},
			{files: map[string]string{fFile: fn("f", "v2"), gFile: fn("g", "v1")}, opts: opts(b(2))},
			{files: map[string]string{fFile: fn("f", "v1"), gFile: fn("g", "v9")}, opts: opts(b(1), funchose.WithFailOn(funchose.ActionStale)),
				err: funchose.ErrFailOn, errText: "stale app.f", want: map[string]funchose.Action{fKey: funchose.ActionStale},
				results: map[string]string{fKey: "v2", gKey: "v1"}},
		},
	},
}

func TestSafety(t *testing.T) {
	t.Parallel()
	runCases(t, safetyCases)
}

// runCases runs every case on a database of its own, in parallel.
func runCases(t *testing.T, cases []providerCase) {
	t.Helper()

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			track := tt.track
			if track == "" {
				track = "pl"
			}

			db, _ := newDB(t, map[string]string{"track_functions": track})

			schemas := tt.schemas
			if len(schemas) == 0 {
				schemas = []string{"app"}
			}

			for _, schema := range schemas {
				_, err := db.ExecContext(t.Context(), "CREATE SCHEMA "+schema)
				require.NoError(t, err)
			}

			for n, st := range tt.steps {
				runStepChecks(t, db, tt.table, n, st)
			}
		})
	}
}

func runStepChecks(t *testing.T, db *sql.DB, table string, n int, st step) {
	t.Helper()

	ctx := t.Context()

	if st.setup != "" {
		_, err := db.ExecContext(ctx, "BEGIN;"+st.setup+";COMMIT;")
		require.NoError(t, err, "step %d setup", n)
	}

	if st.waitCalls != "" {
		schema, name, _ := strings.Cut(st.waitCalls, ".")
		waitCalls(t, db, schema, name)
	}

	fsys := fstest.MapFS{}
	for name, content := range st.files {
		fsys[name+".sql"] = &fstest.MapFile{Data: []byte(content)}
	}

	opts := []funchose.Option{funchose.WithLogger(nil)}
	if table != "" {
		opts = append(opts, funchose.WithTable(table))
	}

	provider, err := funchose.NewProvider(db, fsys, append(opts, st.opts...)...)
	require.NoError(t, err, "step %d", n)

	if st.holdLock {
		holder, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)

		_, beginErr = holder.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "funchose:"+provider.Table())
		require.NoError(t, beginErr)

		defer func() { _ = holder.Rollback() }() // releases the lock
	}

	rep, err := runStep(t, provider, st)
	checkError(t, n, st, err)
	checkReport(t, n, st, rep)
	checkDatabase(t, db, provider.Table(), n, st)
}

func checkError(t *testing.T, n int, st step, err error) {
	t.Helper()

	switch {
	case st.err != nil:
		require.ErrorIs(t, err, st.err, "step %d", n)
	case st.errText == "":
		require.NoError(t, err, "step %d", n)
	}

	if st.errText != "" {
		require.ErrorContains(t, err, st.errText, "step %d", n)
	}
}

func checkReport(t *testing.T, n int, st step, rep funchose.Report) {
	t.Helper()

	got := map[string]funchose.Change{}
	for _, change := range rep.Changes {
		got[change.Routine()] = change
	}

	for name, want := range st.want {
		assert.Equal(t, want, got[name].Action, "step %d: action of %s (%s)", n, name, got[name].Note)
	}

	for name, want := range st.changed {
		assert.Equal(t, want, got[name].Changed, "step %d: changed of %s", n, name)
	}

	for name, want := range st.notes {
		assert.Contains(t, got[name].Note, want, "step %d: note of %s", n, name)
	}

	for name, wants := range st.diffs {
		for _, want := range wants {
			assert.Contains(t, got[name].Diff, want, "step %d: diff of %s", n, name)
		}
	}
}

func checkDatabase(t *testing.T, db *sql.DB, table string, n int, st step) {
	t.Helper()

	ctx := t.Context()

	for name, want := range st.results {
		var res string
		require.NoError(t, db.QueryRowContext(ctx, "SELECT "+name+"()").Scan(&res), "step %d: call %s", n, name)
		assert.Equal(t, want, res, "step %d: %s()", n, name)
	}

	if st.rolledBack > 0 {
		var marked int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE rolled_back_by IS NOT NULL").Scan(&marked))
		assert.Equal(t, st.rolledBack, marked, "step %d: records marked as rolled back", n)
	}

	for _, name := range st.absent {
		schema, routine, _ := strings.Cut(name, ".")

		var count int
		require.NoError(t, db.QueryRowContext(ctx,
			"SELECT count(*) FROM pg_proc WHERE proname = $1 AND pronamespace::regnamespace::text = $2", routine, schema).Scan(&count))
		assert.Zero(t, count, "step %d: %s must not exist", n, name)
	}

	if st.check != "" {
		var ok bool
		require.NoError(t, db.QueryRowContext(ctx, st.check).Scan(&ok), "step %d", n)
		assert.True(t, ok, "step %d: %s", n, st.check)
	}
}

func runStep(t *testing.T, provider *funchose.Provider, st step) (funchose.Report, error) {
	t.Helper()

	var (
		rep funchose.Report
		err error
	)

	switch {
	case st.parallel > 0:
		return runParallel(t, provider, st.parallel), nil
	case st.method == methodPlan:
		rep, err = provider.Plan(t.Context())
	case st.method == methodStatus:
		rep, err = provider.Status(t.Context())
	default:
		rep, err = provider.Up(t.Context())
	}

	if err != nil {
		return rep, fmt.Errorf("%s: %w", cmp.Or(st.method, "up"), err)
	}

	return rep, nil
}

// runParallel runs Up n times at once: exactly one run applies the routine.
func runParallel(t *testing.T, provider *funchose.Provider, n int) funchose.Report {
	t.Helper()

	var wg sync.WaitGroup

	reps := make([]funchose.Report, n)
	errs := make([]error, n)

	for i := range n {
		wg.Go(func() { reps[i], errs[i] = provider.Up(t.Context()) })
	}

	wg.Wait()

	applied := 0

	for i := range reps {
		require.NoError(t, errs[i])

		applied += len(reps[i].Applied())
	}

	require.Equal(t, 1, applied, "%d concurrent runs applied %d routines, want 1", n, applied)

	return reps[0]
}

func opts(o ...funchose.Option) []funchose.Option { return o }
