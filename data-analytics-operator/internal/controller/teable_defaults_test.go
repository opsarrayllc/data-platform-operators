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
	"net/url"
	"testing"

	"k8s.io/utils/ptr"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

func TestTeableStoragePublicURL(t *testing.T) {
	cases := []struct {
		name string
		spec analyticsv1alpha1.TeableStorageSpec
		want string
	}{
		{
			name: "path style MinIO",
			spec: analyticsv1alpha1.TeableStorageSpec{Endpoint: "https://minio-api.data-platform.local/", ForcePathStyle: ptr.To(true)},
			want: "https://minio-api.data-platform.local/teable-public",
		},
		{
			name: "virtual-hosted S3",
			spec: analyticsv1alpha1.TeableStorageSpec{Endpoint: "https://s3.eu-west-1.amazonaws.com", PublicBucket: "acme-teable-public"},
			want: "https://acme-teable-public.s3.eu-west-1.amazonaws.com",
		},
		{
			name: "explicit",
			spec: analyticsv1alpha1.TeableStorageSpec{Endpoint: "https://s3.amazonaws.com", PublicURL: "https://cdn.example.com/assets/"},
			want: "https://cdn.example.com/assets",
		},
	}
	for _, tc := range cases {
		if got := tc.spec.PublicURLOrDefault(); got != tc.want {
			t.Errorf("%s: PublicURLOrDefault() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTeableDatabaseURLEscapesCredentials(t *testing.T) {
	conn := postgresConn{Host: "pg.example.com", Port: 5432, Database: "teable", SSLMode: "require"}
	raw := teableDatabaseURL(conn, "app@corp", "p@ss/w:rd?#")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	pass, _ := u.User.Password()
	if u.User.Username() != "app@corp" || pass != "p@ss/w:rd?#" {
		t.Errorf("credentials round-trip as %q / %q", u.User.Username(), pass)
	}
	if u.Host != "pg.example.com:5432" || u.Path != "/teable" || u.Query().Get("sslmode") != "require" {
		t.Errorf("unexpected URL %q", raw)
	}
}

func TestTeableGeneratedKeysFitAES128(t *testing.T) {
	keys, err := teableGeneratedKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range teableAESSlots {
		if len(keys[slot]) != 16 {
			t.Errorf("%s has %d characters, Teable needs exactly 16", slot, len(keys[slot]))
		}
	}
	if len(keys["SECRET_KEY"]) < 32 {
		t.Errorf("SECRET_KEY is too short: %d", len(keys["SECRET_KEY"]))
	}
}
