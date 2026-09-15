package controller

import (
	"encoding/json"
	"testing"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// TestRealmPinsSigningKey guards the realm import against losing the operator's
// RS256 key. Keycloak generates a new one whenever a realm has no key provider,
// which would invalidate every token issued before a pod recreate.
func TestRealmPinsSigningKey(t *testing.T) {
	key, cert, err := generateRealmSigningKey("dataplatform")
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	raw, err := keycloakRealmJSON(realmOptions{
		spec:                dataplatformv1alpha1.KeycloakSpec{},
		adminPassword:       "adminpass",
		trinoSecret:         "trinosecret",
		operatorSecret:      "operatorsecret",
		opaSecret:           "opasecret",
		flinkSecret:         "flinksecret",
		supersetSecret:      "supersetsecret",
		signingKey:          key,
		signingCertificate:  cert,
		lakekeeperNamespace: "lakekeeper",
	})
	if err != nil {
		t.Fatalf("render realm: %v", err)
	}

	var realm struct {
		Components struct {
			KeyProviders []struct {
				ProviderID string              `json:"providerId"`
				Config     map[string][]string `json:"config"`
			} `json:"org.keycloak.keys.KeyProvider"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(raw), &realm); err != nil {
		t.Fatalf("realm is not valid JSON: %v", err)
	}

	var imported *struct {
		ProviderID string              `json:"providerId"`
		Config     map[string][]string `json:"config"`
	}
	providers := map[string]bool{}
	for i := range realm.Components.KeyProviders {
		p := &realm.Components.KeyProviders[i]
		providers[p.ProviderID] = true
		if p.ProviderID == "rsa" {
			imported = p
		}
	}
	if imported == nil {
		t.Fatal("realm has no imported rsa key provider")
	}
	if got := imported.Config["privateKey"]; len(got) != 1 || got[0] != key {
		t.Errorf("imported provider does not carry the generated private key")
	}
	if got := imported.Config["certificate"]; len(got) != 1 || got[0] != cert {
		t.Errorf("imported provider does not carry the generated certificate")
	}
	if got := imported.Config["algorithm"]; len(got) != 1 || got[0] != "RS256" {
		t.Errorf("imported provider algorithm = %v, want RS256", got)
	}
	// Declaring a provider suppresses the defaults Keycloak would create, so
	// the ones other flows need have to be declared alongside it.
	for _, want := range []string{"hmac-generated", "aes-generated", "rsa-enc-generated"} {
		if !providers[want] {
			t.Errorf("realm is missing the %s key provider", want)
		}
	}
}

// TestRealmSigningKeysAreUnique catches a generator that returns a fixed key.
func TestRealmSigningKeysAreUnique(t *testing.T) {
	first, _, err := generateRealmSigningKey("dataplatform")
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	second, _, err := generateRealmSigningKey("dataplatform")
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	if first == second {
		t.Error("two generated keys are identical")
	}
}

func TestRealmAccessGroups(t *testing.T) {
	key, cert, err := generateRealmSigningKey("dataplatform")
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	raw, err := keycloakRealmJSON(realmOptions{
		spec:                dataplatformv1alpha1.KeycloakSpec{},
		adminPassword:       "adminpass",
		trinoSecret:         "trinosecret",
		operatorSecret:      "operatorsecret",
		opaSecret:           "opasecret",
		flinkSecret:         "flinksecret",
		supersetSecret:      "supersetsecret",
		signingKey:          key,
		signingCertificate:  cert,
		lakekeeperNamespace: "lakekeeper",
	})
	if err != nil {
		t.Fatalf("render realm: %v", err)
	}

	var realm struct {
		Roles struct {
			Realm []struct {
				Name string `json:"name"`
			} `json:"realm"`
		} `json:"roles"`
		Groups []struct {
			Name       string   `json:"name"`
			Path       string   `json:"path"`
			RealmRoles []string `json:"realmRoles"`
		} `json:"groups"`
		Users []struct {
			Username string   `json:"username"`
			Groups   []string `json:"groups"`
		} `json:"users"`
		ClientScopes []struct {
			Name            string `json:"name"`
			ProtocolMappers []struct {
				ProtocolMapper string `json:"protocolMapper"`
				Config         struct {
					ClaimName string `json:"claim.name"`
				} `json:"config"`
			} `json:"protocolMappers"`
		} `json:"clientScopes"`
	}
	if err := json.Unmarshal([]byte(raw), &realm); err != nil {
		t.Fatalf("realm is not valid JSON: %v", err)
	}

	wantGroups := []string{
		dataplatformv1alpha1.DefaultGroupPlatformAdmins,
		dataplatformv1alpha1.DefaultGroupDataEngineers,
		dataplatformv1alpha1.DefaultGroupAnalysts,
	}
	gotRoles := map[string]bool{}
	for _, role := range realm.Roles.Realm {
		gotRoles[role.Name] = true
	}
	gotGroups := map[string]bool{}
	for _, group := range realm.Groups {
		gotGroups[group.Name] = true
		if group.Path != "/"+group.Name {
			t.Errorf("group %s path = %q", group.Name, group.Path)
		}
		if len(group.RealmRoles) != 1 || group.RealmRoles[0] != group.Name {
			t.Errorf("group %s realmRoles = %v", group.Name, group.RealmRoles)
		}
	}
	for _, name := range wantGroups {
		if !gotRoles[name] {
			t.Errorf("realm is missing role %s", name)
		}
		if !gotGroups[name] {
			t.Errorf("realm is missing group %s", name)
		}
	}

	foundAdmin := false
	for _, user := range realm.Users {
		if user.Username != dataplatformv1alpha1.DefaultOIDCAdminUser {
			continue
		}
		foundAdmin = true
		if len(user.Groups) != 1 || user.Groups[0] != "/"+dataplatformv1alpha1.DefaultGroupPlatformAdmins {
			t.Errorf("admin groups = %v", user.Groups)
		}
	}
	if !foundAdmin {
		t.Fatal("realm is missing the admin user")
	}

	foundGroupsClaim := false
	for _, scope := range realm.ClientScopes {
		for _, mapper := range scope.ProtocolMappers {
			if mapper.ProtocolMapper == "oidc-group-membership-mapper" && mapper.Config.ClaimName == "groups" {
				foundGroupsClaim = true
			}
		}
	}
	if !foundGroupsClaim {
		t.Error("realm tokens do not include a groups claim")
	}
}
