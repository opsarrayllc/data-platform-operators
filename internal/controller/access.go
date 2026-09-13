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
	"net/http"
	"net/url"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// accessGroup is a Keycloak group that maps onto a LakeKeeper role.
type accessGroup struct {
	Name               string
	Description        string
	ServerRelations    []string
	ProjectRelations   []string
	WarehouseRelations []string
}

func defaultAccessGroups() []accessGroup {
	return []accessGroup{
		{
			Name:             dataplatformv1alpha1.DefaultGroupPlatformAdmins,
			Description:      "Full control of the catalog, warehouse, and project",
			ServerRelations:  []string{catalogRelationAdmin},
			ProjectRelations: []string{catalogRelationProjectAdmin},
		},
		{
			Name:               dataplatformv1alpha1.DefaultGroupDataEngineers,
			Description:        "Create and modify tables in the default warehouse",
			ProjectRelations:   []string{"describe", "select", "create", "modify"},
			WarehouseRelations: []string{"describe", "select", "create", "modify"},
		},
		{
			Name:               dataplatformv1alpha1.DefaultGroupAnalysts,
			Description:        "Read tables in the default warehouse",
			ProjectRelations:   []string{"describe", "select"},
			WarehouseRelations: []string{"describe", "select"},
		},
	}
}

func (r *DataPlatformReconciler) reconcileAccessGroups(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	oidc oidcConfig,
	fga openfgaConfig,
) error {
	if !oidc.enabled || !dp.Spec.Auth.IsEmbedded() {
		return nil
	}
	log := logf.FromContext(ctx)
	members, err := r.accessGroupMembers(ctx, dp)
	if err != nil {
		return err
	}

	roles := make([]CatalogAccessRole, 0, len(defaultAccessGroups()))
	var tuples []AuthzTuple
	for _, group := range defaultAccessGroups() {
		roles = append(roles, CatalogAccessRole{
			Name:               group.Name,
			Description:        group.Description,
			ServerRelations:    group.ServerRelations,
			ProjectRelations:   group.ProjectRelations,
			WarehouseRelations: group.WarehouseRelations,
			Members:            members[group.Name],
		})
		for _, member := range members[group.Name] {
			tuples = append(tuples, AuthzTuple{
				User:     "user:" + strings.TrimPrefix(member.Subject, oidcSubjectPrefix),
				Relation: authzGroupRelation,
				Object:   authzGroupType + ":" + group.Name,
			})
		}
	}

	ns := dp.Spec.Lakekeeper.NamespaceOrDefault()
	warehouse := dp.Spec.Lakekeeper.Warehouse.NameOrDefault()
	token, err := r.oidcAccessToken(ctx, oidc)
	if err != nil {
		return err
	}
	if err := r.Catalog.EnsureAccessRoles(ctx, ns, nameLakekeeper, lakekeeperPort, token, warehouse, roles); err != nil {
		return err
	}
	log.Info("Ensured LakeKeeper access roles", "roles", len(roles))

	if fga.rowFilterStoreID == "" || len(tuples) == 0 {
		return nil
	}
	openfgaNS := dp.Spec.Authz.OpenFGA.NamespaceOrDefault()
	if err := r.Catalog.EnsureAuthzTuples(ctx, openfgaNS, nameOpenFGA, openfgaHTTPPort, fga.apiKey, fga.rowFilterStoreID, tuples); err != nil {
		return err
	}
	log.Info("Ensured OpenFGA group membership tuples", "tuples", len(tuples))
	return nil
}

func (r *DataPlatformReconciler) accessGroupMembers(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
) (map[string][]CatalogPrincipal, error) {
	if r.Catalog == nil {
		return nil, fmt.Errorf("catalog client is not configured")
	}
	ns := dp.Spec.Auth.Keycloak.NamespaceOrDefault()
	realm := dp.Spec.Auth.Keycloak.RealmOrDefault()
	user, err := r.getSecretData(ctx, secretKeycloakAdmin, ns, keyKeycloakAdminUser)
	if err != nil {
		return nil, err
	}
	password, err := r.getSecretData(ctx, secretKeycloakAdmin, ns, keyKeycloakAdminPassword)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {user},
		"password":   {password},
	}
	status, body, err := r.Catalog.FormPost(ctx, ns, nameKeycloak, keycloakPort, "realms/master/protocol/openid-connect/token", form)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("keycloak admin token returned %d: %s", status, truncate(body))
	}
	token, err := parseAccessToken(body)
	if err != nil {
		return nil, err
	}

	wanted := map[string]bool{}
	for _, group := range defaultAccessGroups() {
		wanted[group.Name] = true
	}
	status, body, err = r.Catalog.JSON(ctx, http.MethodGet, ns, nameKeycloak, keycloakPort,
		"admin/realms/"+url.PathEscape(realm)+"/groups", token, nil)
	if err != nil {
		return nil, err
	}
	if !isSuccess(status) {
		return nil, fmt.Errorf("list Keycloak groups returned %d: %s", status, truncate(body))
	}
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &groups); err != nil {
		return nil, fmt.Errorf("parse Keycloak groups: %w", err)
	}

	out := map[string][]CatalogPrincipal{}
	for _, group := range groups {
		if !wanted[group.Name] {
			continue
		}
		members, err := r.keycloakGroupMembers(ctx, ns, realm, token, group.ID)
		if err != nil {
			return nil, err
		}
		out[group.Name] = members
	}
	return out, nil
}

func (r *DataPlatformReconciler) keycloakGroupMembers(
	ctx context.Context,
	ns, realm, token, groupID string,
) ([]CatalogPrincipal, error) {
	path := "admin/realms/" + url.PathEscape(realm) + "/groups/" + url.PathEscape(groupID) + "/members?max=500"
	status, body, err := r.Catalog.JSON(ctx, http.MethodGet, ns, nameKeycloak, keycloakPort, path, token, nil)
	if err != nil {
		return nil, err
	}
	if !isSuccess(status) {
		return nil, fmt.Errorf("list Keycloak group members returned %d: %s", status, truncate(body))
	}
	var members []struct {
		ID            string `json:"id"`
		Username      string `json:"username"`
		Email         string `json:"email"`
		FirstName     string `json:"firstName"`
		LastName      string `json:"lastName"`
		ServiceClient string `json:"serviceAccountClientId"`
	}
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, fmt.Errorf("parse Keycloak group members: %w", err)
	}
	principals := make([]CatalogPrincipal, 0, len(members))
	for _, member := range members {
		if member.ID == "" || member.ServiceClient != "" || strings.HasPrefix(member.Username, "service-account-") {
			continue
		}
		name := strings.TrimSpace(member.FirstName + " " + member.LastName)
		if name == "" {
			name = member.Username
		}
		principals = append(principals, CatalogPrincipal{
			Subject: oidcSubjectPrefix + member.ID,
			Name:    name,
			Email:   member.Email,
			Type:    "human",
		})
	}
	return principals, nil
}
