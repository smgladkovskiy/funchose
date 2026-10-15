package functional_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/smgladkovskiy/funchose"
	"github.com/smgladkovskiy/funchose/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exporting the routines of a schema and applying the files adopts every
// routine without changing it: the way to bring an existing database under
// funchose.
func TestExportThenAdopt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  string
		objects string // routines that already live in the database
		files   int
	}{
		{
			name:   "routines of many kinds",
			schema: "legacy",
			objects: `
CREATE DOMAIN legacy.code AS text;
CREATE FUNCTION legacy.plain(a int) RETURNS int LANGUAGE sql IMMUTABLE AS 'SELECT a + 1';
CREATE FUNCTION legacy.over(a int) RETURNS int LANGUAGE sql AS 'SELECT a';
CREATE FUNCTION legacy.over(a text) RETURNS text LANGUAGE sql AS 'SELECT a';
CREATE FUNCTION legacy.secure(c legacy.code DEFAULT 'x') RETURNS SETOF legacy.code
    LANGUAGE plpgsql SECURITY DEFINER SET search_path TO legacy, public AS $$
BEGIN
    RETURN NEXT c; -- a comment survives
END $$;
CREATE FUNCTION legacy.atomic(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT x * 2; END;
CREATE FUNCTION legacy.begin_param(begin int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT begin; END;
CREATE PROCEDURE legacy.proc(INOUT n int) LANGUAGE plpgsql AS $$ BEGIN n := n + 1; END $$;
COMMENT ON FUNCTION legacy.plain(int) IS 'kept out of the file';`,
			files: 6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			db, dsn := newDB(t, nil)
			_, err := db.ExecContext(ctx, "CREATE SCHEMA "+tt.schema+";"+tt.objects)
			require.NoError(t, err)
			dir := t.TempDir()

			var out bytes.Buffer

			code := cli.Run(ctx, []string{"export", "-dsn", dsn, "-dir", dir, "-schema", tt.schema}, &out, &out)
			require.Equal(t, 0, code, out.String())
			assert.Contains(t, out.String(), fmt.Sprintf("%d files written", tt.files))

			p, err := funchose.NewProvider(db, os.DirFS(dir), funchose.WithBuild(1), funchose.WithLogger(nil))
			require.NoError(t, err)
			rep, err := p.Up(ctx)
			require.NoError(t, err)
			require.Len(t, rep.Changes, tt.files)

			for _, ch := range rep.Changes {
				assert.Equal(t, funchose.ActionAdopt, ch.Action, ch.Routine())
				assert.False(t, ch.Changed, "%s changed on adoption", ch.Routine())
			}

			rep, err = p.Up(ctx)
			require.NoError(t, err)

			for _, ch := range rep.Changes {
				assert.Equal(t, funchose.ActionUnchanged, ch.Action, ch.Routine())
			}
		})
	}
}

const (
	flagBuild = "-build"
	cmdStatus = "status"
)

// cmdStep is one funchose command against the database of a test.
type cmdStep struct {
	file string // content of routines/app/f.sql
	args []string
	code int
	out  []string
}

// The command line end to end: what a deploy job sees.
func TestCommands(t *testing.T) {
	const (
		v1 = "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';\n"
		v2 = "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 2';\n"
	)

	tests := []struct {
		name  string
		steps []cmdStep
	}{
		{
			name: "deploy, a stale old job, a deliberate rollback",
			steps: []cmdStep{
				{file: v1, args: []string{"plan", flagBuild, "1"}, code: 0, out: []string{"funchose plan: build 1 on app.funchose_history, 1 to apply", "create"}},
				{file: v1, args: []string{"up", flagBuild, "1"}, code: 0, out: []string{"1 applied"}},
				{file: v2, args: []string{"up", flagBuild, "2"}, code: 0, out: []string{"update"}},
				// The job of an older pipeline runs again: stale fails it by default.
				{file: v1, args: []string{"up", flagBuild, "1"}, code: 1, out: []string{"stale", "funchose: a routine needs attention: stale app.f"}},
				{file: v1, args: []string{"up", flagBuild, "1", "-fail-on", "none"}, code: 0, out: []string{"1 warning"}},
				{file: v1, args: []string{"up", flagBuild, "1", "-allow-downgrade"}, code: 0, out: []string{"rollback"}},
				{file: v1, args: []string{cmdStatus}, code: 0, out: []string{"funchose status: build 0 on app.funchose_history"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			db, dsn := newDB(t, nil)
			_, err := db.ExecContext(ctx, "CREATE SCHEMA app")
			require.NoError(t, err)

			for _, v := range []string{"FUNCHOSE_BUILD", "FUNCHOSE_FAIL_ON", "FUNCHOSE_ALLOW_DOWNGRADE", "FUNCHOSE_TABLE", "FUNCHOSE_DIR"} {
				t.Setenv(v, "")
			}

			t.Setenv("FUNCHOSE_DSN", dsn)
			t.Setenv("NO_COLOR", "1")
			t.Chdir(t.TempDir())
			require.NoError(t, os.MkdirAll(filepath.Join("routines", "app"), 0o755))

			for n, st := range tt.steps {
				require.NoError(t, os.WriteFile(filepath.Join("routines", "app", "f.sql"), []byte(st.file), 0o644))

				var buf bytes.Buffer

				code := cli.Run(ctx, st.args, &buf, &buf)
				assert.Equal(t, st.code, code, "step %d: %s", n, buf.String())

				for _, want := range st.out {
					assert.Contains(t, buf.String(), want, "step %d", n)
				}
			}
		})
	}
}
