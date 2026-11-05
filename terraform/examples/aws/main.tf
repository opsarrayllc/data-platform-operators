# Copy this directory into your Terraform code base. If your root module already
# configures the aws and kubernetes providers, copy main.tf, variables.tf, and
# outputs.tf, and drop the provider blocks in versions.tf.

data "aws_eks_cluster" "this" {
  name = var.eks_cluster_name
}

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

  # Managed node groups use the cluster security group, so allowing it lets
  # every pod reach Postgres. Self-managed nodes need their own group listed.
  postgres_allowed_security_group_ids = length(var.postgres_allowed_security_group_ids) > 0 ? var.postgres_allowed_security_group_ids : [
    data.aws_eks_cluster.this.vpc_config[0].cluster_security_group_id,
  ]
}

module "warehouse" {
  source = "git::https://github.com/opsarrayllc/data-platform-operators.git//terraform/modules/aws?ref=terraform-data-lake-aws-v1.1.0"

  bucket_name                        = var.bucket_name
  region                             = var.region
  vpc_id                             = var.vpc_id
  s3_vpc_endpoint_id                 = local.s3_vpc_endpoint_id
  kms_key_arn                        = var.kms_key_arn
  vpc_endpoint_bypass_principal_arns = var.vpc_endpoint_bypass_principal_arns
  credentials_secret_name            = var.credentials_secret_name
  credentials_secret_namespace       = var.credentials_secret_namespace
  tags                               = var.tags

  create_postgres                     = var.create_postgres
  postgres_subnet_ids                 = var.postgres_subnet_ids
  postgres_allowed_security_group_ids = local.postgres_allowed_security_group_ids
  postgres_instance_class             = var.postgres_instance_class
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

# The DataLake and Analytics resources read the RDS login from this Secret.
resource "kubernetes_secret_v1" "postgres_credentials" {
  count = var.create_postgres ? 1 : 0

  metadata {
    name      = "platform-postgres"
    namespace = kubernetes_namespace_v1.lakekeeper.metadata[0].name
  }

  data = module.warehouse.postgres_credentials_secret_data
}
