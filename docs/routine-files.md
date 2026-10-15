[funchose](../README.md) › Routine files

# Routine files

- [What a file may contain](#what-a-file-may-contain)
- [Changing a routine](#changing-a-routine)
- [A new signature](#a-new-signature)
- [Removing a routine](#removing-a-routine)
- [What funchose does not do](#what-funchose-does-not-do)

## What a file may contain

- `<schema>/<name>.sql`: the directory is the schema, the file name is the
  routine name. The schema must exist: funchose does not create schemas.
- Only `CREATE OR REPLACE FUNCTION` and `CREATE OR REPLACE PROCEDURE` for that
  name, one statement per overload, and `COMMENT ON` for it. The file runs again
  on every change, so anything else (`DROP`, `GRANT`, `ALTER TABLE`, data)
  belongs in a schema migration. `funchose lint` and `NewProvider` reject files
  that break the rule, and funchose runs the statements it checked one by one,
  not the file as a whole.
- Without a schema in the `CREATE` statement the routine goes to the directory's
  schema: funchose sets `search_path` to `<schema>, public` while the file runs.
  A schema in the statement must match the directory.
- Names inside the body are resolved when the routine is called, with the
  caller's `search_path`. Give routines their own `SET search_path`, especially
  `SECURITY DEFINER` ones.
- `check_function_bodies` stays on, so PL/pgSQL syntax errors fail the run. A
  `LANGUAGE sql` routine that calls a routine created in the same run is retried
  after the others.

## Changing a routine

Edit the file. The merge request shows what you changed and nothing else.
`funchose plan` runs everything `up` would run, including the `CREATE OR REPLACE`
statements and their checks, prints the diff of every definition against the
database, and rolls back. The diff is between what the database has and what it
will have, not between two commits, so it also shows the effect of a change on a
database that drifted.

## A new signature

To change the arguments or the result type, add a new file,
`invoice_total_v2.sql`, switch the code to it, and remove the old file in a later
release. PostgreSQL refuses most such changes in place anyway, and the one it
accepts, an extra argument, creates a second function next to the first;
funchose fails the run instead (`ErrOverload`). `funchose lint` catches it
earlier, in the merge request: a signature that disappears from a changed file
fails the check.

A tip for reviewers: copy the old file in one commit and change the copy in the
next one. The second commit then shows exactly how `_v2` differs from `_v1`.

## Removing a routine

Delete its file. funchose drops the routine, but not in the same deploy: while
that release rolls out, pods of the previous one still call it.

1. The first deploy without the file **retires** the routine. Nothing is dropped.
   The history keeps its definition and its call counter from
   `pg_stat_user_functions`.
2. A later deploy, meaning a newer build and not a restart of the same one,
   **drops** it if the counter did not grow. If it grew, something still calls the
   routine: it is `kept` and checked again on the deploy after.

```
$ funchose up -build 57
  retire    billing.invoice_close_v1  95edb0dba3f8 (build 41)
                                      the file was removed: dropped by the next deploy if nothing calls it until then

$ funchose up -build 58
  drop      billing.invoice_close_v1  95edb0dba3f8 (build 41) -> none
                                      retired by build 57, not called since
```

Put the file back before that, and the retirement is over. After the drop, a
deliberate rollback to a release that still has the file creates the routine
again, and so does reverting the commit that removed the file.

funchose drops a routine only when it can tell that nothing calls it. It cannot
tell, and the routine stays `blocked` until you drop it with `-allow-drop` or in a
schema migration, when:

- `track_functions` is `none`, the PostgreSQL default. Set it to `pl` (or `all`)
  to let funchose count calls;
- the routine is `LANGUAGE sql`: such routines can be inlined into the calling
  query and are then not counted. A counter that grew still keeps them;
- the routine is also called on read replicas, which the primary does not count.
  Say so with `-replicas` (`WithReplicas`);
- another object depends on it: a trigger, a view, a column default, a routine
  with a SQL-standard body. The report names them.

Two guards against losing routines by mistake:

- **Retire limit.** One run retires at most 10 routines (`-retire-limit`). A lost
  directory or a bad merge looks exactly like many removed files; when you do
  remove many, raise the limit for that run.
- **Schemas without a directory.** If the history tracks routines of a schema the
  files no longer cover, the run fails. When you removed the directory on
  purpose, name the schema in `-removed-schemas` until its routines are dropped.

`funchose lint` lists the routine files a merge request deletes, and the other
routine files that still mention them.

## What funchose does not do

- Tables, views, triggers, types, grants. Keep a migration tool for them;
  funchose only replaces bodies.
- Drop routines it does not manage. Untracked routines are only listed; to drop
  one, adopt it (`export`), then delete its file.
- Check that a body works: PostgreSQL only checks PL/pgSQL syntax when a function
  is created. Run [plpgsql_check](https://github.com/okbob/plpgsql_check) and your
  tests against a database built from your migrations.

---

[← Getting started](getting-started.md) · [Contents](../README.md#documentation) · [Builds and rollbacks →](builds-and-rollbacks.md)
