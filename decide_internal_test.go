package funchose

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Fingerprints of the database in the cases below.
const (
	fp1  = "fp1"
	fp2  = "fp2"
	hand = "hand"
)

// rec builds a history record: action, build, file version after it (to) and
// database fingerprint after it (toFP); "" is NULL or none.
func rec(id int64, action Action, build int64, to, toFP string, mods ...func(*record)) record {
	r := record{id: id, action: string(action), build: build, to: nullString(to), toFP: toFP}
	for _, m := range mods {
		m(&r)
	}

	return r
}

func rolledBack(r *record) { r.rolledBack = true }
func withBefore(r *record) { r.hasBefore = true }
func calls(n int64) func(*record) {
	return func(r *record) { r.calls = sql.NullInt64{Int64: n, Valid: true} }
}

func opts(mods ...Option) options {
	o := defaultOptions()
	for _, m := range mods {
		m(&o)
	}

	return o
}

func TestDecideFile(t *testing.T) {
	t.Parallel()

	file := &Routine{Schema: "app", Name: "f", Checksum: "v2"}

	tests := []struct {
		name    string
		recs    []record
		db      state
		opts    options
		action  Action
		op      op
		foreign bool
		note    string
	}{
		{name: "nothing anywhere: create", opts: opts(WithBuild(1)),
			action: ActionCreate, op: opApply},
		{name: "routine without history: adopt a foreign definition", db: state{fp: "db"}, opts: opts(WithBuild(1)),
			action: ActionAdopt, op: opApply, foreign: true},
		{name: "file, history and database agree", recs: []record{rec(1, ActionCreate, 1, "v2", fp2)}, db: state{fp: fp2}, opts: opts(WithBuild(1)),
			action: ActionUnchanged, op: opNone},
		{name: "changed by hand, file unchanged: drift", recs: []record{rec(1, ActionCreate, 1, "v2", fp2)}, db: state{fp: hand}, opts: opts(WithBuild(2)),
			action: ActionDrift, op: opNone, foreign: true, note: "left as is"},
		{name: "changed by hand, overwriting allowed", recs: []record{rec(1, ActionCreate, 1, "v2", fp2)}, db: state{fp: hand}, opts: opts(WithBuild(2), WithOverwriteDrift(true)),
			action: ActionUpdate, op: opApply, foreign: true, note: "replaces a change made in the database by hand"},
		{name: "dropped by hand is drift, but nothing foreign to overwrite", recs: []record{rec(1, ActionCreate, 1, "v2", fp2)}, opts: opts(WithBuild(2)),
			action: ActionDrift, op: opNone},
		{name: "changed file, newer build: update", recs: []record{rec(1, ActionCreate, 1, "v1", fp1)}, db: state{fp: fp1}, opts: opts(WithBuild(2)),
			action: ActionUpdate, op: opApply},
		{name: "changed file over a hand change: update that must not overwrite", recs: []record{rec(1, ActionCreate, 1, "v1", fp1)}, db: state{fp: hand}, opts: opts(WithBuild(2)),
			action: ActionUpdate, op: opApply, foreign: true, note: "the database differs"},
		{name: "older build: stale", recs: []record{rec(1, ActionCreate, 5, "v1", fp1)}, db: state{fp: fp1}, opts: opts(WithBuild(4)),
			action: ActionStale, op: opNone, note: "build 5 applied a newer version, this is build 4"},
		{name: "local run against a CI database: stale", recs: []record{rec(1, ActionCreate, 5, "v1", fp1)}, db: state{fp: fp1}, opts: opts(WithLocal()),
			action: ActionStale, op: opNone},
		{name: "older build allowed to roll back", recs: []record{rec(1, ActionCreate, 5, "v1", fp1)}, db: state{fp: fp1}, opts: opts(WithBuild(4), WithAllowDowngrade(true)),
			action: ActionRollback, op: opApply, note: "no record of this version"},
		{name: "a rolled-back build is stale", recs: []record{rec(1, ActionCreate, 1, "v1", fp1), rec(2, ActionUpdate, 2, "v2", fp2, rolledBack), rec(3, ActionRollback, 1, "v1", fp1)},
			db: state{fp: fp1}, opts: opts(WithBuild(2)), action: ActionStale, op: opNone, note: "was rolled back by build 1"},
		{name: "a build after the rollback moves on", recs: []record{rec(1, ActionCreate, 1, "v1", fp1), rec(2, ActionUpdate, 2, "v3", "fp3", rolledBack), rec(3, ActionRollback, 1, "v1", fp1)},
			db: state{fp: fp1}, opts: opts(WithBuild(3)), action: ActionUpdate, op: opApply},
		{name: "retired, an older build still has the file: stale", recs: []record{rec(1, ActionCreate, 1, "v2", fp2), rec(2, ActionRetire, 2, "v2", fp2)},
			db: state{fp: fp2}, opts: opts(WithBuild(1)), action: ActionStale, op: opNone, note: "removed the file"},
		{name: "retired, the file is back", recs: []record{rec(1, ActionCreate, 1, "v2", fp2), rec(2, ActionRetire, 2, "v2", fp2)},
			db: state{fp: fp2}, opts: opts(WithBuild(3)), action: ActionUpdate, op: opApply, note: "no longer retired"},
		{name: "rolled back past adoption: adopting again is not foreign",
			recs: []record{rec(1, ActionAdopt, 2, "v1", fp1, withBefore, rolledBack), rec(2, ActionRollback, 1, "", "old")},
			db:   state{fp: "old"}, opts: opts(WithBuild(3)), action: ActionAdopt, op: opApply},
		{name: "dropped, then created by hand: adopting it is foreign",
			recs: []record{rec(1, ActionCreate, 1, "v1", fp1), rec(2, ActionDrop, 3, "", "")},
			db:   state{fp: hand}, opts: opts(WithBuild(4)), action: ActionAdopt, op: opApply, foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := decideFile(file, tt.recs, tt.db, tt.opts)
			assert.Equal(t, tt.action, s.change.Action, s.change.Note)
			assert.Equal(t, tt.op, s.op)
			assert.Equal(t, tt.foreign, s.foreign, "foreign")
			assert.Contains(t, s.change.Note, tt.note)
		})
	}
}

