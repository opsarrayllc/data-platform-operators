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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// CatalogClient talks to LakeKeeper's management API and can POST form data
// through the Kubernetes API service proxy (used for Keycloak token requests).
type CatalogClient interface {
	Bootstrap(ctx context.Context, namespace, service string, port int32, bearer string) error
	EnsureGrants(ctx context.Context, namespace, service string, port int32, bearer string, principals []CatalogPrincipal) error
	EnsureWarehouse(ctx context.Context, namespace, service string, port int32, bearer string, req WarehouseRequest) error
	EnsureAccessRoles(ctx context.Context, namespace, service string, port int32, bearer, warehouseName string, roles []CatalogAccessRole) error
	EnsureAuthzStore(ctx context.Context, namespace, service string, port int32, bearer string, req AuthzStoreRequest) (string, error)
	EnsureAuthzTuples(ctx context.Context, namespace, service string, port int32, bearer, storeID string, tuples []AuthzTuple) error
	JSON(ctx context.Context, method, namespace, service string, port int32, path, bearer string, payload any) (int, []byte, error)
	FormPost(ctx context.Context, namespace, service string, port int32, path string, form url.Values) (int, []byte, error)
	FetchToken(ctx context.Context, tokenURL string, form url.Values) (int, []byte, error)
}

// AuthzStoreRequest describes an OpenFGA store the operator owns outright,
// along with the object types its authorization model must define.
type AuthzStoreRequest struct {
	Store string
	Types []AuthzType
}

// AuthzType is one OpenFGA object type whose members are the values permitted
// in a filtered column, plus the relations that grant them.
type AuthzType struct {
	Name string
	// Relations are assignable to "user" and to "group#member".
	Relations []string
}

// CatalogPrincipal is an identity the operator provisions and grants access to
// after bootstrap. Bootstrap only grants the operator that performed it, so
// every other principal needs explicit grants.
type CatalogPrincipal struct {
	Subject string
	Name    string
	Email   string
	// Type is LakeKeeper's user-type, "human" or "application".
	Type string
	// ServerRelation is granted on the server when set, e.g. "admin".
	ServerRelation string
	// ProjectRelation is granted on the default project when set, e.g.
	// "project_admin". The server admin role does not imply project access.
	ProjectRelation string
}

// CatalogAccessRole is a LakeKeeper role plus the grants it should hold.
type CatalogAccessRole struct {
	Name               string
	Description        string
	ServerRelations    []string
	ProjectRelations   []string
	WarehouseRelations []string
	Members            []CatalogPrincipal
}

// AuthzTuple is one OpenFGA relationship, as in "user:alice member group:analysts".
type AuthzTuple struct {
	User     string
	Relation string
	Object   string
}

// WarehouseRequest is a LakeKeeper create-warehouse payload.
type WarehouseRequest struct {
	Name            string
	Bucket          string
	Region          string
	KeyPrefix       string
	Endpoint        string
	PathStyleAccess bool
	Flavor          string
	STSEnabled      bool
	STSRoleARN      string
	AccessKeyID     string
	SecretAccessKey string
}

type proxyCatalogClient struct {
	cfg        *rest.Config
	kube       kubernetes.Interface
	restClient rest.Interface
	http       *http.Client
}

