package funchose

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Routine is one routine file: every overload of one function or procedure
// name in one schema.
type Routine struct {
	Schema   string // directory the file lives in
	Name     string // file name without .sql
	Path     string // path inside the file system, schema/name.sql
	SQL      string // file content with line endings normalized to \n
	Checksum string // sha256 of SQL, hex
	Creates  int    // CREATE OR REPLACE statements in the file, one per overload

	stmts []statement // what runs, statement by statement: exactly what was checked
}

// Signatures returns the identity of every overload the file creates: the
// name, the argument list and the result, normalized and without the schema. A
// signature that disappears from a file is a new argument list or result type,
// which CREATE OR REPLACE cannot do in place.
func (r Routine) Signatures() []string {
	var out []string

	for _, stmt := range r.stmts {
		if sig := signature(stmt.head); sig != "" {
			out = append(out, sig)
		}
	}

	return out
}

func (r Routine) key() routineKey { return routineKey{r.Schema, r.Name} }

// classify tells whether a statement creates an overload of the routine of the
// file, or what is wrong with it.
func (r Routine) classify(stmt statement) (bool, string) {
	if match := createRe.FindStringSubmatch(stmt.head); match != nil {
		schema, name := qualified(match[createFirst], match[createSecond])

		switch {
		case match[createReplace] == "":
			return false, "use CREATE OR REPLACE: a plain CREATE fails once the routine exists"
		case name != r.Name:
			return false, fmt.Sprintf("creates %s, but the file is named %s.sql", name, r.Name)
		case schema != "" && schema != r.Schema:
			return false, fmt.Sprintf("creates the routine in schema %s, but the file lives in %s/", schema, r.Schema)
		default:
			return true, ""
		}
	}

	if match := commentRe.FindStringSubmatch(stmt.head); match != nil {
		schema, name := qualified(match[commentFirst], match[commentSecond])
		if name != r.Name || (schema != "" && schema != r.Schema) {
			return false, "comments on another routine; keep it in that routine's file"
		}

		return false, ""
	}

	what := "this statement"
	if match := wordRe.FindStringSubmatch(stmt.head); match != nil {
		what = strings.ToUpper(strings.Join(strings.Fields(match[1]), " "))
	}

	return false, what + " does not belong in a routine file: it is applied again on every change; " +
		"put it in a schema migration"
}

// routineKey identifies a routine name in a schema.
type routineKey struct{ schema, name string }

func (k routineKey) String() string { return k.schema + "." + k.name }

// Load reads every schema/name.sql file of fsys and checks that each one only
// creates or comments on the routine it is named after. Files that fail the
// check are returned as problems, not as routines.
func Load(fsys fs.FS) ([]Routine, []Problem, error) {
	var (
		routines []Routine
		problems []Problem
	)

	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || path.Ext(name) != ".sql" {
			return nil
		}

		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		routine, found := ParseRoutine(name, raw)

		problems = append(problems, found...)
		if len(found) == 0 {
			routines = append(routines, routine)
		}

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk the routine files: %w", err)
	}

	slices.SortFunc(routines, func(a, b Routine) int { return strings.Compare(a.Path, b.Path) })

	return routines, problems, nil
}

// pathParts are the parts of a routine file path: schema and file name.
const pathParts = 2

// ParseRoutine checks the content of one routine file found at file,
// schema/name.sql. A file with problems returns no routine.
func ParseRoutine(file string, content []byte) (Routine, []Problem) {
	parts := strings.Split(file, "/")
	if len(parts) != pathParts {
		return Routine{}, []Problem{{Path: file, Msg: "routine files live at <schema>/<name>.sql"}}
	}

	routine := Routine{
		Schema: parts[0],
		Name:   strings.TrimSuffix(parts[1], ".sql"),
		Path:   file,
		SQL:    strings.ReplaceAll(string(content), "\r\n", "\n"),
	}
	sum := sha256.Sum256([]byte(routine.SQL))
	routine.Checksum = hex.EncodeToString(sum[:])

	if problems := check(&routine); len(problems) > 0 {
		return Routine{}, problems
	}

	return routine, nil
}

const ident = `("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`

// Submatches of createRe and commentRe.
const (
	createReplace = 1
	createFirst   = 3
	createSecond  = 4
	commentFirst  = 2
	commentSecond = 3
)

var (
	createRe = regexp.MustCompile(`(?is)^create\s+(or\s+replace\s+)?(function|procedure)\s+` +
		ident + `(?:\s*\.\s*` + ident + `)?\s*\(`)
	commentRe = regexp.MustCompile(`(?is)^comment\s+on\s+(function|procedure)\s+` +
		ident + `(?:\s*\.\s*` + ident + `)?\s*\(`)
	wordRe = regexp.MustCompile(`^\s*([A-Za-z]+(?:\s+[A-Za-z]+)?)`)
)

// check classifies the statements of the file and counts its overloads.
func check(routine *Routine) []Problem {
	stmts, err := split(routine.SQL)
	if err != nil {
		return []Problem{{Path: routine.Path, Line: err.line, Msg: err.msg}}
	}

	var problems []Problem

	for _, stmt := range stmts {
		creates, msg := routine.classify(stmt)
		if msg != "" {
			problems = append(problems, Problem{Path: routine.Path, Line: stmt.line, Msg: msg})

			continue
		}

		if creates {
			routine.Creates++
		}
	}

	if routine.Creates == 0 && len(problems) == 0 {
		msg := "no CREATE OR REPLACE FUNCTION or PROCEDURE " + routine.Name
		problems = append(problems, Problem{Path: routine.Path, Msg: msg})
	}

	routine.stmts = stmts

	return problems
}

// qualified turns the matched identifiers of [schema.]name into schema and name:
// unquoted identifiers fold to lower case, quoted ones keep their spelling.
func qualified(first, second string) (string, string) {
	if second == "" {
		return "", unquote(first)
	}

	return unquote(first), unquote(second)
}

func unquote(id string) string {
	if strings.HasPrefix(id, `"`) {
		return strings.ReplaceAll(id[1:len(id)-1], `""`, `"`)
	}

	return strings.ToLower(id)
}
