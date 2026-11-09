/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// Kubernetes ServiceAccount authentication to LakeKeeper
// (spec.auth.serviceAccountTokens).
//
// The operator mints short-lived tokens for its own ServiceAccount with the
// TokenRequest API. Trino and OPA keep their OAuth2 client-credentials code,
// but point it at a sidecar on 127.0.0.1 that answers every token request with
// the pod's projected ServiceAccount token. The kubelet rotates that file, and
// the sidecar reports a short expiry, so clients pick up the new token without
// a restart. LakeKeeper validates each token with the TokenReview API.
const (
	// nameOperatorServiceAccount is the identity the operator uses for
	// LakeKeeper management calls. LakeKeeper makes it the server admin at
	// bootstrap.
	nameOperatorServiceAccount = "lakekeeper-operator"
	configMapTokenProxy        = "lakekeeper-token-proxy"
	keyTokenProxyScript        = "token"
	volumeTokenProxy           = "token-proxy"
	volumeServiceAccountToken  = "lakekeeper-token"
	tokenMountDir              = "/var/run/secrets/lakekeeper"
	tokenProxyPort             = 8099
	tokenProxyRoot             = "/www"
	// tokenProxyExpiresIn is the lifetime the sidecar reports, well under the
	// projected token's, so clients come back for the rotated file.
	tokenProxyExpiresIn = 300
	// projectedTokenSeconds is the projected token lifetime. The kubelet
	// refreshes it at 80% of that.
	projectedTokenSeconds = 3600
	// operatorTokenSeconds is the lifetime of the operator's minted tokens.
	operatorTokenSeconds = 600
	uidTokenProxy        = 65534
	// kubernetesSubjectPrefix is how LakeKeeper namespaces Kubernetes users.
	kubernetesSubjectPrefix  = "kubernetes~"
	clusterRoleAuthDelegator = "system:auth-delegator"
	// kubernetesSubjectSource makes LakeKeeper subjects the TokenReview
	// username, system:serviceaccount:<namespace>:<name>.
	kubernetesSubjectSource = "username"
)

type serviceAccountTokens struct {
	enabled    bool
	audience   string
	proxyImage string
	// namespace holds the operator's ServiceAccount (the LakeKeeper namespace).
	namespace string
}

func serviceAccountTokensFor(dp *dataplatformv1alpha1.DataLake) serviceAccountTokens {
	spec := dp.Spec.Auth.ServiceAccountTokens
	if spec == nil || !dp.Spec.Auth.IsEnabled() {
		return serviceAccountTokens{}
	}
	return serviceAccountTokens{
		enabled:    true,
		audience:   spec.AudienceOrDefault(),
		proxyImage: spec.ProxyImageOrDefault(),
		namespace:  dp.Spec.Lakekeeper.NamespaceOrDefault(),
	}
}

// serviceAccountUsername is the TokenReview username of a ServiceAccount, and
// LakeKeeper's subject with subject source "username".
func serviceAccountUsername(namespace, name string) string {
	return "system:serviceaccount:" + namespace + ":" + name
}

func kubernetesPrincipal(namespace, name string) string {
	return kubernetesSubjectPrefix + serviceAccountUsername(namespace, name)
}

// useServiceAccountTokens switches the machine identities in cfg from OIDC
// clients to ServiceAccounts. Browser sign-in settings are left alone.
func useServiceAccountTokens(dp *dataplatformv1alpha1.DataLake, cfg *oidcConfig) {
	sa := serviceAccountTokensFor(dp)
	if !sa.enabled || !cfg.enabled {
		return
	}
	cfg.serviceAccounts = sa
	cfg.trinoPrincipal = kubernetesPrincipal(dp.Spec.Trino.NamespaceOrDefault(), nameTrino)
	cfg.opaPrincipal = kubernetesPrincipal(dp.Spec.Authz.OpenFGA.NamespaceOrDefault(), nameOPA)
	cfg.debeziumPrincipal = kubernetesPrincipal(dp.Spec.Debezium.NamespaceOrDefault(), nameDebezium)
}

// machineTokenURL is where Trino's Iceberg catalog and OPA fetch tokens.
func (c oidcConfig) machineTokenURL() string {
	if c.serviceAccounts.enabled {
		return fmt.Sprintf("http://127.0.0.1:%d/cgi-bin/%s", tokenProxyPort, keyTokenProxyScript)
	}
	return c.tokenURL
}

// tokenProxyScript is a CGI program for busybox httpd. It ignores the request
// and returns the current projected token in the shape of an OAuth2 token
// response, which satisfies both client-credentials and token-exchange calls.
func tokenProxyScript() string {
	return fmt.Sprintf(`#!/bin/sh
token=$(cat %s/token)
printf 'Content-Type: application/json\r\nCache-Control: no-store\r\n\r\n'
printf '{"access_token":"%%s","token_type":"Bearer","expires_in":%d,"issued_token_type":"urn:ietf:params:oauth:token-type:access_token"}' "$token"
`, tokenMountDir, tokenProxyExpiresIn)
}

