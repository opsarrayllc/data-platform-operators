# GA4 loader

A batch job that copies Google Analytics 4 reports from the
[Data API](https://developers.google.com/analytics/devguides/reporting/data/v1)
into Iceberg tables in a LakeKeeper warehouse. It runs as an Argo Workflow
from `data-orchestration-operator`.

Each configured report becomes one table, `<namespace>.<report>`. The table
holds every configured property, with one row per `property_id`, `date` and
dimension combination. Tables are partitioned by `property_id` and year.

A run replaces one property's rows for exactly the days it loaded, in a
single Iceberg commit, so any run can be repeated safely:

```sh
python -m ga4_loader daily                                 # last LOOKBACK_DAYS days (default 3)
python -m ga4_loader backfill --from 2021-01-01            # through yesterday
python -m ga4_loader backfill --from 2023-01-01 --to 2023-12-31 --report pages --property 123456789
```

`daily` reloads the last three days, because GA4 keeps revising a day's
numbers for about 72 hours. `backfill` makes one request per 12 months of a
report (`--chunk-months`).

The GA4 property daily quota is 200,000 tokens on standard properties. A
backfill stops with exit code 2 before that quota runs out, and logs the
`--from` date to resume with. When the hourly quota runs low, the loader
waits for it to refill.

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `GA4_REPORTS_FILE` | `/etc/ga4/reports.yaml` | Report definitions; see below |
| `GA4_PROPERTIES_FILE` | `/etc/ga4/properties.yaml` | List of `{id, name}`, numeric GA4 property ids |
| `GOOGLE_APPLICATION_CREDENTIALS` | | Service-account key with Viewer on each property |
| `LAKEKEEPER_CATALOG_URL` | | e.g. `http://lakekeeper.lakekeeper.svc:8181/catalog` |
| `LAKEKEEPER_WAREHOUSE` | | Warehouse name |
| `LAKEKEEPER_TOKEN_FILE` | `/var/run/secrets/lakekeeper/token` | Projected ServiceAccount token, audience `lakekeeper` |
| `ICEBERG_NAMESPACE` | `google_analytics` | |
| `LOOKBACK_DAYS` | `3` | `daily` mode |

Example `reports.yaml`:

```yaml
reports:
  - name: traffic_acquisition
    dimensions: [date, sessionSource, sessionMedium]
    metrics: [sessions, totalUsers, keyEvents]
```

The loader refuses a configuration that breaks these rules:
- Every report includes the `date` dimension.
- A report has at most 9 dimensions and between 1 and 10 metrics.
- No dimension that carries a query string or a full URL (`pageLocation`,
  `pagePathPlusQueryString`, `landingPagePlusQueryString`, `fullPageUrl`,
  `pageReferrer`). These are where emails, tokens and order ids end up.
- No `customUser:*` dimensions.

As a second safeguard, the loader strips any query string left in path
dimensions before writing.

Columns are the API names in snake_case. Each table also has
`subject_to_thresholding` and `sampled` columns, which record whether GA4
withheld or estimated the rows, and a `loaded_at` column.

When a configured report gains a metric or dimension, its table gains a
column. Rows loaded before the change read that column as null.

## LakeKeeper access

The loader signs in with its pod's ServiceAccount token. On every run, it
registers itself as a LakeKeeper application user, with the id
`kubernetes~system:serviceaccount:<namespace>:<serviceaccount>`.

A LakeKeeper admin then grants that user **ownership** on the target
namespace, once. This can be done in the UI (warehouse → namespace →
Permissions), or with:

```sh
POST /management/v1/permissions/namespace/{namespace_id}/assignments
{"writes": [{"type": "ownership", "user": "kubernetes~system:serviceaccount:argo:ga4-loader"}]}
```

If the namespace does not exist yet, create it first, or grant `create` on
the warehouse. S3 access is vended by LakeKeeper, so the pod needs no AWS
identity.

## Tests

```sh
docker build -t ga4-loader:test .
docker run --rm --user 0 --entrypoint sh -v "$PWD/tests:/app/tests:ro" ga4-loader:test -c \
  'pip install -q "pyiceberg[sql-sqlite]==0.12.0" pytest==8.4.2 && python -m pytest -q tests'
```
