// Package funchose keeps PostgreSQL functions and procedures as plain files in
// git and applies the ones that changed.
//
// Each routine lives in <schema>/<name>.sql and holds CREATE OR REPLACE
// statements for every overload of that name. A history table records which
// version of every file, identified by its sha256, was applied by which build
// and what the database definition was before. On every run funchose compares
// the files with that history and with the database:
//
//   - a file the history has never seen creates the routine, or adopts it if
//     the routine already exists in the database;
//   - a changed file replaces the routine body;
//   - a build older than the one that applied the current version leaves the
//     routine alone, unless rolling back is allowed;
//   - a definition funchose did not apply, changed by hand or adopted, is
//     reported and not overwritten unless that is allowed;
//   - a removed file retires the routine, and a later deploy drops it once the
//     call counter shows that nothing calls it.
//
// Changes run in one transaction under an advisory lock: either every changed
// routine is applied or none is. Only the body may change in place: a new
// argument list or result type is a new routine.
package funchose
