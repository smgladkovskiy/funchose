package funchose

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// facts is what the database says about a routine whose file is gone: whether
// it exists, whether it is still called, and what would stop a DROP.
type facts struct {
	state

	calls callStats
	deps  []string // objects that depend on the routine, as PostgreSQL describes them
}

// callStats is the call counter of a routine and how far it can be trusted.
type callStats struct {
	calls int64
	seen  bool   // the counter counts calls of this routine at all
	valid bool   // the counter sees every call: zero growth proves the routine unused
	why   string // why it does not, when !valid
}

// keysIn matches the routines of a list of schemas ($1) and names ($2).
const keysIn = `(n.nspname, p.proname) IN (SELECT * FROM unnest(string_to_array($1, '/'), string_to_array($2, '/')))`

const callsSQL = `
SELECT n.nspname, p.proname,
       bool_and(l.lanname NOT IN ('sql', 'c', 'internal')),
       coalesce(sum(s.calls), 0)
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
JOIN pg_language l ON l.oid = p.prolang
LEFT JOIN pg_stat_user_functions s ON s.funcid = p.oid
WHERE p.prokind IN ('f', 'p') AND ` + keysIn + `
GROUP BY n.nspname, p.proname`

// depsSQL lists the objects a DROP without CASCADE would fail on: triggers,
// views, column defaults, routines with SQL-standard bodies. Overloads of the
// same name are dropped together and do not count.
const depsSQL = `
SELECT n.nspname, p.proname, string_agg(DISTINCT pg_describe_object(d.classid, d.objid, d.objsubid), ', ')
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
JOIN pg_depend d ON d.refclassid = 'pg_proc'::regclass AND d.refobjid = p.oid AND d.deptype = 'n'
WHERE p.prokind IN ('f', 'p') AND ` + keysIn + `
  AND NOT EXISTS (SELECT 1 FROM pg_proc q
                  WHERE d.classid = 'pg_proc'::regclass AND q.oid = d.objid
                    AND q.pronamespace = p.pronamespace AND q.proname = p.proname)
GROUP BY n.nspname, p.proname`

// facts reads call counters and dependent objects of the routines whose files
// are gone, two queries for all of them.
func (rn *runner) facts(ctx context.Context, keys []routineKey, db map[routineKey]state) (map[routineKey]facts, error) {
	out := make(map[routineKey]facts, len(keys))

	var schemas, names []string

	for _, key := range keys {
		out[key] = facts{state: db[key]}
		if db[key].fp != "" {
			schemas, names = append(schemas, key.schema), append(names, key.name)
		}
	}

	if len(names) == 0 {
		return out, nil
	}

	args := []any{strings.Join(schemas, "/"), strings.Join(names, "/")}
	if err := rn.readCalls(ctx, args, out); err != nil {
		return nil, err
	}

	if err := rn.readDeps(ctx, args, out); err != nil {
		return nil, err
	}

	return out, nil
}

// readCalls fills in the call counters and how far they can be trusted.
func (rn *runner) readCalls(ctx context.Context, args []any, out map[routineKey]facts) error {
	var track string
	if err := rn.tx.QueryRowContext(ctx, "SELECT current_setting('track_functions')").Scan(&track); err != nil {
		return fmt.Errorf("funchose: %w", err)
	}

	err := rn.each(ctx, callsSQL, args, func(rows *sql.Rows) error {
		var (
			key        routineKey
			procedural bool
			calls      int64
		)

		if err := rows.Scan(&key.schema, &key.name, &procedural, &calls); err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		known := out[key]
		known.calls = countable(track, procedural, rn.p.opts.replicas)
		known.calls.calls = calls
		out[key] = known

		return nil
	})
	if err != nil {
		return fmt.Errorf("funchose: count calls: %w", err)
	}

	return nil
}

// readDeps fills in the objects that would stop a DROP.
func (rn *runner) readDeps(ctx context.Context, args []any, out map[routineKey]facts) error {
	err := rn.each(ctx, depsSQL, args, func(rows *sql.Rows) error {
		var (
			key  routineKey
			list string
		)

		if err := rows.Scan(&key.schema, &key.name, &list); err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		known := out[key]
		known.deps = strings.Split(list, ", ")
		out[key] = known

		return nil
	})
	if err != nil {
		return fmt.Errorf("funchose: read dependencies: %w", err)
	}

	return nil
}

// countable says how far the call counter of a routine can be trusted.
// track_functions = pl counts procedural languages only, all counts SQL
// routines too, but not when they are inlined into the calling query.
func countable(track string, procedural, replicas bool) callStats {
	switch {
	case track == "none":
		return callStats{why: "track_functions is off"}
	case !procedural && track != "all":
		return callStats{why: "track_functions = " + track + " does not count LANGUAGE sql routines"}
	case !procedural:
		return callStats{seen: true, why: "LANGUAGE sql routines can be inlined into queries and are then not counted"}
	case replicas:
		return callStats{seen: true, why: "calls on read replicas are not counted on the primary"}
	default:
		return callStats{seen: true, valid: true}
	}
}
