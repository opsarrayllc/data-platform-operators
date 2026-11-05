output "datalake_storage" {
  description = "spec.storage for the DataLake."
  value       = module.warehouse.datalake_storage
}

output "datalake_spec_yaml" {
  description = "DataLake spec fields as YAML, ready to paste under spec. Includes the RDS databases when create_postgres is true. Run terraform output -raw datalake_spec_yaml."
  value = yamlencode(merge(
    { storage = module.warehouse.datalake_storage },
    var.create_postgres ? {
      lakekeeper = { postgres = module.warehouse.datalake_postgres.lakekeeper }
      authz      = { openfga = { postgres = module.warehouse.datalake_postgres.openfga } }
    } : null,
  ))
}

output "analytics_spec_yaml" {
  description = "Analytics spec.postgres as YAML, ready to paste under spec. Null when create_postgres is false. Run terraform output -raw analytics_spec_yaml."
  value       = var.create_postgres ? yamlencode({ postgres = module.warehouse.analytics_postgres }) : null
}
