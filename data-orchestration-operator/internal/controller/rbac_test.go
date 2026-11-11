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
	"os"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// Kubernetes refuses to let the operator create a Role or ClusterRole with
// rules it does not hold itself ("attempting to grant RBAC permissions not
// currently held"). envtest runs as cluster-admin and never hits that, so
// check the generated manager role covers every rule the operator grants.
func TestManagerRoleCoversGrantedRules(t *testing.T) {
	data, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manager rbacv1.ClusterRole
	if err := yaml.Unmarshal(data, &manager); err != nil {
		t.Fatal(err)
	}

	granted := map[string][]rbacv1.PolicyRule{
		"argo-role":                controllerNamespaceRules(),
		"argo-cluster-role":        controllerClusterRules(),
		"argo-server-cluster-role": serverClusterRules(),
	}
	for _, g := range groupAccounts() {
		granted[g.name] = []rbacv1.PolicyRule{rule("argoproj.io", workflowResources(), g.verbs)}
		granted[groupClusterRoleName(g.name)] = []rbacv1.PolicyRule{rule("argoproj.io", clusterWorkflowResources(), g.clusterVerbs)}
	}

	for role, rules := range granted {
		for _, r := range rules {
			for _, group := range r.APIGroups {
				for _, resource := range r.Resources {
					for _, verb := range r.Verbs {
						if !holds(manager.Rules, group, resource, verb) {
							t.Errorf("%s grants %s %q/%s, which the manager role does not hold; add a +kubebuilder:rbac marker",
								role, verb, group, resource)
						}
					}
				}
			}
		}
	}
}

// holds reports whether rules allow verb on every object of group/resource.
// Rules limited to resourceNames don't count: a granted rule without names
// needs an unrestricted one.
func holds(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, r := range rules {
		if len(r.ResourceNames) > 0 {
			continue
		}
		if match(r.APIGroups, group) && match(r.Resources, resource) && match(r.Verbs, verb) {
			return true
		}
	}
	return false
}

func match(values []string, want string) bool {
	return slices.Contains(values, want) || slices.Contains(values, "*")
}
