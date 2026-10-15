package funchose

import (
	"context"
	"fmt"
)

// lockNotAvailable is the SQLSTATE of a lock wait that ran into lock_timeout.
const lockNotAvailable = "55P03"

// lock serializes funchose runs on one history table. Two sessions replacing
// the same function at once fail with "tuple concurrently updated", so the
// second run waits, then finds everything applied. The wait is bounded by
// lock_timeout, and the session's own lock_timeout is restored for the DDL.
func (rn *runner) lock(ctx context.Context) error {
	cfg := rn.p.opts

	var prev string
	if err := rn.tx.QueryRowContext(ctx, "SELECT current_setting('lock_timeout')").Scan(&prev); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	wait := fmt.Sprintf("%dms", cfg.lockTimeout.Milliseconds())
	if _, err := rn.tx.ExecContext(ctx, "SELECT set_config('lock_timeout', $1, true)", wait); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	if _, err := rn.tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "funchose:"+rn.p.table); err != nil {
		if sqlState(err) == lockNotAvailable {
			return fmt.Errorf("%w: another run on %s held the lock for more than %s",
				ErrLockTimeout, rn.p.table, cfg.lockTimeout)
		}

		return fmt.Errorf("funchose: lock %s: %w", rn.p.table, err)
	}

	if _, err := rn.tx.ExecContext(ctx, "SELECT set_config('lock_timeout', $1, true)", prev); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	return nil
}
