package funchose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// state is what the database holds for one routine name.
type state struct {
	fp    string // fingerprint of every overload, "" when the routine does not exist
	count int    // overloads
}

// pinned is the search_path every definition and fingerprint is read under:
// pg_get_functiondef qualifies type names that are not on search_path, so the
// same routine must always be read under the same path.
const pinned = "SELECT set_config('search_path', 'pg_catalog', true)"

// routinesIn selects the functions and procedures of a filter, without the ones
// that belong to extensions.
const routinesIn = `
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE p.prokind IN ('f', 'p')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d
                  WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
  AND %s`

const fingerprintSQL = `
SELECT n.nspname, p.proname,
       encode(sha256(convert_to(
           string_agg(pg_get_functiondef(p.oid), E'\n' ORDER BY p.oid::regprocedure::text),
           'UTF8')), 'hex'),
       count(*)` + routinesIn + `
GROUP BY n.nspname, p.proname`

// states fingerprints every routine of the managed schemas and of the schemas
// the history mentions, in one query.
func (rn *runner) states(ctx context.Context) (map[routineKey]state, error) {
	schemas := append([]string(nil), rn.p.schemas...)
	for key := range rn.hist {
		schemas = append(schemas, key.schema)
	}

	query := fmt.Sprintf(fingerprintSQL, "n.nspname = ANY (string_to_array($1, '/'))")
	db := map[routineKey]state{}

	err := rn.each(ctx, query, []any{strings.Join(schemas, "/")}, func(rows *sql.Rows) error {
		var (
			key routineKey
			st  state
		)

		if err := rows.Scan(&key.schema, &key.name, &st.fp, &st.count); err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		db[key] = st

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("funchose: read routines: %w", err)
	}

	return db, nil
}

// state fingerprints one routine.
func (rn *runner) state(ctx context.Context, key routineKey) (state, error) {
	var (
		st           state
		schema, name string
	)

	query := fmt.Sprintf(fingerprintSQL, "n.nspname = $1 AND p.proname = $2")

	err := rn.tx.QueryRowContext(ctx, query, key.schema, key.name).Scan(&schema, &name, &st.fp, &st.count)
	if errors.Is(err, sql.ErrNoRows) {
		return state{}, nil
	}

	if err != nil {
		return st, fmt.Errorf("funchose: read %s: %w", key, err)
	}

	return st, nil
}

// definition returns the executable definitions of every overload, or an
// invalid NullString if the routine does not exist.
func (rn *runner) definition(ctx context.Context, key routineKey) (sql.NullString, error) {
	var def sql.NullString

	err := rn.tx.QueryRowContext(ctx, `
		SELECT string_agg(pg_get_functiondef(p.oid), E';\n' ORDER BY p.oid::regprocedure::text) || ';'
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname = $2 AND p.prokind IN ('f', 'p')`, key.schema, key.name).Scan(&def)
	if err != nil {
		return def, fmt.Errorf("funchose: read %s: %w", key, err)
	}

	return def, nil
}

// diffContext is how many unchanged lines a diff shows around a change.
const diffContext = 3

// diff is the unified diff between a definition and what the database has now.
func (rn *runner) diff(ctx context.Context, key routineKey, before sql.NullString) (string, error) {
	after, err := rn.definition(ctx, key)
	if err != nil {
		return "", err
	}

	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(before.String),
		B:        difflib.SplitLines(after.String),
		FromFile: key.String() + " (database)",
		ToFile:   key.String() + " (after)",
		Context:  diffContext,
	})
	if err != nil {
		return "", fmt.Errorf("funchose: diff %s: %w", key, err)
	}

	return out, nil
}

// ensureSchemas fails on a missing schema: with search_path "<schema>, public"
// the routine would silently land in public.
func (rn *runner) ensureSchemas(ctx context.Context) error {
	var missing []string

	query := `SELECT s FROM unnest(string_to_array($1, '/')) s
		WHERE NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = s)`

	err := rn.each(ctx, query, []any{strings.Join(rn.p.schemas, "/")}, func(rows *sql.Rows) error {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		missing = append(missing, schema)

		return nil
	})
	if err != nil {
		return fmt.Errorf("funchose: check schemas: %w", err)
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %s: create it in a schema migration first", ErrMissingSchema, strings.Join(missing, ", "))
	}

	return nil
}
