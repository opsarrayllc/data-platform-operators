variable "region" {
  description = "Region of the existing VPC and EKS cluster."
  type        = string
}

variable "bucket_name" {
  description = "Globally unique warehouse bucket name."
  type        = string
}

variable "vpc_id" {
  description = "Existing VPC that contains the EKS cluster."
  type        = string
}

variable "s3_vpc_endpoint_id" {
  description = "Existing S3 gateway endpoint in vpc_id, attached to the route tables of the node subnets. Leave null to create one on private_route_table_ids."
  type        = string
  default     = null
}

variable "private_route_table_ids" {
  description = "Route tables of the EKS node subnets. Used only when s3_vpc_endpoint_id is null, to attach the S3 gateway endpoint this example creates."
  type        = list(string)
  default     = []
}

variable "eks_cluster_name" {
  description = "Existing EKS cluster the operator runs on. LakeKeeper gets an EKS Pod Identity association in it."
  type        = string
}

variable "kms_key_arn" {
  description = "Optional customer-managed KMS key for the bucket. Its key policy must allow IAM policies in this account to grant access."
  type        = string
  default     = null
}

variable "vpc_endpoint_bypass_principal_arns" {
  description = "Principals that may read and write objects from outside the VPC endpoint, such as an admin role."
  type        = list(string)
  default     = []
}

variable "create_pod_identity_agent" {
  description = "Install the eks-pod-identity-agent add-on. Leave false if the cluster already runs it."
  type        = bool
  default     = false
}

variable "lakekeeper_namespace" {
  description = "LakeKeeper namespace. Keep the default unless the DataLake sets spec.lakekeeper.namespace. The Postgres Secret goes here too."
  type        = string
  default     = "lakekeeper"
}

variable "tags" {
  description = "Tags applied to the bucket and IAM resources."
  type        = map(string)
  default     = {}
}

variable "create_postgres" {
  description = "Create an RDS for PostgreSQL instance for the LakeKeeper catalog, OpenFGA, and Superset instead of running Postgres in the cluster."
  type        = bool
  default     = false
}

variable "postgres_subnet_ids" {
  description = "Private subnets for RDS, in at least two availability zones. Required when create_postgres is true."
  type        = list(string)
  default     = []
}

variable "postgres_allowed_security_group_ids" {
  description = "Security groups allowed to reach Postgres. Defaults to the EKS cluster security group, which managed node groups use."
  type        = list(string)
  default     = []
}

variable "postgres_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.micro"
}
