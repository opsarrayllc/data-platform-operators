locals {
  # GCS XML API SigV4 credential scope uses the region "auto".
  s3_region       = "auto"
  s3_endpoint     = "https://storage.googleapis.com"
  s3_path_style   = true
  s3_flavor       = "s3-compat"
  subnet_parts    = regex("projects/([^/]+)/regions/([^/]+)/subnetworks/([^/]+)$", var.subnetwork)
  network_name    = regex("[^/]+$", var.network)
  subnetwork_name = regex("[^/]+$", var.subnetwork)
}

data "google_compute_subnetwork" "cluster" {
  project = local.subnet_parts[0]
  region  = local.subnet_parts[1]
  name    = local.subnet_parts[2]
}

data "google_container_cluster" "cluster" {
  project  = var.project_id
  name     = var.gke_cluster_name
  location = var.gke_cluster_location
}

resource "google_service_account" "lakekeeper" {
  project      = var.project_id
  account_id   = var.name
  display_name = "${var.name} lakehouse warehouse"
  description  = "HMAC identity for the Iceberg warehouse. LakeKeeper and Trino use the key as S3 credentials."

  lifecycle {
    precondition {
      condition     = local.network_name == regex("[^/]+$", data.google_container_cluster.cluster.network) && local.subnetwork_name == regex("[^/]+$", data.google_container_cluster.cluster.subnetwork)
      error_message = "gke_cluster_name must be the cluster on var.network and var.subnetwork."
    }
    precondition {
      condition     = data.google_compute_subnetwork.cluster.private_ip_google_access
      error_message = "The cluster subnet must already have Private Google Access enabled."
    }
    precondition {
      condition     = length(data.google_container_cluster.cluster.workload_identity_config) > 0 && data.google_container_cluster.cluster.workload_identity_config[0].workload_pool == "${var.project_id}.svc.id.goog"
      error_message = "The GKE cluster must already have Workload Identity on ${var.project_id}.svc.id.goog."
    }
  }
}

resource "google_storage_bucket" "warehouse" {
  name                        = var.bucket_name
  project                     = var.project_id
  location                    = var.location
  force_destroy               = var.force_destroy
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  labels                      = var.labels

  versioning {
    enabled = var.versioning
  }
}

resource "google_storage_bucket_iam_member" "object_admin" {
  bucket = google_storage_bucket.warehouse.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.lakekeeper.email}"
}

resource "google_storage_bucket_iam_member" "bucket_reader" {
  bucket = google_storage_bucket.warehouse.name
  role   = "roles/storage.legacyBucketReader"
  member = "serviceAccount:${google_service_account.lakekeeper.email}"
}

resource "google_storage_hmac_key" "lakekeeper" {
  project               = var.project_id
  service_account_email = google_service_account.lakekeeper.email
}

resource "google_service_account_iam_member" "workload_identity" {
  for_each = {
    for sa in var.kubernetes_service_accounts : "${sa.namespace}/${sa.name}" => sa
  }

  service_account_id = google_service_account.lakekeeper.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[${each.value.namespace}/${each.value.name}]"
}
