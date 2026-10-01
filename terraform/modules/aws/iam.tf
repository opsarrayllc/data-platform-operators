data "aws_iam_openid_connect_provider" "cluster" {
  arn = var.oidc_provider_arn
}

locals {
  oidc_host = replace(data.aws_iam_openid_connect_provider.cluster.url, "https://", "")
  service_account_subjects = [
    for sa in var.kubernetes_service_accounts : "system:serviceaccount:${sa.namespace}/${sa.name}"
  ]
}

# LakeKeeper stores this user's access key as the warehouse storage credential
# and uses it for its own object IO and for remote signing. When STS is enabled,
# LakeKeeper calls sts:AssumeRole on the warehouse role and returns the temporary
# credentials to Trino (storage.s3.stsRoleARN, vended-credentials-enabled).

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

resource "aws_iam_user" "lakekeeper" {
  name = "${var.name}-lakekeeper"
  tags = var.tags
}

resource "aws_iam_user_policy_attachment" "lakekeeper" {
  user       = aws_iam_user.lakekeeper.name
  policy_arn = aws_iam_policy.warehouse.arn
}

resource "aws_iam_access_key" "lakekeeper" {
  user = aws_iam_user.lakekeeper.name
}

data "aws_iam_policy_document" "warehouse_trust" {
  statement {
    sid     = "TrustLakekeeperUser"
    actions = ["sts:AssumeRole", "sts:TagSession"]

    principals {
      type        = "AWS"
      identifiers = [aws_iam_user.lakekeeper.arn]
    }
  }

  statement {
    sid     = "TrustClusterServiceAccounts"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.cluster.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:sub"
      values   = local.service_account_subjects
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

resource "aws_iam_user_policy" "assume_warehouse" {
  name   = "${var.name}-assume-warehouse"
  user   = aws_iam_user.lakekeeper.name
  policy = data.aws_iam_policy_document.assume_warehouse.json
}
