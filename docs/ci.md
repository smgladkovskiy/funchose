[funchose](../README.md) › CI

# CI

- [GitLab](#gitlab)
- [GitHub Actions](#github-actions)
- [Rolling back from CI](#rolling-back-from-ci)

Pin the version of funchose: the deploy job runs with a user that may replace
every routine of the database. Build the binary once, in an image or a cache,
instead of `go run ...@latest` in every job.

## GitLab

Checks in merge requests and a deploy job:

```yaml
variables:
  FUNCHOSE_VERSION: v0.1.0

.funchose:
  image: golang:1.26
  before_script:
    - go install github.com/smgladkovskiy/funchose/cmd/funchose@$FUNCHOSE_VERSION

routines lint:
  extends: .funchose
  stage: test
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  script:
    - git fetch --depth=1 origin $CI_MERGE_REQUEST_TARGET_BRANCH_NAME $CI_MERGE_REQUEST_DIFF_BASE_SHA
    - funchose lint

routines:
  extends: .funchose
  stage: deploy
  variables:
    FUNCHOSE_BUILD: $CI_PIPELINE_IID
    FUNCHOSE_TABLE: billing.funchose_history
  script:
    - funchose plan
    - funchose up
```

`FUNCHOSE_DSN` comes from a protected CI variable or your secret store. `plan`
before `up` puts the diff of every routine into the job log. Both fail on a
`stale` routine (the default `-fail-on`): a newer pipeline already deployed to
this database.

## GitHub Actions

```yaml
on: pull_request
jobs:
  routines:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0
          ref: ${{ github.event.pull_request.head.sha }}
      - uses: actions/setup-go@v7
        with:
          go-version: "1.26"
      - run: go install github.com/smgladkovskiy/funchose/cmd/funchose@v0.1.0
      - run: funchose lint -base origin/$GITHUB_BASE_REF
```

`lint` compares the branch with the target, so it needs the branch itself, not
the merge commit GitHub checks out for a pull request by default: hence `ref`.

## Rolling back from CI

| Goal                                                                         | How                                                                                   |
|------------------------------------------------------------------------------|---------------------------------------------------------------------------------------|
| undo a change                                                                | revert the commit; the next pipeline applies the old file                             |
| go back with an older image                                                  | rerun the deploy job of the older pipeline with `FUNCHOSE_ALLOW_DOWNGRADE=true`       |
| the older release did not have a routine a newer one created                 | add `FUNCHOSE_ALLOW_DROP=true`, once you know nothing calls it                        |
| a newer pipeline is stale because a branch was deployed to the same database | run the newest pipeline again, or deploy it once with `FUNCHOSE_ALLOW_DOWNGRADE=true` |

---

[← Command line](cli.md) · [Contents](../README.md#documentation) · [Operations →](operations.md)
