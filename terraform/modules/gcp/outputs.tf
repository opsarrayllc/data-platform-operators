output "bucket" {
  description = "Warehouse bucket name. DataLake field storage.s3.bucket."
  value       = google_storage_bucket.warehouse.name
}

output "location" {
  description = "GCS bucket location. This is not the SigV4 region; use the region output in the DataLake spec."
  value       = google_storage_bucket.warehouse.location
}

output "region" {
  description = "SigV4 region for the GCS XML API. DataLake field storage.s3.region."
  value       = local.s3_region
}

output "endpoint" {
  description = "S3-compatible endpoint. DataLake field storage.s3.endpoint."
  value       = local.s3_endpoint
}

output "service_account_email" {
  description = "Service account that owns the HMAC key and the bucket IAM bindings."
  value       = google_service_account.lakekeeper.email
}

output "kubernetes_service_account_annotation" {
  description = "Annotation for the existing service accounts listed in kubernetes_service_accounts."
  value = {
    "iam.gke.io/gcp-service-account" = google_service_account.lakekeeper.email
  }
}

output "access_key_id" {
  description = "HMAC access id. Value for AWS_ACCESS_KEY_ID in the credentials Secret."
  value       = google_storage_hmac_key.lakekeeper.access_id
  sensitive   = true
}

output "secret_access_key" {
  description = "HMAC secret. Value for AWS_SECRET_ACCESS_KEY in the credentials Secret."
  value       = google_storage_hmac_key.lakekeeper.secret
  sensitive   = true
}

output "credentials_secret_data" {
  description = "Kubernetes Secret data. Keys match the operator defaults accessKeyIDKey and secretAccessKeyKey."
  value = {
    AWS_ACCESS_KEY_ID     = google_storage_hmac_key.lakekeeper.access_id
    AWS_SECRET_ACCESS_KEY = google_storage_hmac_key.lakekeeper.secret
  }
  sensitive = true
}

output "datalake_storage" {
  description = "spec.storage for a DataLake with storage.embedded=false. GCS has no AWS STS endpoint, so stsEnabled is false and Trino uses the same HMAC key as LakeKeeper."
  value = {
    embedded = false
    s3 = {
      bucket          = google_storage_bucket.warehouse.name
      region          = local.s3_region
      endpoint        = local.s3_endpoint
      pathStyleAccess = local.s3_path_style
      flavor          = local.s3_flavor
      stsEnabled      = false
      credentialsSecretRef = {
        name      = var.credentials_secret_name
        namespace = var.credentials_secret_namespace
      }
    }
  }
}
