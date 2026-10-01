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

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

const (
	managedBy            = "data-orchestration-operator"
	labelAppName         = "app.kubernetes.io/name"
	labelAppInstance     = "app.kubernetes.io/instance"
	labelAppComponent    = "app.kubernetes.io/component"
	labelAppManagedBy    = "app.kubernetes.io/managed-by"
	labelPlatform        = "dataplatform.opsarray.io/orchestration"
	annotationConfigHash = "dataplatform.opsarray.io/config-hash"

	componentArgo       = "argo"
	componentController = "workflow-controller"
	componentServer     = "argo-server"
	componentExecutor   = "workflow-executor"

	nameController   = "workflow-controller"
	nameServer       = "argo-server"
	nameExecutor     = "workflow-executor"
	nameControllerSA = "argo"

	configMapWorkflowController = "workflow-controller-configmap"
	secretSSO                   = "argo-sso"
	keySSOClientID              = "client-id"
	keySSOClientSecret          = "client-secret"
	keyConfig                   = "config"

	portServer  = int32(2746)
	portHealthz = int32(6060)
	portMetrics = int32(9090)

	ingressNamespace = "ingress-nginx"
	ingressService   = "ingress-nginx-controller"

	reasonReconciling = "Reconciling"
	reasonReady       = "Ready"
	reasonNotReady    = "NotReady"
	reasonError       = "Error"
)

func labelsFor(o *orchestrationv1alpha1.Orchestration, component string) map[string]string {
	name := nameServer
	switch component {
	case componentController:
		name = nameController
	case componentExecutor:
		name = nameExecutor
	}
	return map[string]string{
		labelAppName:      name,
		labelAppInstance:  o.Name,
		labelAppComponent: component,
		labelAppManagedBy: managedBy,
		labelPlatform:     o.Name,
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

func setCondition(o *orchestrationv1alpha1.Orchestration, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&o.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: o.Generation,
	})
}

func conditionTrue(o *orchestrationv1alpha1.Orchestration, condType string) bool {
	return meta.IsStatusConditionTrue(o.Status.Conditions, condType)
}

func (r *OrchestrationReconciler) apply(
	ctx context.Context,
	o *orchestrationv1alpha1.Orchestration,
	obj client.Object,
	mutate func() error,
) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if err := mutate(); err != nil {
			return err
		}
		return controllerutil.SetControllerReference(o, obj, r.Scheme)
	})
	return err
}

func (r *OrchestrationReconciler) getSecretData(ctx context.Context, name, namespace, key string) (string, error) {
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

func (r *OrchestrationReconciler) ensureNamespace(ctx context.Context, o *orchestrationv1alpha1.Orchestration, name, component string) error {
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
			Labels: labelsFor(o, component),
		},
	}
	if err := controllerutil.SetControllerReference(o, ns, r.Scheme); err != nil {
		return err
	}
	log.Info("Creating Namespace", "name", name)
	return r.Create(ctx, ns)
}

func (r *OrchestrationReconciler) deploymentReady(ctx context.Context, ns, name string) (bool, error) {
	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, deploy); err != nil {
		return false, err
	}
	return deploy.Status.ReadyReplicas >= 1, nil
}

func restrictedPodSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func restrictedContainerSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
	}
}

func (r *OrchestrationReconciler) patchStatus(ctx context.Context, o *orchestrationv1alpha1.Orchestration) error {
	latest := &orchestrationv1alpha1.Orchestration{}
	if err := r.Get(ctx, types.NamespacedName{Name: o.Name}, latest); err != nil {
		return err
	}
	latest.Status = o.Status
	return r.Status().Update(ctx, latest)
}
