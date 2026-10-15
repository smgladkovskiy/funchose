<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset=".github/logo-dark.svg">
    <img alt="funchose" src=".github/logo-light.svg" height="56">
  </picture>
</h1>

[![ci](https://github.com/smgladkovskiy/funchose/actions/workflows/ci.yml/badge.svg)](https://github.com/smgladkovskiy/funchose/actions/workflows/ci.yml)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/smgladkovskiy/funchose/badges/coverage.json)](https://github.com/smgladkovskiy/funchose/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/smgladkovskiy/funchose.svg)](https://pkg.go.dev/github.com/smgladkovskiy/funchose)

Keep PostgreSQL functions and procedures in git as plain files, one file per
routine, and apply the ones that changed. A Go library and a CLI. It sits next
to goose, golang-migrate or any other migration tool and takes over the part of
the schema they handle worst: stored routines.

## Why

Versioned migrations are a good fit for tables. A column is added once, and the
migration file says exactly that. Functions are different. To change one line
of a 1,500-line PL/pgSQL function, a migration has to carry the whole new body
in `up` and the whole old body in `down`. The reviewer gets 3,000 new lines and
has to find the three that changed. Two branches that touch the same function
each carry a full copy, and whichever is deployed last quietly wins.

funchose puts the function back where code lives: in one file that you edit in
place.

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

MIT
