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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

const (
	managedBy            = "data-analytics-operator"
	labelAppName         = "app.kubernetes.io/name"
	labelAppInstance     = "app.kubernetes.io/instance"
	labelAppComponent    = "app.kubernetes.io/component"
	labelAppManagedBy    = "app.kubernetes.io/managed-by"
	labelPlatform        = "dataplatform.opsarray.io/analytics"
	annotationConfigHash = "dataplatform.opsarray.io/config-hash"

	componentSuperset         = "superset"
	componentSupersetPostgres = "superset-postgres"
	componentSupersetRedis    = "superset-redis"

	namePostgres      = "postgres"
	nameSuperset      = "superset"
	nameSupersetRedis = "superset-redis"

	secretPostgres     = "postgres"
	secretSuperset     = "superset"
	secretSupersetOIDC = "superset-oidc"
	configMapSuperset  = "superset-config"

	keyPostgresUsername         = "username"
	keyPostgresPassword         = "password"
	keyPostgresDatabase         = "database"
	envPostgresUser             = "POSTGRES_USER"
	envPostgresPassword         = "POSTGRES_PASSWORD"
	envPostgresDB               = "POSTGRES_DB"
	keyOIDCSupersetClientID     = "supersetClientID"
	keyOIDCSupersetClientSecret = "supersetClientSecret"
	keyOIDCTrinoClientID        = "trinoClientID"
	keyOIDCTrinoClientSecret    = "trinoClientSecret"
	keySupersetSecretKey        = "SECRET_KEY"

	portNameHTTP = "http"
	volumeData   = "data"
	volumeConfig = "config"
	keyPGDATA    = "PGDATA"
	pgDataMount  = "/var/lib/postgresql/data"
	pgDataPath   = "/var/lib/postgresql/data/pgdata"

	postgresPort = int32(5432)
	supersetPort = int32(8088)
	redisPort    = int32(6379)

	uidPostgres = int64(999)
	gidPostgres = int64(999)
	uidSuperset = int64(1000)
	gidSuperset = int64(1000)
	uidRedis    = int64(999)
	gidRedis    = int64(999)

	reasonReconciling = "Reconciling"
	reasonReady       = "Ready"
	reasonNotReady    = "NotReady"
	reasonError       = "Error"
)

func labelsFor(a *analyticsv1alpha1.Analytics, component string) map[string]string {
	name := nameSuperset
	switch component {
	case componentSupersetPostgres:
		name = namePostgres
	case componentSupersetRedis:
		name = nameSupersetRedis
	}
	return map[string]string{
		labelAppName:      name,
		labelAppInstance:  a.Name,
		labelAppComponent: component,
		labelAppManagedBy: managedBy,
		labelPlatform:     a.Name,
	}
}

func ensureLabels(obj metav1.Object, labels map[string]string) {
	current := obj.GetLabels()
	if current == nil {
		current = map[string]string{}
	}
	maps.Copy(current, labels)
	obj.SetLabels(current)
}

func clusterServiceURL(name, namespace string, port int32) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", name, namespace, port)
}

func hashData(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func setCondition(a *analyticsv1alpha1.Analytics, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: a.Generation,
	})
}

func conditionTrue(a *analyticsv1alpha1.Analytics, condType string) bool {
	return meta.IsStatusConditionTrue(a.Status.Conditions, condType)
}

func (r *AnalyticsReconciler) apply(
	ctx context.Context,
	a *analyticsv1alpha1.Analytics,
	obj client.Object,
	mutate func() error,
) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if err := mutate(); err != nil {
			return err
		}
		return controllerutil.SetControllerReference(a, obj, r.Scheme)
	})
	return err
}

func (r *AnalyticsReconciler) ensureGeneratedSecret(
	ctx context.Context,
	a *analyticsv1alpha1.Analytics,
	name, namespace, component string,
	data map[string][]byte,
) error {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: name, Namespace: namespace}
	err := r.Get(ctx, key, secret)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labelsFor(a, component),
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if err := controllerutil.SetControllerReference(a, secret, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, secret)
}

func (r *AnalyticsReconciler) getSecretData(ctx context.Context, name, namespace, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err != nil {
		return "", err
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s is missing key %s", namespace, name, key)
	}
	return string(val), nil
}

func (r *AnalyticsReconciler) ensureNamespace(ctx context.Context, a *analyticsv1alpha1.Analytics, name, component string) error {
	log := logf.FromContext(ctx)
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, ns)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labelsFor(a, component),
		},
	}
	if err := controllerutil.SetControllerReference(a, ns, r.Scheme); err != nil {
		return err
	}
	log.Info("Creating Namespace", "name", name)
	return r.Create(ctx, ns)
}

func (r *AnalyticsReconciler) deploymentReady(ctx context.Context, ns, name string) (bool, error) {
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, deploy); err != nil {
		return false, err
	}
	return deploy.Status.ReadyReplicas >= 1, nil
}

func (r *AnalyticsReconciler) statefulSetReady(ctx context.Context, ns, name string) (bool, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, sts); err != nil {
		return false, err
	}
	return sts.Status.ReadyReplicas >= 1, nil
}

func restrictedPodSecurity(uid, gid int64) *corev1.PodSecurityContext {
	sc := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if uid > 0 {
		sc.RunAsUser = ptr.To(uid)
	}
	if gid > 0 {
		sc.RunAsGroup = ptr.To(gid)
		sc.FSGroup = ptr.To(gid)
		sc.FSGroupChangePolicy = ptr.To(corev1.FSGroupChangeOnRootMismatch)
	}
	return sc
}

func restrictedContainerSecurity(uid, gid int64) *corev1.SecurityContext {
	sc := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		RunAsNonRoot:             ptr.To(true),
	}
	if uid > 0 {
		sc.RunAsUser = ptr.To(uid)
	}
	if gid > 0 {
		sc.RunAsGroup = ptr.To(gid)
	}
	return sc
}

func (r *AnalyticsReconciler) patchStatus(ctx context.Context, a *analyticsv1alpha1.Analytics) error {
	latest := &analyticsv1alpha1.Analytics{}
	if err := r.Get(ctx, types.NamespacedName{Name: a.Name}, latest); err != nil {
		return err
	}
	latest.Status = a.Status
	return r.Status().Update(ctx, latest)
}
