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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

type groupAccess struct {
	name       string
	rule       string
	precedence string
	verbs      []string
}

func groupAccounts() []groupAccess {
	read := []string{"get", "list", "watch"}
	write := []string{"get", "list", "watch", "create", "update", "patch"}
	admin := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	return []groupAccess{
		{orchestrationv1alpha1.DefaultGroupPlatformAdmins, "'platform-admins' in groups", "30", admin},
		{orchestrationv1alpha1.DefaultGroupDataEngineers, "'data-engineers' in groups", "20", write},
		{orchestrationv1alpha1.DefaultGroupAnalysts, "'analysts' in groups", "10", read},
	}
}

func workflowResources() []string {
	return []string{"workflows", "workflowtemplates", "cronworkflows", "workflowtaskresults"}
}

func (r *OrchestrationReconciler) applyPriorityClass(ctx context.Context, o *orchestrationv1alpha1.Orchestration) error {
	pc := &schedulingv1.PriorityClass{}
	err := r.Get(ctx, types.NamespacedName{Name: nameController}, pc)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	pc = &schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nameController,
			Labels: labelsFor(o, componentController),
		},
		Value:            1000000,
		PreemptionPolicy: ptr.To(corev1.PreemptLowerPriority),
		Description:      "Argo workflow controller",
	}
	if err := controllerutil.SetControllerReference(o, pc, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, pc)
}

func (r *OrchestrationReconciler) applyControllerRBAC(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string) error {
	if err := r.applyServiceAccount(ctx, o, ns, nameControllerSA, componentController, nil); err != nil {
		return err
	}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "argo-role", Namespace: ns}}
	if err := r.apply(ctx, o, role, func() error {
		ensureLabels(role, labelsFor(o, componentController))
		role.Rules = []rbacv1.PolicyRule{
			rule("coordination.k8s.io", []string{"leases"}, []string{"create", "get", "update"}),
			rule("", []string{"secrets"}, []string{"get"}),
		}
		return nil
	}); err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "argo-binding", Namespace: ns}}
	if err := r.apply(ctx, o, binding, func() error {
		ensureLabels(binding, labelsFor(o, componentController))
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}
		binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: nameControllerSA, Namespace: ns}}
		return nil
	}); err != nil {
		return err
	}

	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "argo-cluster-role"}}
	if err := r.apply(ctx, o, clusterRole, func() error {
		ensureLabels(clusterRole, labelsFor(o, componentController))
		clusterRole.Rules = controllerClusterRules()
		return nil
	}); err != nil {
		return err
	}
	clusterBinding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "argo-binding"}}
	return r.apply(ctx, o, clusterBinding, func() error {
		ensureLabels(clusterBinding, labelsFor(o, componentController))
		clusterBinding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole.Name}
		clusterBinding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: nameControllerSA, Namespace: ns}}
		return nil
	})
}

func (r *OrchestrationReconciler) applyServerRBAC(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string) error {
	if err := r.applyServiceAccount(ctx, o, ns, nameServer, componentServer, nil); err != nil {
		return err
	}
	clusterRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "argo-server-cluster-role"}}
	if err := r.apply(ctx, o, clusterRole, func() error {
		ensureLabels(clusterRole, labelsFor(o, componentServer))
		clusterRole.Rules = serverClusterRules()
		return nil
	}); err != nil {
		return err
	}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "argo-server-binding"}}
	return r.apply(ctx, o, binding, func() error {
		ensureLabels(binding, labelsFor(o, componentServer))
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRole.Name}
		binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: nameServer, Namespace: ns}}
		return nil
	})
}

