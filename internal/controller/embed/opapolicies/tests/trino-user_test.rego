# Tests for how Trino users map to LakeKeeper principals.
#
# Deliberately outside the parent directory so that `go:embed` does not ship
# them to OPA. Run them with `make test-rego`.

package trino_user_test

import data.trino

test_oidc_user_maps_to_oidc_principal if {
	trino.lakekeeper_user_id == "oidc~5b0c1d2e-0000-4000-8000-000000000001" with input as {"context": {"identity": {"user": "5b0c1d2e-0000-4000-8000-000000000001"}}}
}

test_service_account_maps_to_kubernetes_principal if {
	trino.lakekeeper_user_id == "kubernetes~system:serviceaccount:argo-workflows:transform-keyme" with input as {"context": {"identity": {"user": "system:serviceaccount:argo-workflows:transform-keyme"}}}
}

test_prefix_must_be_at_start if {
	trino.lakekeeper_user_id == "oidc~x-system:serviceaccount:a:b" with input as {"context": {"identity": {"user": "x-system:serviceaccount:a:b"}}}
}
