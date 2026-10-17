[funchose](../README.md) › How it works

# How it works

- [Three sources](#three-sources)
- [A run step by step](#a-run-step-by-step)
- [What a run decides](#what-a-run-decides)
- [Actions](#actions)

## Three sources

|              | What it is                                                                                                                                                                                 |
|--------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **file**     | `<schema>/<name>.sql`: every overload of one routine name. Its **version** is the sha256 of its content, line endings normalized                                                           |
| **database** | the routine in `pg_proc`. Its **fingerprint** is the sha256 of `pg_get_functiondef` of every overload, always read with `search_path = pg_catalog`, so types print the same way every time |
| **history**  | the table `<schema>.funchose_history`: a log of what every build did to every routine, with the definition it replaced                                                                     |

Every run compares the three and decides, routine by routine, what to do.

## A run step by step

1. `up` first runs the comparison below in a read-only transaction, without the
   lock. If there is nothing to write, it returns right there: pod restarts and
   repeated deploys never queue behind each other.
2. Otherwise it opens a transaction and takes an advisory lock on the history
   table, waiting no longer than the lock timeout (5 minutes by default). It
   checks that every schema directory has a schema and creates the history table
   if needed.
3. It reads the history (without the stored definitions: those are only read
   when a rollback needs one) and fingerprints the routines of the managed
   schemas.
4. It decides what to do with every file, every routine the history tracks
   without a file, and lists the routines that have neither.
5. It runs each file statement by statement under a savepoint, with
   `search_path` set to `<schema>, public`, and checks the result: as many
   overloads as `CREATE` statements in the file, and no definition funchose did
   not apply replaced without permission. A `LANGUAGE sql` routine that calls a
   routine created in the same run is retried after the others.
6. It retires and drops the routines whose files are gone, writes a history
   record for everything it did, and commits. `plan` rolls back instead and
   shows a diff of every definition; `status` only reports.

Any error rolls the whole transaction back: a release gets all its routines or
none.

## What a run decides

For a routine with a file:

| The database has                                                         | funchose does                                                                            | Action                 |
|--------------------------------------------------------------------------|------------------------------------------------------------------------------------------|------------------------|
| nothing with this name                                                   | runs the file                                                                            | `create`               |
| a routine funchose never saw, the same as the file                       | runs the file, records it                                                                | `adopt`                |
| a routine funchose never saw, different from the file                    | fails unless overwriting drift is allowed: it would replace a definition nobody reviewed | `adopt` or error       |
| the version of this file                                                 | nothing                                                                                  | `unchanged`            |
| another version, applied by an older build or by this one                | runs the file                                                                            | `update`               |
| another version, applied by a newer build, or this build was rolled back | nothing, unless rolling back is allowed                                                  | `stale`, or `rollback` |
| a definition someone changed by hand, the file did not change            | nothing                                                                                  | `drift`                |
| a definition someone changed by hand, and the file would change it       | fails unless overwriting drift is allowed                                                | error, or `update`     |

For a routine the history tracks but no file describes:

| Situation                                                                          | funchose does                                              | Action    |
|------------------------------------------------------------------------------------|------------------------------------------------------------|-----------|
| the first deploy without the file                                                  | records the retirement and the call counter, drops nothing | `retire`  |
| the same build again, a pod restart                                                | nothing                                                    | `retired` |
| a newer build, the counter did not grow                                            | drops every overload                                       | `drop`    |
| a newer build, the counter grew                                                    | keeps it, checks again on the next deploy                  | `kept`    |
| the calls cannot be counted, other objects depend on it, or other routines call it | keeps it                                                   | `blocked` |
| a newer build put it under funchose, this one is older                             | nothing, unless rolling back is allowed                    | `stale`   |

Routines in the managed schemas that have neither a file nor a history are
listed as `untracked` and never touched.

## Actions

| Action                                          | Changes the database | Needs attention                      |
|-------------------------------------------------|----------------------|--------------------------------------|
| `create`, `adopt`, `update`, `rollback`, `drop` | yes                  |                                      |
| `retire`                                        | history only         |                                      |
| `unchanged`, `retired`, `untracked`             | no                   |                                      |
| `drift`, `stale`, `blocked`, `kept`             | no                   | yes: see [Operations](operations.md) |

The command line fails a run that ends in `stale` (`-fail-on`); the library
fails on what `WithFailOn` names.

---

[Contents](../README.md#documentation) · [Getting started →](getting-started.md)
