data "azurerm_client_config" "current" {}

locals {
  private_endpoint_resource_group_name = coalesce(var.private_endpoint_resource_group_name, var.resource_group_name)
}

resource "azurerm_storage_account" "warehouse" {
  name                            = var.storage_account_name
  resource_group_name             = var.resource_group_name
  location                        = var.location
  account_tier                    = "Standard"
  account_replication_type        = var.replication_type
  account_kind                    = "StorageV2"
  is_hns_enabled                  = true
  min_tls_version                 = "TLS1_2"
  allow_nested_items_to_be_public = false
  shared_access_key_enabled       = true
  public_network_access_enabled   = var.public_network_access_enabled
  tags                            = var.tags
}

resource "azurerm_storage_container" "warehouse" {
  name                  = var.filesystem
  storage_account_id    = azurerm_storage_account.warehouse.id
  container_access_type = "private"
}

resource "azurerm_private_endpoint" "warehouse" {
  name                = "${var.name}-dfs"
  location            = var.location
  resource_group_name = local.private_endpoint_resource_group_name
  subnet_id           = var.subnet_id
  tags                = var.tags

  private_service_connection {
    name                           = "${var.name}-dfs"
    private_connection_resource_id = azurerm_storage_account.warehouse.id
    subresource_names              = ["dfs"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "dfs"
    private_dns_zone_ids = [var.private_dns_zone_id]
  }
}

resource "azuread_application" "lakekeeper" {
  display_name     = "${var.name}-lakekeeper"
  sign_in_audience = "AzureADMyOrg"
  description      = "Client-credentials identity for the ${var.storage_account_name} Iceberg warehouse."
}

resource "azuread_service_principal" "lakekeeper" {
  client_id = azuread_application.lakekeeper.client_id
}

resource "azuread_application_password" "lakekeeper" {
  application_id = azuread_application.lakekeeper.id
  display_name   = "${var.name}-warehouse"

  lifecycle {
    ignore_changes = [end_date]
  }
}

# Contributor is data-plane access. Delegator lets this app mint user-delegation
# keys, which is how LakeKeeper vends SAS tokens to query engines.
resource "azurerm_role_assignment" "blob_contributor" {
  scope                            = azurerm_storage_account.warehouse.id
  role_definition_name             = "Storage Blob Data Contributor"
  principal_id                     = azuread_service_principal.lakekeeper.object_id
  skip_service_principal_aad_check = true
}

resource "azurerm_role_assignment" "blob_delegator" {
  scope                            = azurerm_storage_account.warehouse.id
  role_definition_name             = "Storage Blob Delegator"
  principal_id                     = azuread_service_principal.lakekeeper.object_id
  skip_service_principal_aad_check = true
}

resource "azuread_application_federated_identity_credential" "workload" {
  for_each = {
    for sa in var.kubernetes_service_accounts : "${sa.namespace}/${sa.name}" => sa
  }

  application_id = azuread_application.lakekeeper.id
  display_name   = substr("${each.value.namespace}-${each.value.name}", 0, 120)
  description    = "Workload identity for ${each.value.namespace}/${each.value.name}."
  audiences      = ["api://AzureADTokenExchange"]
  issuer         = var.aks_oidc_issuer_url
  subject        = "system:serviceaccount:${each.value.namespace}/${each.value.name}"
}