func (r *OrchestrationReconciler) applyGroupAccess(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string) error {
	for _, group := range groupAccounts() {
		annotations := map[string]string{
			annoRBACRule:       group.rule,
			annoRBACPrecedence: group.precedence,
		}
		if err := r.applyServiceAccount(ctx, o, ns, group.name, componentArgo, annotations); err != nil {
			return err
		}
		if err := r.applyServiceAccountToken(ctx, o, ns, group.name); err != nil {
			return err
		}
		role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: group.name, Namespace: ns}}
		if err := r.apply(ctx, o, role, func() error {
			ensureLabels(role, labelsFor(o, componentArgo))
			role.Rules = []rbacv1.PolicyRule{rule("argoproj.io", workflowResources(), group.verbs)}
			return nil
		}); err != nil {
			return err
		}
		binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: group.name, Namespace: ns}}
		if err := r.apply(ctx, o, binding, func() error {
			ensureLabels(binding, labelsFor(o, componentArgo))
			binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}
			binding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: group.name, Namespace: ns}}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *OrchestrationReconciler) applyServiceAccount(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns, name, component string, annotations map[string]string) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	return r.apply(ctx, o, sa, func() error {
		ensureLabels(sa, labelsFor(o, component))
		if sa.Annotations == nil {
			sa.Annotations = map[string]string{}
		}
		for k, v := range annotations {
			sa.Annotations[k] = v
		}
		return nil
	})
}

func (r *OrchestrationReconciler) applyServiceAccountToken(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns, name string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name + ".service-account-token", Namespace: ns}}
	return r.apply(ctx, o, secret, func() error {
		ensureLabels(secret, labelsFor(o, componentArgo))
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations[annoSAName] = name
		secret.Type = corev1.SecretTypeServiceAccountToken
		return nil
	})
}

func (r *OrchestrationReconciler) applySSOSecret(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string, oidc oidcConfig) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretSSO, Namespace: ns}}
	return r.apply(ctx, o, secret, func() error {
		ensureLabels(secret, labelsFor(o, componentServer))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			keySSOClientID:     []byte(oidc.clientID),
			keySSOClientSecret: []byte(oidc.clientSecret),
		}
		return nil
	})
}

func (r *OrchestrationReconciler) applyWorkflowConfig(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns, config string) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapWorkflowController, Namespace: ns}}
	return r.apply(ctx, o, cm, func() error {
		ensureLabels(cm, labelsFor(o, componentController))
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[keyConfig] = config
		return nil
	})
}

func (r *OrchestrationReconciler) applyServerService(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: nameServer, Namespace: ns}}
	return r.apply(ctx, o, svc, func() error {
		ensureLabels(svc, labelsFor(o, componentServer))
		svc.Spec.Type = o.Spec.Service.TypeOrDefault()
		svc.Spec.Selector = map[string]string{"app": nameServer}
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "web",
			Port:       portServer,
			TargetPort: intstr.FromInt32(portServer),
		}}
		return nil
	})
}

func (r *OrchestrationReconciler) applyControllerDeployment(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns, cfgHash string) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameController, Namespace: ns}}
	return r.apply(ctx, o, deploy, func() error {
		ensureLabels(deploy, labelsFor(o, componentController))
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": nameController}}
		deploy.Spec.Template.Labels = labelsFor(o, componentController)
		deploy.Spec.Template.Labels["app"] = nameController
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[annotationConfigHash] = cfgHash
		deploy.Spec.Template.Spec.ServiceAccountName = nameControllerSA
		deploy.Spec.Template.Spec.PriorityClassName = nameController
		deploy.Spec.Template.Spec.SecurityContext = restrictedPodSecurity()
		deploy.Spec.Template.Spec.NodeSelector = map[string]string{"kubernetes.io/os": "linux"}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{{
			Name:         "tmp",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:            nameController,
			Image:           o.Spec.ControllerImageOrDefault(),
			Command:         []string{"workflow-controller"},
			SecurityContext: restrictedContainerSecurity(),
			Env: []corev1.EnvVar{{
				Name: "LEADER_ELECTION_IDENTITY",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.name",
				}},
			}},
			Ports: []corev1.ContainerPort{
				{Name: "metrics", ContainerPort: portMetrics},
				{Name: "healthz", ContainerPort: portHealthz},
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.FromInt32(portHealthz),
				}},
				InitialDelaySeconds: 90,
				PeriodSeconds:       60,
				TimeoutSeconds:      30,
				FailureThreshold:    3,
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		}}
		return nil
	})
}

