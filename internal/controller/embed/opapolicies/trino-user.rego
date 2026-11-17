package trino

# regal ignore:rule-name-repeats-package
trino_user_id := input.context.identity.user

# ServiceAccount users sign in to Trino with their Kubernetes token (Trino's
# JWT authenticator), so their user is the token subject,
# system:serviceaccount:<namespace>:<name>. LakeKeeper knows them under its
# Kubernetes authenticator prefix. Trino rejects OIDC subjects with this shape,
# so only real ServiceAccount tokens reach the first branch.
lakekeeper_user_id := concat("", ["kubernetes~", trino_user_id]) if {
	startswith(trino_user_id, "system:serviceaccount:")
} else := concat("", ["oidc~", trino_user_id])
