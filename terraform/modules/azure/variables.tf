variable "name" {
  description = "Display name prefix for the Entra app registration. The app is <name>-lakekeeper."
  type        = string
  default     = "datalake"

  validation {
    condition     = can(regex("^[a-zA-Z][a-zA-Z0-9-]{0,40}$", var.name))
    error_message = "name must start with a letter and contain only letters, digits, and hyphens."
  }
}

variable "resource_group_name" {
  description = "Existing resource group for the storage account."
  type        = string
}

variable "location" {
  description = "Azure region of the existing resource group and cluster, such as eastus."
  type        = string
}

variable "subnet_id" {
  description = "Existing subnet id for the storage private endpoint. The cluster must already be able to route to this subnet."
  type        = string

  validation {
    condition     = can(regex("^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft.Network/virtualNetworks/[^/]+/subnets/[^/]+$", var.subnet_id))
    error_message = "subnet_id must be the resource id of an existing subnet."
  }
}

variable "private_endpoint_resource_group_name" {
  description = "Resource group for the private endpoint. Defaults to resource_group_name. Set this when the existing network stack keeps endpoints in another group."
  type        = string
  default     = null
}

variable "private_dns_zone_id" {
  description = "Existing private DNS zone id for privatelink.dfs.core.windows.net. The zone must already be linked to the cluster virtual network."
  type        = string

  validation {
    condition     = can(regex("^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft.Network/privateDnsZones/privatelink\\.dfs\\.core\\.windows\\.net$", var.private_dns_zone_id))
    error_message = "private_dns_zone_id must be the resource id of the existing privatelink.dfs.core.windows.net zone."
  }
}

variable "aks_oidc_issuer_url" {
  description = "OIDC issuer URL of the existing AKS cluster. Workload identity federated credentials trust this issuer."
  type        = string

  validation {
    condition     = can(regex("^https://", var.aks_oidc_issuer_url))
    error_message = "aks_oidc_issuer_url must be the existing cluster OIDC issuer URL."
  }
}

variable "kubernetes_service_accounts" {
  description = "Service accounts on the existing cluster that may use the warehouse app. Defaults are the operator namespaces and the default service account those pods use."
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

variable "public_network_access_enabled" {
  description = "Leave storage reachable from outside the virtual network. Set false when Terraform runs from a network that can use the private endpoint."
  type        = bool
  default     = true
}

variable "storage_account_name" {
  description = "Globally unique storage account name. 3-24 lowercase letters and digits. Hierarchical namespace is enabled so the account is ADLS Gen2."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]{3,24}$", var.storage_account_name))
    error_message = "storage_account_name must be 3-24 lowercase letters and digits."
  }
}

variable "filesystem" {
  description = "ADLS filesystem (blob container) that holds the warehouse."
  type        = string
  default     = "warehouse"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.filesystem))
    error_message = "filesystem must be 3-63 characters of lowercase letters, digits, and hyphens."
  }
}

variable "replication_type" {
  description = "Storage account replication. LRS is enough for a single-region warehouse."
  type        = string
  default     = "LRS"

  validation {
    condition     = contains(["LRS", "ZRS", "GRS", "RAGRS", "GZRS", "RAGZRS"], var.replication_type)
    error_message = "replication_type must be a storage account replication type."
  }
}

variable "credentials_secret_name" {
  description = "Name to use when you create the Kubernetes Secret from credentials_secret_data. This module does not create the Secret."
  type        = string
  default     = "warehouse-credentials"
}

variable "credentials_secret_namespace" {
  description = "Namespace to use for that Secret."
  type        = string
  default     = "lakekeeper"
}

variable "tags" {
  description = "Tags applied to the storage account."
  type        = map(string)
  default     = {}
}
