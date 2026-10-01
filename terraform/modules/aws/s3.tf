data "aws_region" "current" {}

data "aws_vpc_endpoint" "s3" {
  id           = var.s3_vpc_endpoint_id
  vpc_id       = var.vpc_id
  service_name = "com.amazonaws.${var.region}.s3"
}

resource "aws_s3_bucket" "warehouse" {
  bucket        = var.bucket_name
  force_destroy = var.force_destroy
  tags          = merge({ Name = var.bucket_name }, var.tags)

  lifecycle {
    precondition {
      condition     = var.region == data.aws_region.current.region
      error_message = "The AWS provider region must be var.region (${var.region})."
    }
  }
}

resource "aws_s3_bucket_public_access_block" "warehouse" {
  bucket                  = aws_s3_bucket.warehouse.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "warehouse" {
  bucket = aws_s3_bucket.warehouse.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "warehouse" {
  bucket = aws_s3_bucket.warehouse.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = var.kms_key_arn == null ? "AES256" : "aws:kms"
      kms_master_key_id = var.kms_key_arn
    }
    bucket_key_enabled = var.kms_key_arn != null
  }
}

data "aws_iam_policy_document" "bucket_tls" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.warehouse.arn,
      "${aws_s3_bucket.warehouse.arn}/*",
    ]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid     = "DenyObjectAccessOutsideVpcEndpoint"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      "${aws_s3_bucket.warehouse.arn}/*",
    ]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "StringNotEquals"
      variable = "aws:SourceVpce"
      values   = [data.aws_vpc_endpoint.s3.id]
    }

    dynamic "condition" {
      for_each = length(var.vpc_endpoint_bypass_principal_arns) == 0 ? [] : [var.vpc_endpoint_bypass_principal_arns]
      content {
        test     = "ArnNotEquals"
        variable = "aws:PrincipalArn"
        values   = condition.value
      }
    }
  }
}

resource "aws_s3_bucket_policy" "warehouse" {
  bucket     = aws_s3_bucket.warehouse.id
  policy     = data.aws_iam_policy_document.bucket_tls.json
  depends_on = [aws_s3_bucket_public_access_block.warehouse]
}
