# Change data capture with Debezium

`spec.debezium` streams row changes from operational databases into the
warehouse. Each source runs [Debezium Server](https://debezium.io/documentation/reference/stable/operations/debezium-server.html)
with the community [Iceberg sink](https://github.com/memiiso/debezium-server-iceberg),
which writes Iceberg tables through the LakeKeeper REST catalog. Kafka isn't needed.

```yaml
spec:
  debezium:
    sources:
      - name: orders
        connector: mysql            # mysql, mariadb, postgresql, sqlserver
        host: orders.cluster-abc.us-east-1.rds.amazonaws.com
        database: orders            # optional for mysql/mariadb
        tableIncludeList:
          - orders.orders
          - orders.line_items
        credentialsSecretRef:
          name: debezium-orders     # keys: username, password
          namespace: lakekeeper     # defaults to the Debezium namespace
```

## What the operator creates

All of the following go in `spec.debezium.namespace`, which defaults to `debezium`:

- **The `debezium` ServiceAccount.** All sources share it, and it is their LakeKeeper identity.
- **`debezium-<name>`.** A single-replica Deployment with the `Recreate` strategy, so two engines never read one source at the same time.
- **`debezium-<name>-config`.** A Secret holding `application.properties`. Use `extraProperties` to override any generated key.
- **`debezium-<name>-credentials`.** A copy of the source login. The pod reads it into environment variables, and the properties file only refers to them.
- **The `debezium-maintenance` ConfigMap.** The snapshot expiry program that every pod mounts (see [Table maintenance](#table-maintenance)).
- **The `debezium-compaction` CronJob and `debezium-trino-ca` ConfigMap.** Compaction of the destination tables through Trino, and a copy of Trino's CA for it. Only with Trino and `spec.auth.serviceAccountTokens`.

The `DebeziumReady` condition reports progress. Debezium waits until `WarehouseReady` is true.

Removing a source deletes its Deployment and Secrets. It leaves the source's replication slot and publication, and the Iceberg tables, in place.

## Identity

| `spec.auth` | Debezium authenticates as |
| --- | --- |
| Embedded Keycloak | The `debezium` confidential client (client credentials) |
| `serviceAccountTokens` (any issuer) | `kubernetes~system:serviceaccount:debezium:debezium`, through the same token sidecar as Trino |
| External issuer without `serviceAccountTokens` | Not supported: the operator can't predict the principal to grant |
| Auth disabled | No authentication |

Once the first source exists, the principal gets `project_admin` on the default project. It needs to create namespaces and tables. The grant isn't revoked when the last source is removed.

## Tables

- **Destination namespace.** Tables go to `icebergNamespace`, which defaults to the source name with `-` replaced by `_`. The namespace is created if it is missing.
- **Table names.** A table is named `<tablePrefix><topic prefix>_<database or schema>_<table>`. The topic prefix is the source name with `-` replaced by `_`. For example, `orders.line_items` becomes `orders_orders_line_items`.
- **Upsert.** By default (`upsert: true`) a table keeps one row per primary key. A deleted row stays, with `__deleted = true`. Tables without a primary key are append-only.
- **Metadata columns.** Every row has `__op`, `__table`, `__source_ts_ns`, `__db` and `__ts_ms`.
- **Offsets and schema history.** Debezium keeps these in `_debezium_offset_storage_<prefix>` and `_debezium_schema_history_<prefix>` in the same namespace, so the pods hold no state. Deleting those tables makes the source snapshot again.

## Table maintenance

Debezium saves its position by overwriting the offset table, which adds an
Iceberg snapshot on every save (at least once a minute with heartbeats). The
sink adds a snapshot and a small data file to a destination table on every
commit, too. Nothing in Debezium expires snapshots or merges files. Every
commit gets slower as table metadata grows, and every read gets slower as
small files pile up.

The offset table is where this hurts first. When a save takes longer than
`offset.flush.timeout.ms` (5 seconds by default), the engine skips confirming
the position to the source. On PostgreSQL the replication slot then stops
advancing, even though the stored offset keeps moving. WAL builds up until
`max_slot_wal_keep_size` invalidates the slot, and the source has to be
snapshotted again.

### Snapshot expiry

Each pod runs a `snapshot-expiry` container next to the engine. It uses the
engine's image, configuration, and LakeKeeper identity. Every 30 minutes it
expires snapshots of every table in the source's namespace, keeping at least
the last 10 of each, and deletes the files only the expired snapshots used:

- **The offset table** keeps an hour.
- **Destination and schema history tables** keep
  `spec.debezium.maintenance.snapshotRetention` (default `24h`), for time
  travel. Every day kept is about 1,400 snapshots per busy table.

Details:

- **Start.** The first run waits two minutes plus up to 15 more, picked from the
  source name, so pods restarted together don't hit the catalog at once.
- **Batches.** Each commit removes at most 1,000 snapshots, oldest first. A table
  that was never expired can hold tens of thousands; removing them in one
  commit holds catalog transactions for minutes and makes the catalog fail
  other commits.
- **Failures.** A failed table is logged and retried on the next tick; the others
  still run, and the engine never stops.
- **Logs.** `kubectl logs -n debezium deploy/debezium-<name> -c snapshot-expiry`
  prints a line per table that had snapshots to expire:
  `Expired N snapshot(s) of <table> in C commit(s), <ms> ms; M remain`.

### Compaction

The `debezium-compaction` CronJob runs Trino's `optimize` on every table in the
sources' namespaces, every six hours by default. It then runs
`expire_snapshots` and `remove_orphan_files` at Trino's 7-day minimum, which
mostly finds nothing after the sidecar. It uses the Transformation runner
image's maintenance mode (see [`transformations.md`](transformations.md)) and
signs in to Trino as the `debezium` ServiceAccount, so it needs Trino and
`spec.auth.serviceAccountTokens`. Without them it isn't created.

- **Concurrent commits.** Optimize keeps the rewritten rows' sequence numbers, so
  equality deletes the sink commits while it runs still apply to them. A
  commit that does conflict fails that table, and the next run retries it.
- **Load.** The first run on tables that were never compacted reads them in
  full. Schedule it when Trino is quiet.
- **Logs.** `kubectl logs -n debezium job/<job>` prints `maintained <ns>.<table>`
  per table, and the error for any that failed.

```yaml
spec:
  debezium:
    maintenance:
      snapshotRetention: 24h
      compaction:
        schedule: "0 */6 * * *"     # UTC
        fileSizeThreshold: 128MB
        # enabled: false
```

## Object storage

| Warehouse storage | How the sink reaches S3 |
| --- | --- |
| `stsEnabled: true` | Table-scoped credentials vended by LakeKeeper (`X-Iceberg-Access-Delegation: vended-credentials`) |
| Static keys or embedded MinIO | The warehouse keys, the same ones Trino gets |
| `systemIdentity` without STS | The pod's AWS credential chain. Associate the `debezium` ServiceAccount with a role that can write the bucket |

The pods must be able to reach the bucket. With the AWS Terraform module, that
means they have to run in the VPC that has the S3 gateway endpoint.

## Preparing the source database

### MySQL and Aurora MySQL

- Turn on binary logging with `binlog_format=ROW` and `binlog_row_image=FULL`. On Aurora, set these in the cluster parameter group, then reboot the writer.
- Keep binlogs long enough to cover an outage. For example, on RDS or Aurora: `CALL mysql.rds_set_configuration('binlog retention hours', 72);`
- Create the replication user:

  ```sql
  CREATE USER 'debezium'@'%' IDENTIFIED BY '...';
  GRANT SELECT, RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'debezium'@'%';
  ```

  On Aurora and RDS, `RELOAD` alone won't let Debezium take the global read lock. The connector falls back to table-level locks, which also needs `LOCK TABLES`.
- `serverID` defaults to a value derived from the source name, between 5400 and 105399. Set it if that collides with another replica or binlog client.

MariaDB uses the same settings, with `connector: mariadb`.

### PostgreSQL and Aurora PostgreSQL

- Turn on logical decoding: `rds.logical_replication = 1` on RDS and Aurora, or `wal_level = logical` elsewhere. This needs a restart.
- Create a user with replication rights that owns the captured tables, or that is a member of the role that owns them:

  ```sql
  CREATE ROLE debezium WITH LOGIN PASSWORD '...';
  GRANT rds_replication TO debezium;      -- RDS/Aurora; elsewhere: ALTER ROLE debezium REPLICATION;
  GRANT SELECT ON ALL TABLES IN SCHEMA public TO debezium;
  ```

- The operator names the slot and publication `debezium_<prefix>`, uses `pgoutput`, and creates the publication with `publication.autocreate.mode=filtered`. That mode needs table ownership, not superuser.
- Watch the slot's retained WAL (`pg_replication_slots`). A stopped or removed source keeps its slot, and WAL builds up until you drop the slot. Also watch `Flush of the offsets failed` in the engine's logs: while saves time out, the slot isn't confirmed (see [Table maintenance](#table-maintenance)).

### SQL Server

Turn on CDC for the database (`sys.sp_cdc_enable_db`) and for each captured
table (`sys.sp_cdc_enable_table`), and make sure SQL Server Agent is running.

## Known gaps

- Verified with envtest only. The rendered configuration hasn't yet been run against a live LakeKeeper. In particular, vended credentials haven't been exercised with this sink.
- `project_admin` is broader than Debezium needs. Narrow it once grants can target a warehouse or namespace.
- One Deployment per source. There's no sharding of large sources.
