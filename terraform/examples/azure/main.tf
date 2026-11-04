# The operator cannot use ADLS yet: a DataLake only accepts S3-compatible
# storage. This example creates the account and app registration so they are
# ready once ADLS support lands. Copy this directory into your code base.

module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/azure?ref=terraform-data-lake-azure-v1.0.0"

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
