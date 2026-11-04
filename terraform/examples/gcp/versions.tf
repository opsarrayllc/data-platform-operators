terraform {
  required_version = ">= 1.5.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0.0, < 8.0.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.23.0, < 4.0.0"
    }
  }
}

provider "google" {
  project = var.project_id
}

data "google_client_config" "current" {}

data "google_container_cluster" "this" {
  name     = var.gke_cluster_name
  location = var.gke_cluster_location
}

provider "kubernetes" {
  host                   = "https://${data.google_container_cluster.this.endpoint}"
  cluster_ca_certificate = base64decode(data.google_container_cluster.this.master_auth[0].cluster_ca_certificate)
  token                  = data.google_client_config.current.access_token
}
