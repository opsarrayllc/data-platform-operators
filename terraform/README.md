# Warehouse object storage

These modules create the Iceberg warehouse bucket, and the identity LakeKeeper and Trino use to reach it. They don't create the network or the cluster. You pass those in from the stack that already created them.

To use one, start from the example for your cloud under [`examples/`](examples). Each example is a complete root module that pulls the released module from this repo by git tag. Copy the directory into your codebase, fill in `terraform.tfvars`, and apply. The AWS and GCP examples also create the Kubernetes Secret the `DataLake` reads, and print the `spec.storage` block to paste into it.

| Cloud | Example | Operator support |
| --- | --- | --- |
| AWS | [`examples/aws`](examples/aws) | Ready |
| GCP | [`examples/gcp`](examples/gcp) | Ready, through the GCS S3-compatible API |
| Azure | [`examples/azure`](examples/azure) | Not yet. The `DataLake` API only accepts S3-compatible storage |

## Quick start: AWS

### What you need already

- An EKS cluster in a VPC, which the operator runs on.
- An S3 **gateway** endpoint in that VPC, attached to the route tables of the node subnets. The bucket rejects object access from anywhere else. If you don't have one, the example can create it (see step 2). An interface endpoint won't work, because the operator doesn't point LakeKeeper or Trino at the endpoint's DNS name.
- Outbound access from the nodes to AWS STS, through a NAT gateway or an STS interface endpoint. LakeKeeper calls `sts:AssumeRole` to vend temporary credentials to Trino.
- An AWS account that allows IAM users with access keys. LakeKeeper authenticates with one, and SCPs that block IAM users will stop the apply.
- Credentials for Terraform that can do three things:
  - manage S3 and IAM
  - read the EKS cluster
  - create a namespace and a Secret in the cluster, through an EKS access entry or `aws-auth`

### Steps

1. Copy [`examples/aws`](examples/aws) into your codebase.

   If your root module already configures the `aws` and `kubernetes` providers, take only these files:
   - `main.tf`
   - `variables.tf`
   - `outputs.tf`

   Then drop the provider blocks from `versions.tf`.

2. Rename `terraform.tfvars.example` to `terraform.tfvars` and fill it in:

   ```hcl
   region           = "us-east-1"
   bucket_name      = "my-org-warehouse"
   vpc_id           = "vpc-0123456789abcdef0"
   eks_cluster_name = "my-cluster"

   # Use the VPC's existing S3 gateway endpoint:
   s3_vpc_endpoint_id = "vpce-0123456789abcdef0"

   # Or remove s3_vpc_endpoint_id and create one on the node subnet route tables:
   # private_route_table_ids = ["rtb-0123456789abcdef0", "rtb-0fedcba9876543210"]
   ```

   Find an existing endpoint with:

   ```bash
   aws ec2 describe-vpc-endpoints \
     --filters Name=vpc-id,Values=<vpc-id> Name=service-name,Values=com.amazonaws.<region>.s3 \
     --query 'VpcEndpoints[?VpcEndpointType==`Gateway`].VpcEndpointId'
   ```

   If you create one, list every route table your node subnets use. Traffic from a subnet whose route table isn't listed misses the endpoint and gets access denied. The new endpoint carries all same-region S3 traffic from those subnets, not only the warehouse's. With the default full-access endpoint policy that changes nothing for other buckets, and it usually cuts NAT data charges.

   Optional inputs:
   - `kms_key_arn`: encrypt the bucket with your own KMS key. Its key policy must let IAM policies in the account grant access.
   - `vpc_endpoint_bypass_principal_arns`: principals, such as an admin role, that may read objects from outside the VPC.
   - `tags`

3. Apply:

   ```bash
   terraform init
   terraform apply
   ```

   This creates the following:
   - the S3 gateway endpoint, if you didn't pass `s3_vpc_endpoint_id`
   - the bucket
   - the IAM user and its access key
   - the warehouse role LakeKeeper assumes
   - the `lakekeeper` namespace
   - the `warehouse-credentials` Secret

   The access key is stored in Terraform state, so keep the state encrypted.

4. Print the spec fields and add them to your `DataLake`:

   ```bash
   terraform output -raw datalake_spec_yaml | sed 's/^/  /'
   ```

   The `sed` indents it to sit under `spec:`. The output has quoted keys, which is still valid YAML. With the defaults, the `DataLake` looks like this:

   ```yaml
   apiVersion: dataplatform.opsarray.io/v1alpha1
   kind: DataLake
   metadata:
     name: datalake
   spec:
     storage:
       embedded: false
       s3:
         bucket: my-org-warehouse
         region: us-east-1
         flavor: aws
         pathStyleAccess: false
         stsEnabled: true
         stsRoleARN: arn:aws:iam::123456789012:role/datalake-warehouse
         credentialsSecretRef:
           name: warehouse-credentials
           namespace: lakekeeper
   ```

The operator reuses the `lakekeeper` namespace because it already exists. Because Terraform owns that namespace, `terraform destroy` also deletes LakeKeeper.

### Optional: RDS Postgres

By default each component runs its own single-replica Postgres in the cluster, on a PVC with no backups. Set `create_postgres = true` to put the LakeKeeper catalog, OpenFGA, and Superset on one RDS for PostgreSQL instance instead. Each gets its own database: `lakekeeper`, `openfga`, and `superset`.

```hcl
create_postgres     = true
postgres_subnet_ids = ["subnet-0123456789abcdef0", "subnet-0fedcba9876543210"]
```

The defaults are PostgreSQL 17 on `db.t4g.micro`, single-AZ, with 20 GiB of gp3 storage that grows to 100 GiB, encryption at rest, and 7 days of backups. The databases hold only metadata, since table data lives in S3, so this is about $15 a month. Raise `postgres_instance_class` to `db.t4g.small` as Superset use grows. Set the module's `postgres_multi_az` for a standby in a second zone.

