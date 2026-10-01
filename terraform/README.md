# Warehouse object storage

Modules that create the Iceberg warehouse and the identity LakeKeeper and Trino use to reach it. They do not create the network or the cluster. Pass those from the stack that already did.

Set `spec.storage.embedded` to false and point the `DataLake` at the module output. The operator reads `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` from the Secret in `spec.storage.s3.credentialsSecretRef`. On AWS, when `stsEnabled` and `stsRoleARN` are set, LakeKeeper assumes that role and vends temporary credentials to Trino.

`kubernetes_service_accounts` defaults to the operator namespaces and the `default` service account those pods use. Override it when the existing cluster uses other names. Each module outputs `kubernetes_service_account_annotation` for those accounts.

| Module | Existing inputs | What this module adds |
| --- | --- | --- |
| `modules/aws` | `region`, `vpc_id`, `s3_vpc_endpoint_id`, `oidc_provider_arn`, optional `kms_key_arn` | Bucket limited to that S3 endpoint. IAM user access key for LakeKeeper, and a role the cluster service accounts can assume. |
| `modules/gcp` | `project_id`, `network`, `subnetwork`, `gke_cluster_name`, `gke_cluster_location` | GCS bucket. Service account HMAC key for the operator, and Workload Identity bindings for the cluster service accounts. The subnet must already have Private Google Access, and the cluster must already have Workload Identity. |
| `modules/azure` | `resource_group_name`, `location`, `subnet_id`, `private_dns_zone_id`, `aks_oidc_issuer_url` | ADLS Gen2 account, a private endpoint on that subnet, and an Entra app. The app has Storage Blob Data Contributor and Storage Blob Delegator, plus federated credentials for the cluster service accounts. The DNS zone must already be linked to the cluster virtual network. |

```hcl
module "warehouse" {
  source = "./modules/aws"

  bucket_name        = "my-org-warehouse"
  region             = module.network.region
  vpc_id             = module.network.vpc_id
  s3_vpc_endpoint_id = module.network.s3_vpc_endpoint_id
  oidc_provider_arn  = module.eks.oidc_provider_arn
  kms_key_arn        = module.kms.key_arn
}
```

AWS and GCP export `datalake_storage`, which is the `spec.storage` object for the current `DataLake` API. Azure exports `lakekeeper_warehouse`, the LakeKeeper create-warehouse body (`credential-type: client-credentials`, profile type `adls`). The operator only creates S3 storage profiles today, so a `DataLake` cannot consume the Azure body yet.

```bash
kubectl -n lakekeeper create secret generic warehouse-credentials \
  --from-literal=AWS_ACCESS_KEY_ID="$(terraform output -raw access_key_id)" \
  --from-literal=AWS_SECRET_ACCESS_KEY="$(terraform output -raw secret_access_key)"
```

GCP uses those same Secret keys. Its `datalake_storage` sets `endpoint` to `https://storage.googleapis.com`, `region` to `auto`, `pathStyleAccess` to true, `flavor` to `s3-compat`, and `stsEnabled` to false.

```yaml
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

## Releases

A push to `main` that changes a directory under `terraform/modules` publishes a GitHub release for that module only. Tags are `terraform-data-lake-<cloud>-v<version>`, so they stay off the operator image and Helm tags (`v*`). The first release of a module is `1.0.0`. Later commits that touch it bump the version: `feat!:` or `BREAKING CHANGE` bumps major, `feat:` bumps minor, and any other change bumps patch.

```hcl
module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/aws?ref=terraform-data-lake-aws-v1.2.3"
}
```

Each release also attaches a zip of that module directory.
