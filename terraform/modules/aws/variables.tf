variable "name" {
  description = "Prefix for the IAM roles and policy. LakeKeeper's Pod Identity role is <name>-lakekeeper and the warehouse role is <name>-warehouse."
  type        = string
  default     = "datalake"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,22}$", var.name))
    error_message = "name must be 1-23 characters, start with a letter, and contain only lowercase letters, digits, and hyphens."
  }
}

variable "bucket_name" {
  description = "Globally unique name of the warehouse bucket."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.bucket_name))
    error_message = "bucket_name must be 3-63 characters of lowercase letters, digits, and hyphens, and must start and end with a letter or digit."
  }
}

variable "region" {
  description = "Region of the existing VPC and cluster. The AWS provider must be configured for this same region."
  type        = string
}

variable "eks_cluster_name" {
  description = "Existing EKS cluster the operator runs on. The module creates EKS Pod Identity associations in it. The cluster must run the eks-pod-identity-agent add-on."
  type        = string
}

variable "lakekeeper_namespace" {
  description = "Namespace of the LakeKeeper ServiceAccount (spec.lakekeeper.namespace). The operator names the account lakekeeper."
  type        = string
  default     = "lakekeeper"
}

variable "trino_namespace" {
  description = "Namespace of the Trino pods (spec.trino.namespace). Used only when sts_enabled is false, when Trino reaches the bucket with its own Pod Identity."
  type        = string
  default     = "trino"
}

variable "trino_service_account" {
  description = "ServiceAccount of the Trino pods. The operator runs Trino as trino. Used only when sts_enabled is false."
  type        = string
  default     = "trino"
}

variable "vpc_id" {
  description = "Existing VPC that contains the cluster. Object access is limited to the S3 VPC endpoint in this VPC."
  type        = string

  validation {
    condition     = can(regex("^vpc-", var.vpc_id))
    error_message = "vpc_id must be an existing VPC id."
  }
}

variable "s3_vpc_endpoint_id" {
  description = "Existing S3 gateway or interface endpoint in vpc_id. The cluster route tables must already send S3 traffic to this endpoint."
  type        = string

  validation {
    condition     = can(regex("^vpce-", var.s3_vpc_endpoint_id))
    error_message = "s3_vpc_endpoint_id must be an existing VPC endpoint id."
  }
}

variable "vpc_endpoint_bypass_principal_arns" {
  description = "Existing IAM principals that may use the bucket from outside the VPC endpoint, such as the role that runs Terraform. Cluster workloads are not in this list; they go through s3_vpc_endpoint_id."
  type        = list(string)
  default     = []
}

variable "sts_enabled" {
  description = "LakeKeeper vends temporary credentials to Trino. When false, Trino gets its own Pod Identity association with the warehouse role instead. The DataLake spec field is storage.s3.stsEnabled."
  type        = bool
  default     = true
}

variable "force_destroy" {
  description = "Delete bucket objects on terraform destroy. Leave false for a warehouse that already holds table data."
  type        = bool
  default     = false
}

variable "kms_key_arn" {
  description = "Customer-managed KMS key for bucket encryption. Empty uses SSE-S3. When set, the warehouse identities receive decrypt and GenerateDataKey on this key."
  type        = string
  default     = null
}

variable "tags" {
  description = "Tags applied to the bucket and IAM resources."
  type        = map(string)
  default     = {}
}

variable "create_postgres" {
  description = "Create an RDS for PostgreSQL instance for the LakeKeeper catalog, OpenFGA, Superset, and Teable. Each gets its own database on it."
  type        = bool
  default     = false
}

variable "postgres_subnet_ids" {
  description = "Private subnets for the RDS subnet group, in at least two availability zones. Required when create_postgres is true."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for id in var.postgres_subnet_ids : can(regex("^subnet-", id))])
    error_message = "postgres_subnet_ids must be subnet ids."
  }
}

variable "postgres_allowed_security_group_ids" {
  description = "Security groups allowed to reach Postgres on 5432, such as the EKS cluster security group the nodes use. Required when create_postgres is true."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for id in var.postgres_allowed_security_group_ids : can(regex("^sg-", id))])
    error_message = "postgres_allowed_security_group_ids must be security group ids."
  }
}

variable "postgres_instance_class" {
  description = "RDS instance class. db.t4g.micro is enough to start, since the databases hold only metadata. Use db.t4g.small or larger as Superset use grows."
  type        = string
  default     = "db.t4g.micro"
}

variable "postgres_engine_version" {
  description = "RDS for PostgreSQL major or exact version. 17 matches the operators' embedded Postgres."
  type        = string
  default     = "17"
}

variable "postgres_multi_az" {
  description = "Run a standby in a second availability zone. Roughly doubles the instance cost."
  type        = bool
  default     = false
}

variable "postgres_deletion_protection" {
  description = "Block deleting the instance. Set false and apply before terraform destroy."
  type        = bool
  default     = true
}

variable "postgres_credentials_secret_name" {
  description = "Name of the Kubernetes Secret, in postgres_credentials_secret_namespace, that the DataLake and Analytics resources reference for Postgres. This module does not create the Secret."
  type        = string
  default     = "platform-postgres"
}

variable "postgres_credentials_secret_namespace" {
  description = "Namespace of the Postgres credentials Secret."
  type        = string
  default     = "lakekeeper"
}

variable "create_teable" {
  description = "Create Teable's attachment buckets and an IAM user whose access key Teable uses. Teable's S3 client only takes static keys, so this is the module's one long-lived credential."
  type        = bool
  default     = false
}

variable "teable_bucket_prefix" {
  description = "Prefix for Teable's buckets, <prefix>-public and <prefix>-private. Must be globally unique. Defaults to <bucket_name>-teable."
  type        = string
  default     = null

  validation {
    condition     = var.teable_bucket_prefix == null || can(regex("^[a-z0-9][a-z0-9-]{1,53}[a-z0-9]$", var.teable_bucket_prefix))
    error_message = "teable_bucket_prefix must be 3-55 characters of lowercase letters, digits, and hyphens, and must start and end with a letter or digit."
  }
}

variable "teable_public_url" {
  description = "Teable's browser URL (Analytics spec.teable.publicURL), such as https://teable.example.com. The buckets allow cross-origin uploads and downloads from this origin only. Required when create_teable is true."
  type        = string
  default     = null
}
