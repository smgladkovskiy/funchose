package funchose

import (
	"database/sql"
	"fmt"
	"strings"
)

// fileCase is what decideFile knows about a routine that has a file.
type fileCase struct {
	file    *Routine
	recs    []record
	cur     *record
	cfg     options
	tracked bool // the history holds a version of the file
	recent  bool // this build may move the routine forward
	retired bool // the file was removed and the routine waits to be dropped
	same    bool // the file is the version the history holds
	drifted bool // the database differs from what funchose recorded
	missing bool // the routine does not exist in the database
}

// decideFile decides what to do with a routine that has a file. It only looks
// at its arguments: the whole decision table is tested without a database.
func decideFile(file *Routine, recs []record, st state, cfg options) step {
	fc := newFileCase(file, recs, st, cfg)
	stp := step{recs: recs, fp: st.fp, file: file}
	// The database holds a definition funchose did not apply: changed by hand,
	// or an existing routine seen for the first time. A run may not replace it
	// unless overwriting is allowed.
	stp.foreign = st.fp != "" && (fc.cur == nil || st.fp != fc.cur.toFP)

	change := &stp.change
	change.Schema, change.Name = file.Schema, file.Name

	if fc.tracked {
		change.From, change.FromBuild = fc.cur.to.String, fc.cur.build
		change.To, change.ToBuild = change.From, change.FromBuild
	}

	switch {
	case fc.retired:
		fc.decideRetired(change)
	case fc.same:
		fc.decideSame(change)
	default:
		fc.decideChanged(change)
	}

	if change.Action.Applies() {
		stp.op = opApply

		if fc.drifted && !fc.same {
			addNote(change, "the database differs from the version funchose applied")
		}
	}

	return stp
}

func newFileCase(file *Routine, recs []record, st state, cfg options) fileCase {
	cur := last(recs)
	tracked := cur != nil && cur.to.Valid

	return fileCase{
		file: file, recs: recs, cur: cur, cfg: cfg, tracked: tracked,
		// A build moves a routine forward only if it is newer than every build
		// the history has seen for it, or is the build that applied the current
		// version. Anything else is an old release or a rolled-back one.
		recent:  cur == nil || cfg.build == cur.build || cfg.build > highWater(recs),
		retired: tracked && cur.action == string(ActionRetire),
		same:    tracked && file.Checksum == cur.to.String,
		drifted: tracked && st.fp != cur.toFP,
		missing: st.fp == "",
	}
}

// decideRetired: the file of a retired routine is back.
func (fc fileCase) decideRetired(change *Change) {
	if !fc.recent && !fc.cfg.allowDowngrade {
		change.Action = ActionStale
		addNote(change, fmt.Sprintf("build %d removed the file, this is build %d: not applied", fc.cur.build, fc.cfg.build))

		return
	}

	change.Action = ActionUpdate
	if fc.cfg.build < fc.cur.build {
		change.Action = ActionRollback
	}

	addNote(change, "the file is back: the routine is no longer retired")
}

// decideSame: the file is the version the history holds.
func (fc fileCase) decideSame(change *Change) {
	switch {
	case !fc.drifted:
		change.Action = ActionUnchanged
	case !fc.cfg.overwriteDrift:
		change.Action = ActionDrift
		addNote(change, "changed in the database after funchose applied it; the file did not change, so it was left as is")
	default:
		change.Action = ActionUpdate
		addNote(change, "replaces a change made in the database by hand")
	}
}

// decideChanged: a file the history does not hold.
func (fc fileCase) decideChanged(change *Change) {
	cfg := fc.cfg

	switch {
	case !fc.recent && !cfg.allowDowngrade:
		change.Action = ActionStale
		if cfg.build < fc.cur.build {
			addNote(change, fmt.Sprintf("build %d applied a newer version, this is build %d: not rolled back",
				fc.cur.build, cfg.build))
		} else {
			addNote(change, fmt.Sprintf("build %d was rolled back by build %d and is not applied again",
				cfg.build, fc.cur.build))
		}
	case !fc.tracked && fc.missing:
		change.Action = ActionCreate
	case !fc.tracked:
		change.Action = ActionAdopt
	case cfg.build < fc.cur.build:
		change.Action = ActionRollback
		if k := kept(fc.recs, cfg.build); k == nil || k.to.String != fc.file.Checksum {
			addNote(change, "the history has no record of this version for this build or older ones")
		}
	default:
		change.Action = ActionUpdate
		if !fc.recent {
			addNote(change, fmt.Sprintf("applies build %d again after it was rolled back", cfg.build))
		}
	}
}

