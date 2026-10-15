[funchose](../README.md) › Getting started

# Getting started

- [Export what is there](#export-what-is-there)
- [Check every environment](#check-every-environment)
- [The first run](#the-first-run)
- [The history table](#the-history-table)
- [Rights](#rights)
- [Next to goose](#next-to-goose)

## Export what is there

Most databases that need funchose already have hundreds of functions created by
migrations, by hand, or both. Start from what is there, exporting from the
environment you trust most, usually production:

```
$ export FUNCHOSE_DSN=postgres://app@db/app
$ funchose export -schema billing
funchose export: 3 files written, 0 existing kept
```

`export` writes every routine of the schema exactly as PostgreSQL prints it, one
file per name with all its overloads:

```sql
CREATE OR REPLACE FUNCTION billing.invoice_total(invoice_id bigint)
    RETURNS numeric
    LANGUAGE plpgsql
    STABLE
AS
$function$
BEGIN
    RETURN (SELECT coalesce(sum(amount), 0) FROM billing.invoice_line WHERE invoice = invoice_id);
END
$function$
;
```

Delete the files of routines you do not want to manage, dead `_v1`s for example,
and commit the rest as exported. Reformat them in a later change, not in this
one: the first run should adopt the routines without changing them.

## Check every environment

Run `status` against every environment before the first `up`. Staging often
differs from production, and so do the routines:

```
$ funchose status
funchose status: build 0 on billing.funchose_history, 3 to apply, 0 unchanged

  adopt     billing.invoice_close_v1  untracked -> 95edb0dba3f8 (build 0)
  adopt     billing.invoice_close_v2  untracked -> 99bb2be67fe3 (build 0)
  adopt     billing.invoice_total     untracked -> 4f20431699a2 (build 0)

  1 routine in the managed schemas has no file and no history:
    billing.legacy_report
```

`plan -build N` goes further: it runs the files in a transaction it rolls back
and says, for every adopted routine, whether the file matches the database.

## The first run

```
$ funchose up -build 41
funchose up: build 41 on billing.funchose_history, 3 applied, 0 unchanged

  adopt     billing.invoice_close_v1  untracked -> 95edb0dba3f8 (build 41)
                                      the file matches the database
  ...
```

If a file differs from the routine in the database, the run fails instead of
replacing a definition nobody reviewed:

```
funchose: the run would overwrite a definition funchose did not apply: reporting/daily.sql:
reporting.daily already exists with a definition funchose never applied, and this file changes it.
Compare them with funchose plan; to take the file, allow overwrite-drift; to keep the routine as it
is, export it into the file (funchose export -force)
```

Either run again with `-overwrite-drift` to take the file, or export the
database version into the file. The definition a run replaces is kept in the
history either way.

## The history table

The history lives in `<schema>.funchose_history` when the files cover one schema.
With several schemas, name the table: `-table billing.funchose_history`
(`WithTable`). The name always includes the schema: an unqualified name would
follow the `search_path` of each connection, and a deploy job and a service
would keep two different histories of one database.

**One table per service.** A run treats every routine its table tracks as its
own. If two services shared a table, the second would see the routines of the
first without files. funchose refuses that run (`ErrSchemaWithoutDir`) instead of
retiring them, but the fix is a table per service.

## Rights

The user funchose connects as must:

- own the routines it replaces: `CREATE OR REPLACE` fails with
  `must be owner of function` otherwise. Check before the first run:

  ```sql
  SELECT proowner::regrole, count(*) FROM pg_proc
  WHERE pronamespace = 'billing'::regnamespace GROUP BY 1;
  ```

- have `CREATE` on the schemas of the routines and of the history table.

`plan` needs the same rights as `up`: it runs the statements and takes the lock,
then rolls back. `status` only reads.

These are DDL rights. Give them to a deploy user, not to the user the service
runs as: run funchose as a [deploy job](ci.md), not at application start, unless
the service already runs its migrations itself.

## Next to goose

Routines go after the table migrations: a routine is written against the schema
it will run on. In a Go service:

```go
if err := migrateTables(ctx, db); err != nil { // goose
return err
}
p, err := funchose.NewProvider(db, routines, funchose.WithBuild(build))
...
```

See [Go library](go-library.md) for the full example, and
[Operations](operations.md#schema-changes-next-to-goose) for changes that remove a
column a routine reads.

---

[← How it works](how-it-works.md) · [Contents](../README.md#documentation) · [Routine files →](routine-files.md)
