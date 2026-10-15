package cli

import (
	"database/sql"
	"flag"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // the pgx driver for database/sql
)

// conn holds the flags every database command shares.
type conn struct {
	dsn, dir, table string
}

func (c *conn) register(flags *flag.FlagSet, env *envReader) {
	flags.StringVar(&c.dsn, "dsn", env.text("FUNCHOSE_DSN", env.text("DATABASE_URL", "")),
		"PostgreSQL connection string (env FUNCHOSE_DSN or DATABASE_URL)")
	flags.StringVar(&c.dir, "dir", env.text("FUNCHOSE_DIR", "routines"),
		"directory with <schema>/<name>.sql files (env FUNCHOSE_DIR)")
	flags.StringVar(&c.table, "table", env.text("FUNCHOSE_TABLE", ""),
		"history table as schema.name; default <schema>.funchose_history when the files cover one schema "+
			"(env FUNCHOSE_TABLE)")
}

func (c *conn) open() (*sql.DB, error) {
	if c.dsn == "" {
		return nil, errNoDatabase
	}

	db, err := sql.Open("pgx", c.dsn)
	if err != nil {
		return nil, fmt.Errorf("open the database: %w", err)
	}

	return db, nil
}
