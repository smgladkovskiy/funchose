package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smgladkovskiy/funchose/internal/cli"
)

const (
	envBuild  = "FUNCHOSE_BUILD"
	flagBuild = "-build"
)

// Mistakes in flags and environment stop funchose before it touches the
// database: the DSN below points nowhere on purpose.
func TestApplyArguments(t *testing.T) {
	tests := []struct {
		name  string
		files []string // routine files under routines/
		env   map[string]string
		args  []string
		code  int
		out   string
	}{
		{name: "up without a build", args: []string{"up"}, code: 2, out: "build number required"},
		{name: "plan without a build", args: []string{"plan"}, code: 2, out: "build number required"},
		{name: "a build that is not a number", env: map[string]string{envBuild: "abc"}, args: []string{"up"}, code: 2, out: `FUNCHOSE_BUILD="abc" is not a whole number`},
		{name: "a build with a space", env: map[string]string{envBuild: " 42"}, args: []string{"up"}, code: 2, out: "is not a whole number"},
		{name: "a flag that is not a boolean", env: map[string]string{"FUNCHOSE_ALLOW_DROP": "yes"}, args: []string{"up", flagBuild, "1"}, code: 2, out: `FUNCHOSE_ALLOW_DROP="yes" is not true or false`},
		{name: "a lock timeout that is not a duration", env: map[string]string{"FUNCHOSE_LOCK_TIMEOUT": "5"}, args: []string{"up", flagBuild, "1"}, code: 2, out: "is not a duration"},
		{name: "fail-on with an action that is not a warning", args: []string{"up", flagBuild, "1", "-fail-on", "create"}, code: 2, out: "-fail-on takes"},
		{name: "local with a build from the environment", env: map[string]string{envBuild: "5"}, args: []string{"up", "-local"}, code: 2, out: "exclude each other"},
		{name: "several schemas without a table", files: []string{"a/f.sql", "b/g.sql"}, args: []string{"up", flagBuild, "1"}, code: 2, out: "history table required"},
		{name: "a table without a schema", args: []string{"up", flagBuild, "1", "-table", "history"}, code: 2, out: "schema.name"},
		{name: "an invalid routine file", files: []string{"a/bad.sql"}, args: []string{"up", flagBuild, "1"}, code: 1, out: "invalid routine files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range []string{envBuild, "FUNCHOSE_ALLOW_DOWNGRADE", "FUNCHOSE_ALLOW_DROP", "FUNCHOSE_OVERWRITE_DRIFT",
				"FUNCHOSE_REPLICAS", "FUNCHOSE_RETIRE_LIMIT", "FUNCHOSE_REMOVED_SCHEMAS", "FUNCHOSE_FAIL_ON", "FUNCHOSE_LOCK_TIMEOUT", "FUNCHOSE_TABLE", "FUNCHOSE_DIR"} {
				t.Setenv(v, "")
			}

			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			t.Setenv("FUNCHOSE_DSN", "postgres://nobody@127.0.0.1:1/nothing?connect_timeout=1")
			t.Setenv("NO_COLOR", "1")
			t.Chdir(t.TempDir())

			files := tt.files
			if files == nil {
				files = []string{"app/f.sql"}
			}

			for _, f := range files {
				content := "CREATE OR REPLACE FUNCTION " + filepath.Base(f[:len(f)-4]) + "() RETURNS int LANGUAGE sql AS 'SELECT 1';\n"
				if filepath.Base(f) == "bad.sql" {
					content = "DROP TABLE t;\n"
				}

				require.NoError(t, os.MkdirAll(filepath.Join("routines", filepath.Dir(f)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join("routines", f), []byte(content), 0o644))
			}

			var buf bytes.Buffer

			code := cli.Run(t.Context(), tt.args, &buf, &buf)
			assert.Equal(t, tt.code, code, buf.String())
			assert.Contains(t, buf.String(), tt.out)
		})
	}
}
