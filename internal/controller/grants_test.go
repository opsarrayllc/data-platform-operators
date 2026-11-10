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
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// fakeLakekeeper serves the endpoints EnsureNamespaceOwners calls.
type fakeLakekeeper struct {
	mu              sync.Mutex
	namespaces      map[string]string // name -> uuid
	calls           []string
	grants          []map[string]any
	warehouseGrants []map[string]any
	users           []map[string]any
}

func (f *fakeLakekeeper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer op-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	const nsPath = "/catalog/v1/wh-1/namespaces"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/catalog/v1/config":
		if r.URL.Query().Get("warehouse") != "keyme" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"overrides":{"prefix":"wh-1"},"defaults":{}}`))
	case r.Method == http.MethodGet && r.URL.Path == "/management/v1/warehouse":
		_, _ = w.Write([]byte(`{"warehouses":[{"id":"wh-1","name":"keyme"}]}`))
	case r.Method == http.MethodPost && r.URL.Path == "/management/v1/permissions/warehouse/wh-1/assignments":
		f.warehouseGrants = append(f.warehouseGrants, payload)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/management/v1/user":
		f.users = append(f.users, payload)
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodGet && len(r.URL.Path) > len(nsPath) && r.URL.Path[:len(nsPath)] == nsPath:
		name := r.URL.Path[len(nsPath)+1:]
		id, ok := f.namespaces[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("returnUuid") != "true" {
			id = ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"namespace": []string{name}, "namespace-uuid": id})
	case r.Method == http.MethodPost && r.URL.Path == nsPath:
		name := payload["namespace"].([]any)[0].(string)
		f.namespaces[name] = "uuid-" + name
		_, _ = w.Write(body)
	case r.Method == http.MethodPost && slices.Contains([]string{
		"/management/v1/permissions/namespace/uuid-google_analytics/assignments",
		"/management/v1/permissions/namespace/existing-uuid/assignments",
	}, r.URL.Path):
		f.grants = append(f.grants, map[string]any{"path": r.URL.Path, "body": payload})
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

func TestEnsureNamespaceOwners(t *testing.T) {
	lk := &fakeLakekeeper{namespaces: map[string]string{"marketing": "existing-uuid"}}
	server := httptest.NewServer(lk)
	defer server.Close()
	c := &proxyCatalogClient{http: server.Client(), baseURL: server.URL}

	dp := &dataplatformv1alpha1.DataLake{Spec: dataplatformv1alpha1.DataLakeSpec{
		Auth: dataplatformv1alpha1.AuthSpec{
			Embedded: ptrBool(false),
			OIDC:     &dataplatformv1alpha1.OIDCSpec{Issuer: "https://idp.example/"},
			ServiceAccountTokens: &dataplatformv1alpha1.ServiceAccountTokensSpec{
				Grants: []dataplatformv1alpha1.ServiceAccountGrant{{
					Namespace: "argo-workflows", Name: "ga4-loader",
					IcebergNamespaces: []string{"google_analytics", "marketing"},
				}},
			},
		},
	}}
	owners := namespaceOwners(dp)
	if len(owners) != 1 {
		t.Fatalf("owners = %v", owners)
	}
	for range 2 { // idempotent
		if err := c.EnsureNamespaceOwners(context.Background(), "lakekeeper", "lakekeeper", 8181, "op-token", "keyme", owners); err != nil {
			t.Fatal(err)
		}
	}

	const subject = "kubernetes~system:serviceaccount:argo-workflows:ga4-loader"
	if got := lk.namespaces["google_analytics"]; got != "uuid-google_analytics" {
		t.Errorf("google_analytics not created: %v", lk.namespaces)
	}
	creates := 0
	for _, call := range lk.calls {
		if call == "POST /catalog/v1/wh-1/namespaces" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("namespace created %d times, want once: %v", creates, lk.calls)
	}
	if len(lk.users) != 2 || lk.users[0]["id"] != subject || lk.users[0]["user-type"] != "application" {
		t.Errorf("users = %v", lk.users)
	}
	if len(lk.warehouseGrants) != 2 {
		t.Fatalf("warehouse grants = %v", lk.warehouseGrants)
	}
	if w := lk.warehouseGrants[0]["writes"].([]any)[0].(map[string]any); w["type"] != "describe" || w["user"] != subject {
		t.Errorf("warehouse grant %v", w)
	}
	if len(lk.grants) != 4 {
		t.Fatalf("grants = %v", lk.grants)
	}
	for _, g := range lk.grants {
		writes := g["body"].(map[string]any)["writes"].([]any)
		w := writes[0].(map[string]any)
		if w["type"] != "ownership" || w["user"] != subject {
			t.Errorf("grant %v", g)
		}
	}
}

func TestNamespaceOwnersNeedServiceAccountTokens(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	if owners := namespaceOwners(dp); owners != nil {
		t.Errorf("owners without serviceAccountTokens = %v", owners)
	}
}

func TestParseCatalogPrefix(t *testing.T) {
	if _, err := parseCatalogPrefix([]byte(`{"overrides":{}}`), "w"); err == nil {
		t.Error("want an error without a prefix")
	}
	if p, _ := parseCatalogPrefix([]byte(`{"defaults":{"prefix":"x"}}`), "w"); p != "x" {
		t.Errorf("prefix = %q", p)
	}
}

func ptrBool(b bool) *bool { return &b }
