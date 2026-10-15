package cli

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// exportSQL prints every routine name of a schema with all its overloads, as
// pg_get_functiondef prints them, without the routines of extensions.
const exportSQL = `
SELECT p.proname, string_agg(pg_get_functiondef(p.oid), E';\n\n' ORDER BY p.oid::regprocedure::text) || E';\n'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.prokind IN ('f', 'p')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d
                  WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
GROUP BY p.proname ORDER BY p.proname`

// Permissions of exported files and directories: git keeps only the
// executable bit, so they matter only on the machine that exports.
const (
	dirMode  = 0o750
	fileMode = 0o600
)

// exportFlags are the flags of export.
type exportFlags struct {
	conn

	schemas string
	force   bool
}

// export writes every routine of the given schemas into <dir>/<schema>/<name>.sql,
// one file per name with all its overloads, exactly as pg_get_functiondef
// prints them. Existing files are kept unless -force is set.
func export(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, code := parseExport(args, stderr)
	if opts == nil {
		return code
	}

	db, err := opts.open()
	if err != nil {
		fmt.Fprintln(stderr, "funchose:", err)

		return ExitNoStart
	}
	defer db.Close()

	total, err := exportAll(ctx, db, opts, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "funchose:", err)

		return ExitFailed
	}

	fmt.Fprintf(stdout, "funchose export: %s written, %d existing kept\n", plural(total.written, "file"), total.kept)

	return ExitOK
}

func parseExport(args []string, stderr io.Writer) (*exportFlags, int) {
	flags := flag.NewFlagSet("funchose export", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		env  envReader
		opts exportFlags
	)

	opts.register(flags, &env)
	flags.StringVar(&opts.schemas, "schema", "", "comma-separated schemas to export (required)")
	flags.BoolVar(&opts.force, "force", false, "overwrite existing files")

	if err := env.err(); err != nil {
		fmt.Fprintln(stderr, "funchose:", err)

		return nil, ExitNoStart
	}

	if err := flags.Parse(args); err != nil {
		return nil, ExitNoStart
	}

	if opts.schemas == "" {
		fmt.Fprintln(stderr, "funchose export: -schema is required")

		return nil, ExitNoStart
	}

	return &opts, ExitOK
}

type exported struct{ written, kept int }

// exportAll exports every schema in one read-only transaction, under the same
// pinned search_path funchose reads definitions under.
func exportAll(ctx context.Context, db *sql.DB, opts *exportFlags, stderr io.Writer) (exported, error) {
	var total exported

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return total, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only

	if _, err = tx.ExecContext(ctx, "SELECT set_config('search_path', 'pg_catalog', true)"); err != nil {
		return total, fmt.Errorf("pin search_path: %w", err)
	}

	for _, schema := range list(opts.schemas) {
		done, err := exportSchema(ctx, tx, opts, schema, stderr)
		if err != nil {
			return total, err
		}

		total.written += done.written
		total.kept += done.kept
	}

	return total, nil
}

func exportSchema(
	ctx context.Context, tx *sql.Tx, opts *exportFlags, schema string, stderr io.Writer,
) (exported, error) {
	var done exported

	rows, err := tx.QueryContext(ctx, exportSQL, schema)
	if err != nil {
		return done, fmt.Errorf("read schema %s: %w", schema, err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, def string
		if err = rows.Scan(&name, &def); err != nil {
			return done, fmt.Errorf("read schema %s: %w", schema, err)
		}

		if strings.ContainsAny(name, `/\`) {
			fmt.Fprintf(stderr, "funchose: skipped %s.%s: the name cannot be a file name\n", schema, name)

			continue
		}

		path := filepath.Join(opts.dir, schema, name+".sql")
		if _, err = os.Stat(path); err == nil && !opts.force {
			done.kept++

			continue
		}

		if err = os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
			return done, fmt.Errorf("export %s.%s: %w", schema, name, err)
		}

		if err = os.WriteFile(path, []byte(def), fileMode); err != nil {
			return done, fmt.Errorf("export %s.%s: %w", schema, name, err)
		}

		done.written++
	}

	return done, rows.Err()
}
