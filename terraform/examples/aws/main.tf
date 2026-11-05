# Copy this directory into your Terraform code base. If your root module already
# configures the aws and kubernetes providers, copy main.tf, variables.tf, and
# outputs.tf, and drop the provider blocks in versions.tf.

# The bucket only accepts object access through an S3 gateway endpoint in the
# cluster VPC. Leave s3_vpc_endpoint_id unset to create one on the node subnet
# route tables. It carries all same-region S3 traffic from those subnets, not
# only the warehouse's.
resource "aws_vpc_endpoint" "s3" {
  count = var.s3_vpc_endpoint_id == null ? 1 : 0

  vpc_id            = var.vpc_id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = var.private_route_table_ids
  tags              = merge({ Name = "${var.bucket_name}-s3" }, var.tags)

  lifecycle {
    precondition {
      condition     = length(var.private_route_table_ids) > 0
      error_message = "Set s3_vpc_endpoint_id to an existing S3 gateway endpoint, or set private_route_table_ids to the node subnet route tables so this example can create one."
    }
  }
}

locals {
  s3_vpc_endpoint_id = var.s3_vpc_endpoint_id != null ? var.s3_vpc_endpoint_id : aws_vpc_endpoint.s3[0].id
}

module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/aws?ref=terraform-data-lake-aws-v1.0.1"

  bucket_name                        = var.bucket_name
  region                             = var.region
  vpc_id                             = var.vpc_id
  s3_vpc_endpoint_id                 = local.s3_vpc_endpoint_id
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
