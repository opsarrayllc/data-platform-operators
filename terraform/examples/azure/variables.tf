variable "subscription_id" {
  description = "Azure subscription id."
  type        = string
}

variable "resource_group_name" {
  description = "Existing resource group for the storage account."
  type        = string
}

variable "location" {
  description = "Azure region of the storage account."
  type        = string
}

variable "storage_account_name" {
  description = "Globally unique storage account name."
  type        = string
}

variable "subnet_id" {
  description = "Existing subnet id for the private endpoint."
  type        = string
}

variable "private_dns_zone_id" {
  description = "Existing privatelink.dfs.core.windows.net zone id."
  type        = string
}

variable "aks_oidc_issuer_url" {
  description = "Existing AKS OIDC issuer URL."
  type        = string
}
