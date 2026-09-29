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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

const requeueWhileProgressing = 15 * time.Second

// AnalyticsReconciler reconciles an Analytics object.
type AnalyticsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=analytics,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=analytics/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=analytics/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services;configmaps;secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete

func (r *AnalyticsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	a := &analyticsv1alpha1.Analytics{}
	if err := r.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, a)
	if statusErr := r.patchStatus(ctx, a); statusErr != nil {
		log.Error(statusErr, "Failed to update Analytics status")
		return ctrl.Result{}, statusErr
	}
	return result, err
}

func (r *AnalyticsReconciler) reconcile(ctx context.Context, a *analyticsv1alpha1.Analytics) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Reconciling Analytics")

	ns := a.Spec.NamespaceOrDefault()
	if err := r.ensureNamespace(ctx, a, ns, componentSuperset); err != nil {
		setCondition(a, analyticsv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}

	oidc, err := r.resolveOIDC(ctx, a)
	if err != nil {
		setCondition(a, analyticsv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}

	if err := r.reconcileSuperset(ctx, a, oidc); err != nil {
		setCondition(a, analyticsv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}
	if !conditionTrue(a, analyticsv1alpha1.ConditionSupersetReady) {
		setCondition(a, analyticsv1alpha1.ConditionReady, metav1.ConditionFalse, reasonReconciling, "Waiting for Superset to become ready")
		return ctrl.Result{RequeueAfter: requeueWhileProgressing}, nil
	}

	setCondition(a, analyticsv1alpha1.ConditionReady, metav1.ConditionTrue, reasonReady, "Superset is ready")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AnalyticsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&analyticsv1alpha1.Analytics{}).
		Owns(&corev1.Namespace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Named("analytics").
		Complete(r)
}

type oidcConfig struct {
	enabled          bool
	issuer           string
	publicIssuer     string
	tokenURL         string
	supersetClientID string
	supersetSecret   string
	trinoClientID    string
	trinoSecret      string
}

func (r *AnalyticsReconciler) resolveOIDC(ctx context.Context, a *analyticsv1alpha1.Analytics) (oidcConfig, error) {
	if !a.Spec.Auth.IsEnabled() {
		return oidcConfig{}, nil
	}
	spec := a.Spec.Auth.OIDC
	if spec.Issuer == "" {
		return oidcConfig{}, fmt.Errorf("spec.auth.oidc.issuer is required when auth is enabled")
	}
	ref := spec.CredentialsSecretRef
	if ref == nil || ref.Name == "" || ref.Namespace == "" {
		return oidcConfig{}, fmt.Errorf("spec.auth.oidc.credentialsSecretRef is required when auth is enabled")
	}

	supersetID, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.SupersetClientIDKeyOrDefault())
	if err != nil {
		return oidcConfig{}, err
	}
	supersetSecret, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.SupersetClientSecretKeyOrDefault())
	if err != nil {
		return oidcConfig{}, err
	}
	trinoID, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.TrinoClientIDKeyOrDefault())
	if err != nil {
		return oidcConfig{}, err
	}
	trinoSecret, err := r.getSecretData(ctx, ref.Name, ref.Namespace, ref.TrinoClientSecretKeyOrDefault())
	if err != nil {
		return oidcConfig{}, err
	}

	issuer := strings.TrimRight(spec.Issuer, "/")
	publicIssuer := strings.TrimRight(spec.PublicIssuer, "/")
	if publicIssuer == "" {
		publicIssuer = issuer
	}
	return oidcConfig{
		enabled:          true,
		issuer:           issuer,
		publicIssuer:     publicIssuer,
		tokenURL:         issuer + "/protocol/openid-connect/token",
		supersetClientID: supersetID,
		supersetSecret:   supersetSecret,
		trinoClientID:    trinoID,
		trinoSecret:      trinoSecret,
	}, nil
}
