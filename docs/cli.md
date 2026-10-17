[funchose](../README.md) › Command line

# Command line

- [Commands](#commands)
- [Flags and environment](#flags-and-environment)
- [Exit codes](#exit-codes)
- [lint](#lint)

```
go install github.com/smgladkovskiy/funchose/cmd/funchose@latest
```

## Commands

```
funchose up      [flags]   apply the routines that changed
funchose plan    [flags]   the same, with diffs, rolled back at the end
funchose status  [flags]   compare files, history and database; changes nothing, takes no lock
funchose lint    [flags]   check routine files; in a merge request, also the signatures the branch changed
funchose export  [flags]   write the routines of a schema into files
funchose version
```

## Flags and environment

The flags that change behaviour have environment variables on purpose: a
rollback in GitLab is "run the job of the old pipeline again, with one variable
set", without editing the job. A variable that does not parse stops funchose
instead of falling back to the default.

| Flag                       | Environment                    | Default                     |                                                                                    |
|----------------------------|--------------------------------|-----------------------------|------------------------------------------------------------------------------------|
| `-dsn`                     | `FUNCHOSE_DSN`, `DATABASE_URL` |                             | connection string of a user allowed to create the routines                         |
| `-dir`                     | `FUNCHOSE_DIR`                 | `routines`                  | directory with `<schema>/<name>.sql`                                               |
| `-table`                   | `FUNCHOSE_TABLE`               | `<schema>.funchose_history` | history table as `schema.name`; required when the files cover several schemas      |
| `-build`                   | `FUNCHOSE_BUILD`               |                             | build number; `up` and `plan` need it                                              |
| `-local`                   |                                |                             | run as build 0 against a developer database                                        |
| `-allow-downgrade`         | `FUNCHOSE_ALLOW_DOWNGRADE`     | `false`                     | let an older build roll routines back                                              |
| `-allow-drop`              | `FUNCHOSE_ALLOW_DROP`          | `false`                     | drop routines funchose cannot prove unused                                         |
| `-overwrite-drift`         | `FUNCHOSE_OVERWRITE_DRIFT`     | `false`                     | replace definitions funchose did not apply                                         |
| `-replicas`                | `FUNCHOSE_REPLICAS`            | `false`                     | routines are also called on read replicas                                          |
| `-retire-limit`            | `FUNCHOSE_RETIRE_LIMIT`        | `10`                        | most routines one run may retire, 0 for no limit                                   |
| `-removed-schemas`         | `FUNCHOSE_REMOVED_SCHEMAS`     |                             | schemas whose directory was removed on purpose                                     |
| `-fail-on`                 | `FUNCHOSE_FAIL_ON`             | `stale`                     | warnings that fail `up` and `plan`: `drift`, `stale`, `blocked`, `kept`, or `none` |
| `-lock-timeout`            | `FUNCHOSE_LOCK_TIMEOUT`        | `5m`                        | wait for another run on the same history table                                     |
| `-schema`                  |                                |                             | `export`: comma-separated schemas                                                  |
| `-force`                   |                                | `false`                     | `export`: overwrite existing files                                                 |
| `-base`, `-since`, `-head` |                                | from CI variables           | `lint`: target branch, fork point, last commit of the branch                       |

## Exit codes

| Code |                                                                                         |
|------|-----------------------------------------------------------------------------------------|
| `0`  | done, warnings that `-fail-on` does not name included                                   |
| `1`  | the run failed, a warning named in `-fail-on`, or `lint` found problems                 |
| `2`  | funchose could not start: flags, environment, a missing build number, the history table |

## lint

`lint` checks the routine files against the same rule `NewProvider` applies,
without a database. In a merge request pipeline it also fails **a signature that
disappeared** from a file the branch changed. A changed argument list or result
type, or a removed overload, cannot be done in place, and the deploy would fail
on it:

```
routines/billing/invoice_total.sql
  invoice_total(invoice_id bigint) returns numeric is gone from the file: a new argument list or
  result type is a new routine (copy the file to invoice_total_v2.sql and change the copy); removing
  an overload needs a schema migration that drops it
```

`lint` compares the branch itself with the commit it forked from, not the merge
commit CI may check out. In GitLab merged results pipelines it takes the fork
point from `CI_MERGE_REQUEST_DIFF_BASE_SHA` and the branch from
`CI_MERGE_REQUEST_SOURCE_BRANCH_SHA` on its own; on GitHub check out the head of
the pull request (see [CI](ci.md)); elsewhere pass `-since <commit>` and
`-head <commit>`. Problems are printed as `::error file=...` annotations on GitHub
Actions.

Removed files are the deploy's business: it drops a routine only once nothing
calls it ([Removing a routine](routine-files.md#removing-a-routine)).

---

[← Go library](go-library.md) · [Contents](../README.md#documentation) · [CI →](ci.md)
