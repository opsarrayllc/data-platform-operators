module "warehouse" {
  source = "../../modules/gcp"

  project_id           = var.project_id
  bucket_name          = var.bucket_name
  location             = var.location
  network              = var.network
  subnetwork           = var.subnetwork
  gke_cluster_name     = var.gke_cluster_name
  gke_cluster_location = var.gke_cluster_location
}

output "datalake_storage" {
  value = module.warehouse.datalake_storage
}

output "access_key_id" {
  value     = module.warehouse.access_key_id
  sensitive = true
}

output "secret_access_key" {
  value     = module.warehouse.secret_access_key
  sensitive = true
}
