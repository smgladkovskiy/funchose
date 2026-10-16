package funchose

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"
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

// callersSQL finds the routines whose bodies call the routines about to be
// dropped: schemas $1, names $2, patterns $3. PostgreSQL records no dependency
// on a routine that a body calls by name, so DROP succeeds, and the caller
// fails when it runs. Overloads of the same name do not count.
const callersSQL = `
SELECT c.schema, c.name, n.nspname, p.proname, p.oid::regprocedure::text
FROM unnest(string_to_array($1, '/'), string_to_array($2, '/'), string_to_array($3, '/')) AS c(schema, name, pattern)
JOIN pg_proc p ON p.prosrc ~* c.pattern
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND (n.nspname, p.proname) <> (c.schema, c.name)
ORDER BY 3, 5`

// caller is a routine whose body calls a routine about to be dropped.
type caller struct {
	key routineKey
	sig string
}

// holdCalled turns the drops of routines that other routines still call into
// blocked. Up and Plan run it after everything else the run does, so it reads
// the bodies the run leaves. A routine that only routines dropped in the same
// run call goes with them. The call counter cannot see such callers: one that
// did not run between two deploys left the counter as it was.
func (rn *runner) holdCalled(ctx context.Context, steps []step) error {
	var drops []*step

	dropping := map[routineKey]bool{}

	for i := range steps {
		if steps[i].op == opDrop {
			drops, dropping[steps[i].key()] = append(drops, &steps[i]), true
		}
	}

	if len(drops) == 0 {
		return nil
	}

	callers, err := rn.readCallers(ctx, drops)
	if err != nil {
		return err
	}

	// A routine that a routine left in the database calls stays, and then so do
	// the routines it calls: repeat until nothing more stays.
	for held := true; held; {
		held = false

		for key := range dropping {
			if slices.ContainsFunc(callers[key], func(cl caller) bool { return !dropping[cl.key] }) {
				delete(dropping, key)

				held = true
			}
		}
	}

	for _, stp := range drops {
		if dropping[stp.key()] {
			continue
		}

		var live []string

		for _, cl := range callers[stp.key()] {
			if !dropping[cl.key] {
				live = append(live, cl.sig)
			}
		}

		change := &stp.change
		change.Action, change.To, change.ToBuild, stp.op = ActionBlocked, change.From, change.FromBuild, opNone
		change.Note = "called by " + abbreviate(live, listed) + ": not dropped; remove those calls, " +
			"or drop it in a schema migration"
	}

	return nil
}

// readCallers reads, for each routine about to be dropped, the routines whose
// bodies call it by name.
func (rn *runner) readCallers(ctx context.Context, drops []*step) (map[routineKey][]caller, error) {
	schemas, names, patterns := make([]string, 0, len(drops)), make([]string, 0, len(drops)), make([]string, 0, len(drops))

	for _, stp := range drops {
		key := stp.key()
		schemas, names = append(schemas, key.schema), append(names, key.name)
		patterns = append(patterns, `\m`+regexp.QuoteMeta(key.name)+`"?\s*\(`)
	}

	callers := map[routineKey][]caller{}
	args := []any{strings.Join(schemas, "/"), strings.Join(names, "/"), strings.Join(patterns, "/")}

	err := rn.each(ctx, callersSQL, args, func(rows *sql.Rows) error {
		var (
			key routineKey
			cl  caller
		)

		if err := rows.Scan(&key.schema, &key.name, &cl.key.schema, &cl.key.name, &cl.sig); err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		callers[key] = append(callers[key], cl)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("funchose: find callers: %w", err)
	}

	return callers, nil
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
