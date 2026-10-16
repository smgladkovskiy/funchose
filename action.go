package funchose

// Action says what funchose did, or would do, with a routine.
type Action string

const (
	// ActionCreate: the routine did not exist and the file created it.
	ActionCreate Action = "create"
	// ActionAdopt: the routine existed but was not tracked; the file is now its
	// first version. Change.Changed tells whether the file changed the definition.
	ActionAdopt Action = "adopt"
	// ActionUpdate: the file changed since the version the history holds.
	ActionUpdate Action = "update"
	// ActionRollback: an older build put back its own version of the routine and
	// the records of newer builds were marked as rolled back.
	ActionRollback Action = "rollback"
	// ActionDrop: the routine was dropped, because its file was removed and
	// nothing called it while it was retired, or because a rollback went back to
	// a build that never had it.
	ActionDrop Action = "drop"

	// ActionUnchanged: file, history and database agree.
	ActionUnchanged Action = "unchanged"
	// ActionDrift: the database definition differs from what funchose applied,
	// and the file did not change. Nothing is applied.
	ActionDrift Action = "drift"
	// ActionStale: this build is older than the one that applied the current
	// version, or it was rolled back. Nothing is applied unless rolling back is
	// allowed.
	ActionStale Action = "stale"
	// ActionBlocked: the routine should be dropped, but dropping it is not safe
	// or not allowed: a rollback would drop a routine a newer build created, its
	// calls are not counted, other objects depend on it, or the bodies of other
	// routines call it. The routine stays.
	ActionBlocked Action = "blocked"
	// ActionRetire: the file is gone. The routine stays for now; funchose
	// remembers its definition and call counter and drops it on a later deploy.
	ActionRetire Action = "retire"
	// ActionRetired: retired by this build, or its call counter was reset;
	// waiting for a newer build.
	ActionRetired Action = "retired"
	// ActionKept: a newer build came, but the routine was called since it was
	// retired. Not dropped; checked again on the next deploy.
	ActionKept Action = "kept"
	// ActionUntracked: the routine exists in a schema funchose manages, but there
	// is neither a file nor a history record for it.
	ActionUntracked Action = "untracked"
)

// Applies reports whether the action changes a routine in the database.
func (a Action) Applies() bool {
	switch a {
	case ActionCreate, ActionAdopt, ActionUpdate, ActionRollback, ActionDrop:
		return true
	default:
		return false
	}
}

// Warns reports whether the action needs a human to look at it.
func (a Action) Warns() bool {
	switch a {
	case ActionDrift, ActionStale, ActionBlocked, ActionKept:
		return true
	default:
		return false
	}
}
