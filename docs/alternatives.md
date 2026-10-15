[funchose](../README.md) › Alternatives

# Alternatives

- **Flyway repeatable migrations** (`R__name.sql`) and **Liquibase `runOnChange`**
  use the same idea: rerun a file when its checksum changes. They are JVM tools
  with their own migration history, so with goose you end up with two migrators,
  and Flyway's undo needs a paid edition. Neither knows which build applied a
  version, so neither protects against a restarting old pod, and neither notices
  hand-made changes.
- **Sqitch `rework`** keeps one deploy script per change and generates the revert
  from the previous version in git. Elegant, but it is a full migration system
  with its own plan file and registry.
- **Graphile Migrate** and **tern code packages** keep functions as files and copy
  them into immutable migrations. The repository still grows by a full copy per
  change.
- **Atlas** supports functions in its Pro edition, **pgschema** and
  **pgCodeKeeper** diff the whole schema declaratively. They want to own the
  tables too, which conflicts with an existing migration tool.

---

[← Operations](operations.md) · [Contents](../README.md#documentation)
