[funchose](../README.md) › Operations

# Operations

- [Warnings](#warnings)
- [Errors](#errors)
- [Recipes](#recipes)
- [Schema changes next to goose](#schema-changes-next-to-goose)
- [The history table](#the-history-table)

## Warnings

A warning leaves the routine as it is. The command line fails on `stale` by
default; add the others to `-fail-on` where you want a deploy to stop.

| Action    | What happened                                                                                                                                                           | What to do                                                                                                                                                                                                                                                                     |
|-----------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `drift`   | the routine was changed in the database after funchose applied it, and the file did not change                                                                          | find out who changed it and why. Keep the change: put it into the file (`funchose export -schema S -force`, then commit the file). Drop it: run once with `-overwrite-drift`                                                                                                   |
| `stale`   | a newer build applied this routine, or this build was rolled back                                                                                                       | in a deploy job: the job of an older pipeline ran again, or a branch was deployed to the same database with a newer pipeline. Run the newest pipeline again; to go back on purpose, use `-allow-downgrade`. At application start: a pod of the previous release, nothing to do |
| `blocked` | the routine should be dropped, but funchose cannot prove it safe: calls are not counted, objects depend on it, or a rollback would drop a routine a newer build created | read the note. Dependents: remove them in a schema migration. Uncounted calls: check the callers, then run once with `-allow-drop`, or drop the routine in a migration                                                                                                         |
| `kept`    | a retired routine was called since the file was removed                                                                                                                 | something still calls it. Find the caller in `pg_stat_statements` or the code; funchose checks again on the next deploy                                                                                                                                                        |

## Errors

| Error                                                                | What to do                                                                                                                                                                                                                                                                                            |
|----------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `build number required`                                              | pass `-build $CI_PIPELINE_IID` (`FUNCHOSE_BUILD`), or `-local` for a developer database                                                                                                                                                                                                               |
| `invalid environment variable`                                       | a `FUNCHOSE_*` variable does not parse: fix the CI variable                                                                                                                                                                                                                                           |
| `history table required`                                             | the files cover several schemas or none: set `-table schema.funchose_history` and keep it for every run of the service                                                                                                                                                                                |
| `tracked routines in a schema without a directory`                   | the history tracks routines of a schema the files do not cover. A directory was removed on purpose: name the schema in `-removed-schemas` until its routines are dropped. A directory got lost in the build: fix the build. Another service writes to the same table: give each service its own table |
| `too many routines to retire`                                        | the run would retire more than `-retire-limit` routines. Check the list in the error. Removed on purpose: raise the limit for this run                                                                                                                                                                |
| `the run would overwrite a definition funchose did not apply`        | the database holds a definition funchose did not apply, and the file would replace it. `funchose plan` shows the diff. Take the file: `-overwrite-drift`. Keep the database version: export it into the file                                                                                          |
| `a new argument list adds an overload`                               | `CREATE OR REPLACE` with new arguments created a second routine. Put the new signature into a new file (`_v2`)                                                                                                                                                                                        |
| `cannot change return type`, `cannot change name of input parameter` | the same: a new signature is a new routine                                                                                                                                                                                                                                                            |
| `must be owner of function`                                          | the user funchose runs as does not own the routine: `ALTER FUNCTION ... OWNER TO` in a migration, or run as the owner                                                                                                                                                                                 |
| `schema does not exist`                                              | create the schema in a schema migration first                                                                                                                                                                                                                                                         |
| `lock not acquired`                                                  | another run held the lock longer than `-lock-timeout`: a long deploy, or a stuck session holding a transaction open (`pg_stat_activity`)                                                                                                                                                              |
| `a routine needs attention`                                          | a warning named in `-fail-on`; the report above the error says which                                                                                                                                                                                                                                  |

## Recipes

**Fix a function on production right now.** Run the fixed `CREATE OR REPLACE`,
or the previous definition from the history (see [Builds and
rollbacks](builds-and-rollbacks.md#rolling-back)). funchose reports `drift` from
then on and does not undo it. Bring the fix into the file; the next deploy with
the same change goes through as an ordinary `update`.

**Accept what the database has.** `funchose export -schema S -force` writes the
current definitions into the files; commit the ones you want. A file that equals
the database is applied without changing anything.

**The build counter started again.** A new CI project or a reset counter gives
builds lower than the ones in the history, and everything becomes `stale`. Deploy
once with `-allow-downgrade`: the newest files become the current versions, and
the following builds move forward from there.

**Remove many dead routines.** Delete the files in one merge request and deploy
with `-retire-limit` set to their number; the next deploy drops the ones nothing
called.

## Schema changes next to goose

funchose runs after the table migrations. A change that removes or renames a
column a routine reads needs two releases:

1. change the routine so it no longer reads the column (funchose), and add the new
   column if there is one (goose);
2. drop the old column (goose).

Done in one release, the migration runs first: a routine with a SQL-standard body (`BEGIN ATOMIC`) depends on the
column, and the `DROP COLUMN` fails; a PL/pgSQL
routine does not, the migration goes through, and the routine fails when it is
called.

## The history table

```sql
CREATE TABLE billing.funchose_history
(
    id                bigserial PRIMARY KEY,
    schema_name       text        NOT NULL,
    routine_name      text        NOT NULL,
    action            text        NOT NULL, -- create, adopt, update, rollback, retire, drop
    build             bigint      NOT NULL,
    from_checksum     text,                 -- file version before, NULL if untracked
    to_checksum       text,                 -- file version after, NULL if no longer tracked
    from_fingerprint  text        NOT NULL, -- database state before, '' if the routine did not exist
    to_fingerprint    text        NOT NULL, -- database state after
    definition_before text,                 -- what was replaced, ready to run
    calls             bigint,               -- retire: the call counter when the file was found gone
    rolled_back_by    bigint,               -- id of the record that rolled this one back
    applied_at        timestamptz NOT NULL DEFAULT now(),
    applied_by        text        NOT NULL DEFAULT current_user
);
```

It is a log, not a set of applied versions: records are only added, and a
rollback marks the records it undoes. "What happened to `invoice_total` and when"
is one query:

```sql
SELECT id,
       action,
       build,
       left(from_checksum, 12) AS "from",
       left(to_checksum, 12)   AS "to",
       applied_at,
       applied_by,
       rolled_back_by
FROM billing.funchose_history
WHERE schema_name = 'billing'
  AND routine_name = 'invoice_total'
ORDER BY id;
```
