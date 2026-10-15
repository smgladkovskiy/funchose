package funchose

import (
	"context"
	"fmt"
)

// markRetired records a retirement with the call counter of now: the next
// deploy compares the counter with it.
func (rn *runner) markRetired(ctx context.Context, stp *step) error {
	before, err := rn.definition(ctx, stp.key())
	if err != nil {
		return err
	}

	from := stp.cur().to

	return rn.write(ctx, stp, entry{action: ActionRetire, from: from, to: from,
		fromFP: stp.fp, toFP: stp.fp, definitionBefore: before, calls: stp.snapshot})
}

// markGone records that a retired routine is gone: it was dropped outside
// funchose.
func (rn *runner) markGone(ctx context.Context, stp *step) error {
	return rn.write(ctx, stp, entry{action: ActionDrop, from: stp.cur().to})
}

// drop drops every overload in one statement, so overloads that use each other
// go together, and records the definitions.
func (rn *runner) drop(ctx context.Context, stp *step) error {
	key := stp.key()

	before, err := rn.definition(ctx, key)
	if err != nil {
		return err
	}

	if before.Valid {
		var stmt string

		err = rn.tx.QueryRowContext(ctx, `SELECT 'DROP ROUTINE ' || string_agg(p.oid::regprocedure::text, ', ')
			FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1 AND p.proname = $2 AND p.prokind IN ('f', 'p')`, key.schema, key.name).Scan(&stmt)
		if err != nil {
			return fmt.Errorf("funchose: drop %s: %w", key, err)
		}

		if _, err = rn.tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("funchose: drop %s: %w", key, err)
		}
	}

	stp.change.Changed = before.Valid

	err = rn.write(ctx, stp, entry{action: ActionDrop, from: nullString(stp.change.From), to: nullString(stp.change.To),
		fromFP: stp.fp, definitionBefore: before})
	if err != nil {
		return err
	}

	if rn.mode == modePlan {
		stp.change.Diff, err = rn.diff(ctx, key, before)
	}

	return err
}

// restore puts back the definition a newer build replaced.
func (rn *runner) restore(ctx context.Context, stp *step) error {
	key := stp.key()

	def, err := rn.definitionBefore(ctx, stp.restore.id)
	if err != nil {
		return err
	}

	before, err := rn.definition(ctx, key)
	if err != nil {
		return err
	}

	if _, err = rn.tx.ExecContext(ctx, def); err != nil {
		return fmt.Errorf("funchose: roll back %s: %w", key, err)
	}

	after, err := rn.state(ctx, key)
	if err != nil {
		return err
	}

	stp.change.Changed = after.fp != stp.fp

	err = rn.write(ctx, stp, entry{
		action: ActionRollback, from: nullString(stp.change.From), to: nullString(stp.change.To),
		fromFP: stp.fp, toFP: after.fp, definitionBefore: before,
	})
	if err != nil {
		return err
	}

	if rn.mode == modePlan {
		stp.change.Diff, err = rn.diff(ctx, key, before)
	}

	return err
}
