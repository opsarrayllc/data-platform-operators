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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

var _ = Describe("Orchestration Controller", func() {
	const resourceName = "local"
	ctx := context.Background()
	key := types.NamespacedName{Name: resourceName}

	var reconciler *OrchestrationReconciler

	BeforeEach(func() {
		reconciler = &OrchestrationReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "keycloak"}}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)
		if errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "oidc", Namespace: "keycloak"}}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, secret)
		if errors.IsNotFound(err) {
			secret.Data = map[string][]byte{
				"argoClientID":     []byte("argo"),
				"argoClientSecret": []byte("argo-secret"),
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		}

		existing := &orchestrationv1alpha1.Orchestration{}
		err = k8sClient.Get(ctx, key, existing)
		if errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, &orchestrationv1alpha1.Orchestration{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec: orchestrationv1alpha1.OrchestrationSpec{
					PublicURL: "https://argo.data-platform.local",
					Auth: orchestrationv1alpha1.AuthSpec{
						Enabled: ptr.To(true),
						OIDC: orchestrationv1alpha1.OIDCSpec{
							Issuer:       "http://keycloak.keycloak.svc:8080/realms/dataplatform",
							PublicIssuer: "https://keycloak.data-platform.local/realms/dataplatform",
							CredentialsSecretRef: &orchestrationv1alpha1.CredentialsSecretRef{
								Name:      "oidc",
								Namespace: "keycloak",
							},
						},
					},
				},
			})).To(Succeed())
		}
	})

	AfterEach(func() {
		resource := &orchestrationv1alpha1.Orchestration{}
		err := k8sClient.Get(ctx, key, resource)
		if errors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
	})

	It("configures Argo SSO from the public issuer and group ServiceAccounts", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapWorkflowController, Namespace: orchestrationv1alpha1.DefaultNamespace}, cm)).To(Succeed())
		Expect(cm.Data[keyConfig]).To(ContainSubstring("https://keycloak.data-platform.local/realms/dataplatform"))
		Expect(cm.Data[keyConfig]).To(ContainSubstring("- openid"))
		Expect(cm.Data[keyConfig]).To(ContainSubstring("- profile"))
		Expect(cm.Data[keyConfig]).To(ContainSubstring("- email"))
		Expect(cm.Data[keyConfig]).To(ContainSubstring("enabled: true"))
		Expect(cm.Data[keyConfig]).To(ContainSubstring("serviceAccountName: " + nameExecutor))

		server := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameServer, Namespace: orchestrationv1alpha1.DefaultNamespace}, server)).To(Succeed())
		Expect(server.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--auth-mode"))
		Expect(server.Spec.Template.Spec.Containers[0].Args).To(ContainElement("sso"))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameController, Namespace: orchestrationv1alpha1.DefaultNamespace}, &appsv1.Deployment{})).To(Succeed())

		for _, group := range groupAccounts() {
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: group.name, Namespace: orchestrationv1alpha1.DefaultNamespace}, sa)).To(Succeed())
			Expect(sa.Annotations[annoRBACRule]).To(Equal(group.rule))
			Expect(sa.Annotations[annoRBACPrecedence]).To(Equal(group.precedence))

			role := &rbacv1.Role{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: group.name, Namespace: orchestrationv1alpha1.DefaultNamespace}, role)).To(Succeed())
			Expect(role.Rules[0].Verbs).To(Equal(group.verbs))

			token := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: group.name + ".service-account-token", Namespace: orchestrationv1alpha1.DefaultNamespace}, token)).To(Succeed())
			Expect(token.Type).To(Equal(corev1.SecretTypeServiceAccountToken))
		}
	})
})
