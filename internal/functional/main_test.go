package functional_test

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the driver of every test database
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// server is the PostgreSQL server every test of the package shares; each test
// case gets a database of its own on it.
var server struct {
	dsn   string
	admin *sql.DB
	seq   atomic.Int64
}

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	image := cmp.Or(os.Getenv("FUNCHOSE_PG_IMAGE"), "postgres:18-alpine")

	ctr, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "functional tests need Docker to start %s: %v\n", image, err)

		return 1
	}

	defer func() {
		if stopErr := testcontainers.TerminateContainer(ctr); stopErr != nil {
			fmt.Fprintf(os.Stderr, "stop %s: %v\n", image, stopErr)
		}
	}()

	if server.dsn, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
		fmt.Fprintf(os.Stderr, "connection string of %s: %v\n", image, err)

		return 1
	}

	if server.admin, err = sql.Open("pgx", server.dsn); err != nil {
		fmt.Fprintf(os.Stderr, "connect to %s: %v\n", image, err)

		return 1
	}
	defer server.admin.Close()

	return m.Run()
}

// newDB creates a database for one test with the given settings (ALTER
// DATABASE ... SET), opens it and drops it when the test ends. It returns the
// database and its connection string.
func newDB(t *testing.T, settings map[string]string) (*sql.DB, string) {
	t.Helper()

	name := fmt.Sprintf("funchose_t%d", server.seq.Add(1))
	if _, err := server.admin.ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	t.Cleanup(func() {
		// t.Context is done by now.
		if _, err := server.admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})

	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range settings {
			if !yield(k) {
				return
			}
		}
	})
	for _, k := range keys {
		set := fmt.Sprintf("ALTER DATABASE %s SET %s = '%s'", name, k, strings.ReplaceAll(settings[k], "'", "''"))
		if _, err := server.admin.ExecContext(t.Context(), set); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	u, err := url.Parse(server.dsn)
	if err != nil {
		t.Fatalf("connection string %q: %v", server.dsn, err)
	}

	u.Path = "/" + name

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db, u.String()
}

// waitCalls waits until pg_stat_user_functions shows a call of the routine:
// backends report their counters with a delay.
func waitCalls(t *testing.T, db *sql.DB, schema, name string) {
	t.Helper()

	const tries = 100
	for range tries {
		var calls int64

		err := db.QueryRowContext(t.Context(), `SELECT coalesce(sum(calls), 0) FROM pg_stat_user_functions
			WHERE schemaname = $1 AND funcname = $2`, schema, name).Scan(&calls)
		if err != nil {
			t.Fatal(err)
		}

		if calls > 0 {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("no calls of %s.%s reached pg_stat_user_functions", schema, name)
}