// decideOrphan decides what to do with a routine the history tracks but no file
// describes: retire it, drop it after retirement, or, for an older build, roll
// it back to what it was before a newer build touched it.
func decideOrphan(key routineKey, recs []record, fx facts, cfg options) step {
	cur := last(recs)
	stp := step{recs: recs, fp: fx.fp}
	stp.change = Change{
		Schema: key.schema, Name: key.name,
		From: cur.to.String, FromBuild: cur.build, To: cur.to.String, ToBuild: cur.build,
	}

	switch {
	case cur.build <= cfg.build:
		retire(&stp, fx, cfg)
	case !cfg.allowDowngrade:
		stp.change.Action = ActionStale
		addNote(&stp.change, fmt.Sprintf("build %d brought it under funchose, this build (%d) has no file for it: "+
			"not rolled back", cur.build, cfg.build))
	default:
		rollBack(&stp, fx, cfg)
	}

	return stp
}

// retire handles a tracked routine whose file is gone. The deploy that first
// runs without the file only records the retirement, the definition and the call
// counter: pods of the previous release may still call the routine while that
// rollout goes on. The first run of a newer build drops it if the counter proves
// nothing called it in between. A counter that cannot prove it keeps the
// routine unless dropping is allowed. Reruns of the same build, pod restarts
// included, never drop anything.
func retire(stp *step, fx facts, cfg options) {
	cur, change, counter := stp.cur(), &stp.change, fx.calls
	// Show the build that applied the version, not the one that retired it.
	change.FromBuild = appliedBuild(stp.recs)
	change.ToBuild = change.FromBuild

	if fx.fp == "" {
		change.Action, change.To, change.ToBuild, stp.op = ActionDrop, "", cfg.build, opForget
		addNote(change, "the file was removed and the routine is already gone from the database")

		return
	}

	snapshot := sql.NullInt64{Int64: counter.calls, Valid: counter.seen}

	switch {
	case cur.action != string(ActionRetire):
		change.Action, stp.op, stp.snapshot = ActionRetire, opRetire, snapshot
		if counter.valid {
			addNote(change, "the file was removed: dropped by the next deploy if nothing calls it until then")
		} else {
			addNote(change, fmt.Sprintf("the file was removed; its calls are not counted (%s), "+
				"so it is dropped only with allow-drop", counter.why))
		}
	case cfg.build <= cur.build:
		change.Action = ActionRetired
		addNote(change, fmt.Sprintf("retired by build %d: dropped by a newer build, not by this one", cur.build))
	case counter.seen && (!cur.calls.Valid || counter.calls < cur.calls.Int64):
		change.Action, stp.op, stp.snapshot = ActionRetired, opRetire, snapshot
		addNote(change, "call statistics were reset or just enabled: checked again on the next deploy")
	case counter.seen && counter.calls > cur.calls.Int64:
		change.Action, stp.op, stp.snapshot = ActionKept, opRetire, snapshot
		addNote(change, fmt.Sprintf("called %d times since the file was removed: "+
			"not dropped, checked again on the next deploy", counter.calls-cur.calls.Int64))
	case !counter.valid && !cfg.allowDrop:
		change.Action = ActionBlocked
		addNote(change, fmt.Sprintf("its calls are not counted (%s): not dropped; "+
			"drop it with allow-drop or in a schema migration", counter.why))
	case len(fx.deps) > 0:
		change.Action = ActionBlocked
		addNote(change, dependents(fx.deps))
	default:
		change.Action, change.To, change.ToBuild, stp.op = ActionDrop, "", cfg.build, opDrop
		if counter.valid {
			addNote(change, fmt.Sprintf("retired by build %d, not called since", cur.build))
		} else {
			addNote(change, fmt.Sprintf("retired by build %d; its calls are not counted (%s): "+
				"dropped because dropping is allowed", cur.build, counter.why))
		}
	}
}

// rollBack puts back the definition a routine had before the first build newer
// than this one touched it, or drops the routine if it did not exist then.
func rollBack(stp *step, fx facts, cfg options) {
	change := &stp.change

	first := firstNewer(stp.recs, cfg.build)
	if first == nil {
		change.Action = ActionStale
		addNote(change, "no record of a newer build is left to roll back")

		return
	}

	keep := kept(stp.recs, cfg.build)

	change.To, change.ToBuild = "", cfg.build
	if keep != nil {
		change.To = keep.to.String
	}

	switch {
	case first.hasBefore:
		change.Action, stp.op, stp.restore = ActionRollback, opRestore, first
		if keep == nil {
			addNote(change, "put back the definition it had before funchose adopted it; it is no longer tracked")
		}
	case !cfg.allowDrop:
		change.Action, change.To, change.ToBuild = ActionBlocked, change.From, change.FromBuild
		addNote(change, fmt.Sprintf("build %d created it and this build has no file for it: "+
			"rolling back means dropping it, which is not allowed; it stays", first.build))
	case len(fx.deps) > 0:
		change.Action, change.To, change.ToBuild = ActionBlocked, change.From, change.FromBuild
		addNote(change, dependents(fx.deps))
	default:
		change.Action, stp.op = ActionDrop, opDrop
	}
}

func dependents(deps []string) string {
	return "used by " + strings.Join(deps, ", ") + ": not dropped; change or drop those in a schema migration first"
}
