package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

func TestRoleMemberDiff(t *testing.T) {
	current := []string{
		"oidc~alice",
		"oidc~bob",
		"kubernetes~system:serviceaccount:trino:trino",
	}
	wanted := []CatalogPrincipal{
		{Subject: "oidc~alice"},
		{Subject: "oidc~carol"},
		{Subject: "oidc~carol"}, // duplicates are added once
	}

	add, remove := roleMemberDiff(current, wanted, oidcSubjectPrefix)
	if len(add) != 1 || add[0].Subject != "oidc~carol" {
		t.Fatalf("add = %v, want [oidc~carol]", add)
	}
	if !reflect.DeepEqual(remove, []string{"oidc~bob"}) {
		t.Fatalf("remove = %v, want [oidc~bob]; ServiceAccounts must never be pruned", remove)
	}

	_, remove = roleMemberDiff(current, wanted, "")
	if len(remove) != 0 {
		t.Fatalf("an empty prune prefix must never remove members, got %v", remove)
	}

	// A group emptied in the directory empties the role, but only of people.
	_, remove = roleMemberDiff(current, nil, oidcSubjectPrefix)
	if !reflect.DeepEqual(remove, []string{"oidc~alice", "oidc~bob"}) {
		t.Fatalf("remove for an empty group = %v", remove)
	}
}

func TestRoleAssigneeUsers(t *testing.T) {
	body := []byte(`{"assignments":[
		{"type":"assignee","user":"oidc~alice"},
		{"type":"assignee","role":"6f1c3e0a-0000-0000-0000-000000000000"},
		{"type":"ownership","user":"oidc~owner"}
	]}`)
	users, err := roleAssigneeUsers(body)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(users, []string{"oidc~alice"}) {
		t.Fatalf("users = %v, want [oidc~alice]", users)
	}
	if _, err := roleAssigneeUsers([]byte("not json")); err == nil {
		t.Fatal("expected a parse error")
	}

	// LakeKeeper 0.13.3's response for a new role, fetched without a relations
	// filter: only the creator's ownership, which is not a member.
	live := []byte(`{"assignments":[{"type":"ownership","user":"kubernetes~system:serviceaccount:lakekeeper:lakekeeper-operator"}]}`)
	users, err = roleAssigneeUsers(live)
	if err != nil || len(users) != 0 {
		t.Fatalf("ownership must not count as membership, got %v, %v", users, err)
	}
}

// fakeJumpCloud serves the subset of the JumpCloud API the sync uses.
type fakeJumpCloud struct {
	apiKey string
	orgID  string
	users  []jumpCloudUser
	groups map[string][]string // group name -> member user ids
}

