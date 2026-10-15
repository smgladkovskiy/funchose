package funchose

import "errors"

// Errors a run can fail with. They are wrapped with details, so test them with
// errors.Is.
var (
	// ErrInvalidFiles: a routine file fails the checks of Load. The error lists
	// every problem.
	ErrInvalidFiles = errors.New("funchose: invalid routine files")
	// ErrTableRequired: the history table cannot be chosen by default, because the
	// files cover several schemas or none.
	ErrTableRequired = errors.New("funchose: history table required")
	// ErrBuildRequired: Up or Plan was called without a build number and without
	// WithLocal.
	ErrBuildRequired = errors.New("funchose: build number required")
	// ErrLockTimeout: another run held the lock of the history table for longer
	// than the lock timeout.
	ErrLockTimeout = errors.New("funchose: lock not acquired")
	// ErrMissingSchema: a schema directory has no schema in the database.
	ErrMissingSchema = errors.New("funchose: schema does not exist")
	// ErrSchemaWithoutDir: the history tracks routines in a schema the files do
	// not cover: the directory was removed, or another service writes to the
	// same history table.
	ErrSchemaWithoutDir = errors.New("funchose: tracked routines in a schema without a directory")
	// ErrRetireLimit: the run would retire more routines than the retire limit.
	ErrRetireLimit = errors.New("funchose: too many routines to retire")
	// ErrOverload: CREATE OR REPLACE with a new argument list added an overload
	// instead of replacing the routine.
	ErrOverload = errors.New("funchose: a new argument list adds an overload")
	// ErrDrift: the run would overwrite a definition funchose did not apply,
	// changed by hand or found on adoption, and overwriting it is not allowed.
	ErrDrift = errors.New("funchose: the run would overwrite a definition funchose did not apply")
	// ErrFailOn: a change has an action that WithFailOn turns into an error.
	ErrFailOn = errors.New("funchose: a routine needs attention")
	// ErrInvalidOption: options that contradict each other or a value out of
	// range.
	ErrInvalidOption = errors.New("funchose: invalid option")
)

// sqlState returns the SQLSTATE of a PostgreSQL error, or "" for other errors.
// pgx and lib/pq errors both have the method.
func sqlState(err error) string {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState()
	}

	return ""
}

// undefined reports a "does not exist" error: undefined function, table or object.
func undefined(err error) bool {
	switch sqlState(err) {
	case "42883", "42P01", "42704":
		return true
	}

	return false
}
