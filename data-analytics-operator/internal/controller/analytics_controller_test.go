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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

var _ = Describe("Analytics Controller", func() {
	const resourceName = "local"
	ctx := context.Background()
	key := types.NamespacedName{Name: resourceName}

	var reconciler *AnalyticsReconciler

	BeforeEach(func() {
		reconciler = &AnalyticsReconciler{
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
				"supersetClientID":     []byte("superset"),
				"supersetClientSecret": []byte("superset-secret"),
				"trinoClientID":        []byte("trino"),
				"trinoClientSecret":    []byte("trino-secret"),
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		}

		// The data lake operator publishes Trino's CA here once Trino serves HTTPS.
		trinoNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: analyticsv1alpha1.DefaultTrinoNamespace}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: trinoNS.Name}, trinoNS); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, trinoNS)).To(Succeed())
		}
		trinoCA := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: analyticsv1alpha1.DefaultTrinoCAConfigMap, Namespace: trinoNS.Name}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: trinoCA.Name, Namespace: trinoCA.Namespace}, trinoCA); errors.IsNotFound(err) {
			trinoCA.Data = map[string]string{analyticsv1alpha1.DefaultTrinoCAKey: testTrinoCA}
			Expect(k8sClient.Create(ctx, trinoCA)).To(Succeed())
		}

		existing := &analyticsv1alpha1.Analytics{}
		err = k8sClient.Get(ctx, key, existing)
		if errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, &analyticsv1alpha1.Analytics{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName},
				Spec: analyticsv1alpha1.AnalyticsSpec{
					PublicURL: "https://superset.data-platform.local",
					Trino:     analyticsv1alpha1.TrinoSpec{Host: "trino.trino.svc"},
					Auth: analyticsv1alpha1.AuthSpec{
						Enabled: ptr.To(true),
						OIDC: analyticsv1alpha1.OIDCSpec{
							Issuer:       "http://keycloak.keycloak.svc:8080/realms/dataplatform",
							PublicIssuer: "https://keycloak.data-platform.local/realms/dataplatform",
							CredentialsSecretRef: &analyticsv1alpha1.CredentialsSecretRef{
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
		resource := &analyticsv1alpha1.Analytics{}
		err := k8sClient.Get(ctx, key, resource)
		if errors.IsNotFound(err) {
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
	})

	It("deploys Superset with Keycloak OAuth and a Trino database seed", func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namePostgres, Namespace: nameSuperset}, sts)).To(Succeed())
		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameSupersetRedis, Namespace: nameSuperset}, deploy)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameSuperset, Namespace: nameSuperset}, deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Spec.Containers[0].SecurityContext.RunAsUser).To(Equal(ptr.To(uidSuperset)))
		Expect(deploy.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name:      volumeConfig,
			MountPath: "/app/pythonpath",
		}))

		supersetCM := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapSuperset, Namespace: nameSuperset}, supersetCM)).To(Succeed())
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("AUTH_TYPE = AUTH_OAUTH"))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("DATABASE_OAUTH2_CLIENTS"))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring(`"Trino"`))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("https://superset.data-platform.local/api/v1/database/oauth2/"))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("platform-admins"))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring(`"authorize_url": "https://keycloak.data-platform.local/realms/dataplatform/protocol/openid-connect/auth"`))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring(`"access_token_url": "http://keycloak.keycloak.svc:8080/realms/dataplatform/protocol/openid-connect/token"`))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring(`"api_base_url": "http://keycloak.keycloak.svc:8080/realms/dataplatform/protocol/"`))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("DB_CONNECTION_MUTATOR"))
		Expect(supersetCM.Data[supersetConfigKey]).NotTo(ContainSubstring("server_metadata_url"))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring(`quote_plus(os.environ["DB_PASS"])`))
		pgSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretPostgres, Namespace: nameSuperset}, pgSecret)).To(Succeed())
		Expect(supersetCM.Data[supersetConfigKey]).NotTo(ContainSubstring(string(pgSecret.Data[keyPostgresPassword])))
		Expect(supersetCM.Data[supersetBootstrapKey]).To(ContainSubstring("database_name=name"))
		Expect(supersetCM.Data[supersetBootstrapKey]).To(ContainSubstring("impersonate_user = True"))
		By("reaching Trino over HTTPS, verified against the data lake's CA")
		Expect(supersetCM.Data[supersetBootstrapKey]).To(ContainSubstring("trino://trino.trino.svc:8443/lakekeeper"))
		Expect(supersetCM.Data[supersetBootstrapKey]).To(ContainSubstring(`connect_args["http_scheme"] = "https"`))
		Expect(supersetCM.Data[supersetBootstrapKey]).To(ContainSubstring(`connect_args["verify"] = "/app/trino-ca/ca.crt"`))
		// Superset's per-user OAuth session must verify against the same CA.
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("session.verify = verify"))
		// JumpCloud only accepts client_secret_basic for the Trino client.
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("auth=(config[\"id\"], config[\"secret\"])"))
		Expect(supersetCM.Data[supersetConfigKey]).To(ContainSubstring("TrinoEngineSpec.get_oauth2_token = classmethod(get_oauth2_token)"))
		caCopy := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapTrinoCA, Namespace: nameSuperset}, caCopy)).To(Succeed())
		Expect(caCopy.Data[keyTrinoCA]).To(Equal(testTrinoCA))
		Expect(deploy.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(HaveField("MountPath", trinoCADir)))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretSupersetOIDC, Namespace: nameSuperset}, &corev1.Secret{})).To(Succeed())

		updated := &analyticsv1alpha1.Analytics{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		Expect(updated.Status.SupersetEndpoint).To(Equal("http://superset.superset.svc:8088"))
	})

	It("uses an external Postgres without a password in the pod spec or ConfigMap", func() {
		const password = "s3cret-password"
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lakekeeper"}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}
		source := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "platform-postgres", Namespace: "lakekeeper"},
			Data: map[string][]byte{
				keyPostgresUsername: []byte("platform"),
				keyPostgresPassword: []byte(password),
			},
		}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: source.Name, Namespace: source.Namespace}, &corev1.Secret{}); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, source)).To(Succeed())
		}

		a := &analyticsv1alpha1.Analytics{}
		Expect(k8sClient.Get(ctx, key, a)).To(Succeed())
		a.Spec.Postgres = analyticsv1alpha1.PostgresSpec{
			Embedded: ptr.To(false),
			Host:     "pg.example.com",
			SSLMode:  "require",
			CredentialsSecretRef: &analyticsv1alpha1.PostgresCredentialsSecretRef{
				Name:      "platform-postgres",
				Namespace: "lakekeeper",
			},
		}
		Expect(k8sClient.Update(ctx, a)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		copied := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretPostgresExternal, Namespace: nameSuperset}, copied)).To(Succeed())
		Expect(string(copied.Data[keyPostgresPassword])).To(Equal(password))

		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameSuperset, Namespace: nameSuperset}, deploy)).To(Succeed())
		pod := deploy.Spec.Template.Spec
		Expect(pod.InitContainers).To(HaveLen(1))
		Expect(pod.InitContainers[0].Name).To(Equal("ensure-database"))
		env := pod.Containers[0].Env
		Expect(envValue(env, "DB_HOST")).To(Equal("pg.example.com"))
		Expect(envValue(env, "DB_NAME")).To(Equal("superset"))
		Expect(envValue(env, "DB_SSLMODE")).To(Equal("require"))
		Expect(envSecretName(env, "DB_PASS")).To(Equal(secretPostgresExternal))
		for _, c := range append(pod.InitContainers, pod.Containers...) {
			for _, e := range c.Env {
				Expect(e.Value).NotTo(ContainSubstring(password), "container %s env %s", c.Name, e.Name)
			}
		}

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: configMapSuperset, Namespace: nameSuperset}, cm)).To(Succeed())
		Expect(cm.Data[supersetConfigKey]).NotTo(ContainSubstring(password))

		updated := &analyticsv1alpha1.Analytics{}
		Expect(k8sClient.Get(ctx, key, updated)).To(Succeed())
		cond := meta.FindStatusCondition(updated.Status.Conditions, analyticsv1alpha1.ConditionPostgresReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Message).To(Equal("Using external Postgres"))
	})
})

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func envSecretName(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			return e.ValueFrom.SecretKeyRef.Name
		}
	}
	return ""
}

const testTrinoCA = "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"
