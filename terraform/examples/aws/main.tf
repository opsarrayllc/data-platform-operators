module "warehouse" {
  source = "../../modules/aws"

  bucket_name        = var.bucket_name
  region             = var.region
  vpc_id             = var.vpc_id
  s3_vpc_endpoint_id = var.s3_vpc_endpoint_id
  oidc_provider_arn  = var.oidc_provider_arn
}

output "datalake_storage" {
  value = module.warehouse.datalake_storage
}

output "access_key_id" {
  value     = module.warehouse.access_key_id
  sensitive = true
}

output "secret_access_key" {
  value     = module.warehouse.secret_access_key
  sensitive = true
}
