package controller

import "testing"

func TestWarehouseIDForName(t *testing.T) {
	body := []byte(`{"warehouses":[{"id":"wh-1","warehouse-id":"wh-1","name":"default"}]}`)
	if got := warehouseIDForName(body, "default"); got != "wh-1" {
		t.Fatalf("warehouseIDForName = %q, want wh-1", got)
	}
	if got := warehouseIDForName(body, "missing"); got != "" {
		t.Fatalf("warehouseIDForName missing = %q, want empty", got)
	}
}

func TestDefaultAccessGroupsCoverPersonas(t *testing.T) {
	groups := defaultAccessGroups()
	if len(groups) != 3 {
		t.Fatalf("got %d access groups, want 3", len(groups))
	}
	byName := map[string]accessGroup{}
	for _, group := range groups {
		byName[group.Name] = group
	}
	admins := byName["platform-admins"]
	if len(admins.ServerRelations) == 0 || len(admins.ProjectRelations) == 0 {
		t.Fatal("platform-admins must have server and project grants")
	}
	engineers := byName["data-engineers"]
	if !containsAll(engineers.WarehouseRelations, "create", "modify", "select") {
		t.Fatalf("data-engineers warehouse grants = %v", engineers.WarehouseRelations)
	}
	analysts := byName["analysts"]
	if !containsAll(analysts.WarehouseRelations, "select", "describe") {
		t.Fatalf("analysts warehouse grants = %v", analysts.WarehouseRelations)
	}
	if containsAll(analysts.WarehouseRelations, "modify") {
		t.Fatal("analysts must not be able to modify the warehouse")
	}
}

func containsAll(have []string, want ...string) bool {
	set := map[string]bool{}
	for _, v := range have {
		set[v] = true
	}
	for _, v := range want {
		if !set[v] {
			return false
		}
	}
	return true
}
