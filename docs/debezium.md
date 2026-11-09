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
- Watch the slot's retained WAL (`pg_replication_slots`). A stopped or removed source keeps its slot, and WAL builds up until you drop the slot.

### SQL Server

Turn on CDC for the database (`sys.sp_cdc_enable_db`) and for each captured
table (`sys.sp_cdc_enable_table`), and make sure SQL Server Agent is running.

## Known gaps

- Verified with envtest only. The rendered configuration hasn't yet been run against a live LakeKeeper. In particular, vended credentials haven't been exercised with this sink.
- `project_admin` is broader than Debezium needs. Narrow it once grants can target a warehouse or namespace.
- One Deployment per source. There's no sharding of large sources.