func (r *OrchestrationReconciler) applyServerDeployment(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string, sso bool, cfgHash string, aliases []corev1.HostAlias) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameServer, Namespace: ns}}
	return r.apply(ctx, o, deploy, func() error {
		ensureLabels(deploy, labelsFor(o, componentServer))
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": nameServer}}
		deploy.Spec.Template.Labels = labelsFor(o, componentServer)
		deploy.Spec.Template.Labels["app"] = nameServer
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[annotationConfigHash] = cfgHash
		deploy.Spec.Template.Spec.ServiceAccountName = nameServer
		deploy.Spec.Template.Spec.SecurityContext = restrictedPodSecurity()
		deploy.Spec.Template.Spec.NodeSelector = map[string]string{"kubernetes.io/os": "linux"}
		deploy.Spec.Template.Spec.HostAliases = aliases
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{{
			Name:         "tmp",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}}
		mode := "server"
		if sso {
			mode = "sso"
		}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:            nameServer,
			Image:           o.Spec.ServerImageOrDefault(),
			Args:            []string{"server", "--auth-mode", mode, "--secure=false"},
			SecurityContext: restrictedContainerSecurity(),
			Ports:           []corev1.ContainerPort{{Name: "web", ContainerPort: portServer}},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
					Path: "/",
					Port: intstr.FromInt32(portServer),
				}},
				InitialDelaySeconds: 10,
				PeriodSeconds:       20,
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		}}
		return nil
	})
}

func rule(group string, resources, verbs []string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs}
}

func controllerClusterRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("", []string{"pods", "pods/exec"}, []string{"create", "get", "list", "watch", "update", "patch", "delete"}),
		rule("", []string{"configmaps", "namespaces"}, []string{"get", "watch", "list"}),
		rule("", []string{"persistentvolumeclaims", "persistentvolumeclaims/finalizers"}, []string{"create", "update", "delete", "get"}),
		rule("argoproj.io", []string{"workflows", "workflows/finalizers", "workflowtasksets", "workflowtasksets/finalizers", "workflowartifactgctasks"}, []string{"get", "list", "watch", "update", "patch", "delete", "create"}),
		rule("argoproj.io", []string{"workflowtemplates", "workflowtemplates/finalizers", "clusterworkflowtemplates", "clusterworkflowtemplates/finalizers"}, []string{"get", "list", "watch"}),
		rule("argoproj.io", []string{"workflowtaskresults"}, []string{"list", "watch", "deletecollection"}),
		rule("", []string{"serviceaccounts"}, []string{"get", "list"}),
		rule("argoproj.io", []string{"cronworkflows", "cronworkflows/finalizers"}, []string{"get", "list", "watch", "update", "patch", "delete"}),
		rule("", []string{"events"}, []string{"create", "patch"}),
		rule("policy", []string{"poddisruptionbudgets"}, []string{"create", "get", "delete"}),
		{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{"argo-workflows-agent-ca-certificates"},
			Verbs:         []string{"get"},
		},
	}
}

func serverClusterRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("", []string{"configmaps"}, []string{"get", "watch", "list", "create", "update"}),
		rule("", []string{"secrets"}, []string{"get", "create"}),
		rule("", []string{"pods", "pods/exec", "pods/log"}, []string{"get", "list", "watch", "delete"}),
		rule("", []string{"events"}, []string{"watch", "create", "patch"}),
		rule("", []string{"serviceaccounts"}, []string{"get", "list", "watch"}),
		rule("argoproj.io", []string{"eventsources", "sensors", "workflows", "workfloweventbindings", "workflowtemplates", "cronworkflows", "clusterworkflowtemplates"}, []string{"create", "get", "list", "watch", "update", "patch", "delete"}),
	}
}
