[funchose](../README.md) › Builds and rollbacks

# Builds and rollbacks

- [Builds](#builds)
- [Rolling back](#rolling-back)
- [Changes made by hand](#changes-made-by-hand)

## Builds

funchose needs to know which release is running, so every `up` and `plan` gets a
build number: a number that grows with every release, such as `$CI_PIPELINE_IID`
in GitLab or `$GITHUB_RUN_NUMBER` in GitHub Actions. It is written to the history
next to every version it applied.

The build is what tells a new release from an old one. A rollout replaces pods
one by one, and in a service deployed to several clusters one cluster may run the
previous release for an hour while another already runs the new one. If a pod of
the previous release restarts in that hour, it must not roll the functions back
under the new code. So funchose applies a file only if its build is newer than
every build the history has seen for that routine, or is the build that applied
the current version. Anything else is `stale` and left alone:

```
$ funchose up -build 41
funchose up: build 41 on billing.funchose_history, 0 applied, 2 unchanged, 1 warning

  stale     billing.invoice_total  a7ad2cd9d6ad (build 42)
                                   build 42 applied a newer version, this is build 41: not rolled back

funchose: a routine needs attention: stale billing.invoice_total
```

In a deploy job a stale routine fails the run (`-fail-on stale`, the default of
the command line): the job of an older pipeline ran again, or a branch was
deployed to the same database with a newer pipeline. Code that runs funchose at
application start does not fail on it, because pods of the previous release are
stale by design.

**A build number is required.** `up` and `plan` refuse to run without one, and the
command line refuses a `FUNCHOSE_BUILD` that is not a number: a missing CI
variable must not quietly turn every release into the same build and switch the
protection off. For a developer database, `-local` (`WithLocal`) runs as build 0,
which is older than any real build, so a local run against a shared database
never overwrites what CI applied.

## Rolling back

There are two ways back, and they do different things.

**Revert the commit.** The file goes back to its old content, and the next build
applies it as an ordinary `update`. This is the normal way: the rollback is
reviewed, tested and recorded like any other change.

**Deploy the older release on purpose** with `-allow-downgrade`
(`WithAllowDowngrade`). The older build puts back its versions of the routines it
has files for, and the history records of newer builds are marked as rolled back,
not deleted, so the history stays a log of what happened:

```
$ funchose up -build 41 -allow-downgrade
funchose up: build 41 on billing.funchose_history, 1 applied, 2 unchanged

  rollback  billing.invoice_total  a7ad2cd9d6ad (build 42) -> 4f20431699a2 (build 41)
```

After that, build 42 counts as rolled back: if one of its pods restarts, it is
`stale` and does not undo the rollback. Build 43 moves forward as usual.

Routines that the older build has no file for are handled from the history:

| The newer build                                | Rolling back to the older one                                                                                                                        |
|------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| changed a routine the older build also has     | puts back the older build's file                                                                                                                     |
| adopted a routine that existed before funchose | puts back the definition it had before adoption and stops tracking it                                                                                |
| created a routine that did not exist before    | would have to drop it. Not done unless `-allow-drop` is given: code of the newer release may still call it. Reported as `blocked`, the routine stays |

**In an emergency** put a single routine back by hand, without waiting for a
pipeline. The definition every record replaced is in `definition_before`, ready
to run:

```sql
SELECT definition_before
FROM billing.funchose_history
WHERE schema_name = 'billing'
  AND routine_name = 'invoice_total'
ORDER BY id DESC
LIMIT 1;
-- run what it returns, then revert the commit
```

Until the commit is reverted, funchose sees the hand-made change and reports
`drift` instead of fighting it.

## Changes made by hand

Someone fixes a function on production at 3 a.m. funchose notices on the next
run, because the fingerprint of the routine no longer matches the one it
recorded:

- if the file did not change, the routine is left as it is and reported as
  `drift`;
- if the file changed and would overwrite the hand-made change, the run fails (`ErrDrift`) with an error that names the
  routine. `funchose plan` shows the
  diff; bring the fix into the file, or decide to drop it and run again with
  `-overwrite-drift`;
- if the new file contains the same change, there is nothing to overwrite and the
  run goes through.

The same rule protects routines funchose has never seen: adopting a file that
differs from the routine in the database needs `-overwrite-drift` too.