// NewProxyCatalogClient talks to in-cluster services. Keycloak token requests
// use the API service proxy (no Authorization header). LakeKeeper management
// calls cannot: kube-apiserver strips Authorization, so those go over HTTP
// (cluster DNS in-cluster, port-forward when running locally).
func NewProxyCatalogClient(cfg *rest.Config) (CatalogClient, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &proxyCatalogClient{
		cfg:        cfg,
		kube:       cs,
		restClient: cs.CoreV1().RESTClient(),
		http:       &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *proxyCatalogClient) Bootstrap(ctx context.Context, namespace, service string, port int32, bearer string) error {
	// is-operator grants the full management API. The plain admin role a
	// bootstrap otherwise grants cannot create warehouses in a project until it
	// grants itself project permissions, which fails under the OpenFGA authorizer.
	status, body, err := c.do(ctx, http.MethodPost, namespace, service, port, "management/v1/bootstrap", bearer,
		map[string]any{"accept-terms-of-use": true, "is-operator": true})
	if err != nil {
		return err
	}
	if isSuccess(status) || isAlreadyDone(status, body) {
		return nil
	}
	return fmt.Errorf("bootstrap returned %d: %s", status, truncate(body))
}

// EnsureGrants provisions each principal and applies its grants.
func (c *proxyCatalogClient) EnsureGrants(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer string,
	principals []CatalogPrincipal,
) error {
	for _, p := range principals {
		if err := c.post(ctx, namespace, service, port,
			"management/v1/user", bearer, catalogUserJSON(p), "provision user "+p.Name); err != nil {
			return err
		}
		if p.ServerRelation != "" {
			grant := map[string]any{
				"writes": []map[string]any{{"type": p.ServerRelation, "user": p.Subject}},
			}
			if err := c.post(ctx, namespace, service, port,
				"management/v1/permissions/server/assignments", bearer, grant,
				"grant server "+p.ServerRelation+" to "+p.Name); err != nil {
				return err
			}
		}
		if p.ProjectRelation != "" {
			grant := map[string]any{
				"writes": []map[string]any{{"type": p.ProjectRelation, "user": p.Subject}},
			}
			if err := c.post(ctx, namespace, service, port,
				"management/v1/permissions/project/assignments", bearer, grant,
				"grant project "+p.ProjectRelation+" to "+p.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *proxyCatalogClient) post(
	ctx context.Context,
	namespace, service string,
	port int32,
	path, bearer string,
	payload any,
	what string,
) error {
	status, body, err := c.do(ctx, http.MethodPost, namespace, service, port, path, bearer, payload)
	if err != nil {
		return err
	}
	if isSuccess(status) || isAlreadyDone(status, body) {
		return nil
	}
	return fmt.Errorf("%s returned %d: %s", what, status, truncate(body))
}

func (c *proxyCatalogClient) EnsureWarehouse(ctx context.Context, namespace, service string, port int32, bearer string, req WarehouseRequest) error {
	status, body, err := c.do(ctx, http.MethodGet, namespace, service, port, "management/v1/warehouse", bearer, nil)
	if err != nil {
		return err
	}
	if isSuccess(status) && warehouseListed(body, req.Name) {
		return nil
	}
	payload := warehouseCreateJSON(req)
	status, body, err = c.do(ctx, http.MethodPost, namespace, service, port, "management/v1/warehouse", bearer, payload)
	if err != nil {
		return err
	}
	if isSuccess(status) || isAlreadyDone(status, body) {
		return nil
	}
	return fmt.Errorf("create warehouse returned %d: %s", status, truncate(body))
}

func (c *proxyCatalogClient) JSON(
	ctx context.Context,
	method, namespace, service string,
	port int32,
	path, bearer string,
	payload any,
) (int, []byte, error) {
	return c.do(ctx, method, namespace, service, port, path, bearer, payload)
}

// EnsureAccessRoles creates each role if missing and applies its grants and members.
func (c *proxyCatalogClient) EnsureAccessRoles(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, warehouseName string,
	roles []CatalogAccessRole,
) error {
	if len(roles) == 0 {
		return nil
	}
	warehouseID, err := c.warehouseID(ctx, namespace, service, port, bearer, warehouseName)
	if err != nil {
		return err
	}
	for _, role := range roles {
		roleID, err := c.ensureRole(ctx, namespace, service, port, bearer, role)
		if err != nil {
			return err
		}
		if err := c.grantRoleAccess(ctx, namespace, service, port, bearer, roleID, warehouseID, role); err != nil {
			return err
		}
		for _, member := range role.Members {
			if err := c.post(ctx, namespace, service, port,
				"management/v1/user", bearer, catalogUserJSON(member), "provision user "+member.Name); err != nil {
				return err
			}
			grant := map[string]any{
				"writes": []map[string]any{{"type": "assignee", "user": member.Subject}},
			}
			if err := c.post(ctx, namespace, service, port,
				"management/v1/permissions/role/"+roleID+"/assignments", bearer, grant,
				"assign "+member.Name+" to role "+role.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func catalogUserJSON(p CatalogPrincipal) map[string]any {
	user := map[string]any{
		"id":               p.Subject,
		"name":             p.Name,
		"user-type":        p.Type,
		"update-if-exists": true,
	}
	if p.Email != "" {
		user["email"] = p.Email
	}
	return user
}

func (c *proxyCatalogClient) ensureRole(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer string,
	role CatalogAccessRole,
) (string, error) {
	if id, err := c.findRoleID(ctx, namespace, service, port, bearer, role.Name); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}
	payload := map[string]any{"name": role.Name}
	if role.Description != "" {
		payload["description"] = role.Description
	}
	status, body, err := c.do(ctx, http.MethodPost, namespace, service, port, "management/v1/role", bearer, payload)
	if err != nil {
		return "", err
	}
	if isSuccess(status) {
		id := parseRoleID(body)
		if id == "" {
			return "", fmt.Errorf("created role %q has no id", role.Name)
		}
		return id, nil
	}
	if isAlreadyDone(status, body) {
		id, err := c.findRoleID(ctx, namespace, service, port, bearer, role.Name)
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("role %q already exists but was not listed", role.Name)
		}
		return id, nil
	}
	return "", fmt.Errorf("create role %q returned %d: %s", role.Name, status, truncate(body))
}

func (c *proxyCatalogClient) findRoleID(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, name string,
) (string, error) {
	status, body, err := c.do(ctx, http.MethodGet, namespace, service, port, "management/v1/role?pageSize=100", bearer, nil)
	if err != nil {
		return "", err
	}
	if !isSuccess(status) {
		return "", fmt.Errorf("list roles returned %d: %s", status, truncate(body))
	}
	var parsed struct {
		Roles []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse role list: %w", err)
	}
	for _, role := range parsed.Roles {
		if role.Name == name {
			return role.ID, nil
		}
	}
	return "", nil
}

func parseRoleID(body []byte) string {
	var parsed struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	return parsed.ID
}

func (c *proxyCatalogClient) grantRoleAccess(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, roleID, warehouseID string,
	role CatalogAccessRole,
) error {
	for _, relation := range role.ServerRelations {
		grant := map[string]any{
			"writes": []map[string]any{{"type": relation, "role": roleID}},
		}
		if err := c.post(ctx, namespace, service, port,
			"management/v1/permissions/server/assignments", bearer, grant,
			"grant server "+relation+" to role "+role.Name); err != nil {
			return err
		}
	}
	for _, relation := range role.ProjectRelations {
		grant := map[string]any{
			"writes": []map[string]any{{"type": relation, "role": roleID}},
		}
		if err := c.post(ctx, namespace, service, port,
			"management/v1/permissions/project/assignments", bearer, grant,
			"grant project "+relation+" to role "+role.Name); err != nil {
			return err
		}
	}
	if warehouseID == "" {
		return nil
	}
	for _, relation := range role.WarehouseRelations {
		grant := map[string]any{
			"writes": []map[string]any{{"type": relation, "role": roleID}},
		}
		if err := c.post(ctx, namespace, service, port,
			"management/v1/permissions/warehouse/"+warehouseID+"/assignments", bearer, grant,
			"grant warehouse "+relation+" to role "+role.Name); err != nil {
			return err
		}
	}
	return nil
}

func (c *proxyCatalogClient) warehouseID(
	ctx context.Context,
	namespace, service string,
	port int32,
	bearer, name string,
) (string, error) {
	status, body, err := c.do(ctx, http.MethodGet, namespace, service, port, "management/v1/warehouse", bearer, nil)
	if err != nil {
		return "", err
	}
	if !isSuccess(status) {
		return "", fmt.Errorf("list warehouses returned %d: %s", status, truncate(body))
	}
	id := warehouseIDForName(body, name)
	if id == "" {
		return "", fmt.Errorf("warehouse %q was not listed", name)
	}
	return id, nil
}

func warehouseIDForName(body []byte, name string) string {
	var parsed struct {
		Warehouses []struct {
			ID            string `json:"id"`
			WarehouseID   string `json:"warehouse-id"`
			Name          string `json:"name"`
			WarehouseName string `json:"warehouse-name"`
		} `json:"warehouses"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	for _, w := range parsed.Warehouses {
		if w.Name == name || w.WarehouseName == name {
			if w.ID != "" {
				return w.ID
			}
			return w.WarehouseID
		}
	}
	return ""
}

func (c *proxyCatalogClient) FormPost(ctx context.Context, namespace, service string, port int32, path string, form url.Values) (int, []byte, error) {
	name := service + ":" + strconv.Itoa(int(port))
	req := c.restClient.Post().
		Namespace(namespace).
		Resource("services").
		Name(name).
		SubResource("proxy").
		Suffix(path).
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		Body([]byte(form.Encode()))
	result := req.Do(ctx)
	var statusCode int
	result.StatusCode(&statusCode)
	raw, err := result.Raw()
	if err != nil && statusCode == 0 {
		return 0, nil, err
	}
	if raw == nil && err != nil {
		raw = []byte(err.Error())
	}
	return statusCode, raw, nil
}

func (c *proxyCatalogClient) FetchToken(ctx context.Context, tokenURL string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (c *proxyCatalogClient) do(
	ctx context.Context,
	method, namespace, service string,
	port int32,
	path, bearer string,
	payload any,
) (int, []byte, error) {
	base, cleanup, err := c.serviceBaseURL(ctx, namespace, service, port)
	if err != nil {
		return 0, nil, err
	}
	defer cleanup()

	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+"/"+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (c *proxyCatalogClient) serviceBaseURL(ctx context.Context, namespace, service string, port int32) (string, func(), error) {
	if runningInCluster() {
		return fmt.Sprintf("http://%s.%s.svc:%d", service, namespace, port), func() {}, nil
	}
	pod, err := c.readyPodForService(ctx, namespace, service)
	if err != nil {
		return "", nil, err
	}
	local, stop, err := c.portForward(ctx, namespace, pod, port)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", local), stop, nil
}

func (c *proxyCatalogClient) readyPodForService(ctx context.Context, namespace, service string) (string, error) {
	svc, err := c.kube.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if len(svc.Spec.Selector) == 0 {
		return "", fmt.Errorf("service %s/%s has no selector", namespace, service)
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(svc.Spec.Selector).String(),
	})
	if err != nil {
		return "", err
	}
	for i := range pods.Items {
		if podReady(&pods.Items[i]) {
			return pods.Items[i].Name, nil
		}
	}
	return "", fmt.Errorf("no ready Pod for Service %s/%s", namespace, service)
}

func (c *proxyCatalogClient) portForward(ctx context.Context, namespace, pod string, port int32) (int, func(), error) {
	reqURL := c.kube.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("portforward").
		URL()
	transport, upgrader, err := spdy.RoundTripperFor(c.cfg)
	if err != nil {
		return 0, nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, reqURL)
	readyCh := make(chan struct{})
	stopCh := make(chan struct{})
	errCh := make(chan error, 1)
	fw, err := portforward.NewOnAddresses(
		dialer,
		[]string{"127.0.0.1"},
		[]string{fmt.Sprintf("0:%d", port)},
		stopCh,
		readyCh,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		return 0, nil, err
	}
	go func() { errCh <- fw.ForwardPorts() }()
	select {
	case <-readyCh:
	case err := <-errCh:
		return 0, nil, err
	case <-ctx.Done():
		close(stopCh)
		return 0, nil, ctx.Err()
	}
	ports, err := fw.GetPorts()
	if err != nil {
		close(stopCh)
		return 0, nil, err
	}
	if len(ports) == 0 {
		close(stopCh)
		return 0, nil, fmt.Errorf("port-forward to Pod %s/%s produced no ports", namespace, pod)
	}
	return int(ports[0].Local), sync.OnceFunc(func() { close(stopCh) }), nil
}

func runningInCluster() bool {
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func warehouseCreateJSON(req WarehouseRequest) map[string]any {
	profile := map[string]any{
		"type":        "s3", //nolint:goconst
		"bucket":      req.Bucket,
		"region":      req.Region,
		"flavor":      req.Flavor,
		"sts-enabled": req.STSEnabled,
	}
	if req.KeyPrefix != "" {
		profile["key-prefix"] = req.KeyPrefix
	}
	if req.Endpoint != "" {
		profile["endpoint"] = req.Endpoint
	}
	if req.PathStyleAccess {
		profile["path-style-access"] = true
	}
	if req.STSRoleARN != "" {
		profile["sts-role-arn"] = req.STSRoleARN
	}
	return map[string]any{
		"warehouse-name":  req.Name,
		"storage-profile": profile,
		"storage-credential": map[string]any{
			"type":                  "s3", //nolint:goconst
			"credential-type":       "access-key",
			"aws-access-key-id":     req.AccessKeyID,
			"aws-secret-access-key": req.SecretAccessKey,
		},
	}
}

func warehouseListed(body []byte, name string) bool {
	var parsed struct {
		Warehouses []struct {
			Name          string `json:"name"`
			WarehouseName string `json:"warehouse-name"`
		} `json:"warehouses"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return bytes.Contains(body, []byte(`"`+name+`"`))
	}
	for _, w := range parsed.Warehouses {
		if w.Name == name || w.WarehouseName == name {
			return true
		}
	}
	return false
}

func isSuccess(code int) bool {
	return code >= 200 && code < 300
}

func isAlreadyDone(code int, body []byte) bool {
	if code == http.StatusConflict {
		return true
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "already") && (code == http.StatusBadRequest || code == http.StatusUnprocessableEntity)
}

func truncate(b []byte) string {
	const max = 512
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
