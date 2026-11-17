# Transformations

A `Transformation` (data-orchestration-operator) runs a dbt project on Trino through Argo Workflows. It splits the project into **lanes**, each running its own dbt selection on its own schedule. A fast lane can follow replication closely, while heavy models run less often.

The runner image is `ghcr.io/opsarrayllc/data-platform-dbt-runner`, built from [`images/dbt-runner`](../images/dbt-runner/README.md).

## Example

```yaml
apiVersion: dataplatform.opsarray.io/v1alpha1
kind: Transformation
metadata:
  name: keyme
spec:
  orchestrationRef: local          # defaults to the only Orchestration
  project:
    git:
      url: https://github.com/example/dbt.git
      ref: main
      path: ""                     # directory holding dbt_project.yml
      credentialsSecretRef:        # Secret in the Orchestration namespace
        name: dbt-git
        key: token
  lanes:
    - name: live                   # runs soon after replication writes
      select: tag:live
      onChange:
        sources: [atlantis, lks]   # Iceberg namespaces or ns.table
    - name: hourly
      select: tag:hourly
      schedule: "5 * * * *"
    - name: daily
      command: build               # run | build | test | snapshot | seed
      select: tag:daily
      schedule: "30 6 * * *"
      timezone: America/New_York
  maintenance:
    namespaces: [luigi_transformations_rebuilt, luigi_transformations_staging]
```

## What the operator creates

Everything lands in the Orchestration's namespace (`status.namespace`):

| Object | Name | Purpose |
|---|---|---|
| ServiceAccount | `transform-<name>` | The runner's identity in Trino and LakeKeeper |
| Role and RoleBinding | `transform-<name>` | Lets the account write Argo task results and update only its own state ConfigMap |
| ConfigMap | `transform-<name>-state` | Where `onChange` lanes record the snapshots they last processed. The runner owns its contents. |
| ConfigMap | `transform-<name>-trino-ca` | Copy of the CA that signs Trino's certificate (`spec.trino.caConfigMapRef`, default `trino/trino-ca`) |
| CronWorkflow | `<name>-<lane>` | One per lane |
| CronWorkflow | `<name>-maintenance` | When `spec.maintenance` is set |

**CronWorkflow settings:**
- `concurrencyPolicy: Forbid`. A tick that finds the previous run still going is skipped, so a slow run never piles up behind a one-minute poll.
- Lanes run independently of each other. Keep their selections disjoint so no two lanes write the same table. When maintenance and a lane commit to the same table at once, Iceberg's optimistic commits keep the table consistent; one side retries or fails, and the next run catches up.
- Successful pods are deleted at once, and successful workflows after an hour. Failed workflows are kept for a week.

**Removing things:** removing a lane deletes its CronWorkflow. Setting `suspend` on the Transformation, a lane, or maintenance pauses it without deleting anything.

**Name length:** CronWorkflow names may be at most 52 characters, because Argo appends a suffix to each Workflow. `<name>-<lane>` must fit, and `maintenance` is not allowed as a lane name.

## Access

The runner signs in to Trino with a projected ServiceAccount token, audience `spec.trino.tokenAudience` (default `lakekeeper`). Trino accepts these tokens only when the DataLake has `spec.auth.serviceAccountTokens` set; see [`jumpcloud.md`](jumpcloud.md).

**Writing:** grant the account the namespaces it writes:

```yaml
spec:
  auth:
    serviceAccountTokens:
      grants:
        - namespace: argo            # status.namespace
          name: transform-keyme      # transform-<name>
          icebergNamespaces: [luigi_transformations_rebuilt, luigi_transformations_staging]
```

**Reading:** the account also needs read access to its source namespaces. The DataLake can't grant read-only access yet.

## Runner contract

The container gets one argument, its mode, and these environment variables:

| Variable | Modes | Value |
|---|---|---|
| `TRANSFORM_NAME`, `TRANSFORM_LANE`, `TRANSFORM_MODE` | all | Identify the run |
| `TRANSFORM_NAMESPACE`, `TRANSFORM_STATE_CONFIGMAP` | all | Where the state ConfigMap lives |
| `TRINO_HOST`, `TRINO_PORT`, `TRINO_CATALOG` | all | From `spec.trino` |
| `TRINO_CA_FILE` | all | `/etc/trino-ca/ca.crt` |
| `TRINO_TOKEN_FILE` | all | `/var/run/secrets/opsarray/token`. The kubelet rotates it; re-read it before each connection. |
| `PROJECT_GIT_URL`, `PROJECT_GIT_REF`, `PROJECT_PATH` | all | From `spec.project.git` |
| `PROJECT_GIT_TOKEN` | all | From `credentialsSecretRef`, when set |
| `DBT_TARGET`, `DBT_VARS` | all | `spec.target` (default `prod`), and `spec.vars` as JSON |
| `DBT_COMMAND`, `DBT_SELECT` | `run`, `on-change` | The lane's dbt command and selection |
| `DBT_EXCLUDE`, `DBT_FULL_REFRESH` | `run`, `on-change` | Set when the lane sets them |
| `WATCH_SOURCES` | `on-change` | Comma-separated namespaces or tables, sorted |
| `MAINTAIN_NAMESPACES` | `maintain` | Comma-separated namespaces |
| `MAINTAIN_FILE_SIZE_THRESHOLD` | `maintain` | Default `128MB` |
| `MAINTAIN_SNAPSHOT_RETENTION` | `maintain` | Default `7d` |
| `HOME` | all | `/work`, a writable emptyDir. `/tmp` is writable too; the root filesystem is read-only. |

`spec.env` is appended after these, so it can override them. It can also set the runner's optional variables (`DBT_SCHEMA`, `DBT_THREADS`, `DBT_PROFILES_DIR`, `PROJECT_GIT_USERNAME`, `LOG_LEVEL`); see the [runner README](../images/dbt-runner/README.md).

**Modes:**
- **`run`:** clone the project, run `dbt deps` if it has packages, then `dbt <command> --select … [--exclude …] [--full-refresh] --target … --vars …`.
- **`on-change`:**
  1. For each watched table, read its newest snapshot via `"<table>$snapshots"`, ignoring `replace` snapshots from compaction. A namespace means every table in it.
  2. Compare with `<lane>.json` in the state ConfigMap. If nothing changed, exit 0 without running dbt.
  3. Otherwise run as in `run` mode. Record the snapshots only after dbt succeeds, so a failed run is retried on the next tick.

  Don't watch a table the lane itself writes, or the lane runs on every tick.
- **`maintain`:** for every table in each namespace, run `ALTER TABLE … EXECUTE optimize(file_size_threshold => …)`, `expire_snapshots(retention_threshold => …)` and `remove_orphan_files(retention_threshold => …)`.

The runner runs as uid 65532.

## Status

`kubectl get transformations` shows `Ready` and the workflow namespace. `status.lanes` copies each CronWorkflow's last scheduled time, succeeded and failed counts, and active runs. It is refreshed every minute.
