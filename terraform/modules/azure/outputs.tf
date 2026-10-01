output "storage_account_name" {
  description = "ADLS account name. LakeKeeper storage profile field account-name."
  value       = azurerm_storage_account.warehouse.name
}

output "filesystem" {
  description = "ADLS filesystem. LakeKeeper storage profile field filesystem."
  value       = azurerm_storage_container.warehouse.name
}

output "dfs_endpoint" {
  description = "DFS endpoint for the storage account."
  value       = azurerm_storage_account.warehouse.primary_dfs_endpoint
}

output "tenant_id" {
  description = "Entra tenant id. LakeKeeper storage credential field tenant-id."
  value       = data.azurerm_client_config.current.tenant_id
}

output "client_id" {
  description = "Application id. LakeKeeper storage credential field client-id."
  value       = azuread_application.lakekeeper.client_id
}

output "private_endpoint_id" {
  description = "Private endpoint attached to the existing subnet."
  value       = azurerm_private_endpoint.warehouse.id
}

output "kubernetes_service_account_annotation" {
  description = "Annotations for the existing service accounts listed in kubernetes_service_accounts. The cluster also needs the azure.workload.identity/use label on those pods."
  value = {
    "azure.workload.identity/client-id" = azuread_application.lakekeeper.client_id
    "azure.workload.identity/tenant-id" = data.azurerm_client_config.current.tenant_id
  }
}

output "client_secret" {
  description = "Application password. LakeKeeper storage credential field client-secret."
  value       = azuread_application_password.lakekeeper.value
  sensitive   = true
}

output "credentials_secret_name" {
  description = "Suggested Secret name. The DataLake API does not read this Secret yet."
  value       = var.credentials_secret_name
}

output "credentials_secret_namespace" {
  description = "Suggested Secret namespace."
  value       = var.credentials_secret_namespace
}

output "credentials_secret_data" {
  description = "Kubernetes Secret data for the warehouse app registration."
  value = {
    AZURE_CLIENT_ID     = azuread_application.lakekeeper.client_id
    AZURE_CLIENT_SECRET = azuread_application_password.lakekeeper.value
    AZURE_TENANT_ID     = data.azurerm_client_config.current.tenant_id
  }
  sensitive = true
}

output "lakekeeper_warehouse" {
  description = "LakeKeeper management API body for an ADLS warehouse. credential-type client-credentials, storage profile type adls. SAS vending is enabled by LakeKeeper's default sas-enabled=true."
  sensitive   = true
  value = {
    storage-credential = {
      type            = "az"
      credential-type = "client-credentials"
      client-id       = azuread_application.lakekeeper.client_id
      client-secret   = azuread_application_password.lakekeeper.value
      tenant-id       = data.azurerm_client_config.current.tenant_id
    }
    storage-profile = {
      type         = "adls"
      account-name = azurerm_storage_account.warehouse.name
      filesystem   = azurerm_storage_container.warehouse.name
    }
  }
}
