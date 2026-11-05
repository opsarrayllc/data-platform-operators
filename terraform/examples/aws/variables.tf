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
  description = "Existing EKS cluster the operator runs on. Terraform writes the credentials Secret to it."
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

variable "credentials_secret_name" {
  description = "Name of the Secret that holds the warehouse access key."
  type        = string
  default     = "warehouse-credentials"
}

variable "credentials_secret_namespace" {
  description = "Namespace for that Secret. Keep the default unless the DataLake sets spec.lakekeeper.namespace."
  type        = string
  default     = "lakekeeper"
}

variable "tags" {
  description = "Tags applied to the bucket and IAM resources."
  type        = map(string)
  default     = {}
}
