package funchose

import (
	"context"
	"database/sql"
	"fmt"
)

// applyAll runs the files of the steps. A LANGUAGE sql function is checked when
// it is created, so one that calls a routine created later in the same run
// fails with "does not exist"; such files are retried after the others, as long
// as every pass applies something.
func (rn *runner) applyAll(ctx context.Context, steps []*step) error {
	for len(steps) > 0 {
		var (
			retry    []*step
			firstErr error
		)

		for _, stp := range steps {
			err := rn.applyOne(ctx, stp)
			if err == nil {
				continue
			}

			if !undefined(err) {
				return err
			}

			if firstErr == nil {
				firstErr = err
			}

			retry = append(retry, stp)
		}

		if len(retry) == len(steps) {
			return firstErr
		}

		steps = retry
	}

	return nil
}

// applyOne applies one file under a savepoint, so that a file that has to wait
// for another one leaves nothing behind.
func (rn *runner) applyOne(ctx context.Context, stp *step) error {
	if _, err := rn.tx.ExecContext(ctx, "SAVEPOINT funchose_routine"); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	err := rn.apply(ctx, stp)
	if err == nil {
		if _, rerr := rn.tx.ExecContext(ctx, "RELEASE SAVEPOINT funchose_routine"); rerr != nil {
			return fmt.Errorf("funchose: %w", rerr)
		}

		return nil
	}

	if undefined(err) {
		if _, rerr := rn.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT funchose_routine"); rerr != nil {
			return fmt.Errorf("funchose: %w", rerr)
		}
	}

	return err
}

// apply runs one file statement by statement, checks the result and records it.
func (rn *runner) apply(ctx context.Context, stp *step) error {
	file, change := stp.file, &stp.change
	key := file.key()

	before, err := rn.definition(ctx, key)
	if err != nil {
		return err
	}

	if err = rn.run(ctx, file); err != nil {
		return err
	}

	after, err := rn.state(ctx, key)
	if err != nil {
		return err
	}

	if err = rn.checkApplied(stp, after); err != nil {
		return err
	}

	change.To, change.ToBuild = file.Checksum, rn.p.opts.build

	var from sql.NullString
	if cur := stp.cur(); cur != nil {
		from = cur.to
	}

	err = rn.write(ctx, stp, entry{action: change.Action, from: from, to: nullString(file.Checksum),
		fromFP: stp.fp, toFP: after.fp, definitionBefore: before})
	if err != nil {
		return err
	}

	if rn.mode == modePlan {
		change.Diff, err = rn.diff(ctx, key, before)
	}

	return err
}

// run executes the statements of a file in its schema.
func (rn *runner) run(ctx context.Context, file *Routine) error {
	setPath := "SELECT set_config('search_path', quote_ident($1) || ', public', true)"
	if _, err := rn.tx.ExecContext(ctx, setPath, file.Schema); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	for _, stmt := range file.stmts {
		if _, err := rn.tx.ExecContext(ctx, stmt.text); err != nil {
			return fmt.Errorf("funchose: %s:%d: %w", file.Path, stmt.line, err)
		}
	}

	if _, err := rn.tx.ExecContext(ctx, pinned); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	return nil
}

// checkApplied checks what a file did: no new overload, and no definition
// funchose did not apply replaced without permission.
func (rn *runner) checkApplied(stp *step, after state) error {
	file, change := stp.file, &stp.change

	if after.count != file.Creates {
		return fmt.Errorf("%w: %s: the file defines %d routine(s) named %s, the database now has %d. "+
			"Only the body swaps in place: a new signature is a new routine (a new file), "+
			"and the old one is dropped in a schema migration",
			ErrOverload, file.Path, file.Creates, file.Name, after.count)
	}

	change.Changed = after.fp != stp.fp
	if stp.foreign && change.Changed && !rn.p.opts.overwriteDrift {
		return rn.overwrites(stp)
	}

	if change.Action == ActionAdopt && !change.Changed {
		addNote(change, "the file matches the database")
	}

	return nil
}

// overwrites reports a run that would replace a definition funchose did not
// apply: a note for Plan, ErrDrift for Up.
func (rn *runner) overwrites(stp *step) error {
	change, file, cur := &stp.change, stp.file, stp.cur()
	if rn.mode == modePlan {
		addNote(change, "would replace a definition funchose did not apply: up fails unless overwriting drift is allowed")

		return nil
	}

	if cur != nil && cur.to.Valid {
		return fmt.Errorf("%w: %s: %s was changed in the database after build %d applied it, "+
			"and this version would overwrite that change. Compare the database with the file "+
			"(funchose plan shows the diff), then apply with overwrite-drift allowed",
			ErrDrift, file.Path, change.Routine(), cur.build)
	}

	return fmt.Errorf("%w: %s: %s already exists with a definition funchose never applied, and this file changes it. "+
		"Compare them with funchose plan; to take the file, allow overwrite-drift; "+
		"to keep the routine as it is, export it into the file (funchose export -force)",
		ErrDrift, file.Path, change.Routine())
}
