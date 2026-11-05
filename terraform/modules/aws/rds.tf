# Optional shared Postgres for the LakeKeeper catalog, OpenFGA, and Superset.
# Each component uses its own database on this instance; the operators create
# them on first start with an ensure-database init container, so Terraform does
# not need a network path to the instance.

locals {
  create_postgres = var.create_postgres ? 1 : 0
  postgres_port   = 5432
  postgres_user   = "platform"
}

resource "random_password" "postgres" {
  count = local.create_postgres

  length = 32
  # RDS rejects /, @, ", and spaces in the master password.
  override_special = "!#$%^&*()-_=+[]{}<>:?"
}

resource "aws_db_subnet_group" "postgres" {
  count = local.create_postgres

  name       = "${var.name}-postgres"
  subnet_ids = var.postgres_subnet_ids
  tags       = var.tags

  lifecycle {
    precondition {
      condition     = length(var.postgres_subnet_ids) >= 2
      error_message = "postgres_subnet_ids must list at least two private subnets in different availability zones when create_postgres is true."
    }
  }
}

resource "aws_security_group" "postgres" {
  count = local.create_postgres

  name        = "${var.name}-postgres"
  description = "Postgres access for the ${var.name} data platform."
  vpc_id      = var.vpc_id
  tags        = merge({ Name = "${var.name}-postgres" }, var.tags)

  lifecycle {
    precondition {
      condition     = length(var.postgres_allowed_security_group_ids) > 0
      error_message = "postgres_allowed_security_group_ids must include the EKS node or cluster security group when create_postgres is true."
    }
  }
}

# count rather than for_each, so security group ids that are unknown until
# apply (such as a new EKS cluster's) still plan.
resource "aws_vpc_security_group_ingress_rule" "postgres" {
  count = var.create_postgres ? length(var.postgres_allowed_security_group_ids) : 0

  security_group_id            = aws_security_group.postgres[0].id
  description                  = "Postgres from the data platform cluster"
  ip_protocol                  = "tcp"
  from_port                    = local.postgres_port
  to_port                      = local.postgres_port
  referenced_security_group_id = var.postgres_allowed_security_group_ids[count.index]
  tags                         = var.tags
}

resource "aws_db_instance" "postgres" {
  count = local.create_postgres

  identifier     = "${var.name}-postgres"
  engine         = "postgres"
  engine_version = var.postgres_engine_version
  instance_class = var.postgres_instance_class

  allocated_storage     = 20
  max_allocated_storage = 100
  storage_type          = "gp3"
  storage_encrypted     = true
  kms_key_id            = var.kms_key_arn

  username = local.postgres_user
  password = random_password.postgres[0].result
  port     = local.postgres_port

  db_subnet_group_name   = aws_db_subnet_group.postgres[0].name
  vpc_security_group_ids = [aws_security_group.postgres[0].id]
  publicly_accessible    = false
  multi_az               = var.postgres_multi_az

  backup_retention_period    = 7
  auto_minor_version_upgrade = true
  copy_tags_to_snapshot      = true
  deletion_protection        = var.postgres_deletion_protection
  skip_final_snapshot        = false
  final_snapshot_identifier  = "${var.name}-postgres-final"

  tags = var.tags
}
