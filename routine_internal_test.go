package funchose

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// appF is the path of most routine files below.
const appF = "app/f.sql"

const plpgsql = `CREATE OR REPLACE FUNCTION total(a int) RETURNS int LANGUAGE plpgsql AS $$
BEGIN
    RETURN a; -- a semicolon inside the body
END
$$;`

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		sql      string
		creates  int
		stmts    int
		problems []string // expected problem messages, substring match
	}{
		{name: "plpgsql body with semicolons", path: "app/total.sql", sql: plpgsql, creates: 1, stmts: 1},
		{
			name: "overloads and a comment",
			path: "app/total.sql",
			sql: plpgsql + `
CREATE OR REPLACE FUNCTION app.total(a int, b int) RETURNS int LANGUAGE sql AS 'SELECT a + b';
COMMENT ON FUNCTION total(int) IS 'sums; nothing else';`,
			creates: 2, stmts: 3,
		},
		{
			name: "nested dollar quotes and tags",
			path: "app/run.sql",
			sql: `create or replace procedure run() language plpgsql as $body$
begin
    execute $q$ insert into t values (';') $q$;
end $body$`,
			creates: 1, stmts: 1,
		},
		{name: "escape string with quote", path: appF, sql: `CREATE OR REPLACE FUNCTION f() RETURNS text LANGUAGE sql AS E'SELECT \'a;b\'';`, creates: 1, stmts: 1},
		{
			name: "begin atomic body",
			path: appF,
			sql: `CREATE OR REPLACE FUNCTION f(x int) RETURNS int LANGUAGE sql
BEGIN ATOMIC
    SELECT CASE WHEN x > 0 THEN 1 ELSE 0 END;
    SELECT x;
END;`,
			creates: 1, stmts: 1,
		},
		{
			name:    "begin atomic with a comment before atomic",
			path:    appF,
			sql:     "CREATE OR REPLACE FUNCTION f(x int) RETURNS int LANGUAGE sql\nBEGIN -- the body\n ATOMIC\n SELECT x; SELECT x;\nEND;",
			creates: 1, stmts: 1,
		},
		{
			name:    "parameter named begin",
			path:    appF,
			sql:     "CREATE OR REPLACE FUNCTION f(begin int) RETURNS int LANGUAGE sql AS 'SELECT begin';\nCREATE OR REPLACE FUNCTION f(begin text) RETURNS int LANGUAGE sql AS 'SELECT 2';\n",
			creates: 2, stmts: 2,
		},
		{
			name:    "result column named begin",
			path:    appF,
			sql:     "CREATE OR REPLACE FUNCTION f() RETURNS TABLE(begin int, \"end\" int) LANGUAGE sql AS 'SELECT 1, 2';\nCOMMENT ON FUNCTION f() IS 'x';\n",
			creates: 1, stmts: 2,
		},
		{
			name:     "a statement after a parameter named begin is still checked",
			path:     appF,
			sql:      "CREATE OR REPLACE FUNCTION f(begin int) RETURNS int LANGUAGE sql AS 'SELECT 1';\nDROP TABLE victim;\n",
			problems: []string{"2: DROP TABLE does not belong"},
		},
		{
			name:    "quoted name and parameter placeholder",
			path:    "App/Odd Name.sql",
			sql:     `CREATE OR REPLACE FUNCTION "App"."Odd Name"(int) RETURNS int LANGUAGE sql AS 'SELECT $1';`,
			creates: 1, stmts: 1,
		},
		{name: "plain create", path: appF, sql: `CREATE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';`, problems: []string{"use CREATE OR REPLACE"}},
		{name: "file named after another routine", path: appF, sql: `CREATE OR REPLACE FUNCTION g() RETURNS int LANGUAGE sql AS 'SELECT 1';`, problems: []string{"creates g, but the file is named f.sql"}},
		{name: "schema differs from directory", path: appF, sql: `CREATE OR REPLACE FUNCTION other.f() RETURNS int LANGUAGE sql AS 'SELECT 1';`, problems: []string{"in schema other, but the file lives in app/"}},
		{
			name: "drop, grant and dml",
			path: appF,
			sql: `DROP FUNCTION IF EXISTS f(int);
CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';
GRANT EXECUTE ON FUNCTION f() TO api;
INSERT INTO t VALUES (1);`,
			problems: []string{"1: DROP FUNCTION does not belong", "3: GRANT EXECUTE does not belong", "4: INSERT INTO does not belong"},
		},
		{name: "comment on another routine", path: appF, sql: `CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1'; COMMENT ON FUNCTION g() IS 'x';`, problems: []string{"comments on another routine"}},
		{name: "empty file", path: appF, sql: "-- nothing yet\n", problems: []string{"no CREATE OR REPLACE FUNCTION or PROCEDURE f"}},
		{name: "unterminated body", path: appF, sql: `CREATE OR REPLACE FUNCTION f() RETURNS int AS $$ SELECT 1`, problems: []string{"unterminated dollar-quoted body"}},
		{name: "file outside a schema directory", path: "f.sql", sql: plpgsql, problems: []string{"routine files live at <schema>/<name>.sql"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			routines, problems, err := Load(fstest.MapFS{tt.path: {Data: []byte(tt.sql)}})
			require.NoError(t, err)

			if len(tt.problems) == 0 {
				require.Empty(t, problems)
				require.Len(t, routines, 1)
				assert.Equal(t, tt.creates, routines[0].Creates)
				assert.Len(t, routines[0].stmts, tt.stmts)
				assert.Len(t, routines[0].Checksum, 64)

				return
			}

			assert.Empty(t, routines)
			require.Len(t, problems, len(tt.problems))

			for i, want := range tt.problems {
				assert.Contains(t, problems[i].String(), want)
			}
		})
	}
}

