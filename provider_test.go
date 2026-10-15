package funchose_test

import (
	"context"
	"fmt"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smgladkovskiy/funchose"
)

const routine = "CREATE OR REPLACE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT 1';"

func files(paths ...string) fstest.MapFS {
	fsys := fstest.MapFS{}

	for _, p := range paths {
		name := strings.TrimSuffix(path.Base(p), ".sql")
		fsys[p] = &fstest.MapFile{Data: []byte(fmt.Sprintf(routine, name))}
	}

	return fsys
}

func TestNewProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		fsys  fstest.MapFS
		opts  []funchose.Option
		table string
		err   error
		text  string
	}{
		{name: "one schema: the table lives in it", fsys: files("billing/f.sql", "billing/g.sql"), table: "billing.funchose_history"},
		{name: "a schema that needs quoting", fsys: files("Billing/f.sql"), table: `"Billing".funchose_history`},
		{name: "a table given with its schema", fsys: files("billing/f.sql"), opts: []funchose.Option{funchose.WithTable("ops.history")}, table: "ops.history"},
		{name: "several schemas need a table", fsys: files("billing/f.sql", "reporting/g.sql"), err: funchose.ErrTableRequired, text: "billing, reporting"},
		{name: "no files need a table", fsys: fstest.MapFS{}, err: funchose.ErrTableRequired},
		{name: "no files with a table", fsys: fstest.MapFS{}, opts: []funchose.Option{funchose.WithTable("ops.history")}, table: "ops.history"},
		{name: "a table without a schema depends on search_path", fsys: files("billing/f.sql"), opts: []funchose.Option{funchose.WithTable("history")}, text: "give it as schema.name"},
		{name: "local and a build exclude each other", fsys: files("billing/f.sql"), opts: []funchose.Option{funchose.WithLocal(), funchose.WithBuild(3)}, text: "exclude each other"},
		{name: "a negative build", fsys: files("billing/f.sql"), opts: []funchose.Option{funchose.WithBuild(-1)}, text: "positive"},
		{name: "fail-on takes warnings only", fsys: files("billing/f.sql"), opts: []funchose.Option{funchose.WithFailOn(funchose.ActionCreate)}, text: "fail-on takes"},
		{name: "invalid files", fsys: fstest.MapFS{"billing/f.sql": {Data: []byte("DROP TABLE t;")}}, err: funchose.ErrInvalidFiles},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := funchose.NewProvider(nil, tt.fsys, tt.opts...)
			if tt.err == nil && tt.text == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.table, p.Table())

				return
			}

			require.Error(t, err)

			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
			}

			assert.Contains(t, err.Error(), tt.text)
		})
	}
}

// Up and Plan refuse to run without a build: a missing CI variable must not
// quietly become build 0 and switch off the protection against old pods. The
// check comes before any database access, so a nil database is enough.
func TestBuildRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(*funchose.Provider) error
	}{
		{name: "up", call: func(p *funchose.Provider) error {
			_, err := p.Up(context.Background())

			return fmt.Errorf("up: %w", err)
		}},
		{name: "plan", call: func(p *funchose.Provider) error {
			_, err := p.Plan(context.Background())

			return fmt.Errorf("plan: %w", err)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := funchose.NewProvider(nil, files("billing/f.sql"))
			require.NoError(t, err)
			assert.ErrorIs(t, tt.call(p), funchose.ErrBuildRequired)
		})
	}
}
