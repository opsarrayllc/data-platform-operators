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

variable "credentials_secret_name" {
  description = "Name of the Secret that holds the HMAC key."
  type        = string
  default     = "warehouse-credentials"
}

variable "credentials_secret_namespace" {
  description = "Namespace for that Secret. Keep the default unless the DataLake sets spec.lakekeeper.namespace."
  type        = string
  default     = "lakekeeper"
}

variable "labels" {
  description = "Labels applied to the bucket and service account."
  type        = map(string)
  default     = {}
}
