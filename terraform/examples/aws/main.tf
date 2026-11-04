# Copy this directory into your Terraform code base. If your root module already
# configures the aws and kubernetes providers, copy main.tf, variables.tf, and
# outputs.tf, and drop the provider blocks in versions.tf.

module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/aws?ref=terraform-data-lake-aws-v1.0.1"

  bucket_name                        = var.bucket_name
  region                             = var.region
  vpc_id                             = var.vpc_id
  s3_vpc_endpoint_id                 = var.s3_vpc_endpoint_id
  kms_key_arn                        = var.kms_key_arn
  vpc_endpoint_bypass_principal_arns = var.vpc_endpoint_bypass_principal_arns
  credentials_secret_name            = var.credentials_secret_name
  credentials_secret_namespace       = var.credentials_secret_namespace
  tags                               = var.tags
}

# The DataLake reads the warehouse access key from this Secret, so it has to
# exist before the DataLake is applied. The operator reuses a namespace that
# already exists. Destroying this namespace also deletes LakeKeeper.
resource "kubernetes_namespace_v1" "lakekeeper" {
  metadata {
    name = var.credentials_secret_namespace
  }
}

resource "kubernetes_secret_v1" "warehouse_credentials" {
  metadata {
    name      = var.credentials_secret_name
    namespace = kubernetes_namespace_v1.lakekeeper.metadata[0].name
  }

  data = module.warehouse.credentials_secret_data
}
