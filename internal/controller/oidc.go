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
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

type oidcConfig struct {
	enabled          bool
	issuer           string
	publicIssuer     string
	audience         string
	scope            string
	uiClientID       string
	uiTokenType      string
	subjectClaim     string
	trinoClientID    string
	trinoSecret      string
	opaClientID      string
	opaSecret        string
	operatorClientID string
	operatorSecret   string
	// adminSubject is the OIDC subject of the human admin the operator manages,
	// empty for external issuers where the operator does not own the user list.
	adminSubject string
	adminName    string
	adminEmail   string
	// opaPrincipal is the LakeKeeper user id of the OPA bridge, which needs
	// its own grant to inspect other users' permissions.
	opaPrincipal string
	// trinoPrincipal is the LakeKeeper user id of Trino's Iceberg REST
	// connector, which needs project access to resolve warehouses.
	trinoPrincipal string
	// serviceAccounts replaces the machine OIDC clients with Kubernetes
	// ServiceAccount tokens (spec.auth.serviceAccountTokens).
	serviceAccounts serviceAccountTokens

	tokenURL       string
	proxyNamespace string
	proxyService   string
	proxyPort      int32
	proxyTokenPath string
}

func (r *DataLakeReconciler) reconcileAuth(ctx context.Context, dp *dataplatformv1alpha1.DataLake) (oidcConfig, bool, error) {
	if !dp.Spec.Auth.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionTrue, reasonDisabled, "Authentication is disabled")
		return oidcConfig{}, true, nil
	}
	if dp.Spec.Auth.IsEmbedded() {
		cfg, ready, err := r.reconcileKeycloak(ctx, dp)
		useServiceAccountTokens(dp, &cfg)
		return cfg, ready, err
	}
	cfg, err := r.externalOIDC(ctx, dp)
	if err != nil {
		return oidcConfig{}, false, err
	}
	useServiceAccountTokens(dp, &cfg)
	setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionTrue, reasonReady, "Using external OIDC issuer")
	return cfg, true, nil
}

func (r *DataLakeReconciler) externalOIDC(ctx context.Context, dp *dataplatformv1alpha1.DataLake) (oidcConfig, error) {
	spec := dp.Spec.Auth.OIDC
	if spec == nil || spec.Issuer == "" {
		err := fmt.Errorf("spec.auth.oidc.issuer is required when auth.embedded is false")
		setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionFalse, reasonMissing, err.Error())
		return oidcConfig{}, err
	}
	if spec.CredentialsSecretRef == nil || spec.CredentialsSecretRef.Name == "" {
		err := fmt.Errorf("spec.auth.oidc.credentialsSecretRef is required when auth.embedded is false")
		setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionFalse, reasonMissing, err.Error())
		return oidcConfig{}, err
	}

	ref := spec.CredentialsSecretRef
	clientID := spec.ClientIDOrDefault()
	if ref.ClientIDKey != "" {
		val, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.ClientIDKey)
		if err != nil {
			setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionFalse, reasonError, err.Error())
			return oidcConfig{}, err
		}
		clientID = val
	}
	clientSecret, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.ClientSecretKeyOrDefault())
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionFalse, reasonError, err.Error())
		return oidcConfig{}, err
	}

	trinoID := clientID
	trinoSecret := clientSecret
	if ref.TrinoClientIDKey != "" {
		trinoID, err = r.getSecretData(ctx, ref.Name, ref.Namespace, ref.TrinoClientIDKey)
		if err != nil {
			setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionFalse, reasonError, err.Error())
			return oidcConfig{}, err
		}
	}
	if ref.TrinoClientSecretKey != "" {
		trinoSecret, err = r.getSecretData(ctx, ref.Name, ref.Namespace, ref.TrinoClientSecretKey)
		if err != nil {
			setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionFalse, reasonError, err.Error())
			return oidcConfig{}, err
		}
	}

	// Keep the issuer exactly as written. Validators compare it to the token's
	// iss claim, and some providers (JumpCloud) end it with a slash.
	issuer := spec.Issuer
	return oidcConfig{
		enabled:          true,
		issuer:           issuer,
		audience:         spec.AudienceOrDefault(),
		scope:            spec.ScopeOrDefault(),
		uiClientID:       clientID,
		uiTokenType:      spec.UITokenType,
		subjectClaim:     spec.SubjectClaim,
		trinoClientID:    trinoID,
		trinoSecret:      trinoSecret,
		opaClientID:      trinoID,
		opaSecret:        trinoSecret,
		operatorClientID: clientID,
		operatorSecret:   clientSecret,
		tokenURL:         spec.TokenEndpointOrDefault(),
	}, nil
}

func (r *DataLakeReconciler) oidcAccessToken(ctx context.Context, cfg oidcConfig) (string, error) {
	if !cfg.enabled {
		return "", nil
	}
	if cfg.serviceAccounts.enabled {
		return r.serviceAccountToken(ctx, cfg.serviceAccounts)
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {cfg.operatorClientID},
		"client_secret": {cfg.operatorSecret},
	}
	if cfg.scope != "" {
		form.Set("scope", cfg.scope)
	}

	if cfg.proxyService != "" {
		if r.Catalog == nil {
			return "", fmt.Errorf("catalog client is not configured")
		}
		status, body, err := r.Catalog.FormPost(ctx, cfg.proxyNamespace, cfg.proxyService, cfg.proxyPort, cfg.proxyTokenPath, form)
		if err != nil {
			return "", err
		}
		if status < 200 || status >= 300 {
			return "", fmt.Errorf("token endpoint returned %d: %s", status, truncate(body))
		}
		return parseAccessToken(body)
	}

	if r.Catalog == nil {
		return "", fmt.Errorf("catalog client is not configured")
	}
	status, body, err := r.Catalog.FetchToken(ctx, cfg.tokenURL, form)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("token endpoint returned %d: %s", status, truncate(body))
	}
	return parseAccessToken(body)
}

func parseAccessToken(body []byte) (string, error) {
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}
	return parsed.AccessToken, nil
}

func oidcIssuer(base, realm string) string {
	return strings.TrimRight(base, "/") + "/realms/" + realm
}

func oidcTokenPath(realm string) string {
	return "realms/" + realm + "/protocol/openid-connect/token"
}

func oidcTokenURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/protocol/openid-connect/token"
}

func oidcAuthURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/protocol/openid-connect/auth"
}

func oidcJWKSURL(issuer string) string {
	return strings.TrimRight(issuer, "/") + "/protocol/openid-connect/certs"
}