func TestDecideOrphan(t *testing.T) {
	t.Parallel()

	k := routineKey{"app", "f"}
	counted := func(n int64) callStats { return callStats{calls: n, seen: true, valid: true} }
	retired := []record{rec(1, ActionCreate, 1, "v1", fp1), rec(2, ActionRetire, 2, "v1", fp1, calls(5))}
	created := []record{rec(1, ActionCreate, 5, "v1", fp1)}
	adopted := []record{rec(1, ActionAdopt, 5, "v1", fp1, withBefore)}

	tests := []struct {
		name     string
		recs     []record
		fx       facts
		opts     options
		action   Action
		op       op
		snapshot sql.NullInt64
		note     string
	}{
		{name: "the file is gone: retire with the counter", recs: created[:1:1], fx: facts{state: state{fp: fp1}, calls: counted(3)}, opts: opts(WithBuild(5)),
			action: ActionRetire, op: opRetire, snapshot: sql.NullInt64{Int64: 3, Valid: true}, note: "dropped by the next deploy"},
		{name: "the file is gone, calls not counted: retire, say it will not be dropped", recs: created, fx: facts{state: state{fp: fp1}, calls: countable("none", true, false)}, opts: opts(WithBuild(6)),
			action: ActionRetire, op: opRetire, note: "track_functions is off"},
		{name: "the same build restarts: retired, nothing written", recs: retired, fx: facts{state: state{fp: fp1}, calls: counted(5)}, opts: opts(WithBuild(2)),
			action: ActionRetired, op: opNone, note: "dropped by a newer build"},
		{name: "a newer build, not called: drop", recs: retired, fx: facts{state: state{fp: fp1}, calls: counted(5)}, opts: opts(WithBuild(3)),
			action: ActionDrop, op: opDrop, note: "not called since"},
		{name: "a newer build, called: kept with a fresh counter", recs: retired, fx: facts{state: state{fp: fp1}, calls: counted(9)}, opts: opts(WithBuild(3)),
			action: ActionKept, op: opRetire, snapshot: sql.NullInt64{Int64: 9, Valid: true}, note: "called 4 times"},
		{name: "the counter went down: checked again", recs: retired, fx: facts{state: state{fp: fp1}, calls: counted(1)}, opts: opts(WithBuild(3)),
			action: ActionRetired, op: opRetire, snapshot: sql.NullInt64{Int64: 1, Valid: true}, note: "reset"},
		{name: "a called SQL routine is kept although its counter is not proof", recs: retired, fx: facts{state: state{fp: fp1}, calls: callStats{calls: 105, seen: true}}, opts: opts(WithBuild(3)),
			action: ActionKept, op: opRetire, snapshot: sql.NullInt64{Int64: 105, Valid: true}},
		{name: "calls not counted: blocked", recs: retired, fx: facts{state: state{fp: fp1}, calls: countable("pl", false, false)}, opts: opts(WithBuild(3)),
			action: ActionBlocked, op: opNone, note: "not counted"},
		{name: "calls not counted, dropping allowed: drop", recs: retired, fx: facts{state: state{fp: fp1}, calls: countable("none", true, false)}, opts: opts(WithBuild(3), WithAllowDrop(true)),
			action: ActionDrop, op: opDrop, note: "dropped because dropping is allowed"},
		{name: "replicas: a silent counter proves nothing", recs: retired, fx: facts{state: state{fp: fp1}, calls: withCalls(countable("pl", true, true), 5)}, opts: opts(WithBuild(3)),
			action: ActionBlocked, op: opNone, note: "read replicas"},
		{name: "a trigger depends on it: blocked", recs: retired, fx: facts{state: state{fp: fp1}, calls: counted(5), deps: []string{"trigger tr on table app.t"}}, opts: opts(WithBuild(3)),
			action: ActionBlocked, op: opNone, note: "used by trigger tr on table app.t"},
		{name: "already gone from the database: record it", recs: retired, fx: facts{}, opts: opts(WithBuild(3)),
			action: ActionDrop, op: opForget},
		{name: "a newer build created it: stale", recs: created, fx: facts{state: state{fp: fp1}}, opts: opts(WithBuild(4)),
			action: ActionStale, op: opNone, note: "brought it under funchose"},
		{name: "rolling back a created routine: blocked", recs: created, fx: facts{state: state{fp: fp1}}, opts: opts(WithBuild(4), WithAllowDowngrade(true)),
			action: ActionBlocked, op: opNone, note: "build 5 created it"},
		{name: "rolling back a created routine, dropping allowed: drop", recs: created, fx: facts{state: state{fp: fp1}}, opts: opts(WithBuild(4), WithAllowDowngrade(true), WithAllowDrop(true)),
			action: ActionDrop, op: opDrop},
		{name: "rolling back a created routine that a view uses: blocked", recs: created, fx: facts{state: state{fp: fp1}, deps: []string{"rule _RETURN on view app.v"}},
			opts: opts(WithBuild(4), WithAllowDowngrade(true), WithAllowDrop(true)), action: ActionBlocked, op: opNone, note: "view app.v"},
		{name: "rolling back past adoption: restore", recs: adopted, fx: facts{state: state{fp: fp1}}, opts: opts(WithBuild(4), WithAllowDowngrade(true)),
			action: ActionRollback, op: opRestore, note: "no longer tracked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := decideOrphan(k, tt.recs, tt.fx, tt.opts)
			assert.Equal(t, tt.action, s.change.Action, s.change.Note)
			assert.Equal(t, tt.op, s.op)
			assert.Equal(t, tt.snapshot, s.snapshot)
			assert.Contains(t, s.change.Note, tt.note)

			if tt.op == opRestore {
				assert.Equal(t, int64(1), s.restore.id)
			}
		})
	}
}

func withCalls(c callStats, n int64) callStats {
	c.calls = n

	return c
}

func TestCountable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		track                string
		procedural, replicas bool
		seen, valid          bool
	}{
		{name: "plpgsql, track pl", track: "pl", procedural: true, seen: true, valid: true},
		{name: "plpgsql, track all", track: "all", procedural: true, seen: true, valid: true},
		{name: "plpgsql, track none", track: "none", procedural: true},
		{name: "sql, track pl", track: "pl"},
		{name: "sql, track all", track: "all", seen: true},
		{name: "plpgsql with replicas", track: "pl", procedural: true, replicas: true, seen: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := countable(tt.track, tt.procedural, tt.replicas)
			assert.Equal(t, tt.seen, c.seen, "seen")
			assert.Equal(t, tt.valid, c.valid, "valid")
			assert.Equal(t, tt.valid, c.why == "", "why: %q", c.why)
		})
	}
}
