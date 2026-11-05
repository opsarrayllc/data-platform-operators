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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

const (
	testExternalPGHost     = "pg.example.com"
	testExternalPGPassword = "s3cret-password"
	testExternalPGSecret   = "platform-postgres"
)

var _ = Describe("External Postgres", func() {
	// Reuse the main suite's name: envtest has no garbage collector, so objects
	// from earlier specs stay owned by a DataLake with this name.
	const resourceName = "test-platform"

	ctx := context.Background()
	name := types.NamespacedName{Name: resourceName}

	ensureNamespace := func(n string) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: n}, ns); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}
	}

	BeforeEach(func() {
		existing := &dataplatformv1alpha1.DataLake{}
		if err := k8sClient.Get(ctx, name, existing); err == nil {
			Expect(k8sClient.Delete(ctx, existing)).To(Succeed())
			Eventually(func() bool {
				return errors.IsNotFound(k8sClient.Get(ctx, name, existing))
			}).Should(BeTrue())
		}
		ensureNamespace(nameLakekeeper)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testExternalPGSecret, Namespace: nameLakekeeper},
			Data: map[string][]byte{
				keyPostgresUsername: []byte("platform"),
				keyPostgresPassword: []byte(testExternalPGPassword),
			},
		}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &corev1.Secret{}); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		}
	})

	AfterEach(func() {
		dp := &dataplatformv1alpha1.DataLake{}
		if err := k8sClient.Get(ctx, name, dp); err == nil {
			Expect(k8sClient.Delete(ctx, dp)).To(Succeed())
		}
	})

	It("runs OpenFGA against an external datastore without a password in the pod spec", func() {
		dp := &dataplatformv1alpha1.DataLake{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			Spec: dataplatformv1alpha1.DataLakeSpec{
				Authz: dataplatformv1alpha1.AuthzSpec{
					OpenFGA: dataplatformv1alpha1.OpenFGASpec{
						Postgres: dataplatformv1alpha1.PostgresSpec{
							Embedded: ptr.To(false),
							Host:     testExternalPGHost,
							SSLMode:  "require",
							CredentialsSecretRef: &dataplatformv1alpha1.PostgresCredentialsSecretRef{
								Name:      testExternalPGSecret,
								Namespace: nameLakekeeper,
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, dp)).To(Succeed())

		reconciler := &DataLakeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Catalog: &fakeCatalog{}}
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: name})
		Expect(err).NotTo(HaveOccurred())

		By("copying the credentials into the OpenFGA namespace")
		copied := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretPostgresExternal, Namespace: nameOpenFGA}, copied)).To(Succeed())
		Expect(string(copied.Data[keyPostgresPassword])).To(Equal(testExternalPGPassword))

		By("creating the Deployment without waiting for an embedded StatefulSet")
		deploy := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nameOpenFGA, Namespace: nameOpenFGA}, deploy)).To(Succeed())
		pod := deploy.Spec.Template.Spec
		Expect(pod.InitContainers).To(HaveLen(2))
		Expect(pod.InitContainers[0].Name).To(Equal("ensure-database"))
		Expect(pod.InitContainers[1].Name).To(Equal("migrate"))
		Expect(envValue(pod.InitContainers[0].Env, "TARGET_DB")).To(Equal(defaultOpenFGAExternalDatabase))
		Expect(envValue(pod.InitContainers[0].Env, "PGSSLMODE")).To(Equal("require"))
		Expect(deploy.Spec.Template.Annotations).To(HaveKey(annotationConfigHash))

		env := pod.Containers[0].Env
		Expect(envValue(env, "OPENFGA_DATASTORE_URI")).To(Equal("postgres://pg.example.com:5432/openfga?sslmode=require"))
		Expect(envSecretName(env, "OPENFGA_DATASTORE_PASSWORD")).To(Equal(secretPostgresExternal))
		for _, c := range append(pod.InitContainers, pod.Containers...) {
			for _, e := range c.Env {
				Expect(e.Value).NotTo(ContainSubstring(testExternalPGPassword), "container %s env %s", c.Name, e.Name)
			}
		}
	})

	It("rejects a database name that is not a plain identifier", func() {
		dp := &dataplatformv1alpha1.DataLake{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName},
			Spec: dataplatformv1alpha1.DataLakeSpec{
				Lakekeeper: dataplatformv1alpha1.LakekeeperSpec{
					Postgres: dataplatformv1alpha1.PostgresSpec{Database: `x"; DROP DATABASE postgres; --`},
				},
			},
		}
		err := k8sClient.Create(ctx, dp)
		Expect(err).To(HaveOccurred())
		Expect(errors.IsInvalid(err)).To(BeTrue())
	})
})

var _ = Describe("lakekeeperEnv", func() {
	It("reads external credentials from the operator-owned Secret", func() {
		conn := postgresConn{
			Host:     testExternalPGHost,
			Port:     postgresPort,
			Database: "lakekeeper",
			Username: "platform",
			Password: testExternalPGPassword,
			SSLMode:  "require",
			Secret:   secretPostgresExternal,
			External: true,
		}
		env := lakekeeperEnv(&dataplatformv1alpha1.DataLake{}, conn, oidcConfig{}, openfgaConfig{})
		Expect(envSecretName(env, "LAKEKEEPER__PG_USER")).To(Equal(secretPostgresExternal))
		Expect(envSecretName(env, "LAKEKEEPER__PG_PASSWORD")).To(Equal(secretPostgresExternal))
		Expect(envValue(env, "LAKEKEEPER__PG_SSL_MODE")).To(Equal("require"))
		for _, e := range env {
			Expect(e.Value).NotTo(ContainSubstring(testExternalPGPassword), "env %s", e.Name)
		}
	})
})

var _ = Describe("openfgaDatastoreURI", func() {
	It("leaves credentials out and defaults sslmode to prefer", func() {
		uri := openfgaDatastoreURI(postgresConn{Host: "db", Port: 5432, Database: "openfga", Username: "u", Password: "p"})
		Expect(uri).To(Equal("postgres://db:5432/openfga?sslmode=prefer"))
		Expect(strings.Contains(uri, "@")).To(BeFalse())
	})
})

func envSecretName(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			return e.ValueFrom.SecretKeyRef.Name
		}
	}
	return ""
}
