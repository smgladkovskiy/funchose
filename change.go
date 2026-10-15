package funchose

// Change describes one routine in a Report.
type Change struct {
	Schema string
	Name   string
	Action Action
	// From is the file version the database was at, To the one it is at after
	// the run. Empty means "no tracked version": the routine did not exist or was
	// not managed by funchose.
	From, To string
	// FromBuild and ToBuild are the builds behind From and To.
	FromBuild, ToBuild int64
	// Changed is true when the database definition actually changed. Adopting a
	// file that matches the database, or a change in comments only, leaves it false.
	Changed bool
	// Diff is the unified diff of the definition, filled in by Plan.
	Diff string
	// Note explains warnings and unusual cases.
	Note string
}

// Routine returns schema.name.
func (c Change) Routine() string { return c.Schema + "." + c.Name }

// addNote appends one more explanation to the note of a change.
func addNote(change *Change, note string) {
	if change.Note != "" {
		change.Note += "; "
	}

	change.Note += note
}