func (r *DataLakeReconciler) ensureServiceAccount(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	namespace, name, component string,
	annotations map[string]string,
) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	labels := labelsFor(dp, component)
	return r.apply(ctx, dp, sa, func() error {
		ensureLabels(sa, labels)
		if len(annotations) > 0 && sa.Annotations == nil {
			sa.Annotations = map[string]string{}
		}
		maps.Copy(sa.Annotations, annotations)
		return nil
	})
}

// applyTokenReviewBinding lets LakeKeeper call TokenReview, which it needs to
// accept Kubernetes tokens.
func (r *DataLakeReconciler) applyTokenReviewBinding(ctx context.Context, dp *dataplatformv1alpha1.DataLake, lkNS string) error {
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: dp.Name + "-lakekeeper-tokenreview"}}
	labels := labelsFor(dp, componentLakekeeper)
	return r.apply(ctx, dp, binding, func() error {
		ensureLabels(binding, labels)
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRoleAuthDelegator}
		binding.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: nameLakekeeper, Namespace: lkNS}}
		return nil
	})
}

func (r *DataLakeReconciler) applyTokenProxyConfigMap(ctx context.Context, dp *dataplatformv1alpha1.DataLake, ns, component string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapTokenProxy, Namespace: ns}}
	labels := labelsFor(dp, component)
	return r.apply(ctx, dp, cm, func() error {
		ensureLabels(cm, labels)
		cm.Data = map[string]string{keyTokenProxyScript: tokenProxyScript()}
		return nil
	})
}

// serviceAccountToken mints a token for the operator's ServiceAccount.
func (r *DataLakeReconciler) serviceAccountToken(ctx context.Context, sa serviceAccountTokens) (string, error) {
	account := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: nameOperatorServiceAccount, Namespace: sa.namespace}}
	req := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
		Audiences:         []string{sa.audience},
		ExpirationSeconds: ptr.To(int64(operatorTokenSeconds)),
	}}
	if err := r.SubResource("token").Create(ctx, account, req); err != nil {
		return "", fmt.Errorf("request token for ServiceAccount %s/%s: %w", sa.namespace, nameOperatorServiceAccount, err)
	}
	if req.Status.Token == "" {
		return "", fmt.Errorf("token request for ServiceAccount %s/%s returned no token", sa.namespace, nameOperatorServiceAccount)
	}
	return req.Status.Token, nil
}

// withTokenProxy adds the token sidecar and its volumes to a pod spec.
func withTokenProxy(spec *corev1.PodSpec, sa serviceAccountTokens) {
	spec.Containers = append(spec.Containers, corev1.Container{
		Name:  volumeTokenProxy,
		Image: sa.proxyImage,
		Command: []string{
			"httpd", "-f", "-v",
			"-p", "127.0.0.1:" + strconv.Itoa(tokenProxyPort),
			"-h", tokenProxyRoot,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeTokenProxy, MountPath: tokenProxyRoot + "/cgi-bin", ReadOnly: true},
			{Name: volumeServiceAccountToken, MountPath: tokenMountDir, ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("5m"),
				corev1.ResourceMemory: resource.MustParse("8Mi"),
			},
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi")},
		},
		SecurityContext: restrictedContainerSecurity(uidTokenProxy, uidTokenProxy),
	})
	spec.Volumes = append(spec.Volumes,
		corev1.Volume{
			Name: volumeTokenProxy,
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapTokenProxy},
				DefaultMode:          ptr.To(int32(0o555)),
			}},
		},
		corev1.Volume{
			Name: volumeServiceAccountToken,
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: ptr.To(int32(0o444)),
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Audience:          sa.audience,
					ExpirationSeconds: ptr.To(int64(projectedTokenSeconds)),
					Path:              "token",
				}}},
			}},
		},
	)
}

// lakekeeperKubernetesAuthEnv turns on LakeKeeper's Kubernetes authenticator.
// Subjects come from the TokenReview username, so principal ids are
// predictable (and survive a cluster rebuild) instead of using UIDs.
func lakekeeperKubernetesAuthEnv(sa serviceAccountTokens) []corev1.EnvVar {
	if !sa.enabled {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "LAKEKEEPER__ENABLE_KUBERNETES_AUTHENTICATION", Value: strconv.FormatBool(true)},
		{Name: "LAKEKEEPER__KUBERNETES_AUTHENTICATION_AUDIENCE", Value: sa.audience},
		{Name: "LAKEKEEPER__KUBERNETES_AUTHENTICATION_SUBJECT_SOURCE", Value: kubernetesSubjectSource},
	}
}
