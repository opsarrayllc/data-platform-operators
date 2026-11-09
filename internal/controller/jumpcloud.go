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
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

const (
	// jumpCloudPageSize is the largest page both the v1 and v2 JumpCloud APIs accept.
	jumpCloudPageSize = 100

	// Values of spec.auth.groupSync.jumpcloud.subjectAttribute.
	jumpCloudSubjectEmail    = "email"
	jumpCloudSubjectUsername = "username"

	jumpCloudMemberTypeUser = "user"
	jumpCloudParamLimit     = "limit"

	// catalogUserTypeHuman is LakeKeeper's user-type for people.
	catalogUserTypeHuman = "human"
	// catalogUserTypeApplication is LakeKeeper's user-type for machine clients.
	catalogUserTypeApplication = "application"
)

// jumpCloudClient reads user groups and users from the JumpCloud Directory API.
type jumpCloudClient struct {
	baseURL string
	apiKey  string
	orgID   string
	http    *http.Client
}

// jumpCloudUser is the subset of a v1 systemuser the sync needs.
type jumpCloudUser struct {
	ID        string `json:"_id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	FirstName string `json:"firstname"`
	LastName  string `json:"lastname"`
	Activated bool   `json:"activated"`
	Suspended bool   `json:"suspended"`
	State     string `json:"state"`
}

// active reports whether the user can sign in. Suspended, staged, and
// not-yet-activated users get no LakeKeeper access.
func (u jumpCloudUser) active() bool {
	if u.Suspended || !u.Activated {
		return false
	}
	return u.State == "" || strings.EqualFold(u.State, "ACTIVATED")
}

func (u jumpCloudUser) subject(attribute string) string {
	switch attribute {
	case jumpCloudSubjectEmail:
		return u.Email
	case jumpCloudSubjectUsername:
		return u.Username
	default:
		return u.ID
	}
}

func (u jumpCloudUser) displayName() string {
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return name
	}
	if u.Username != "" {
		return u.Username
	}
	return u.Email
}

func (c *jumpCloudClient) get(ctx context.Context, path string, query url.Values, out any) error {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if c.orgID != "" {
		req.Header.Set("x-org-id", c.orgID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jumpcloud GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("jumpcloud GET %s: %w", path, err)
	}
	if !isSuccess(resp.StatusCode) {
		// Never echo request headers: they carry the API key.
		return fmt.Errorf("jumpcloud GET %s returned %d: %s", path, resp.StatusCode, truncate(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse jumpcloud %s: %w", path, err)
	}
	return nil
}

// groupID returns the id of the user group with exactly this name.
func (c *jumpCloudClient) groupID(ctx context.Context, name string) (string, error) {
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	query := url.Values{"filter": {"name:eq:" + name}, jumpCloudParamLimit: {strconv.Itoa(jumpCloudPageSize)}}
	if err := c.get(ctx, "/api/v2/usergroups", query, &groups); err != nil {
		return "", err
	}
	for _, g := range groups {
		if g.Name == name {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("jumpcloud user group %q not found", name)
}

// groupUserIDs returns the ids of the users bound directly to a user group.
func (c *jumpCloudClient) groupUserIDs(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	for skip := 0; ; skip += jumpCloudPageSize {
		var page []struct {
			To struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"to"`
		}
		query := url.Values{jumpCloudParamLimit: {strconv.Itoa(jumpCloudPageSize)}, "skip": {strconv.Itoa(skip)}}
		if err := c.get(ctx, "/api/v2/usergroups/"+url.PathEscape(groupID)+"/members", query, &page); err != nil {
			return nil, err
		}
		for _, m := range page {
			if m.To.Type == jumpCloudMemberTypeUser && m.To.ID != "" {
				ids = append(ids, m.To.ID)
			}
		}
		if len(page) < jumpCloudPageSize {
			return ids, nil
		}
	}
}

// users returns every user in the organization, keyed by id.
func (c *jumpCloudClient) users(ctx context.Context) (map[string]jumpCloudUser, error) {
	out := map[string]jumpCloudUser{}
	for skip := 0; ; skip += jumpCloudPageSize {
		var page struct {
			TotalCount int             `json:"totalCount"`
			Results    []jumpCloudUser `json:"results"`
		}
		query := url.Values{
			jumpCloudParamLimit: {strconv.Itoa(jumpCloudPageSize)},
			"skip":              {strconv.Itoa(skip)},
			"fields":            {"_id username email firstname lastname activated suspended state"},
		}
		if err := c.get(ctx, "/api/systemusers", query, &page); err != nil {
			return nil, err
		}
		for _, u := range page.Results {
			out[u.ID] = u
		}
		if len(page.Results) < jumpCloudPageSize || skip+len(page.Results) >= page.TotalCount {
			return out, nil
		}
	}
}

// jumpCloudGroupMembers resolves each access group's JumpCloud user group to
// LakeKeeper principals. Any lookup failure, including a missing group,
// returns an error so the caller changes nothing: an outage must not empty
// the roles.
func (r *DataLakeReconciler) jumpCloudGroupMembers(
	ctx context.Context,
	sync *dataplatformv1alpha1.JumpCloudGroupSyncSpec,
	roles []string,
) (map[string][]CatalogPrincipal, error) {
	known := map[string]bool{}
	for _, role := range roles {
		known[role] = true
	}
	for role := range sync.GroupNames {
		if !known[role] {
			return nil, fmt.Errorf("spec.auth.groupSync.jumpcloud.groupNames: unknown role %q (want one of %s)",
				role, strings.Join(roles, ", "))
		}
	}

	ref := sync.APIKeySecretRef
	apiKey, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.KeyOrDefault())
	if err != nil {
		return nil, err
	}
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	jc := &jumpCloudClient{
		baseURL: sync.APIURLOrDefault(),
		apiKey:  strings.TrimSpace(apiKey),
		orgID:   sync.OrgID,
		http:    httpClient,
	}

	users, err := jc.users(ctx)
	if err != nil {
		return nil, err
	}
	attribute := sync.SubjectAttributeOrDefault()
	out := map[string][]CatalogPrincipal{}
	for _, role := range roles {
		groupID, err := jc.groupID(ctx, sync.GroupNameFor(role))
		if err != nil {
			return nil, err
		}
		ids, err := jc.groupUserIDs(ctx, groupID)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		members := make([]CatalogPrincipal, 0, len(ids))
		for _, id := range ids {
			user, ok := users[id]
			if !ok || !user.active() {
				continue
			}
			subject := user.subject(attribute)
			if subject == "" || seen[subject] {
				continue
			}
			seen[subject] = true
			members = append(members, CatalogPrincipal{
				Subject: oidcSubjectPrefix + subject,
				Name:    user.displayName(),
				Email:   user.Email,
				Type:    catalogUserTypeHuman,
			})
		}
		out[role] = members
	}
	return out, nil
}
