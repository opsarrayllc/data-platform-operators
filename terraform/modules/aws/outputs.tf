output "bucket" {
  description = "Warehouse bucket name. DataLake field storage.s3.bucket."
  value       = aws_s3_bucket.warehouse.id
}

output "region" {
  description = "Bucket region. DataLake field storage.s3.region."
  value       = var.region
}

output "sts_enabled" {
  description = "DataLake field storage.s3.stsEnabled."
  value       = var.sts_enabled
}

output "sts_role_arn" {
  description = "Role LakeKeeper assumes for vended credentials. DataLake field storage.s3.stsRoleARN. Null when sts_enabled is false."
  value       = var.sts_enabled ? aws_iam_role.warehouse.arn : null
}

output "warehouse_role_arn" {
  description = "Warehouse role ARN. LakeKeeper's Pod Identity role assumes it, and so does Trino's Pod Identity when sts_enabled is false."
  value       = aws_iam_role.warehouse.arn
}

output "lakekeeper_role_arn" {
  description = "EKS Pod Identity role of the LakeKeeper ServiceAccount."
  value       = aws_iam_role.lakekeeper.arn
}

output "datalake_storage" {
  description = "spec.storage for a DataLake with storage.embedded=false. LakeKeeper uses EKS Pod Identity, so no credentials Secret is needed."
  value = {
    embedded = false
    s3 = merge(
      {
        bucket          = aws_s3_bucket.warehouse.id
        region          = var.region
        flavor          = "aws"
        pathStyleAccess = false
        stsEnabled      = var.sts_enabled
        systemIdentity = {
          assumeRoleARN = aws_iam_role.warehouse.arn
        }
      },
      var.sts_enabled ? { stsRoleARN = aws_iam_role.warehouse.arn } : {},
    )
  }
}

locals {
  postgres_ref = var.create_postgres ? {
    embedded = false
    host     = aws_db_instance.postgres[0].address
    port     = local.postgres_port
    sslMode  = "require"
    credentialsSecretRef = {
      name      = var.postgres_credentials_secret_name
      namespace = var.postgres_credentials_secret_namespace
    }
  } : null
}

output "postgres_endpoint" {
  description = "RDS host name. Null when create_postgres is false."
  value       = var.create_postgres ? aws_db_instance.postgres[0].address : null
}

output "postgres_credentials_secret_data" {
  description = "Kubernetes Secret data for the RDS master user. Keys match the operators' default usernameKey and passwordKey. Null when create_postgres is false."
  sensitive   = true
  value = var.create_postgres ? {
    username = local.postgres_user
    password = random_password.postgres[0].result
  } : null
}

output "datalake_postgres" {
  description = "DataLake fields for the RDS instance: lakekeeper is spec.lakekeeper.postgres and openfga is spec.authz.openfga.postgres. Null when create_postgres is false."
  value = var.create_postgres ? {
    lakekeeper = merge(local.postgres_ref, { database = "lakekeeper" })
    openfga    = merge(local.postgres_ref, { database = "openfga" })
  } : null
}

output "analytics_postgres" {
  description = "Analytics spec.postgres for the RDS instance. Null when create_postgres is false."
  value       = var.create_postgres ? merge(local.postgres_ref, { database = "superset" }) : null
}
