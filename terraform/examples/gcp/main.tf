# Copy this directory into your Terraform code base. If your root module already
# configures the google and kubernetes providers, copy main.tf, variables.tf, and
# outputs.tf, and drop the provider blocks in versions.tf.

module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/gcp?ref=terraform-data-lake-gcp-v1.0.0"

  project_id                   = var.project_id
  bucket_name                  = var.bucket_name
  location                     = var.location
  network                      = var.network
  subnetwork                   = var.subnetwork
  gke_cluster_name             = var.gke_cluster_name
  gke_cluster_location         = var.gke_cluster_location
  credentials_secret_name      = var.credentials_secret_name
  credentials_secret_namespace = var.credentials_secret_namespace
  labels                       = var.labels
}

# The DataLake reads the HMAC key from this Secret, so it has to exist before
# the DataLake is applied. The operator reuses a namespace that already exists.
# Destroying this namespace also deletes LakeKeeper.
resource "kubernetes_namespace_v1" "lakekeeper" {
  metadata {
    name = var.credentials_secret_namespace
  }
}

resource "kubernetes_secret_v1" "warehouse_credentials" {
  metadata {
    name      = var.credentials_secret_name
    namespace = kubernetes_namespace_v1.lakekeeper.metadata[0].name
  }

  data = module.warehouse.credentials_secret_data
}
