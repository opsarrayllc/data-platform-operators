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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

const fakeSAIssuerURL = "https://kubernetes.default.svc.cluster.local"

// fakeSAIssuer serves a fixed issuer and one RSA signing key.
type fakeSAIssuer struct {
	jwks []byte
}

func newFakeSAIssuer(t interface{ Fatal(...any) }) *fakeSAIssuer {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSAIssuer{jwks: jwksJSON(t, rsaJWK("rsa-kid_1", &key.PublicKey))}
}

func (f *fakeSAIssuer) Issuer(context.Context) (string, []byte, error) {
	return fakeSAIssuerURL, f.jwks, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func jwksJSON(t interface{ Fatal(...any) }, keys ...map[string]string) []byte {
	raw, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parsePEMKey(t *testing.T, s string) any {
	t.Helper()
	block, _ := pem.Decode([]byte(s))
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("not a PEM public key: %q", s)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestJWKSToPEM(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPub, err := ecKey.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	ec := map[string]string{
		"kty": "EC", "kid": "ec-1", "crv": "P-256",
		"x": b64(ecPub[1:33]), "y": b64(ecPub[33:]),
	}
	raw := jwksJSON(t,
		rsaJWK("rsa-1", &rsaKey.PublicKey),
		ec,
		// Skipped: unsafe key id, encryption key, unsupported type.
		rsaJWK("../etc/passwd", &rsaKey.PublicKey),
		map[string]string{"kty": "RSA", "kid": "enc", "use": "enc", "n": "AQAB", "e": "AQAB"},
		map[string]string{"kty": "oct", "kid": "hmac", "k": "c2VjcmV0"},
	)

	keys, err := jwksToPEM(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("got keys %v, want rsa-1.pem and ec-1.pem only", keys)
	}
	if pub, ok := parsePEMKey(t, keys["rsa-1.pem"]).(*rsa.PublicKey); !ok || !pub.Equal(&rsaKey.PublicKey) {
		t.Error("RSA key did not round-trip")
	}
	if pub, ok := parsePEMKey(t, keys["ec-1.pem"]).(*ecdsa.PublicKey); !ok || !pub.Equal(&ecKey.PublicKey) {
		t.Error("EC key did not round-trip")
	}
}

func TestJWKSToPEMRejectsEmptyAndBadKeys(t *testing.T) {
	if _, err := jwksToPEM([]byte(`{"keys":[]}`)); err == nil {
		t.Error("a JWKS without usable keys must fail")
	}
	if _, err := jwksToPEM([]byte(`not json`)); err == nil {
		t.Error("invalid JSON must fail")
	}
	bad := jwksJSON(t, map[string]string{"kty": "EC", "kid": "ec", "crv": "P-256", "x": b64([]byte{1}), "y": b64([]byte{2})})
	if _, err := jwksToPEM(bad); err == nil {
		t.Error("a point off the curve must fail")
	}
}

func TestTrinoCoordinatorAcceptsServiceAccountTokens(t *testing.T) {
	oidc := oidcConfig{
		enabled: true, issuer: "https://idp.example.com/", trinoClientID: "trino", trinoSecret: "s",
		trinoJWT: &trinoJWTConfig{issuer: fakeSAIssuerURL, audience: "lakekeeper"},
	}
	props := parsePropertiesString(trinoConfigProperties(true, false, "trino", nil, oidc, "", "shared"))
	want := map[string]string{
		"http-server.authentication.type":                        "oauth2,jwt",
		"http-server.authentication.jwt.key-file":                "/etc/trino/jwt-keys/${KID}.pem",
		"http-server.authentication.jwt.required-issuer":         fakeSAIssuerURL,
		"http-server.authentication.jwt.required-audience":       "lakekeeper",
		"http-server.authentication.jwt.principal-field":         "sub",
		"http-server.authentication.oauth2.user-mapping.pattern": trinoOAuth2UserPattern,
		"http-server.authentication.allow-insecure-over-http":    "false",
	}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %q, want %q", k, props[k], v)
		}
	}

	oidc.trinoJWT = nil
	props = parsePropertiesString(trinoConfigProperties(true, false, "trino", nil, oidc, "", "shared"))
	if props["http-server.authentication.type"] != "oauth2" {
		t.Errorf("without ServiceAccount tokens Trino must stay OAuth2-only, got %q", props["http-server.authentication.type"])
	}
	if _, ok := props["http-server.authentication.oauth2.user-mapping.pattern"]; ok {
		t.Error("OAuth2 user mapping is only needed next to JWT login")
	}
}

func TestTrinoPodSpecMountsJWTKeysOnlyWhenEnabled(t *testing.T) {
	has := func(jwt bool) bool {
		spec := trinoPodSpec("trino", "config.properties.coordinator", corev1.ResourceRequirements{}, nil, true, false, true, jwt, serviceAccountTokens{})
		for _, m := range spec.Containers[0].VolumeMounts {
			if m.Name == volumeTrinoJWTKeys && m.MountPath == trinoJWTKeysDir && m.SubPath == "" {
				return true
			}
		}
		return false
	}
	if !has(true) {
		t.Error("coordinator must mount the JWT keys directory")
	}
	if has(false) {
		t.Error("JWT keys must not be mounted when JWT login is off")
	}
}
