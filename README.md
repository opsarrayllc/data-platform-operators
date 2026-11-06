# Data Platform Operator

Kubernetes operator that deploys the resources needed to bootstrap a data platform.

Goals:
* OIDC immediately
  * Keycloak locally
  * OIDC Configuration overrides
* Fine-Grained Authorization immediately
  * OpenFGA
* Runs on as many cloud platforms as we can reasonably test on
* Open Source Utilities
* Cost should be negligible and scale appropriately

Built with [Kubebuilder](https://book.kubebuilder.io) v4 / controller-runtime.

## API

| Group | Version | Kind | Operator |
| --- | --- | --- | --- |
| `dataplatform.opsarray.io` | `v1alpha1` | `DataLake` | this module |
| `dataplatform.opsarray.io` | `v1alpha1` | `Analytics` | [`data-analytics-operator`](data-analytics-operator) |
| `dataplatform.opsarray.io` | `v1alpha1` | `Orchestration` | [`data-orchestration-operator`](data-orchestration-operator) |

`DataLake` deploys the lake (object storage, catalog, Trino, and identity).
`Analytics` deploys Superset against that Trino. Its type lives in
`data-analytics-operator/api/v1alpha1/analytics_types.go`.
`Orchestration` deploys Argo Workflows. Its type lives in
`data-orchestration-operator/api/v1alpha1/orchestration_types.go`.

## Prerequisites

- Go 1.26+
- Docker (or set `CONTAINER_TOOL` to another OCI builder)
- `kubectl` and access to a cluster ([kind](https://kind.sigs.k8s.io/) is fine for local work)

All other tooling (`controller-gen`, `kustomize`, `setup-envtest`, `golangci-lint`) is
downloaded into `./bin` automatically by the Makefile. That directory is gitignored.

## Common tasks

```bash
make manifests generate   # regenerate CRDs, RBAC, and DeepCopy code after editing types
make test                 # unit + envtest suite
make test-rego            # OPA policy tests (internal/controller/embed)
make lint                 # golangci-lint
make build                # compile the data lake manager to bin/manager
make run-data-lake        # run the data lake operator against your current kubecontext
make run-data-analytics   # run the data analytics operator against your current kubecontext
make run-data-orchestration # run the data orchestration operator against your current kubecontext
make platform-up          # recreate the kind cluster, apply local samples, run the operators
```

Both run targets use your active kubeconfig context, so install the CRDs first:

```bash
make install
kubectl apply -k config/samples/
```

## Container images

CI publishes multi-arch (`linux/amd64`, `linux/arm64`) images to GitHub Packages. Each operator image has the same name as its Helm chart:

| Image | Built from |
| --- | --- |
| `ghcr.io/opsarrayllc/data-lake-operator` | repo root |
| `ghcr.io/opsarrayllc/data-analytics-operator` | `data-analytics-operator/` |
| `ghcr.io/opsarrayllc/data-orchestration-operator` | `data-orchestration-operator/` |
| `ghcr.io/opsarrayllc/data-platform-superset` | `data-analytics-operator/images/superset/` |

The operator image tags come from the ref that triggered the build:
- `latest` and `main` from the default branch
- `sha-<commit>` for every build
- semver tags (`1.2.3`, `1.2`, `1`) when you push a `v*` tag

The Superset image is tagged with its Superset version (`5.0.0`), which is the tag the analytics operator uses by default. Pull requests are built as a check but never pushed.

To cut a release of the operator images and charts:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## Installing with Helm

Each operator has a chart, published to GitHub Packages as an OCI artifact under `charts/`. A chart installs its CRD, RBAC, and controller together:

```bash
for chart in data-lake-operator data-analytics-operator data-orchestration-operator; do
  helm install "${chart}" "oci://ghcr.io/opsarrayllc/charts/${chart}" \
    --version 0.2.0 \
    --namespace data-platform-system --create-namespace
done
```

Install only the charts you need. Analytics and Orchestration expect a `DataLake` to sign users in through and to query.

A chart's version and its operator image tag are released together. The default `image.tag` follows the chart's `appVersion`, so you rarely need to set it. Chart sources live in `deploy/<chart>`; see each `values.yaml` for the full set of options. Notable ones:

| Value | Default | Purpose |
| --- | --- | --- |
| `crds.enabled` | `true` | Install the chart's CRD with the release |
| `crds.keep` | `true` | Keep the CRD, and every resource of that kind, on uninstall |
| `metrics.secure` | `true` | Serve metrics over HTTPS with authn/authz |
| `metrics.serviceMonitor.enabled` | `false` | Create a Prometheus `ServiceMonitor` |
| `replicaCount` | `1` | Extra replicas are standbys; leader election picks one |

The CRD and RBAC templates are generated from each operator's `config/`. After changing an API or RBAC markers, run `make helm-sync`, which syncs all three charts, and commit the result. CI fails the release if a chart's copy is stale.

## Deploying the data lake operator with kustomize

```bash
export IMG=ghcr.io/opsarrayllc/data-lake-operator:latest
make deploy IMG=$IMG
```

To build and push the image yourself instead of relying on CI:

```bash
export IMG=<registry>/data-lake-operator:<tag>
make docker-build docker-push IMG=$IMG   # single-arch, your host platform
make docker-buildx IMG=$IMG              # multi-arch
```

The analytics and orchestration operators have no kustomize deployment; install them with Helm. To build their images yourself, run `make -C data-analytics-operator docker-build IMG=...` or the same in `data-orchestration-operator`.

Tear down with `make undeploy` and `make uninstall`.

To produce a single self-contained manifest for distribution:

```bash
make build-installer IMG=$IMG   # writes dist/data-platform-operator-install.yaml
```

## Cloud object storage

By default a `DataLake` runs MinIO in the cluster. To keep the warehouse in S3 or GCS instead, use the Terraform modules under [`terraform/`](terraform). Each cloud has a copy-and-paste example that creates the following:
- the bucket
- the access identity: an EKS Pod Identity role on AWS, an HMAC key in a Secret on GCP

The example then prints the `spec.storage` block to use. Start with [`terraform/README.md`](terraform/README.md).

## Users and groups

Embedded Keycloak ships three groups. Create a user in the `dataplatform` realm
and add them to a group; the operator syncs membership into LakeKeeper (and
into the row-filter OpenFGA store as `group:<name>#member`):

| Group | Default warehouse access |
| --- | --- |
| `platform-admins` | Catalog and project admin (the `admin` user is already in this group) |
| `data-engineers` | Create and modify tables |
| `analysts` | Read tables |

Tokens include a `groups` claim. Finer grants (a namespace, a table, a region)
still go through LakeKeeper's UI or OpenFGA tuples; the groups are the starting
set of personas, not a replacement for those APIs.

The analytics operator maps the same groups to Superset roles (`platform-admins` → Admin,
`data-engineers` → Alpha, `analysts` → Gamma). Charts and SQL Lab query Trino
with a per-user OAuth2 token, so catalog, row-filter, and column-mask policies
still apply. The first Trino query after login may prompt a one-time Keycloak
consent.

The orchestration operator maps those groups onto Argo ServiceAccounts. A login
whose token contains the group runs as that account: `platform-admins` can
delete workflows, `data-engineers` can create and update them, and `analysts`
can read them. A token with none of those groups cannot sign in.

### External identity providers

Keycloak is for local development. To sign people in through JumpCloud, Okta, or another provider, and to run machine-to-LakeKeeper calls on Kubernetes ServiceAccount tokens, see [`docs/jumpcloud.md`](docs/jumpcloud.md).

## Row-level access control

LakeKeeper and OpenFGA authorize whole objects: a warehouse, a namespace, a
table. Two optional lists narrow that further in Trino:

- `spec.authz.rowFilters` keeps only the rows whose key column holds a value
  the user may see.
- `spec.authz.columnAccess` replaces a restricted column with a mask (NULL by
  default) unless the user holds a grant on that column.

```yaml
spec:
  authz:
    rowFilters:
      - schema: sales
        table: orders
        column: region
        openfga:
          type: region
          relation: viewer
    columnAccess:
      - schema: sales
        table: orders
        column: ssn
        openfga:
          type: column
          relation: viewer
```

The operator provisions a dedicated OpenFGA store for these types (separate
from LakeKeeper's, whose authorization model LakeKeeper migrates itself), points
Trino at OPA's row-filter and batch-column-mask endpoints, and OPA turns each
user's grants into a `WHERE region IN (...)` clause or a column mask.

Granting a value or a column is still yours: write those tuples against
the store id in `status.rowFilterStoreID`, using the API key in secret
`openfga/openfga`. Group membership for the built-in Keycloak groups is
written by the operator, so `group:analysts#member` works once the user is
in that group:

```bash
STORE=$(kubectl get datalake <name> -o jsonpath='{.status.rowFilterStoreID}')
curl -sS "$OPENFGA/stores/$STORE/write" \
  -H "Authorization: Bearer $OPENFGA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"writes":{"tuple_keys":[
        {"user":"user:alice","relation":"viewer","object":"region:emea"},
        {"user":"group:analysts#member","relation":"viewer","object":"region:us-east"},
        {"user":"user:alice","relation":"viewer","object":"column:lakekeeper.sales.orders.ssn"}
      ]}}'
```

The default column object is `{catalog}.{schema}.{table}.{column}`. Set
`openfga.object` to a short name such as `ssn` to share one grant across
tables. Tuple users are Trino usernames (the OIDC `sub` claim). A relation may be
granted to a user directly or to a group's members; the operator writes
membership tuples for the built-in Keycloak groups (`platform-admins`,
`data-engineers`, `analysts`).

Both features fail closed: a user with no row-filter grant sees no rows, a
user with no column grant sees the mask, and everyone does if OpenFGA becomes
unreachable. Restricted columns still appear in the schema; their values do
not. See `config/samples/dataplatform_v1alpha1_rowfilters.yaml`.

## Adding new APIs

Use the CLI rather than hand-creating files, so the `PROJECT` file and scaffold
markers stay consistent:

```bash
kubebuilder create api --group dataplatform --version v1alpha1 --kind <Kind> --resource --controller
```

Then re-run `make manifests generate`.

## Local kind cluster

```bash
make platform-up
```

That recreates the kind cluster, applies the local DataLake, Analytics, and Orchestration samples, and runs the operators in the foreground. Ctrl+C stops the operators and leaves the cluster running.

The same steps one at a time:

```bash
make kind-up
make run-data-lake
make run-data-analytics
make run-data-orchestration
kubectl --context kind-data-platform-dev apply -f config/samples/dataplatform_v1alpha1_local.yaml
kubectl --context kind-data-platform-dev apply -f data-analytics-operator/config/samples/dataplatform_v1alpha1_local.yaml
kubectl --context kind-data-platform-dev apply -f data-orchestration-operator/config/samples/dataplatform_v1alpha1_local.yaml
```

That brings up Keycloak, LakeKeeper, Trino, Superset, and Argo behind mkcert TLS on
`*.data-platform.local`. Open `https://superset.data-platform.local` or
`https://argo.data-platform.local` and sign in
with Keycloak (`admin` / password from `keycloak/keycloak-admin`). To query Trino
from DBeaver-CE, see [docs/dbeaver.md](docs/dbeaver.md).

Superset uses `ghcr.io/opsarrayllc/data-platform-superset:5.0.0`, built from
[`data-analytics-operator/images/superset/Dockerfile`](data-analytics-operator/images/superset/Dockerfile)
(stock Apache Superset plus `authlib` and the Trino dialect). CI publishes it, and
`make kind-up` builds and loads the same tag into kind. To use your own build, run
`make docker-build-superset SUPERSET_IMG=...` and set `spec.image` on the Analytics resource.

## Notes

- `config/crd/bases/*`, `config/rbac/role.yaml`, `**/zz_generated.*.go`, and `PROJECT`
  are generated. Don't edit them by hand.
- Don't remove `// +kubebuilder:scaffold:*` comments; the CLI injects code at those points.
- End-to-end tests (`make test-e2e`) expect a dedicated kind cluster, not a real environment.
- See `AGENTS.md` for a fuller conventions guide.