What else to know:

- **Network.** `postgres_subnet_ids` must be private subnets in at least two availability zones. Only `postgres_allowed_security_group_ids` can reach port 5432. It defaults to the EKS cluster security group, which managed node groups use. With self-managed nodes, list their security group.
- **Databases are created in the cluster.** Terraform doesn't need a network path to RDS. Each component's pod runs an `ensure-database` init container that creates its database on first start.
- **Credentials.** Terraform generates the master password and writes it to the `platform-postgres` Secret in the `lakekeeper` namespace. All three components use that one login. The operators copy it into each component's namespace and pass it to pods through Secret references, never as plain values. The password is in Terraform state too, so keep the state encrypted.
- **New installs only.** Switching an existing platform from embedded to RDS doesn't move any data. The old in-cluster Postgres is left in place.
- **Operator version.** OpenFGA and Superset need operator images that support an external database. That's the first operator release after this module version.
- **Destroying.** Deletion protection is on. Set `postgres_deletion_protection = false` on the module and apply before you run `terraform destroy`. Destroy takes a final snapshot named `<name>-postgres-final`. Delete that snapshot before you create the instance again with the same `name`.

With `create_postgres = true`, `datalake_spec_yaml` also includes `lakekeeper.postgres` and `authz.openfga.postgres`. Print the Analytics fields too:

```bash
terraform output -raw analytics_spec_yaml | sed 's/^/  /'
```

```yaml
apiVersion: dataplatform.opsarray.io/v1alpha1
kind: Analytics
metadata:
  name: analytics
spec:
  postgres:
    embedded: false
    host: datalake-postgres.abc123.us-east-1.rds.amazonaws.com
    port: 5432
    database: superset
    sslMode: require
    credentialsSecretRef:
      name: platform-postgres
      namespace: lakekeeper
```

## Quick start: GCP

### What you need already

- A GKE cluster on a subnet that has Private Google Access.
- Credentials for Terraform that can do three things:
  - manage GCS, service accounts and IAM in the project
  - read the cluster
  - create a namespace and a Secret in it

### Steps

1. Copy [`examples/gcp`](examples/gcp) into your codebase.
2. Rename `terraform.tfvars.example` to `terraform.tfvars` and fill it in.
3. Run `terraform init` and `terraform apply`.
4. Run `terraform output -raw datalake_storage_yaml | sed 's/^/  /'` and paste the result under the `DataLake`'s `spec:`.

The module creates the bucket and a service account. That account's HMAC key goes into the `warehouse-credentials` Secret. The storage block it prints sets these fields:
- `endpoint`: `https://storage.googleapis.com`
- `region`: `auto`
- `pathStyleAccess`: `true`
- `flavor`: `s3-compat`
- `stsEnabled`: `false`

GCS has no STS, so Trino uses the same HMAC key as LakeKeeper.

## Azure

[`examples/azure`](examples/azure) creates three things:
- an ADLS Gen2 account
- a private endpoint
- an Entra app with federated credentials

Its `lakekeeper_warehouse` output is a LakeKeeper create-warehouse body (`credential-type: client-credentials`, profile type `adls`). The operator only creates S3 storage profiles today, so a `DataLake` can't use this yet.

## Module reference

| Module | Required inputs | What it creates |
| --- | --- | --- |
| `modules/aws` | `bucket_name`, `region`, `vpc_id`, `s3_vpc_endpoint_id` | A bucket limited to that S3 endpoint. An IAM user and access key for LakeKeeper, and a role LakeKeeper assumes to vend temporary credentials to Trino. With `create_postgres`, an RDS for PostgreSQL instance, its subnet group, and a security group. |
| `modules/gcp` | `project_id`, `bucket_name`, `location`, `network`, `subnetwork`, `gke_cluster_name`, `gke_cluster_location` | A GCS bucket. A service account and HMAC key for the operator, and Workload Identity bindings for the cluster service accounts. The cluster must already have Workload Identity. |
| `modules/azure` | `resource_group_name`, `location`, `storage_account_name`, `subnet_id`, `private_dns_zone_id`, `aks_oidc_issuer_url` | An ADLS Gen2 account, a private endpoint on that subnet, and an Entra app. The app has Storage Blob Data Contributor and Storage Blob Delegator, plus federated credentials for the cluster service accounts. The DNS zone must already be linked to the cluster virtual network. |

The operator reads `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` from the Secret in `spec.storage.s3.credentialsSecretRef`. The AWS and GCP modules output those as `credentials_secret_data` if you'd rather create the Secret some other way.

The AWS module doesn't set up IRSA, and only the LakeKeeper IAM user can assume the warehouse role. On GCP and Azure, `kubernetes_service_accounts` defaults to the `default` service account in the `lakekeeper` and `trino` namespaces. Those modules output `kubernetes_service_account_annotation` for those accounts.

## Releases

A push to `main` that changes a directory under `terraform/modules` publishes a GitHub release for that module only. Tags are named `terraform-data-lake-<cloud>-v<version>`, which keeps them separate from the operator image and Helm tags (`v*`). A module's first release is `1.0.0`. After that, each commit that touches the module bumps the version:
- `feat!:` or a `BREAKING CHANGE` footer bumps the major version.
- `feat:` bumps the minor version.
- Anything else bumps the patch version.

Each release also attaches a zip of the module directory. The examples pin a tag in their `source`:

```hcl
module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/aws?ref=terraform-data-lake-aws-v1.1.0"

  # ...
}
```

The full list of releases is at <https://github.com/opsarrayllc/data-platform-operators/releases>.
