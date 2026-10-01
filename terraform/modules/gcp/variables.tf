variable "name" {
  description = "Service account id. LakeKeeper and Trino authenticate with this account's HMAC key."
  type        = string
  default     = "datalake"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.name))
    error_message = "name must be a valid service account id: 6-30 characters, start with a letter, and contain only lowercase letters, digits, and hyphens."
  }
}

variable "project_id" {
  description = "GCP project that owns the bucket and service account."
  type        = string
}

variable "bucket_name" {
  description = "Globally unique name of the warehouse bucket."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$", var.bucket_name))
    error_message = "bucket_name must be 3-63 characters of lowercase letters, digits, dots, underscores, and hyphens, and must start and end with a letter or digit."
  }
}

variable "location" {
  description = "GCS bucket location, such as US, EU, or us-central1. This can differ from the cluster location when the bucket is multi-region."
  type        = string
}

variable "network" {
  description = "Name or self link of the existing VPC the cluster uses."
  type        = string
}

variable "subnetwork" {
  description = "Self link of the existing subnet the cluster nodes use. Private Google Access must already be enabled on it."
  type        = string

  validation {
    condition     = can(regex("projects/[^/]+/regions/[^/]+/subnetworks/[^/]+$", var.subnetwork))
    error_message = "subnetwork must be the self link of the existing subnet."
  }
}

variable "gke_cluster_name" {
  description = "Name of the existing GKE cluster."
  type        = string
}

variable "gke_cluster_location" {
  description = "Location of the existing GKE cluster, either a region or a zone."
  type        = string
}

variable "kubernetes_service_accounts" {
  description = "Service accounts on the existing cluster that may impersonate the warehouse service account. Defaults are the operator namespaces and the default service account those pods use. The cluster must already have Workload Identity enabled."
  type = list(object({
    namespace = string
    name      = string
  }))
  default = [
    { namespace = "lakekeeper", name = "default" },
    { namespace = "trino", name = "default" },
  ]

  validation {
    condition = length(var.kubernetes_service_accounts) > 0 && alltrue([
      for sa in var.kubernetes_service_accounts :
      can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", sa.namespace)) && can(regex("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", sa.name))
    ])
    error_message = "kubernetes_service_accounts must contain at least one namespace and name, each a Kubernetes DNS label."
  }
}

variable "force_destroy" {
  description = "Delete bucket objects on terraform destroy. Leave false for a warehouse that already holds table data."
  type        = bool
  default     = false
}

variable "versioning" {
  description = "Enable object versioning on the bucket."
  type        = bool
  default     = false
}

variable "credentials_secret_name" {
  description = "Name of the Kubernetes Secret the DataLake should reference. This module does not create the Secret."
  type        = string
  default     = "warehouse-credentials"
}

variable "credentials_secret_namespace" {
  description = "Namespace of that Secret."
  type        = string
  default     = "lakekeeper"
}

variable "labels" {
  description = "Labels applied to the bucket and service account."
  type        = map(string)
  default     = {}
}
