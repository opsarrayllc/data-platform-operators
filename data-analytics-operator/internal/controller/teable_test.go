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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

var _ = Describe("Analytics Controller with Teable", func() {
	const (
		resourceName  = "teable-test"
		supersetNS    = "superset-teable-test"
		oidcSecret    = "oidc-teable-test"
		minioPassword = "minio-root-password"
	)
	ctx := context.Background()
	key := types.NamespacedName{Name: resourceName}
	var reconciler *AnalyticsReconciler

	ensureNS := func(name string) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, ns); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}
	}
	ensureSecret := func(name, ns string, data map[string][]byte) {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: data}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &corev1.Secret{}); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, s)).To(Succeed())
		}
	}
	analytics := func(teableNS string, oidcKeys bool) *analyticsv1alpha1.Analytics {
		secretName := oidcSecret
		if !oidcKeys {
			secretName = "oidc"
		}
		return &analyticsv1alpha1.Analytics{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			Spec: analyticsv1alpha1.AnalyticsSpec{
				Namespace: supersetNS,
				Trino:     analyticsv1alpha1.TrinoSpec{HTTPScheme: "http"},
				Auth: analyticsv1alpha1.AuthSpec{
					Enabled: ptr.To(true),
					OIDC: analyticsv1alpha1.OIDCSpec{
						Issuer:       "http://keycloak.keycloak.svc:8080/realms/dataplatform",
						PublicIssuer: "https://keycloak.data-platform.local/realms/dataplatform",
						CredentialsSecretRef: &analyticsv1alpha1.CredentialsSecretRef{
							Name:      secretName,
							Namespace: "keycloak",
						},
					},
				},
				Teable: analyticsv1alpha1.TeableSpec{
					Enabled:   ptr.To(true),
					Namespace: teableNS,
					PublicURL: "https://teable.data-platform.local/",
					Storage: analyticsv1alpha1.TeableStorageSpec{
						Endpoint:         "https://minio-api.data-platform.local",
						InternalEndpoint: "http://minio.minio.svc:9000",
						ForcePathStyle:   ptr.To(true),
						CreateBuckets:    ptr.To(true),
						CredentialsSecretRef: &analyticsv1alpha1.S3CredentialsSecretRef{
							Name:      "minio",
							Namespace: "minio",
						},
					},
				},
			},
		}
	}

	BeforeEach(func() {
		reconciler = &AnalyticsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		ensureNS("keycloak")
		ensureNS("minio")
		ensureNS(analyticsv1alpha1.DefaultTrinoNamespace)
		ensureSecret(oidcSecret, "keycloak", map[string][]byte{
			"supersetClientID":     []byte("superset"),
			"supersetClientSecret": []byte("superset-secret"),
			"trinoClientID":        []byte("trino"),
			"trinoClientSecret":    []byte("trino-secret"),
			"teableClientID":       []byte("teable"),
			"teableClientSecret":   []byte("teable-secret"),
		})
		// The shared fixture from the Superset spec has no Teable keys.
		ensureSecret("oidc", "keycloak", map[string][]byte{
			"supersetClientID":     []byte("superset"),
			"supersetClientSecret": []byte("superset-secret"),
			"trinoClientID":        []byte("trino"),
			"trinoClientSecret":    []byte("trino-secret"),
		})
		ensureSecret("minio", "minio", map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte("minio-root"),
			"AWS_SECRET_ACCESS_KEY": []byte(minioPassword),
		})
	})

	AfterEach(func() {
		existing := &analyticsv1alpha1.Analytics{}
		if err := k8sClient.Get(ctx, key, existing); err == nil {
			Expect(k8sClient.Delete(ctx, existing)).To(Succeed())
		}
	})

	It("deploys Teable with Keycloak sign-in and MinIO storage, and nothing for Trino", func() {
		const teableNS = "teable-keycloak"
		Expect(k8sClient.Create(ctx, analytics(teableNS, true))).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameTeablePostgres, Namespace: teableNS}, &appsv1.StatefulSet{})).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameTeableRedis, Namespace: teableNS}, &appsv1.Deployment{})).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameTeable, Namespace: teableNS}, &corev1.Service{})).To(Succeed())

		keys := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretTeable, Namespace: teableNS}, keys)).To(Succeed())
		for _, slot := range teableAESSlots {
			Expect(keys.Data[slot]).To(HaveLen(16), slot)
		}
		db := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretTeableDatabase, Namespace: teableNS}, db)).To(Succeed())
		Expect(string(db.Data["PRISMA_DATABASE_URL"])).To(HavePrefix("postgresql://teable:"))
		Expect(string(db.Data["PRISMA_DATABASE_URL"])).To(HaveSuffix("@teable-postgres:5432/teable?sslmode=disable"))
		storage := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretTeableStorage, Namespace: teableNS}, storage)).To(Succeed())
		Expect(string(storage.Data["BACKEND_STORAGE_S3_SECRET_KEY"])).To(Equal(minioPassword))
		oidc := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretTeableOIDC, Namespace: teableNS}, oidc)).To(Succeed())
		Expect(string(oidc.Data["BACKEND_OIDC_CLIENT_ID"])).To(Equal("teable"))

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameTeable, Namespace: teableNS}, deploy)).To(Succeed())
		pod := deploy.Spec.Template.Spec
		Expect(pod.EnableServiceLinks).To(Equal(ptr.To(false)))
		Expect(pod.Containers[0].SecurityContext.RunAsUser).To(Equal(ptr.To(uidTeable)))
		env := pod.Containers[0].Env
		Expect(envValue(env, "PUBLIC_ORIGIN")).To(Equal("https://teable.data-platform.local"))
		Expect(envValue(env, "BACKEND_CACHE_REDIS_URI")).To(Equal("redis://teable-redis:6379/0"))
		Expect(envValue(env, "BACKEND_STORAGE_PROVIDER")).To(Equal("s3"))
		Expect(envValue(env, "BACKEND_STORAGE_S3_ENDPOINT")).To(Equal("https://minio-api.data-platform.local"))
		Expect(envValue(env, "BACKEND_STORAGE_S3_INTERNAL_ENDPOINT")).To(Equal("http://minio.minio.svc:9000"))
		Expect(envValue(env, "BACKEND_STORAGE_S3_INTERNAL_FORCE_PATH_STYLE")).To(Equal("true"))
		Expect(envValue(env, "BACKEND_STORAGE_PUBLIC_URL")).To(Equal("https://minio-api.data-platform.local/teable-public"))
		Expect(envValue(env, "SOCIAL_AUTH_PROVIDERS")).To(Equal("oidc"))
		Expect(envValue(env, "PASSWORD_LOGIN_DISABLED")).To(Equal("true"))
		Expect(envValue(env, "BACKEND_OIDC_ISSUER")).To(Equal("https://keycloak.data-platform.local/realms/dataplatform"))
		Expect(envValue(env, "BACKEND_OIDC_AUTHORIZATION_URL")).To(Equal("https://keycloak.data-platform.local/realms/dataplatform/protocol/openid-connect/auth"))
		Expect(envValue(env, "BACKEND_OIDC_TOKEN_URL")).To(Equal("http://keycloak.keycloak.svc:8080/realms/dataplatform/protocol/openid-connect/token"))
		Expect(envValue(env, "BACKEND_OIDC_CALLBACK_URL")).To(Equal("https://teable.data-platform.local/api/auth/oidc/callback"))
		envFrom := make([]string, 0, len(pod.Containers[0].EnvFrom))
		for _, src := range pod.Containers[0].EnvFrom {
			envFrom = append(envFrom, src.SecretRef.Name)
		}
		Expect(envFrom).To(ConsistOf(secretTeable, secretTeableDatabase, secretTeableStorage, secretTeableOIDC))
		Expect(pod.InitContainers).To(HaveLen(1))
		Expect(pod.InitContainers[0].Name).To(Equal("ensure-buckets"))
		Expect(envValue(pod.InitContainers[0].Env, "S3_ENDPOINT")).To(Equal("http://minio.minio.svc:9000"))
		for _, c := range append(pod.InitContainers, pod.Containers...) {
			for _, e := range c.Env {
				Expect(e.Value).NotTo(ContainSubstring(minioPassword), "container %s env %s", c.Name, e.Name)
				Expect(e.Value).NotTo(ContainSubstring("teable-secret"), "container %s env %s", c.Name, e.Name)
			}
		}

		By("keeping generated keys stable across reconciles")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		again := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretTeable, Namespace: teableNS}, again)).To(Succeed())
		Expect(again.Data).To(Equal(keys.Data))

		By("publishing nothing into the Trino namespace")
		trinoSecrets := &corev1.SecretList{}
		Expect(k8sClient.List(ctx, trinoSecrets, client.InNamespace(analyticsv1alpha1.DefaultTrinoNamespace),
			client.MatchingLabels{labelAppComponent: componentTeable})).To(Succeed())
		Expect(trinoSecrets.Items).To(BeEmpty())

		updated := &analyticsv1alpha1.Analytics{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.TeableEndpoint).To(Equal("http://teable.teable-keycloak.svc:3000"))
		ready := meta.FindStatusCondition(updated.Status.Conditions, analyticsv1alpha1.ConditionTeableReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, analyticsv1alpha1.ConditionReady)).To(BeFalse())
	})

	It("reports missing Teable client credentials without a partial Teable deployment", func() {
		const teableNS = "teable-missing-keys"
		Expect(k8sClient.Create(ctx, analytics(teableNS, false))).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(MatchError(ContainSubstring("teableClientID")))
		err = k8sClient.Get(ctx, types.NamespacedName{Name: nameTeable, Namespace: teableNS}, &appsv1.Deployment{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("requires storage settings when Teable is enabled", func() {
		const teableNS = "teable-no-storage"
		a := analytics(teableNS, true)
		a.Spec.Teable.Storage = analyticsv1alpha1.TeableStorageSpec{}
		Expect(k8sClient.Create(ctx, a)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(MatchError(ContainSubstring("spec.teable.storage.endpoint")))
		updated := &analyticsv1alpha1.Analytics{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(meta.IsStatusConditionFalse(updated.Status.Conditions, analyticsv1alpha1.ConditionTeableReady)).To(BeTrue())
	})

	It("leaves Teable out when it is disabled", func() {
		const teableNS = "teable-disabled"
		a := analytics(teableNS, false)
		a.Spec.Teable.Enabled = nil
		Expect(k8sClient.Create(ctx, a)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: teableNS}, &corev1.Namespace{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
		updated := &analyticsv1alpha1.Analytics{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.TeableEndpoint).To(BeEmpty())
		Expect(meta.FindStatusCondition(updated.Status.Conditions, analyticsv1alpha1.ConditionTeableReady)).To(BeNil())
	})
})
