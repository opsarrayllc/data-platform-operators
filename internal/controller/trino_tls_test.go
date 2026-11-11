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
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestGenerateTrinoTLSVerifiesForServiceNames(t *testing.T) {
	now := time.Now()
	caPEM, keystore, err := generateTrinoTLS("trino", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(keystore), "-----BEGIN PRIVATE KEY-----") {
		t.Fatal("keystore must start with the PKCS#8 private key for Trino's PEM keystore")
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM did not parse")
	}
	_, rest := pem.Decode(keystore) // private key
	block, _ := pem.Decode(rest)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"trino.trino.svc", "trino.trino.svc.cluster.local", "localhost", "127.0.0.1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, CurrentTime: now}); err != nil {
			t.Errorf("certificate does not verify for %s: %v", host, err)
		}
	}
}

func TestTrinoTLSUsable(t *testing.T) {
	now := time.Now()
	caPEM, keystore, err := generateTrinoTLS("trino", now)
	if err != nil {
		t.Fatal(err)
	}
	if !trinoTLSUsable(caPEM, keystore, "trino", now) {
		t.Error("fresh certificate must be reused")
	}
	if trinoTLSUsable(caPEM, keystore, "analytics", now) {
		t.Error("certificate for another namespace must be regenerated")
	}
	if trinoTLSUsable(caPEM, keystore, "trino", now.Add(trinoTLSValidity-trinoTLSRenewBefore/2)) {
		t.Error("certificate close to expiry must be regenerated")
	}
	if trinoTLSUsable(nil, keystore, "trino", now) || trinoTLSUsable(caPEM, []byte("garbage"), "trino", now) {
		t.Error("missing or unparseable data must be regenerated")
	}
}

func TestTrinoCoordinatorRequiresSecureAuthentication(t *testing.T) {
	oidc := oidcConfig{enabled: true, issuer: "https://idp.example.com/", trinoClientID: "trino", trinoSecret: "s"}
	props := parsePropertiesString(trinoConfigProperties(true, false, "trino", nil, oidc, "", "shared"))
	want := map[string]string{
		"http-server.authentication.type":                     "oauth2",
		"http-server.authentication.allow-insecure-over-http": "false",
		"http-server.https.enabled":                           "true",
		"http-server.https.port":                              "8443",
		"http-server.https.keystore.path":                     "/etc/trino/tls/keystore.pem",
		// Workers and the coordinator still talk over HTTP with the shared secret.
		"http-server.http.port":                "8080",
		"internal-communication.shared-secret": "shared",
	}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %q, want %q", k, props[k], v)
		}
	}

	worker := parsePropertiesString(trinoConfigProperties(false, false, "trino", nil, oidcConfig{}, "", "shared"))
	if _, ok := worker["http-server.https.enabled"]; ok {
		t.Error("workers serve no clients and need no HTTPS listener")
	}
	if got := trinoClientURL("trino", true); got != "https://trino.trino.svc:8443" {
		t.Errorf("trinoClientURL = %q", got)
	}
}

func parsePropertiesString(s string) map[string]string {
	props := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	return props
}
