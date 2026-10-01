variable "project_id" {
  description = "GCP project id."
  type        = string
}

variable "bucket_name" {
  description = "Globally unique warehouse bucket name."
  type        = string
}

variable "location" {
  description = "GCS bucket location."
  type        = string
}

variable "network" {
  description = "Existing VPC name or self link."
  type        = string
}

variable "subnetwork" {
  description = "Existing subnet self link."
  type        = string
}

variable "gke_cluster_name" {
  description = "Existing GKE cluster name."
  type        = string
}

variable "gke_cluster_location" {
  description = "Existing GKE cluster location."
  type        = string
}
