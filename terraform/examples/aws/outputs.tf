output "datalake_storage" {
  description = "spec.storage for the DataLake."
  value       = module.warehouse.datalake_storage
}

output "datalake_storage_yaml" {
  description = "spec.storage as YAML, ready to paste under the DataLake spec. Run terraform output -raw datalake_storage_yaml."
  value       = yamlencode({ storage = module.warehouse.datalake_storage })
}
