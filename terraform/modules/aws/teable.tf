# Optional attachment storage for Teable (Analytics spec.teable.storage).
#
# Browsers upload and download attachments directly through presigned URLs,
# so unlike the warehouse these buckets are not restricted to the VPC
# endpoint. The private bucket is never public. The public bucket holds only
# avatars and icons, and allows anonymous reads of its objects; an account- or
# organization-level S3 Block Public Access setting will reject that policy.
#
# Teable's S3 client requires a static access key, so this creates an IAM user
# scoped to these two buckets. Rotate its key by tainting
# aws_iam_access_key.teable and updating the Kubernetes Secret.

locals {
  create_teable        = var.create_teable ? 1 : 0
  teable_bucket_prefix = coalesce(var.teable_bucket_prefix, "${var.bucket_name}-teable")
  teable_buckets = var.create_teable ? {
    public  = "${local.teable_bucket_prefix}-public"
    private = "${local.teable_bucket_prefix}-private"
  } : {}
}

resource "aws_s3_bucket" "teable" {
  for_each = local.teable_buckets

  bucket        = each.value
  force_destroy = var.force_destroy
  tags          = merge({ Name = each.value }, var.tags)

  lifecycle {
    precondition {
      condition     = var.teable_public_url != null && can(regex("^https://[^/]+$", trimsuffix(coalesce(var.teable_public_url, "-"), "/")))
      error_message = "teable_public_url must be Teable's https origin, such as https://teable.example.com, when create_teable is true."
    }
  }
}

resource "aws_s3_bucket_public_access_block" "teable" {
  for_each = aws_s3_bucket.teable

  bucket                  = each.value.id
  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = each.key == "private"
  restrict_public_buckets = each.key == "private"
}

resource "aws_s3_bucket_ownership_controls" "teable" {
  for_each = aws_s3_bucket.teable

  bucket = each.value.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

# SSE-S3 rather than var.kms_key_arn: anonymous reads of the public bucket
# cannot use a KMS key.
resource "aws_s3_bucket_server_side_encryption_configuration" "teable" {
  for_each = aws_s3_bucket.teable

  bucket = each.value.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_cors_configuration" "teable" {
  for_each = aws_s3_bucket.teable

  bucket = each.value.id
  cors_rule {
    allowed_methods = ["GET", "HEAD", "PUT", "POST"]
    allowed_origins = [trimsuffix(coalesce(var.teable_public_url, "-"), "/")]
    allowed_headers = ["*"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3600
  }
}

data "aws_iam_policy_document" "teable_bucket" {
  for_each = aws_s3_bucket.teable

  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      each.value.arn,
      "${each.value.arn}/*",
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

  dynamic "statement" {
    for_each = each.key == "public" ? [1] : []
    content {
      sid       = "AnonymousReadObjects"
      actions   = ["s3:GetObject"]
      resources = ["${each.value.arn}/*"]
      principals {
        type        = "*"
        identifiers = ["*"]
      }
    }
  }
}

resource "aws_s3_bucket_policy" "teable" {
  for_each = aws_s3_bucket.teable

  bucket     = each.value.id
  policy     = data.aws_iam_policy_document.teable_bucket[each.key].json
  depends_on = [aws_s3_bucket_public_access_block.teable]
}

data "aws_iam_policy_document" "teable" {
  count = local.create_teable

  statement {
    sid       = "ListBuckets"
    actions   = ["s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"]
    resources = [for b in aws_s3_bucket.teable : b.arn]
  }

  statement {
    sid = "ObjectAccess"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
    ]
    resources = [for b in aws_s3_bucket.teable : "${b.arn}/*"]
  }
}

resource "aws_iam_user" "teable" {
  count = local.create_teable

  name = "${var.name}-teable"
  tags = var.tags
}

resource "aws_iam_user_policy" "teable" {
  count = local.create_teable

  name   = "${var.name}-teable-storage"
  user   = aws_iam_user.teable[0].name
  policy = data.aws_iam_policy_document.teable[0].json
}

resource "aws_iam_access_key" "teable" {
  count = local.create_teable

  user = aws_iam_user.teable[0].name
}
