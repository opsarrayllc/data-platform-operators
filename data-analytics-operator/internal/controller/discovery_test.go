package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

// newDiscoveryServer serves a JumpCloud-shaped discovery document whose
// issuer ends in a slash.
func newDiscoveryServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "oauth2/auth",
			"token_endpoint":         issuer + "oauth2/token",
			"jwks_uri":               issuer + ".well-known/jwks.json",
			"userinfo_endpoint":      issuer + "userinfo",
		})
	}))
	t.Cleanup(srv.Close)
	issuer = srv.URL + "/"
	return srv, issuer
}

func TestResolveOIDCEndpointsDiscoversGenericIssuer(t *testing.T) {
	srv, issuer := newDiscoveryServer(t)
	r := &AnalyticsReconciler{HTTPClient: srv.Client()}
	cfg := oidcConfig{issuer: issuer, publicIssuer: issuer, tokenURL: "https://override.example/token"}
	if err := r.resolveOIDCEndpoints(context.Background(), &cfg); err != nil {
		t.Fatalf("resolveOIDCEndpoints: %v", err)
	}
	if cfg.authURL != issuer+"oauth2/auth" || cfg.jwksURL != issuer+".well-known/jwks.json" || cfg.userinfoURL != issuer+"userinfo" {
		t.Fatalf("endpoints not discovered: %+v", cfg)
	}
	if cfg.tokenURL != "https://override.example/token" {
		t.Fatalf("tokenURL = %q, want the explicit override", cfg.tokenURL)
	}
}

func TestResolveOIDCEndpointsRejectsIssuerMismatch(t *testing.T) {
	srv, issuer := newDiscoveryServer(t)
	r := &AnalyticsReconciler{HTTPClient: srv.Client()}
	cfg := oidcConfig{issuer: strings.TrimSuffix(issuer, "/")}
	err := r.resolveOIDCEndpoints(context.Background(), &cfg)
	if err == nil || !strings.Contains(err.Error(), "states issuer") {
		t.Fatalf("err = %v, want an issuer mismatch", err)
	}
}

func TestResolveOIDCEndpointsKeepsKeycloakPaths(t *testing.T) {
	r := &AnalyticsReconciler{}
	cfg := oidcConfig{
		issuer:       "http://keycloak.keycloak.svc:8080/realms/dataplatform",
		publicIssuer: "https://keycloak.data-platform.local/realms/dataplatform",
		keycloak:     true,
	}
	if err := r.resolveOIDCEndpoints(context.Background(), &cfg); err != nil {
		t.Fatalf("resolveOIDCEndpoints: %v", err)
	}
	if cfg.authURL != "https://keycloak.data-platform.local/realms/dataplatform/protocol/openid-connect/auth" {
		t.Fatalf("authURL = %q", cfg.authURL)
	}
	if cfg.tokenURL != "http://keycloak.keycloak.svc:8080/realms/dataplatform/protocol/openid-connect/token" {
		t.Fatalf("tokenURL = %q", cfg.tokenURL)
	}
}

func TestSupersetConfigGenericOIDC(t *testing.T) {
	issuer := "https://oauth.id.jumpcloud.com/"
	cfg, _ := supersetConfig(&analyticsv1alpha1.Analytics{}, oidcConfig{
		enabled:     true,
		issuer:      issuer,
		authURL:     issuer + "oauth2/auth",
		tokenURL:    issuer + "oauth2/token",
		jwksURL:     issuer + ".well-known/jwks.json",
		userinfoURL: issuer + "userinfo",
		groupsClaim: "memberOf",
	})
	for _, want := range []string{
		`"name": "oidc"`,
		`CUSTOM_SECURITY_MANAGER = OIDCSecurityManager`,
		`SUPERSET_OIDC_GROUPS_CLAIM = "memberOf"`,
		`"userinfo_endpoint": "https://oauth.id.jumpcloud.com/userinfo"`,
		`"issuer": "https://oauth.id.jumpcloud.com/"`,
		`"authorize_url": "https://oauth.id.jumpcloud.com/oauth2/auth"`,
		`"token_request_uri": "https://oauth.id.jumpcloud.com/oauth2/token"`,
		`connect_args["user"] = username`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("superset config missing %s", want)
		}
	}
	if strings.Contains(cfg, "protocol/openid-connect") {
		t.Errorf("superset config still uses Keycloak paths")
	}
}
