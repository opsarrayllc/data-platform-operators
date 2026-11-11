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
	"testing"

	"k8s.io/utils/ptr"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

func TestTrinoConnectionDefaults(t *testing.T) {
	plain := analyticsv1alpha1.AnalyticsSpec{}
	if plain.TrinoSchemeOrDefault() != "http" || plain.TrinoPortOrDefault() != 8080 || plain.TrinoCAOrDefault() != nil {
		t.Errorf("without auth: %s:%d ca=%v, want http:8080 and no CA",
			plain.TrinoSchemeOrDefault(), plain.TrinoPortOrDefault(), plain.TrinoCAOrDefault())
	}

	authed := analyticsv1alpha1.AnalyticsSpec{Auth: analyticsv1alpha1.AuthSpec{Enabled: ptr.To(true)}}
	if authed.TrinoSchemeOrDefault() != "https" || authed.TrinoPortOrDefault() != 8443 {
		t.Errorf("with auth: %s:%d, want https:8443", authed.TrinoSchemeOrDefault(), authed.TrinoPortOrDefault())
	}
	if ca := authed.TrinoCAOrDefault(); ca == nil || *ca != (analyticsv1alpha1.ConfigMapKeyRef{Name: "trino-ca", Namespace: "trino", Key: "ca.crt"}) {
		t.Errorf("with auth: CA = %+v, want trino/trino-ca ca.crt", ca)
	}

	custom := authed
	custom.Trino = analyticsv1alpha1.TrinoSpec{Port: 443, CAConfigMapRef: &analyticsv1alpha1.ConfigMapKeyRef{Namespace: "lake"}}
	if custom.TrinoPortOrDefault() != 443 {
		t.Errorf("explicit port = %d, want 443", custom.TrinoPortOrDefault())
	}
	if ca := custom.TrinoCAOrDefault(); ca.Namespace != "lake" || ca.Name != "trino-ca" || ca.Key != "ca.crt" {
		t.Errorf("partial CA ref = %+v, want unset fields defaulted", ca)
	}

	optOut := authed
	optOut.Trino = analyticsv1alpha1.TrinoSpec{HTTPScheme: "http"}
	if optOut.TrinoPortOrDefault() != 8080 || optOut.TrinoCAOrDefault() != nil {
		t.Error("explicit http must use 8080 and no CA")
	}
}
