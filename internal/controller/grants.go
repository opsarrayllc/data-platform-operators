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

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// NamespaceOwner is a principal that owns Iceberg namespaces in the managed
// warehouse (spec.auth.serviceAccountTokens.grants).
type NamespaceOwner struct {
	Principal  CatalogPrincipal
	Namespaces []string
}

// namespaceOwners lists the ServiceAccount grants. They need the Kubernetes
// authenticator, so they are ignored unless ServiceAccount tokens are on.
func namespaceOwners(dp *dataplatformv1alpha1.DataLake) []NamespaceOwner {
	if !serviceAccountTokensFor(dp).enabled {
		return nil
	}
	var owners []NamespaceOwner
	for _, g := range dp.Spec.Auth.ServiceAccountTokens.Grants {
		owners = append(owners, NamespaceOwner{
			Principal: CatalogPrincipal{
				Subject: kubernetesPrincipal(g.Namespace, g.Name),
				Name:    g.Name,
				Type:    catalogUserTypeApplication,
			},
			Namespaces: g.IcebergNamespaces,
		})
	}
	return owners
}

// EnsureNamespaceOwners provisions each owner, grants it describe on the
// warehouse, creates its namespaces when missing, and grants it ownership on
// them. The operator owns
// the warehouse it created, so it may create namespaces and pass ownership.
func (c *proxyCatalogClient) EnsureNamespaceOwners(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, warehouseName string,
	owners []NamespaceOwner,
) error {
	if len(owners) == 0 {
		return nil
	}
	prefix, err := c.catalogPrefix(ctx, namespace, service, port, bearer, warehouseName)
	if err != nil {
		return err
	}
	warehouseID, err := c.warehouseID(ctx, namespace, service, port, bearer, warehouseName)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		p := owner.Principal
		if err := c.post(ctx, namespace, service, port,
			"management/v1/user", bearer, catalogUserJSON(p), "provision user "+p.Name); err != nil {
			return err
		}
		// Every catalog request checks can_use on the warehouse, defined as
		// "describe or can_get_metadata from namespace". Without describe,
		// OpenFGA walks every namespace and table in the warehouse to answer
		// it, which takes tens of thousands of datastore queries per request
		// and times out. describe shows names and schemas, not data.
		describe := namespaceAssignments{Writes: []namespaceAssignment{{Type: catalogRelationDescribe, User: p.Subject}}}
		if err := c.post(ctx, namespace, service, port,
			"management/v1/permissions/warehouse/"+warehouseID+"/assignments", bearer, describe,
			"grant describe on warehouse "+warehouseName+" to "+p.Name); err != nil {
			return err
		}
		for _, ns := range owner.Namespaces {
			id, err := c.ensureIcebergNamespace(ctx, namespace, service, port, bearer, prefix, ns)
			if err != nil {
				return err
			}
			grant := namespaceAssignments{Writes: []namespaceAssignment{{Type: "ownership", User: p.Subject}}}
			if err := c.post(ctx, namespace, service, port,
				"management/v1/permissions/namespace/"+id+"/assignments", bearer, grant,
				"grant ownership of namespace "+ns+" to "+p.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// namespaceAssignments is the body of
// POST management/v1/permissions/{namespace,warehouse}/{id}/assignments.
type namespaceAssignments struct {
	Writes []namespaceAssignment `json:"writes"`
}

type namespaceAssignment struct {
	Type string `json:"type"`
	User string `json:"user"`
}

// catalogPrefix is the Iceberg REST path prefix of a warehouse, which
// LakeKeeper returns from the config endpoint.
func (c *proxyCatalogClient) catalogPrefix(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, warehouseName string,
) (string, error) {
	status, body, err := c.do(ctx, http.MethodGet, namespace, service, port,
		"catalog/v1/config?warehouse="+url.QueryEscape(warehouseName), bearer, nil)
	if err != nil {
		return "", err
	}
	if !isSuccess(status) {
		return "", fmt.Errorf("catalog config for warehouse %s returned %d: %s", warehouseName, status, truncate(body))
	}
	return parseCatalogPrefix(body, warehouseName)
}

func parseCatalogPrefix(body []byte, warehouseName string) (string, error) {
	var cfg struct {
		Overrides map[string]string `json:"overrides"`
		Defaults  map[string]string `json:"defaults"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("parse catalog config: %w", err)
	}
	if p := cfg.Overrides["prefix"]; p != "" {
		return p, nil
	}
	if p := cfg.Defaults["prefix"]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("catalog config for warehouse %s has no prefix", warehouseName)
}

// ensureIcebergNamespace creates a top-level namespace if it is missing and
// returns its LakeKeeper id.
func (c *proxyCatalogClient) ensureIcebergNamespace(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, prefix, name string,
) (string, error) {
	path := "catalog/v1/" + url.PathEscape(prefix) + "/namespaces"
	get := func() (int, []byte, error) {
		return c.do(ctx, http.MethodGet, namespace, service, port,
			path+"/"+url.PathEscape(name)+"?returnUuid=true", bearer, nil)
	}
	status, body, err := get()
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		if err := c.post(ctx, namespace, service, port, path, bearer,
			map[string]any{"namespace": []string{name}}, "create namespace "+name); err != nil {
			return "", err
		}
		if status, body, err = get(); err != nil {
			return "", err
		}
	}
	if !isSuccess(status) {
		return "", fmt.Errorf("load namespace %s returned %d: %s", name, status, truncate(body))
	}
	return parseNamespaceUUID(body, name)
}

func parseNamespaceUUID(body []byte, name string) (string, error) {
	var ns struct {
		UUID string `json:"namespace-uuid"`
	}
	if err := json.Unmarshal(body, &ns); err != nil {
		return "", fmt.Errorf("parse namespace %s: %w", name, err)
	}
	if ns.UUID == "" {
		return "", fmt.Errorf("namespace %s: LakeKeeper returned no namespace-uuid", name)
	}
	return ns.UUID, nil
}