// What runs is the checked statements, word for word, without the comments
// between them.
func TestStatementsRunAsWritten(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "two statements and comments around them",
			sql:  "-- header\n" + plpgsql + "\n/* between */\nCOMMENT ON FUNCTION total(int) IS 'sums';\n-- trailer\n",
			want: []string{plpgsql, "COMMENT ON FUNCTION total(int) IS 'sums';"},
		},
		{
			name: "a statement without a final semicolon",
			sql:  "CREATE OR REPLACE FUNCTION total() RETURNS int LANGUAGE sql AS 'SELECT 1'",
			want: []string{"CREATE OR REPLACE FUNCTION total() RETURNS int LANGUAGE sql AS 'SELECT 1'"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, problems := ParseRoutine("app/total.sql", []byte(tt.sql))
			require.Empty(t, problems)

			var got []string
			for _, s := range r.stmts {
				got = append(got, s.text)
			}

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestChecksumIgnoresLineEndings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{name: "crlf and lf", a: "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';\n", b: "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';\r\n", same: true},
		{name: "body change", a: "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 1';", b: "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS 'SELECT 2';"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ra, _ := ParseRoutine("s/f.sql", []byte(tt.a))
			rb, _ := ParseRoutine("s/f.sql", []byte(tt.b))
			assert.Equal(t, tt.same, ra.Checksum == rb.Checksum)
		})
	}
}

func TestSignatures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{name: "function", sql: "CREATE OR REPLACE FUNCTION app.F( a  int, b TEXT DEFAULT 'x(' )\n RETURNS SETOF numeric LANGUAGE sql AS 'SELECT 1';",
			want: []string{"f(a int,b text default 'x(') returns setof numeric"}},
		{name: "procedure has no result", sql: "CREATE OR REPLACE PROCEDURE f(INOUT n int) LANGUAGE plpgsql AS $$ BEGIN n := n + 1; END $$;",
			want: []string{"f(inout n int)"}},
		{name: "table result and a multi-word type", sql: "CREATE OR REPLACE FUNCTION f(at timestamp with time zone) RETURNS TABLE(id bigint, \"Amount\" numeric) STABLE AS 'SELECT 1, 2';",
			want: []string{`f(at timestamp with time zone) returns table(id bigint,"amount" numeric)`}},
		{name: "begin atomic body", sql: "CREATE OR REPLACE FUNCTION f(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT x; END;",
			want: []string{"f(x int) returns int"}},
		{name: "overloads, comments skipped", sql: "CREATE OR REPLACE FUNCTION f(int) RETURNS int AS 'SELECT 1' LANGUAGE sql;\nCOMMENT ON FUNCTION f(int) IS 'x';\nCREATE OR REPLACE FUNCTION f(text) RETURNS int AS 'SELECT 2' LANGUAGE sql;",
			want: []string{"f(int) returns int", "f(text) returns int"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, problems := ParseRoutine(appF, []byte(tt.sql))
			require.Empty(t, problems)
			assert.Equal(t, tt.want, r.Signatures())
		})
	}
}
