variable "region" {
  description = "Region of the existing VPC and cluster."
  type        = string
}

variable "bucket_name" {
  description = "Globally unique warehouse bucket name."
  type        = string
}

variable "vpc_id" {
  description = "Existing VPC id."
  type        = string
}

variable "s3_vpc_endpoint_id" {
  description = "Existing S3 VPC endpoint id in vpc_id."
  type        = string
}

variable "oidc_provider_arn" {
  description = "Existing cluster IAM OIDC provider ARN."
  type        = string
}
