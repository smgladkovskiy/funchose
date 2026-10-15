package funchose

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// mode is what a run is for.
type mode int

const (
	modeStatus mode = iota // compare and report, read-only, no lock
	modeCheck              // the lock-free first pass of Up: does it have work?
	modePlan               // do everything under the lock, roll back
	modeUp                 // do everything under the lock, commit
)

func (m mode) readOnly() bool { return m == modeStatus || m == modeCheck }

// runner is one run: one transaction over one history table.
type runner struct {
	p    *Provider
	tx   *sql.Tx
	mode mode
	hist map[routineKey][]record
}

// run reads files, history and database, decides what to do with every routine
// and, unless the mode is read-only, does it. The flag reports whether anything
// had to be written.
func (p *Provider) run(ctx context.Context, runMode mode) (Report, bool, error) {
	rep := p.report()

	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: runMode.readOnly()})
	if err != nil {
		return rep, false, fmt.Errorf("funchose: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit

	rn := &runner{p: p, tx: tx, mode: runMode}

	steps, err := rn.prepare(ctx)
	if err != nil {
		return rep, false, err
	}

	work := slices.ContainsFunc(steps, step.writes)

	if runMode.readOnly() {
		for i := range steps {
			if steps[i].op == opApply {
				steps[i].change.To, steps[i].change.ToBuild = steps[i].file.Checksum, p.opts.build
			}
		}

		rep.Changes = changes(steps)

		return rep, work, nil
	}

	err = rn.execute(ctx, steps)

	rep.Changes = changes(steps)
	if err != nil {
		return rep, work, err
	}

	if err = p.failOn(rep); err != nil {
		return rep, work, err
	}

	if runMode == modeUp {
		if err = tx.Commit(); err != nil {
			return rep, work, fmt.Errorf("funchose: commit: %w", err)
		}

		rep.log(p.opts.logger)
	}

	return rep, work, nil
}

// prepare pins the search_path, takes the lock when the run writes, reads
// history and database and decides what to do with every routine.
func (rn *runner) prepare(ctx context.Context) ([]step, error) {
	if _, err := rn.tx.ExecContext(ctx, pinned); err != nil {
		return nil, fmt.Errorf("funchose: %w", err)
	}

	if !rn.mode.readOnly() {
		if err := rn.lock(ctx); err != nil {
			return nil, err
		}

		if err := rn.ensureSchemas(ctx); err != nil {
			return nil, err
		}

		if err := rn.createHistory(ctx); err != nil {
			return nil, err
		}
	}

	hist, err := rn.history(ctx)
	if err != nil {
		return nil, err
	}

	rn.hist = hist

	db, err := rn.states(ctx)
	if err != nil {
		return nil, err
	}

	return rn.steps(ctx, db)
}

func changes(steps []step) []Change {
	out := make([]Change, len(steps))
	for i, stp := range steps {
		out[i] = stp.change
	}

	return out
}

// steps decides what to do with every routine: the files in file order, then
// the routines the history tracks without a file, then the untracked routines of
// the managed schemas.
func (rn *runner) steps(ctx context.Context, db map[routineKey]state) ([]step, error) {
	files := map[routineKey]bool{}

	steps := make([]step, 0, len(rn.p.routines))
	for i := range rn.p.routines {
		routine := &rn.p.routines[i]
		files[routine.key()] = true
		steps = append(steps, decideFile(routine, rn.hist[routine.key()], db[routine.key()], rn.p.opts))
	}

	orphans, err := rn.orphanSteps(ctx, db, files)
	if err != nil {
		return nil, err
	}

	steps = append(steps, orphans...)

	return append(steps, rn.untrackedSteps(db, files)...), nil
}

// orphanSteps decides what to do with the routines the history tracks but no
// file describes.
func (rn *runner) orphanSteps(ctx context.Context, db map[routineKey]state, files map[routineKey]bool) ([]step, error) {
	var keys []routineKey

	for key, recs := range rn.hist {
		if !files[key] && last(recs).to.Valid {
			keys = append(keys, key)
		}
	}

	slices.SortFunc(keys, func(a, b routineKey) int {
		return cmp.Or(strings.Compare(a.schema, b.schema), strings.Compare(a.name, b.name))
	})

	known, err := rn.facts(ctx, keys, db)
	if err != nil {
		return nil, err
	}

	covered := map[string]bool{}
	for _, schema := range append(slices.Clone(rn.p.schemas), rn.p.opts.removedSchemas...) {
		covered[schema] = true
	}

	var (
		steps             []step
		unknown, retiring []string
	)

	for _, key := range keys {
		stp := decideOrphan(key, rn.hist[key], known[key], rn.p.opts)
		// A routine of a schema the files do not cover is ours only if the schema
		// was removed on purpose: otherwise the directory is lost, or the table is
		// shared with another service. Leaving it alone (stale) is always safe.
		if !covered[key.schema] && stp.change.Action != ActionStale {
			if !slices.Contains(unknown, key.schema) {
				unknown = append(unknown, key.schema)
			}

			continue
		}

		if stp.change.Action == ActionRetire {
			retiring = append(retiring, key.String())
		}

		steps = append(steps, stp)
	}

	return steps, rn.checkOrphans(unknown, retiring)
}

// listed is how many routines an error names before it says "and N more".
const listed = 10

// checkOrphans stops a run that would retire the routines of schemas the files
// do not cover, or too many routines at once.
func (rn *runner) checkOrphans(unknown, retiring []string) error {
	if len(unknown) > 0 {
		return fmt.Errorf("%w: the history in %s tracks routines in %s, and there is no directory for it. "+
			"If the directory was removed on purpose, or this build predates it, name the schema in "+
			"-removed-schemas (WithRemovedSchemas) on every run until its routines are dropped; "+
			"if another service keeps its history in the same table, give each service a table of its own "+
			"(-table, WithTable)", ErrSchemaWithoutDir, rn.p.table, strings.Join(unknown, ", "))
	}

	if limit := rn.p.opts.retireLimit; limit > 0 && len(retiring) > limit {
		return fmt.Errorf("%w: this run would retire %d routines, the limit is %d (%s). "+
			"A lost directory or a bad merge looks the same as removed files: if they were removed on purpose, "+
			"raise the limit for this run (-retire-limit %d, WithRetireLimit)",
			ErrRetireLimit, len(retiring), limit, abbreviate(retiring, listed), len(retiring))
	}

	return nil
}

// untrackedSteps lists the routines of the managed schemas that have neither a
// file nor a tracked version.
func (rn *runner) untrackedSteps(db map[routineKey]state, files map[routineKey]bool) []step {
	managed := map[string]bool{}
	for _, schema := range rn.p.schemas {
		managed[schema] = true
	}

	var keys []routineKey

	for key := range db {
		if cur := last(rn.hist[key]); managed[key.schema] && !files[key] && (cur == nil || !cur.to.Valid) {
			keys = append(keys, key)
		}
	}

	slices.SortFunc(keys, func(a, b routineKey) int { return strings.Compare(a.name, b.name) })

	steps := make([]step, len(keys))
	for i, key := range keys {
		steps[i] = step{change: Change{Schema: key.schema, Name: key.name, Action: ActionUntracked}}
	}

	return steps
}

// abbreviate lists the first n items and how many more there are.
func abbreviate(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

// execute does the work of every step: the files first, then the routines
// without a file.
func (rn *runner) execute(ctx context.Context, steps []step) error {
	var files []*step

	for i := range steps {
		if steps[i].op == opApply {
			files = append(files, &steps[i])
		}
	}

	if err := rn.applyAll(ctx, files); err != nil {
		return err
	}

	for i := range steps {
		var err error

		switch stp := &steps[i]; stp.op {
		case opRetire:
			err = rn.markRetired(ctx, stp)
		case opForget:
			err = rn.markGone(ctx, stp)
		case opDrop:
			err = rn.drop(ctx, stp)
		case opRestore:
			err = rn.restore(ctx, stp)
		default: // opNone, or opApply, which is done above
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// each runs a query and calls scan for every row, closing the rows before it
// returns: a transaction runs one query at a time.
func (rn *runner) each(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := rn.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		if err = scan(rows); err != nil {
			return err
		}
	}

	return rows.Err()
}
