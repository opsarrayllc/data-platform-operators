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
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

const (
	requeueWhileProgressing = 15 * time.Second
	// requeueAccessSync picks up group membership changes in Keycloak or the
	// external directory after the platform first became ready.
	requeueAccessSync = time.Minute
)

// DataLakeReconciler reconciles a DataLake object.
type DataLakeReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Catalog CatalogClient
	// HTTPClient calls external directories such as JumpCloud. Nil uses a
	// client with a 30s timeout.
	HTTPClient *http.Client
}

// syncsAccessGroups reports whether LakeKeeper role membership is synced from
// a directory: embedded Keycloak, or spec.auth.groupSync for external issuers.
func syncsAccessGroups(dp *dataplatformv1alpha1.DataLake) bool {
	if !dp.Spec.Auth.IsEnabled() {
		return false
	}
	return dp.Spec.Auth.IsEmbedded() || dp.Spec.Auth.JumpCloudSync() != nil
}

// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=datalakes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=datalakes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=datalakes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts;configmaps;secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// ServiceAccount tokens (spec.auth.serviceAccountTokens): the operator mints its
// own tokens, and binds LakeKeeper to system:auth-delegator, which RBAC only
// allows when the operator holds that role's permissions itself.
// +kubebuilder:rbac:groups="",resources=serviceaccounts/token,verbs=create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *DataLakeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	dp := &dataplatformv1alpha1.DataLake{}
	if err := r.Get(ctx, req.NamespacedName, dp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, dp)
	if statusErr := r.patchStatus(ctx, dp); statusErr != nil {
		log.Error(statusErr, "Failed to update DataLake status")
		return ctrl.Result{}, statusErr
	}
	return result, err
}

func (r *DataLakeReconciler) reconcile(ctx context.Context, dp *dataplatformv1alpha1.DataLake) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Reconciling DataLake")

	progressing, store, oidc, fga, err := r.reconcileLakekeeperStack(ctx, dp)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}

	trinoProgressing, err := r.reconcileTrinoStack(ctx, dp, store, oidc, fga)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}
	progressing = progressing || trinoProgressing

	sampleProgressing, err := r.reconcileSampleData(ctx, dp, oidc)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}
	progressing = progressing || sampleProgressing

	debeziumProgressing, err := r.reconcileDebezium(ctx, dp, store, oidc)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionReady, metav1.ConditionFalse, reasonError, err.Error())
		return ctrl.Result{}, err
	}
	progressing = progressing || debeziumProgressing

	if progressing {
		setCondition(dp, dataplatformv1alpha1.ConditionReady, metav1.ConditionFalse, reasonReconciling, "Waiting for enabled components to become ready")
		return ctrl.Result{RequeueAfter: requeueWhileProgressing}, nil
	}

	setCondition(dp, dataplatformv1alpha1.ConditionReady, metav1.ConditionTrue, reasonReady, "All enabled components are ready")
	if syncsAccessGroups(dp) {
		return ctrl.Result{RequeueAfter: requeueAccessSync}, nil
	}
	return ctrl.Result{}, nil
}

func (r *DataLakeReconciler) reconcileLakekeeperStack(ctx context.Context, dp *dataplatformv1alpha1.DataLake) (bool, objectStore, oidcConfig, openfgaConfig, error) {
	if !dp.Spec.Lakekeeper.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionMinioReady, metav1.ConditionTrue, reasonDisabled, "LakeKeeper is disabled")
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionTrue, reasonDisabled, "LakeKeeper is disabled")
		setCondition(dp, dataplatformv1alpha1.ConditionLakekeeperReady, metav1.ConditionTrue, reasonDisabled, "LakeKeeper is disabled")
		setCondition(dp, dataplatformv1alpha1.ConditionWarehouseReady, metav1.ConditionTrue, reasonDisabled, "LakeKeeper is disabled")
		setCondition(dp, dataplatformv1alpha1.ConditionOpenFGAReady, metav1.ConditionTrue, reasonDisabled, "LakeKeeper is disabled")
		if !dp.Spec.Auth.IsEnabled() {
			setCondition(dp, dataplatformv1alpha1.ConditionAuthReady, metav1.ConditionTrue, reasonDisabled, "LakeKeeper is disabled")
		}
		return false, objectStore{}, oidcConfig{}, openfgaConfig{}, nil
	}

	ns := dp.Spec.Lakekeeper.NamespaceOrDefault()
	if err := r.ensureNamespace(ctx, dp, ns, componentLakekeeper); err != nil {
		return false, objectStore{}, oidcConfig{}, openfgaConfig{}, err
	}

	conn, err := r.reconcilePostgres(ctx, dp)
	if err != nil {
		return false, objectStore{}, oidcConfig{}, openfgaConfig{}, err
	}

	store, storeReady, err := r.reconcileObjectStore(ctx, dp)
	if err != nil {
		return false, objectStore{}, oidcConfig{}, openfgaConfig{}, err
	}

	oidc, authReady, err := r.reconcileAuth(ctx, dp)
	if err != nil {
		return false, store, oidc, openfgaConfig{}, err
	}

	fga, fgaReady, err := r.reconcileAuthz(ctx, dp)
	if err != nil {
		return false, store, oidc, fga, err
	}

	if authReady && fgaReady {
		if err := r.reconcileLakekeeper(ctx, dp, conn, oidc, fga); err != nil {
			return false, store, oidc, fga, err
		}
	} else if !authReady {
		setCondition(dp, dataplatformv1alpha1.ConditionLakekeeperReady, metav1.ConditionFalse, reasonNotReady, "Waiting for identity provider")
	} else {
		setCondition(dp, dataplatformv1alpha1.ConditionLakekeeperReady, metav1.ConditionFalse, reasonNotReady, "Waiting for OpenFGA")
	}

	progressing := !conditionTrue(dp, dataplatformv1alpha1.ConditionPostgresReady) ||
		!conditionTrue(dp, dataplatformv1alpha1.ConditionLakekeeperReady) ||
		!authReady ||
		!fgaReady ||
		!storeReady
	if progressing {
		return true, store, oidc, fga, nil
	}
	if err := r.reconcileWarehouse(ctx, dp, store, oidc, fga); err != nil {
		return false, store, oidc, fga, err
	}
	return !conditionTrue(dp, dataplatformv1alpha1.ConditionWarehouseReady), store, oidc, fga, nil
}

func (r *DataLakeReconciler) reconcileTrinoStack(ctx context.Context, dp *dataplatformv1alpha1.DataLake, store objectStore, oidc oidcConfig, fga openfgaConfig) (bool, error) {
	if !dp.Spec.Trino.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionTrinoReady, metav1.ConditionTrue, reasonDisabled, "Trino is disabled")
		return false, nil
	}

	ns := dp.Spec.Trino.NamespaceOrDefault()
	if err := r.ensureNamespace(ctx, dp, ns, componentTrinoCoordinator); err != nil {
		return false, err
	}
	if _, err := r.reconcileOPA(ctx, dp, oidc, fga); err != nil {
		return false, err
	}
	if err := r.reconcileTrino(ctx, dp, store, oidc, fga); err != nil {
		return false, err
	}
	return !conditionTrue(dp, dataplatformv1alpha1.ConditionTrinoReady), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DataLakeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dataplatformv1alpha1.DataLake{}).
		Owns(&corev1.Namespace{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&batchv1.Job{}).
		Named("datalake").
		Complete(r)
}
