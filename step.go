package funchose

import "database/sql"

// step is what one run does with one routine: the change it reports and the
// work behind it.
type step struct {
	change Change
	op     op
	recs   []record // history of the routine
	fp     string   // database fingerprint before the run

	file     *Routine      // opApply: the file to run
	foreign  bool          // opApply: the database holds a definition funchose did not apply
	snapshot sql.NullInt64 // opRetire: the call counter to record
	restore  *record       // opRestore: the record whose definition_before goes back
}

// op is the work behind a step.
type op int

const (
	opNone    op = iota
	opApply      // run the file and record it
	opRetire     // record a retirement with a fresh call counter
	opForget     // record that a retired routine is gone: it was dropped outside funchose
	opDrop       // drop every overload and record it
	opRestore    // put back the definition from before a newer build, stop tracking
)

// writes reports whether the step changes the database or the history.
func (s step) writes() bool { return s.op != opNone }

func (s step) key() routineKey { return routineKey{s.change.Schema, s.change.Name} }

func (s step) cur() *record { return last(s.recs) }
