[funchose](../README.md) › Go library

# Go library

- [Next to goose](#next-to-goose)
- [At start or in a deploy job](#at-start-or-in-a-deploy-job)
- [Options](#options)
- [Errors](#errors)
- [The report and metrics](#the-report-and-metrics)

```
go get github.com/smgladkovskiy/funchose
```

The library only needs `database/sql` and a PostgreSQL driver (pgx, lib/pq).
PostgreSQL 14 or newer.

## Next to goose

```go
import (
    "context"
    "database/sql"
    "embed"
    "fmt"
    "io/fs"
    "strconv"
    
    "github.com/pressly/goose/v3"
    "github.com/smgladkovskiy/funchose"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed routines
var routines embed.FS

// build is set at link time: go build -ldflags "-X main.build=$CI_PIPELINE_IID"
var build string

func migrate(ctx context.Context, db *sql.DB) error {
    n, err := strconv.ParseInt(build, 10, 64)
    if err != nil {
    return fmt.Errorf("build %q: set it at link time: %w", build, err)
}

// Tables first: routines are written against the schema they will run on.
tables, err := fs.Sub(migrations, "migrations")
if err != nil {
    return err
}
migrator, err := goose.NewProvider(goose.DialectPostgres, db, tables)
if err != nil {
    return err
}
if _, err := migrator.Up(ctx); err != nil {
    return err
}

files, err := fs.Sub(routines, "routines")
if err != nil {
    return err
}
p, err := funchose.NewProvider(db, files, funchose.WithBuild(n))
if err != nil {
    return err // a routine file failed the checks
}
_, err = p.Up(ctx)
    return err
}
```

Fail when the build number is missing, as above: a release without one would
switch off the protection against old pods.

## At start or in a deploy job

|                           | At application start                                                 | In a deploy job                                        |
|---------------------------|----------------------------------------------------------------------|--------------------------------------------------------|
| runs                      | on every pod start                                                   | once per deploy                                        |
| old pods during a rollout | stopped by the build number                                          | do not run funchose at all                             |
| rights                    | the service user needs DDL rights and must own the routines          | a deploy user has them, the service does not           |
| build number              | needs `-ldflags` in the image                                        | `$CI_PIPELINE_IID` in the job                          |
| a broken routine file     | the new pods fail to start, the old ones keep serving                | the job fails, nothing is deployed                     |
| deliberate rollback       | redeploy the old release with `WithAllowDowngrade(true)` from config | rerun the old job with `FUNCHOSE_ALLOW_DOWNGRADE=true` |

Prefer the deploy job: one run per deploy, and the service keeps its rights to
data. Run at start only where the service already runs its own migrations. Pod
starts that find nothing to do do not take the lock and do not wait for each
other. Leave `WithAllowDowngrade` off at start unless rollbacks really go through
redeploying images: with it on, every restart of an old pod is a rollback.

## Options

| Option                     | Default                     |                                                                                                             |
|----------------------------|-----------------------------|-------------------------------------------------------------------------------------------------------------|
| `WithBuild(n)`             |                             | the build being deployed; `Up` and `Plan` need it or `WithLocal`                                            |
| `WithLocal()`              |                             | run as build 0, for a developer database                                                                    |
| `WithTable(name)`          | `<schema>.funchose_history` | history table as `schema.name`; required when the files cover several schemas                               |
| `WithAllowDowngrade(bool)` | false                       | let an older build roll routines back                                                                       |
| `WithAllowDrop(bool)`      | false                       | drop routines funchose cannot prove unused: created by a rolled-back build, or retired with uncounted calls |
| `WithOverwriteDrift(bool)` | false                       | replace definitions funchose did not apply                                                                  |
| `WithReplicas(bool)`       | false                       | routines are also called on read replicas                                                                   |
| `WithRetireLimit(n)`       | 10                          | most routines one run may retire; 0 for no limit                                                            |
| `WithRemovedSchemas(...)`  |                             | schemas whose directory was removed on purpose                                                              |
| `WithFailOn(...)`          | none                        | warnings that fail `Up` and `Plan`: `ActionDrift`, `ActionStale`, `ActionBlocked`, `ActionKept`             |
| `WithLockTimeout(d)`       | 5m                          | wait for another run on the same table; 0 waits forever                                                     |
| `WithLogger(l)`            | `slog.Default()`            | `nil` turns logging off                                                                                     |

## Errors

Errors wrap one of these, test them with `errors.Is`:

| Error                 | Meaning                                                        |
|-----------------------|----------------------------------------------------------------|
| `ErrInvalidFiles`     | a routine file breaks the rules; the error lists every problem |
| `ErrInvalidOption`    | options that contradict each other or a value out of range     |
| `ErrTableRequired`    | the files cover several schemas or none: set the table         |
| `ErrBuildRequired`    | `Up` or `Plan` without a build and without `WithLocal`         |
| `ErrLockTimeout`      | another run held the lock longer than the lock timeout         |
| `ErrMissingSchema`    | a schema directory has no schema in the database               |
| `ErrSchemaWithoutDir` | the history tracks routines in a schema the files do not cover |
| `ErrRetireLimit`      | the run would retire more routines than allowed                |
| `ErrOverload`         | a new argument list added an overload                          |
| `ErrDrift`            | the run would overwrite a definition funchose did not apply    |
| `ErrFailOn`           | a warning named in `WithFailOn`                                |

What each one means for the person on call is in [Operations](operations.md).

## The report and metrics

`Up`, `Plan` and `Status` return a `Report`: the build, the history table and a
`Change` for every routine with its action, versions and builds before and
after, whether the definition changed, a note, and for `Plan` a diff. `Up` logs
every change that is not `unchanged` with `log/slog`, warnings at the warning
level.

The report turns into metrics without funchose depending on a metrics library:

```go
rep, err := p.Up(ctx)
for _, change := range rep.Changes {
    routineActions.WithLabelValues(string(change.Action)).Inc()
}
warnings.Set(float64(len(rep.Warnings())))
```

Alert on `drift`, `stale`, `blocked` and `kept`: each one needs a person.
