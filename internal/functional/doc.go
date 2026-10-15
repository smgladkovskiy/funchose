// Package functional tests funchose against real PostgreSQL servers started
// with testcontainers. It is a module of its own so that the library and the
// command line do not depend on Docker and testcontainers.
//
// The tests need Docker. FUNCHOSE_PG_IMAGE picks the server, postgres:18-alpine
// by default:
//
//	FUNCHOSE_PG_IMAGE=postgres:14-alpine go test ./...
package functional