func (f *fakeJumpCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != f.apiKey {
		http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if r.Header.Get("x-org-id") != f.orgID {
		http.Error(w, `{"message":"wrong org"}`, http.StatusForbidden)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
	page := func(n int) (int, int) {
		lo, hi := skip, skip+limit
		if lo > n {
			lo = n
		}
		if hi > n {
			hi = n
		}
		return lo, hi
	}
	switch {
	case r.URL.Path == "/api/systemusers":
		lo, hi := page(len(f.users))
		_ = json.NewEncoder(w).Encode(map[string]any{"totalCount": len(f.users), "results": f.users[lo:hi]})
	case r.URL.Path == "/api/v2/usergroups":
		name := strings.TrimPrefix(r.URL.Query().Get("filter"), "name:eq:")
		var out []map[string]string
		if _, ok := f.groups[name]; ok {
			out = append(out, map[string]string{"id": "g-" + name, "name": name})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(r.URL.Path, "/api/v2/usergroups/") && strings.HasSuffix(r.URL.Path, "/members"):
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v2/usergroups/g-"), "/members")
		ids := f.groups[name]
		lo, hi := page(len(ids))
		out := []map[string]any{}
		for _, id := range ids[lo:hi] {
			out = append(out, map[string]any{"to": map[string]string{"id": id, "type": "user"}})
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.NotFound(w, r)
	}
}

func jumpCloudTestReconciler(t *testing.T, apiKey string) *DataLakeReconciler {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "jumpcloud", Namespace: "lakekeeper"},
		Data:       map[string][]byte{"apiKey": []byte(apiKey + "\n")},
	}
	return &DataLakeReconciler{Client: fake.NewClientBuilder().WithObjects(secret).Build()}
}

func subjects(principals []CatalogPrincipal) []string {
	out := make([]string, 0, len(principals))
	for _, p := range principals {
		out = append(out, p.Subject)
	}
	slices.Sort(out)
	return out
}

func TestJumpCloudGroupMembers(t *testing.T) {
	jc := &fakeJumpCloud{apiKey: "jc-key", orgID: "org-1", groups: map[string][]string{}}
	// 230 users force paging on both the v1 user list and v2 group members.
	engineers := make([]string, 0, 230)
	for i := range 230 {
		id := fmt.Sprintf("u%03d", i)
		jc.users = append(jc.users, jumpCloudUser{
			ID: id, Username: "user" + id, Email: id + "@example.com",
			Activated: true, State: "ACTIVATED",
		})
		engineers = append(engineers, id)
	}
	jc.users = append(jc.users,
		jumpCloudUser{ID: "admin", Username: "ada", Email: "ada@example.com", FirstName: "Ada", LastName: "L",
			Activated: true, State: "ACTIVATED"},
		jumpCloudUser{ID: "gone", Email: "gone@example.com", Activated: true, Suspended: true, State: "SUSPENDED"},
		jumpCloudUser{ID: "staged", Email: "staged@example.com", Activated: false, State: "STAGED"},
	)
	jc.groups["Data Platform Admins"] = []string{"admin", "gone", "staged", "missing-user"}
	jc.groups["data-engineers"] = engineers
	jc.groups["analysts"] = []string{}

	server := httptest.NewServer(jc)
	defer server.Close()

	r := jumpCloudTestReconciler(t, "jc-key")
	r.HTTPClient = server.Client()
	sync := &dataplatformv1alpha1.JumpCloudGroupSyncSpec{
		APIKeySecretRef: dataplatformv1alpha1.JumpCloudAPIKeySecretRef{Name: "jumpcloud", Namespace: "lakekeeper"},
		APIURL:          server.URL + "/",
		OrgID:           "org-1",
		GroupNames:      map[string]string{"platform-admins": "Data Platform Admins"},
	}
	roles := []string{"platform-admins", "data-engineers", "analysts"}

	members, err := r.jumpCloudGroupMembers(context.Background(), sync, roles)
	if err != nil {
		t.Fatal(err)
	}
	admins := members["platform-admins"]
	if len(admins) != 1 || admins[0].Subject != "oidc~admin" || admins[0].Name != "Ada L" ||
		admins[0].Email != "ada@example.com" || admins[0].Type != "human" {
		t.Fatalf("platform-admins = %+v; suspended, staged and unknown users must be skipped", admins)
	}
	if got := len(members["data-engineers"]); got != 230 {
		t.Fatalf("data-engineers has %d members, want 230 (paging)", got)
	}
	if got, ok := members["analysts"]; !ok || len(got) != 0 {
		t.Fatalf("analysts = %v, want present and empty", got)
	}

	sync.SubjectAttribute = "email"
	members, err = r.jumpCloudGroupMembers(context.Background(), sync, roles)
	if err != nil {
		t.Fatal(err)
	}
	if got := subjects(members["platform-admins"]); !reflect.DeepEqual(got, []string{"oidc~ada@example.com"}) {
		t.Fatalf("email subjects = %v", got)
	}
}

func TestJumpCloudGroupMembersFailsClosed(t *testing.T) {
	jc := &fakeJumpCloud{apiKey: "jc-key", groups: map[string][]string{"platform-admins": {}, "analysts": {}}}
	server := httptest.NewServer(jc)
	defer server.Close()
	roles := []string{"platform-admins", "data-engineers", "analysts"}

	newSync := func() *dataplatformv1alpha1.JumpCloudGroupSyncSpec {
		return &dataplatformv1alpha1.JumpCloudGroupSyncSpec{
			APIKeySecretRef: dataplatformv1alpha1.JumpCloudAPIKeySecretRef{Name: "jumpcloud", Namespace: "lakekeeper"},
			APIURL:          server.URL,
		}
	}

	r := jumpCloudTestReconciler(t, "jc-key")
	r.HTTPClient = server.Client()
	members, err := r.jumpCloudGroupMembers(context.Background(), newSync(), roles)
	if err == nil || !strings.Contains(err.Error(), `"data-engineers" not found`) || members != nil {
		t.Fatalf("a missing group must fail without members, got %v, %v", members, err)
	}

	bad := jumpCloudTestReconciler(t, "wrong-key")
	bad.HTTPClient = server.Client()
	if _, err := bad.jumpCloudGroupMembers(context.Background(), newSync(), roles); err == nil ||
		!strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "wrong-key") {
		t.Fatalf("a rejected key must fail without echoing the key, got %v", err)
	}

	typo := newSync()
	typo.GroupNames = map[string]string{"platform-admin": "x"}
	if _, err := r.jumpCloudGroupMembers(context.Background(), typo, roles); err == nil ||
		!strings.Contains(err.Error(), `unknown role "platform-admin"`) {
		t.Fatalf("an unknown role in groupNames must be rejected, got %v", err)
	}
}
