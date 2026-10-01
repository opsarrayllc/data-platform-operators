module "warehouse" {
  source = "../../modules/azure"

  resource_group_name  = var.resource_group_name
  location             = var.location
  storage_account_name = var.storage_account_name
  subnet_id            = var.subnet_id
  private_dns_zone_id  = var.private_dns_zone_id
  aks_oidc_issuer_url  = var.aks_oidc_issuer_url
}

output "lakekeeper_warehouse" {
  value     = module.warehouse.lakekeeper_warehouse
  sensitive = true
}

output "credentials_secret_data" {
  value     = module.warehouse.credentials_secret_data
  sensitive = true
}
