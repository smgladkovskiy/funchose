package funchose

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// record is one row of the history table.
type record struct {
	id         int64
	action     string
	build      int64
	from, to   sql.NullString // file checksums
	fromFP     string
	toFP       string
	hasBefore  bool          // definition_before is set; read on demand by definitionBefore
	calls      sql.NullInt64 // retire records: call counter when the file was found gone
	rolledBack bool
}

const historyTableSQL = `CREATE TABLE IF NOT EXISTS %s (
    id                bigserial   PRIMARY KEY,
    schema_name       text        NOT NULL,
    routine_name      text        NOT NULL,
    action            text        NOT NULL,
    build             bigint      NOT NULL,
    from_checksum     text,
    to_checksum       text,
    from_fingerprint  text        NOT NULL,
    to_fingerprint    text        NOT NULL,
    definition_before text,
    calls             bigint,
    rolled_back_by    bigint,
    applied_at        timestamptz NOT NULL DEFAULT now(),
    applied_by        text        NOT NULL DEFAULT current_user
)`

var tableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)

// historyTable resolves the history table: the one given, which must be
// schema-qualified, or <schema>.funchose_history when the files cover one
// schema. An unqualified name would depend on the search_path of each
// connection, and a deploy job and a service would keep two histories.
func historyTable(name string, schemas []string) (string, error) {
	if name != "" {
		if !tableRe.MatchString(name) {
			return "", fmt.Errorf("%w: history table %q: give it as schema.name, for example %s.funchose_history",
				ErrInvalidOption, name, firstOr(schemas, "app"))
		}

		return name, nil
	}

	switch len(schemas) {
	case 1:
		return quoteIdent(schemas[0]) + ".funchose_history", nil
	case 0:
		return "", fmt.Errorf("%w: there are no routine files, so there is no schema to keep the history in: "+
			"set it (WithTable, -table)", ErrTableRequired)
	default:
		return "", fmt.Errorf("%w: the files cover schemas %s: set the table every run of this service uses, "+
			"for example %s.funchose_history (WithTable, -table)", ErrTableRequired, strings.Join(schemas, ", "), schemas[0])
	}
}

func firstOr(items []string, def string) string {
	if len(items) > 0 {
		return items[0]
	}

	return def
}

var plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func quoteIdent(id string) string {
	if plainIdent.MatchString(id) {
		return id
	}

	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

// The history table name is spliced into the SQL below: it is either checked
// against tableRe or built by quoteIdent, so it cannot carry anything else.
// createHistory creates the history table if it is not there yet.
func (rn *runner) createHistory(ctx context.Context) error {
	if _, err := rn.tx.ExecContext(ctx, fmt.Sprintf(historyTableSQL, rn.p.table)); err != nil {
		return fmt.Errorf("funchose: create history table %s: %w", rn.p.table, err)
	}

	return nil
}

// history reads every record, grouped by routine in id order. A missing table
// is an empty history. Definitions stay in the table: they are only needed to
// roll back past an adoption, and reading them on every run would make each
// start of a service slower as the history grows.
func (rn *runner) history(ctx context.Context) (map[routineKey][]record, error) {
	var exists bool
	if err := rn.tx.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", rn.p.table).Scan(&exists); err != nil {
		return nil, fmt.Errorf("funchose: %w", err)
	}

	hist := map[routineKey][]record{}
	if !exists {
		return hist, nil
	}

	query := `SELECT id, schema_name, routine_name, action, build, from_checksum, to_checksum,
		from_fingerprint, to_fingerprint, definition_before IS NOT NULL, calls, rolled_back_by IS NOT NULL
		FROM ` + rn.p.table + ` ORDER BY id`

	err := rn.each(ctx, query, nil, func(rows *sql.Rows) error {
		var (
			rec record
			key routineKey
		)

		err := rows.Scan(&rec.id, &key.schema, &key.name, &rec.action, &rec.build, &rec.from, &rec.to,
			&rec.fromFP, &rec.toFP, &rec.hasBefore, &rec.calls, &rec.rolledBack)
		if err != nil {
			return fmt.Errorf("scan: %w", err)
		}

		hist[key] = append(hist[key], rec)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("funchose: read history: %w", err)
	}

	return hist, nil
}

// definitionBefore reads the definition a record replaced.
func (rn *runner) definitionBefore(ctx context.Context, id int64) (string, error) {
	var def string

	query := "SELECT definition_before FROM " + rn.p.table + " WHERE id = $1"
	if err := rn.tx.QueryRowContext(ctx, query, id).Scan(&def); err != nil {
		return "", fmt.Errorf("funchose: read history record %d: %w", id, err)
	}

	return def, nil
}

// entry is a history record to write.
type entry struct {
	action           Action
	from, to         sql.NullString
	fromFP, toFP     string
	definitionBefore sql.NullString
	calls            sql.NullInt64
}

// write adds a history record for the routine of a step. When an older build
// rolls back, it also marks the records of newer builds as undone by this one,
// so a later rollback does not undo them again.
func (rn *runner) write(ctx context.Context, stp *step, rec entry) error {
	key, cfg := stp.key(), rn.p.opts

	var id int64

	insert := `INSERT INTO ` + rn.p.table + ` (schema_name, routine_name, action, build, from_checksum, to_checksum,
		from_fingerprint, to_fingerprint, definition_before, calls)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`

	err := rn.tx.QueryRowContext(ctx, insert, key.schema, key.name, string(rec.action), cfg.build,
		rec.from, rec.to, rec.fromFP, rec.toFP, rec.definitionBefore, rec.calls).Scan(&id)
	if err != nil {
		return fmt.Errorf("funchose: record %s: %w", key, err)
	}

	if !cfg.allowDowngrade || cfg.build >= highWater(stp.recs) {
		return nil
	}

	//nolint:gosec // the table name is checked by tableRe or built by quoteIdent
	update := `UPDATE ` + rn.p.table + ` SET rolled_back_by = $1
		WHERE schema_name = $2 AND routine_name = $3 AND build > $4 AND rolled_back_by IS NULL AND id <> $1`
	if _, err = rn.tx.ExecContext(ctx, update, id, key.schema, key.name, cfg.build); err != nil {
		return fmt.Errorf("funchose: mark rolled back %s: %w", key, err)
	}

	return nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// highWater is the newest build the history has seen for a routine, including
// builds that were rolled back.
func highWater(recs []record) int64 {
	var newest int64
	for _, rec := range recs {
		newest = max(newest, rec.build)
	}

	return newest
}

func last(recs []record) *record {
	if len(recs) == 0 {
		return nil
	}

	return &recs[len(recs)-1]
}

// kept returns the newest record of this build or an older one that has not
// been rolled back: the version this build is expected to run with.
func kept(recs []record, build int64) *record {
	for i := len(recs) - 1; i >= 0; i-- {
		if !recs[i].rolledBack && recs[i].build <= build {
			return &recs[i]
		}
	}

	return nil
}

// firstNewer returns the first record of a build newer than this one that has
// not been rolled back: where a rollback to this build has to go back to.
func firstNewer(recs []record, build int64) *record {
	for i := range recs {
		if !recs[i].rolledBack && recs[i].build > build {
			return &recs[i]
		}
	}

	return nil
}

// appliedBuild is the build that applied the current version: the newest record
// that is not a retirement and was not rolled back.
func appliedBuild(recs []record) int64 {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].action != string(ActionRetire) && !recs[i].rolledBack {
			return recs[i].build
		}
	}

	return 0
}
