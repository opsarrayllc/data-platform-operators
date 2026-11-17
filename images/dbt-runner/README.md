# dbt runner

The container that runs a `Transformation`'s dbt project on Trino. The
data-orchestration-operator schedules it from Argo CronWorkflows and sets
every input through the environment; [`docs/transformations.md`](../../docs/transformations.md)
is the contract.

```sh
python -m dbt_runner run         # clone the project, run the lane's dbt command
python -m dbt_runner on-change   # the same, only when a watched table has new data
python -m dbt_runner maintain    # optimize and expire snapshots in MAINTAIN_NAMESPACES
```

Exit codes: 0 success (including an on-change poll that found nothing new),
1 dbt or maintenance failed, 2 bad configuration.

## How it works

- **Project:** a shallow fetch of `PROJECT_GIT_REF` (branch, tag or commit)
  into `/work/project`. A token in `PROJECT_GIT_TOKEN` is sent as an HTTP
  Basic header (`PROJECT_GIT_USERNAME:token`, user `x-access-token` by
  default, which suits GitHub), never in the URL. `dbt deps` runs when the
  project has `packages.yml` or `dependencies.yml`, so the pod needs egress
  to the dbt package hub or the packages' Git hosts.
- **Profile:** written to `/work/profiles/profiles.yml` under the profile name
  in `dbt_project.yml`, with `method: jwt`, the catalog as `database` and the
  CA file as `cert`. Set `DBT_PROFILES_DIR` to use a profiles directory
  shipped in the project instead.
- **Trino sign-in:** dbt runs in-process, and the runner swaps dbt-trino's JWT
  authentication for one that reads `TRINO_TOKEN_FILE` on every request. The
  kubelet rotates the projected token hourly, and long runs outlive one
  token. No user is sent; Trino takes the principal from the token.
- **on-change:** for every watched table, the newest snapshot that is not a
  `replace` (compaction rewrites files without changing data) is compared
  with `<lane>.json` in the state ConfigMap. The snapshots are read before
  dbt runs and recorded only after it succeeds. A failed run is retried on
  the next tick, and a commit that lands during a run triggers the next one.
  Don't watch a table the lane itself writes, or the lane runs on every tick.
- **maintain:** `optimize`, `expire_snapshots` and `remove_orphan_files` on
  every table in each namespace. A failing table is logged and the rest
  carry on. Trino rejects retentions below the catalog's minimum (7 days by
  default).

## Optional variables

Not set by the operator; add them with the Transformation's `spec.env`.

| Variable | Default | |
| --- | --- | --- |
| `DBT_SCHEMA` | Transformation name, `-` → `_` | The profile's default schema |
| `DBT_THREADS` | `4` | |
| `DBT_PROFILES_DIR` | | Use the project's own profiles |
| `PROJECT_GIT_USERNAME` | `x-access-token` | User for `PROJECT_GIT_TOKEN`; `oauth2` for GitLab |
| `LOG_LEVEL` | `INFO` | |

## Tests

```sh
docker build -t dbt-runner:test .
docker run --rm --user 0 --entrypoint sh -v "$PWD/tests:/app/tests:ro" dbt-runner:test -c \
  'pip install -q pytest==8.4.2 && python -m pytest -q tests'
```

The tests fake Trino and the Kubernetes API, and run `dbt deps` and `dbt
parse` against a local Git repository to check the generated profile loads.
