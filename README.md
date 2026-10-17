<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/logo-dark.svg">
    <img alt="funchose" src=".github/logo-light.svg" height="56">
  </picture>
</h1>

[![ci](https://github.com/smgladkovskiy/funchose/actions/workflows/ci.yml/badge.svg)](https://github.com/smgladkovskiy/funchose/actions/workflows/ci.yml)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/smgladkovskiy/funchose/badges/coverage.json)](https://github.com/smgladkovskiy/funchose/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/smgladkovskiy/funchose.svg)](https://pkg.go.dev/github.com/smgladkovskiy/funchose)

Migrations for PostgreSQL functions and stored procedures, where a one-line
change is a one-line diff. A Go library and a CLI that work next to goose,
golang-migrate or any other migration tool: the tables stay with it, the
routines move to funchose.

## The problem

A migration tool can change a function only one way: with a new migration that
carries the whole new body in `up` and the whole old body in `down`. This is
the merge request for one changed line in a 1,500-line PL/pgSQL function:

```diff
 migrations/20261014120000_invoice_total_discount.sql                      +3,061 −0
@@ -0,0 +1,3061 @@
+-- +goose Up
+-- +goose StatementBegin
+CREATE OR REPLACE FUNCTION billing.invoice_total(invoice_id bigint)
+ RETURNS numeric
+ LANGUAGE plpgsql
+AS $function$
+BEGIN
+    … 805 lines of the function, all green, all as they were
+    RETURN (SELECT coalesce(sum(amount - discount), 0) FROM billing.invoice_line WHERE invoice = invoice_id);
+    … 692 more lines, all green, all as they were
+END
+$function$;
+-- +goose StatementEnd
+
+-- +goose Down
+-- +goose StatementBegin
+CREATE OR REPLACE FUNCTION billing.invoice_total(invoice_id bigint)
+ RETURNS numeric
+ LANGUAGE plpgsql
+AS $function$
+BEGIN
+    … the same 1,500 lines once more, copied from the previous migration
+END
+$function$;
+-- +goose StatementEnd
```

The change is in there, one green line among 3,061, with nothing to mark it:
the old body lives in another migration, so the diff has nothing to compare
with. The reviewer either diffs two migrations by hand or approves on trust.
And when two branches change the same function, each carries a full copy, and
whichever is deployed last silently undoes the other.

## With funchose

The function lives in one file, `routines/billing/invoice_total.sql`, that you
edit in place. The same change in the merge request:

```diff
 routines/billing/invoice_total.sql                                              +1 −1
@@ -809,7 +809,7 @@ BEGIN
       WHERE i.id = invoice_id;

     -- an empty invoice totals to zero
-    RETURN (SELECT coalesce(sum(amount), 0) FROM billing.invoice_line WHERE invoice = invoice_id);
+    RETURN (SELECT coalesce(sum(amount - discount), 0) FROM billing.invoice_line WHERE invoice = invoice_id);
 END
 $function$
 ;
```

Two branches that change the same function now meet in git: different lines
merge, the same lines conflict. On deploy funchose applies the files that
changed, in one transaction, and keeps a history of every version.

```
routines/
  billing/                      a directory per schema
    invoice_total.sql           a file per function or procedure name
    invoice_close_v2.sql
```

- The merge request shows a normal diff of the function, and `funchose plan`
  shows the diff against what the database has now.
- A deploy applies only the files that changed, all in one transaction.
- A history table records which build applied which version and keeps the
  definition it replaced, so you can see what changed and roll it back.
- Functions created by hand years ago are adopted as they are.
- Delete a file and a later deploy drops the routine, once the call counter
  shows nothing calls it.
- A pod of the previous release restarting during a rollout does not roll your
  functions back.

The rule behind all of it: only the body changes in place. A new argument list
or result type is a new routine, `invoice_close_v3`, next to the old one.

About the name: functions in a database that has lived for a few years grow long
and tangled, like noodles, and Russian speakers call glass noodles *funchoza*.
Read in English, it is func plus hose: the old body is hosed down and the new one
poured in.

## Quick start

```
go install github.com/smgladkovskiy/funchose/cmd/funchose@latest
export FUNCHOSE_DSN=postgres://app@db/app
```

Bring the routines of an existing schema under funchose, check what the first
run would do, and run it:

```
$ funchose export -schema billing
funchose export: 3 files written, 0 existing kept

$ funchose up -build 41
funchose up: build 41 on billing.funchose_history, 3 applied, 0 unchanged

  adopt     billing.invoice_close_v1  untracked -> 95edb0dba3f8 (build 41)
                                      the file matches the database
  adopt     billing.invoice_close_v2  untracked -> 99bb2be67fe3 (build 41)
                                      the file matches the database
  adopt     billing.invoice_total     untracked -> 4f20431699a2 (build 41)
                                      the file matches the database
```

Edit a file, review the diff against the database, deploy:

```
$ funchose plan -build 42
funchose plan: build 42 on billing.funchose_history, 1 to apply, 2 unchanged

  update    billing.invoice_total  4f20431699a2 (build 41) -> a7ad2cd9d6ad (build 42)

      --- billing.invoice_total (database)
      +++ billing.invoice_total (after)
      @@ -4,7 +4,7 @@
        STABLE
       AS $function$
       BEGIN
      -    RETURN (SELECT coalesce(sum(amount), 0) FROM billing.invoice_line WHERE invoice = invoice_id);
      +    RETURN (SELECT coalesce(sum(amount - discount), 0) FROM billing.invoice_line WHERE invoice = invoice_id);
       END
       $function$
       ;

$ funchose up -build 42
```

`-build` is the number of the release, `$CI_PIPELINE_IID` or `$GITHUB_RUN_NUMBER`
in CI. Up and plan refuse to run without it; `-local` runs against a developer
database.

## Documentation

| Page | What it covers |
| --- | --- |
| [How it works](docs/how-it-works.md) | versions, fingerprints and the history; what a run decides for every routine; one transaction, the lock and the fast path |
| [Getting started](docs/getting-started.md) | bringing an existing database under funchose; the history table; the rights funchose needs |
| [Routine files](docs/routine-files.md) | what a file may contain; changing a routine; a new signature; removing a routine |
| [Builds and rollbacks](docs/builds-and-rollbacks.md) | build numbers and stale releases; reverting, rolling back an image, emergency rollback; changes made by hand |
| [Go library](docs/go-library.md) | running funchose next to goose; options; errors; the report and metrics |
| [Command line](docs/cli.md) | commands, flags and environment variables, exit codes, `lint` |
| [CI](docs/ci.md) | merge request checks and the deploy job for GitLab and GitHub Actions |
| [Operations](docs/operations.md) | what to do on every warning and error; the history table; schema changes with goose |
| [Alternatives](docs/alternatives.md) | Flyway, Liquibase, Sqitch, Graphile Migrate, Atlas and others |

## Development

```
make lint              # golangci-lint, both modules
make test              # unit tests: no database, no Docker
make test-functional   # functional tests against PostgreSQL in Docker (testcontainers)
make cover             # unit and functional coverage together
```

`make test-functional PG_IMAGE=postgres:14-alpine` runs the functional tests on
another PostgreSQL; CI runs them on 14 to 18. They live in a module of their own,
`internal/functional`, so that the library does not depend on testcontainers.

## License

[MIT](LICENSE)
