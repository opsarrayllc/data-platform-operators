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
	"io"
	"net/http"
	"strings"
	"time"
)

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

// isKeycloakIssuer reports whether the issuer is a Keycloak realm.
func isKeycloakIssuer(issuer string) bool {
	return strings.Contains(issuer, "/realms/")
}

// resolveOIDCEndpoints fills unset endpoints. Keycloak issuers get Keycloak's
// paths: the backchannel uses the in-cluster issuer and browsers the public
// one, which Keycloak's own discovery document cannot express. Other issuers
// are discovered.
func (r *AnalyticsReconciler) resolveOIDCEndpoints(ctx context.Context, cfg *oidcConfig) error {
	if cfg.keycloak {
		backchannel := strings.TrimRight(cfg.issuer, "/") + "/protocol/openid-connect/"
		browser := strings.TrimRight(cfg.publicIssuer, "/") + "/protocol/openid-connect/"
		cfg.authURL = firstNonEmpty(cfg.authURL, browser+"auth")
		cfg.tokenURL = firstNonEmpty(cfg.tokenURL, backchannel+"token")
		cfg.jwksURL = firstNonEmpty(cfg.jwksURL, backchannel+"certs")
		cfg.userinfoURL = firstNonEmpty(cfg.userinfoURL, backchannel+"userinfo")
		return nil
	}
	if cfg.authURL != "" && cfg.tokenURL != "" && cfg.jwksURL != "" && cfg.userinfoURL != "" {
		return nil
	}
	doc, err := r.discoverOIDC(ctx, cfg.issuer)
	if err != nil {
		return err
	}
	cfg.authURL = firstNonEmpty(cfg.authURL, doc.AuthorizationEndpoint)
	cfg.tokenURL = firstNonEmpty(cfg.tokenURL, doc.TokenEndpoint)
	cfg.jwksURL = firstNonEmpty(cfg.jwksURL, doc.JWKSURI)
	cfg.userinfoURL = firstNonEmpty(cfg.userinfoURL, doc.UserinfoEndpoint)
	if cfg.authURL == "" || cfg.tokenURL == "" || cfg.jwksURL == "" {
		return fmt.Errorf("OIDC discovery for %s is missing authorization, token, or JWKS endpoints", cfg.issuer)
	}
	return nil
}

func (r *AnalyticsReconciler) discoverOIDC(ctx context.Context, issuer string) (oidcDiscovery, error) {
	url := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return oidcDiscovery{}, err
	}
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("fetch OIDC discovery %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oidcDiscovery{}, fmt.Errorf("read OIDC discovery %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return oidcDiscovery{}, fmt.Errorf("OIDC discovery %s returned %d", url, resp.StatusCode)
	}
	var doc oidcDiscovery
	if err := json.Unmarshal(body, &doc); err != nil {
		return oidcDiscovery{}, fmt.Errorf("parse OIDC discovery %s: %w", url, err)
	}
	// The issuer must match exactly, or every token validation fails later.
	if doc.Issuer != issuer {
		return oidcDiscovery{}, fmt.Errorf("OIDC discovery %s states issuer %q, but spec.auth.oidc.issuer is %q", url, doc.Issuer, issuer)
	}
	return doc, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
