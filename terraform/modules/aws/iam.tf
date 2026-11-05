# LakeKeeper authenticates with EKS Pod Identity: the lakekeeper role below is
# associated with the LakeKeeper ServiceAccount, so no access key exists. That
# role can only assume the warehouse role. LakeKeeper assumes the warehouse
# role for its own object IO (storage.s3.systemIdentity.assumeRoleARN) and,
# when STS is enabled, again with a table-scoped policy to vend temporary
# credentials to Trino (storage.s3.stsRoleARN, vended-credentials-enabled).
#
# The cluster must run the eks-pod-identity-agent add-on.

data "aws_iam_policy_document" "warehouse" {
  statement {
    sid       = "ListBuckets"
    actions   = ["s3:ListAllMyBuckets"]
    resources = ["*"]
  }

  statement {
    sid = "ListBucket"
    actions = [
      "s3:GetBucketLocation",
      "s3:ListBucket",
      "s3:ListBucketMultipartUploads",
    ]
    resources = [aws_s3_bucket.warehouse.arn]
  }

  statement {
    sid       = "ObjectAccess"
    actions   = ["s3:*"]
    resources = ["${aws_s3_bucket.warehouse.arn}/*"]
  }

  dynamic "statement" {
    for_each = var.kms_key_arn == null ? [] : [var.kms_key_arn]
    content {
      sid = "WarehouseKms"
      actions = [
        "kms:Decrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey",
      ]
      resources = [statement.value]
    }
  }
}

resource "aws_iam_policy" "warehouse" {
  name        = "${var.name}-warehouse"
  description = "Object access for the ${var.bucket_name} Iceberg warehouse."
  policy      = data.aws_iam_policy_document.warehouse.json
  tags        = var.tags
}

# EKS Pod Identity requires both actions in the trust policy.
data "aws_iam_policy_document" "pod_identity_trust" {
  statement {
    sid     = "TrustEksPodIdentity"
    actions = ["sts:AssumeRole", "sts:TagSession"]

    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lakekeeper" {
  name               = "${var.name}-lakekeeper"
  description        = "EKS Pod Identity role for LakeKeeper. It can only assume ${var.name}-warehouse."
  assume_role_policy = data.aws_iam_policy_document.pod_identity_trust.json
  tags               = var.tags
}

data "aws_iam_policy_document" "assume_warehouse" {
  statement {
    sid = "AssumeWarehouseRole"
    actions = [
      "sts:AssumeRole",
      "sts:TagSession",
    ]
    resources = [aws_iam_role.warehouse.arn]
  }
}

resource "aws_iam_role_policy" "lakekeeper_assume_warehouse" {
  name   = "${var.name}-assume-warehouse"
  role   = aws_iam_role.lakekeeper.name
  policy = data.aws_iam_policy_document.assume_warehouse.json
}

resource "aws_eks_pod_identity_association" "lakekeeper" {
  cluster_name    = var.eks_cluster_name
  namespace       = var.lakekeeper_namespace
  service_account = "lakekeeper"
  role_arn        = aws_iam_role.lakekeeper.arn
  tags            = var.tags
}

data "aws_iam_policy_document" "warehouse_trust" {
  statement {
    sid     = "TrustLakekeeperRole"
    actions = ["sts:AssumeRole", "sts:TagSession"]

    principals {
      type        = "AWS"
      identifiers = [aws_iam_role.lakekeeper.arn]
    }
  }

  # Without STS, Trino gets no vended credentials and reads the bucket through
  # its own Pod Identity association with this role.
  dynamic "statement" {
    for_each = var.sts_enabled ? [] : [1]
    content {
      sid     = "TrustEksPodIdentity"
      actions = ["sts:AssumeRole", "sts:TagSession"]

      principals {
        type        = "Service"
        identifiers = ["pods.eks.amazonaws.com"]
      }
    }
  }
}

resource "aws_iam_role" "warehouse" {
  name               = "${var.name}-warehouse"
  assume_role_policy = data.aws_iam_policy_document.warehouse_trust.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "warehouse" {
  role       = aws_iam_role.warehouse.name
  policy_arn = aws_iam_policy.warehouse.arn
}

resource "aws_eks_pod_identity_association" "trino" {
  count = var.sts_enabled ? 0 : 1

  cluster_name    = var.eks_cluster_name
  namespace       = var.trino_namespace
  service_account = var.trino_service_account
  role_arn        = aws_iam_role.warehouse.arn
  tags            = var.tags
}
