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
	"fmt"
	"net/url"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

const requeueWhileProgressing = 15 * time.Second

const (
	annoRBACRule       = "workflows.argoproj.io/rbac-rule"
	annoRBACPrecedence = "workflows.argoproj.io/rbac-rule-precedence"
	annoSAName         = "kubernetes.io/service-account.name"
)

// OrchestrationReconciler reconciles an Orchestration object.
type OrchestrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=orchestrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=orchestrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=orchestrations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get;list;watch;create;update;patch;delete

func (r *OrchestrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	o := &orchestrationv1alpha1.Orchestration{}
	if err := r.Get(ctx, req.NamespacedName, o); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, o)
	if statusErr := r.patchStatus(ctx, o); statusErr != nil {
		log.Error(statusErr, "Failed to update Orchestration status")
		return ctrl.Result{}, statusErr
	}
	return result, err
}

func (r *OrchestrationReconciler) reconcile(ctx context.Context, o *orchestrationv1alpha1.Orchestration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Reconciling Orchestration")

	ns := o.Spec.NamespaceOrDefault()
	if err := r.ensureNamespace(ctx, o, ns, componentArgo); err != nil {
		setCondition(o, orchestrationv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}

	oidc, err := r.resolveOIDC(ctx, o)
	if err != nil {
		setCondition(o, orchestrationv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}

	if err := r.reconcileArgo(ctx, o, ns, oidc); err != nil {
		setCondition(o, orchestrationv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}
	if !conditionTrue(o, orchestrationv1alpha1.ConditionControllerReady) || !conditionTrue(o, orchestrationv1alpha1.ConditionServerReady) {
		setCondition(o, orchestrationv1alpha1.ConditionReady, metav1.ConditionFalse, reasonReconciling, "Waiting for Argo to become ready")
		return ctrl.Result{RequeueAfter: requeueWhileProgressing}, nil
	}

	setCondition(o, orchestrationv1alpha1.ConditionReady, metav1.ConditionTrue, reasonReady, "Argo is ready")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OrchestrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&orchestrationv1alpha1.Orchestration{}).
		Owns(&corev1.Namespace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Named("orchestration").
		Complete(r)
}

type oidcConfig struct {
	enabled      bool
	publicIssuer string
	clientID     string
	clientSecret string
	publicURL    string
}

func (r *OrchestrationReconciler) resolveOIDC(ctx context.Context, o *orchestrationv1alpha1.Orchestration) (oidcConfig, error) {
	if !o.Spec.Auth.IsEnabled() {
		return oidcConfig{}, nil
	}
	spec := o.Spec.Auth.OIDC
	if spec.Issuer == "" && spec.PublicIssuer == "" {
		return oidcConfig{}, fmt.Errorf("spec.auth.oidc.publicIssuer is required when auth is enabled")
	}
	if o.Spec.PublicURL == "" {
		return oidcConfig{}, fmt.Errorf("spec.publicURL is required when auth is enabled")
	}
	ref := spec.CredentialsSecretRef
	if ref == nil || ref.Name == "" || ref.Namespace == "" {
		return oidcConfig{}, fmt.Errorf("spec.auth.oidc.credentialsSecretRef is required when auth is enabled")
	}

	clientID, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.ArgoClientIDKeyOrDefault())
	if err != nil {
		return oidcConfig{}, err
	}
	clientSecret, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.ArgoClientSecretKeyOrDefault())
	if err != nil {
		return oidcConfig{}, err
	}

	publicIssuer := strings.TrimRight(spec.PublicIssuer, "/")
	if publicIssuer == "" {
		publicIssuer = strings.TrimRight(spec.Issuer, "/")
	}
	return oidcConfig{
		enabled:      true,
		publicIssuer: publicIssuer,
		clientID:     clientID,
		clientSecret: clientSecret,
		publicURL:    strings.TrimRight(o.Spec.PublicURL, "/"),
	}, nil
}

func (r *OrchestrationReconciler) reconcileArgo(ctx context.Context, o *orchestrationv1alpha1.Orchestration, ns string, oidc oidcConfig) error {
	if err := r.applyArgoCRDs(ctx, o); err != nil {
		return err
	}
	if err := r.applyPriorityClass(ctx, o); err != nil {
		return err
	}
	if err := r.applyControllerRBAC(ctx, o, ns); err != nil {
		return err
	}
	if err := r.applyServerRBAC(ctx, o, ns); err != nil {
		return err
	}
	if err := r.applyServiceAccount(ctx, o, ns, nameExecutor, componentExecutor, nil); err != nil {
		return err
	}
	if oidc.enabled {
		if err := r.applyGroupAccess(ctx, o, ns); err != nil {
			return err
		}
		if err := r.applySSOSecret(ctx, o, ns, oidc); err != nil {
			return err
		}
	}

	config := controllerConfig(oidc)
	if err := r.applyWorkflowConfig(ctx, o, ns, config); err != nil {
		return err
	}
	if err := r.applyServerService(ctx, o, ns); err != nil {
		return err
	}

	aliases, err := r.keycloakHostAliases(ctx, oidc)
	if err != nil {
		return err
	}
	if err := r.applyControllerDeployment(ctx, o, ns, hashData(config)); err != nil {
		return err
	}
	if err := r.applyServerDeployment(ctx, o, ns, oidc.enabled, hashData(config), aliases); err != nil {
		return err
	}

	o.Status.ArgoEndpoint = clusterServiceURL(nameServer, ns, portServer)

	controllerReady, err := r.deploymentReady(ctx, ns, nameController)
	if err != nil {
		return err
	}
	serverReady, err := r.deploymentReady(ctx, ns, nameServer)
	if err != nil {
		return err
	}
	setReady(o, orchestrationv1alpha1.ConditionControllerReady, controllerReady, "workflow controller is not ready")
	setReady(o, orchestrationv1alpha1.ConditionServerReady, serverReady, "argo server is not ready")
	return nil
}

func setReady(o *orchestrationv1alpha1.Orchestration, condType string, ready bool, waiting string) {
	if ready {
		setCondition(o, condType, metav1.ConditionTrue, reasonReady, "Ready")
		return
	}
	setCondition(o, condType, metav1.ConditionFalse, reasonNotReady, waiting)
}

func controllerConfig(oidc oidcConfig) string {
	var b strings.Builder
	b.WriteString("workflowDefaults:\n")
	b.WriteString("  spec:\n")
	b.WriteString("    serviceAccountName: " + nameExecutor + "\n")
	if !oidc.enabled {
		return b.String()
	}
	fmt.Fprintf(&b, `sso:
  issuer: %s
  clientId:
    name: %s
    key: %s
  clientSecret:
    name: %s
    key: %s
  redirectUrl: %s/oauth2/callback
  scopes:
    - openid
    - profile
    - email
  rbac:
    enabled: true
`, oidc.publicIssuer, secretSSO, keySSOClientID, secretSSO, keySSOClientSecret, oidc.publicURL)
	return b.String()
}

func (r *OrchestrationReconciler) keycloakHostAliases(ctx context.Context, oidc oidcConfig) ([]corev1.HostAlias, error) {
	if !oidc.enabled {
		return nil, nil
	}
	parsed, err := url.Parse(oidc.publicIssuer)
	if err != nil || parsed.Hostname() == "" {
		return nil, nil
	}
	svc := &corev1.Service{}
	if err := r.Get(ctx, client.ObjectKey{Name: ingressService, Namespace: ingressNamespace}, svc); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, nil
		}
		return nil, err
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return nil, nil
	}
	return []corev1.HostAlias{{
		IP:        svc.Spec.ClusterIP,
		Hostnames: []string{parsed.Hostname()},
	}}, nil
}
