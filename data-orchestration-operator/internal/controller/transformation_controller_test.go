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
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

var _ = Describe("Transformation Controller", func() {
	const (
		orchName   = "transforms"
		workflowNS = "transforms-argo"
		name       = "acme"
	)
	ctx := context.Background()
	key := types.NamespacedName{Name: name}

	var reconciler *TransformationReconciler

	ensureNamespace := func(n string) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: n}, ns); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		}
	}

	setOrchestrationReady := func(ready bool) {
		o := &orchestrationv1alpha1.Orchestration{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: orchName}, o)).To(Succeed())
		status := metav1.ConditionFalse
		if ready {
			status = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&o.Status.Conditions, metav1.Condition{
			Type: orchestrationv1alpha1.ConditionReady, Status: status, Reason: "Test", Message: "test",
		})
		Expect(k8sClient.Status().Update(ctx, o)).To(Succeed())
	}

	reconcileOnce := func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}

	getCronWorkflow := func(n string) (*unstructured.Unstructured, error) {
		cw := &unstructured.Unstructured{}
		cw.SetGroupVersionKind(cronWorkflowGVK)
		return cw, k8sClient.Get(ctx, types.NamespacedName{Name: n, Namespace: workflowNS}, cw)
	}

	BeforeEach(func() {
		reconciler = &TransformationReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		ensureNamespace(workflowNS)
		ensureNamespace("trino")
		ca := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "trino-ca", Namespace: "trino"}}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ca.Name, Namespace: ca.Namespace}, ca); errors.IsNotFound(err) {
			ca.Data = map[string]string{"ca.crt": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"}
			Expect(k8sClient.Create(ctx, ca)).To(Succeed())
		}

		o := &orchestrationv1alpha1.Orchestration{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: orchName}, o); errors.IsNotFound(err) {
			Expect(k8sClient.Create(ctx, &orchestrationv1alpha1.Orchestration{
				ObjectMeta: metav1.ObjectMeta{Name: orchName},
				Spec: orchestrationv1alpha1.OrchestrationSpec{
					Namespace: workflowNS,
					Service:   orchestrationv1alpha1.ServiceSpec{},
				},
			})).To(Succeed())
		}
		setOrchestrationReady(true)

		Expect(k8sClient.Create(ctx, &orchestrationv1alpha1.Transformation{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: orchestrationv1alpha1.TransformationSpec{
				OrchestrationRef: orchName,
				Project: orchestrationv1alpha1.ProjectSpec{Git: orchestrationv1alpha1.GitSource{
					URL:                  "https://github.com/example/dbt.git",
					Path:                 "project",
					CredentialsSecretRef: &orchestrationv1alpha1.SecretKeyRef{Name: "dbt-git"},
				}},
				Vars: map[string]string{"env": "prod"},
				Lanes: []orchestrationv1alpha1.LaneSpec{
					{
						Name:     "live",
						Select:   "tag:live",
						OnChange: &orchestrationv1alpha1.OnChangeSpec{Sources: []string{"sales", "crm"}},
					},
					{
						Name:     "daily",
						Command:  orchestrationv1alpha1.LaneCommandBuild,
						Select:   "tag:daily",
						Schedule: "30 6 * * *",
						Timezone: "America/New_York",
					},
				},
				Maintenance: &orchestrationv1alpha1.MaintenanceSpec{
					Namespaces: []string{"analytics"},
				},
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		t := &orchestrationv1alpha1.Transformation{}
		if err := k8sClient.Get(ctx, key, t); err == nil {
			Expect(k8sClient.Delete(ctx, t)).To(Succeed())
		}
		// envtest runs no garbage collector, so remove the CronWorkflows here.
		for _, lane := range []string{"live", "daily", "maintenance"} {
			if cw, err := getCronWorkflow(name + "-" + lane); err == nil {
				Expect(k8sClient.Delete(ctx, cw)).To(Succeed())
			}
		}
	})

	It("waits for its Orchestration to be ready", func() {
		setOrchestrationReady(false)
		res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(requeueWhileProgressing))

		t := &orchestrationv1alpha1.Transformation{}
		Expect(k8sClient.Get(ctx, key, t)).To(Succeed())
		cond := meta.FindStatusCondition(t.Status.Conditions, orchestrationv1alpha1.ConditionTransformationReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(reasonReconciling))
		_, err = getCronWorkflow(name + "-live")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("renders one CronWorkflow per lane plus maintenance", func() {
		reconcileOnce()

		By("creating the runner identity")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "transform-acme", Namespace: workflowNS}, &corev1.ServiceAccount{})).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "transform-acme-state", Namespace: workflowNS}, &corev1.ConfigMap{})).To(Succeed())
		role := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "transform-acme", Namespace: workflowNS}, role)).To(Succeed())
		Expect(role.Rules).To(ContainElement(rbacv1.PolicyRule{
			APIGroups: []string{""}, Resources: []string{"configmaps"},
			ResourceNames: []string{"transform-acme-state"}, Verbs: []string{"get", "update", "patch"},
		}))
		binding := &rbacv1.RoleBinding{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "transform-acme", Namespace: workflowNS}, binding)).To(Succeed())
		Expect(binding.Subjects).To(ConsistOf(rbacv1.Subject{Kind: "ServiceAccount", Name: "transform-acme", Namespace: workflowNS}))

		By("copying Trino's CA")
		ca := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "transform-acme-trino-ca", Namespace: workflowNS}, ca)).To(Succeed())
		Expect(ca.Data["ca.crt"]).To(ContainSubstring("BEGIN CERTIFICATE"))

		By("rendering the onChange lane")
		live, err := getCronWorkflow("acme-live")
		Expect(err).NotTo(HaveOccurred())
		Expect(live.GetLabels()).To(HaveKeyWithValue(labelLane, "live"))
		schedules, _, _ := unstructured.NestedStringSlice(live.Object, "spec", "schedules")
		Expect(schedules).To(Equal([]string{"* * * * *"}))
		policy, _, _ := unstructured.NestedString(live.Object, "spec", "concurrencyPolicy")
		Expect(policy).To(Equal("Forbid"))
		sa, _, _ := unstructured.NestedString(live.Object, "spec", "workflowSpec", "serviceAccountName")
		Expect(sa).To(Equal("transform-acme"))

		container := runnerContainerOf(live)
		Expect(container.Image).To(Equal(orchestrationv1alpha1.DefaultTransformationImage))
		Expect(container.Args).To(Equal([]string{"on-change"}))
		env := envMap(container.Env)
		Expect(env).To(HaveKeyWithValue("WATCH_SOURCES", "crm,sales"))
		Expect(env).To(HaveKeyWithValue("DBT_COMMAND", "run"))
		Expect(env).To(HaveKeyWithValue("DBT_SELECT", "tag:live"))
		Expect(env).To(HaveKeyWithValue("DBT_TARGET", "prod"))
		Expect(env).To(HaveKeyWithValue("DBT_VARS", `{"env":"prod"}`))
		Expect(env).To(HaveKeyWithValue("PROJECT_GIT_URL", "https://github.com/example/dbt.git"))
		Expect(env).To(HaveKeyWithValue("PROJECT_PATH", "project"))
		Expect(env).To(HaveKeyWithValue("TRINO_HOST", "trino.trino.svc"))
		Expect(env).To(HaveKeyWithValue("TRINO_PORT", "8443"))
		Expect(env).To(HaveKeyWithValue("TRANSFORM_STATE_CONFIGMAP", "transform-acme-state"))
		var token *corev1.EnvVar
		for i := range container.Env {
			if container.Env[i].Name == "PROJECT_GIT_TOKEN" {
				token = &container.Env[i]
			}
		}
		Expect(token).NotTo(BeNil())
		Expect(token.ValueFrom.SecretKeyRef.Name).To(Equal("dbt-git"))
		Expect(token.ValueFrom.SecretKeyRef.Key).To(Equal("token"))

		volumes, _, _ := unstructured.NestedSlice(live.Object, "spec", "workflowSpec", "volumes")
		audience := ""
		for _, v := range volumes {
			if a, ok, _ := unstructured.NestedSlice(v.(map[string]any), "projected", "sources"); ok {
				audience, _, _ = unstructured.NestedString(a[0].(map[string]any), "serviceAccountToken", "audience")
			}
		}
		Expect(audience).To(Equal("lakekeeper"))

		By("rendering the scheduled lane")
		daily, err := getCronWorkflow("acme-daily")
		Expect(err).NotTo(HaveOccurred())
		schedules, _, _ = unstructured.NestedStringSlice(daily.Object, "spec", "schedules")
		Expect(schedules).To(Equal([]string{"30 6 * * *"}))
		tz, _, _ := unstructured.NestedString(daily.Object, "spec", "timezone")
		Expect(tz).To(Equal("America/New_York"))
		dailyContainer := runnerContainerOf(daily)
		Expect(dailyContainer.Args).To(Equal([]string{"run"}))
		Expect(envMap(dailyContainer.Env)).To(HaveKeyWithValue("DBT_COMMAND", "build"))
		Expect(envMap(dailyContainer.Env)).NotTo(HaveKey("WATCH_SOURCES"))

		By("rendering maintenance")
		maint, err := getCronWorkflow("acme-maintenance")
		Expect(err).NotTo(HaveOccurred())
		maintEnv := envMap(runnerContainerOf(maint).Env)
		Expect(runnerContainerOf(maint).Args).To(Equal([]string{"maintain"}))
		Expect(maintEnv).To(HaveKeyWithValue("MAINTAIN_NAMESPACES", "analytics"))
		Expect(maintEnv).To(HaveKeyWithValue("MAINTAIN_FILE_SIZE_THRESHOLD", "128MB"))
		Expect(maintEnv).To(HaveKeyWithValue("MAINTAIN_SNAPSHOT_RETENTION", "7d"))

		By("reporting status")
		t := &orchestrationv1alpha1.Transformation{}
		Expect(k8sClient.Get(ctx, key, t)).To(Succeed())
		Expect(t.Status.Namespace).To(Equal(workflowNS))
		Expect(t.Status.ServiceAccount).To(Equal("system:serviceaccount:" + workflowNS + ":transform-acme"))
		Expect(meta.IsStatusConditionTrue(t.Status.Conditions, orchestrationv1alpha1.ConditionTransformationReady)).To(BeTrue())
		Expect(t.Status.Lanes).To(HaveLen(3))
		Expect(t.Status.Lanes[0].CronWorkflow).To(Equal("acme-live"))
	})

	It("leaves an unchanged CronWorkflow alone", func() {
		reconcileOnce()
		before, err := getCronWorkflow("acme-live")
		Expect(err).NotTo(HaveOccurred())
		reconcileOnce()
		after, err := getCronWorkflow("acme-live")
		Expect(err).NotTo(HaveOccurred())
		Expect(after.GetResourceVersion()).To(Equal(before.GetResourceVersion()))
	})

	It("suspends lanes and prunes removed ones", func() {
		reconcileOnce()

		t := &orchestrationv1alpha1.Transformation{}
		Expect(k8sClient.Get(ctx, key, t)).To(Succeed())
		t.Spec.Lanes = t.Spec.Lanes[:1]
		t.Spec.Lanes[0].Suspend = true
		t.Spec.Maintenance = nil
		Expect(k8sClient.Update(ctx, t)).To(Succeed())
		reconcileOnce()

		live, err := getCronWorkflow("acme-live")
		Expect(err).NotTo(HaveOccurred())
		suspended, _, _ := unstructured.NestedBool(live.Object, "spec", "suspend")
		Expect(suspended).To(BeTrue())
		_, err = getCronWorkflow("acme-daily")
		Expect(errors.IsNotFound(err)).To(BeTrue())
		_, err = getCronWorkflow("acme-maintenance")
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("reports names too long for Argo", func() {
		t := &orchestrationv1alpha1.Transformation{}
		Expect(k8sClient.Get(ctx, key, t)).To(Succeed())
		Expect(validateNames(t)).To(Succeed())

		long := t.DeepCopy()
		long.Name = "a-transformation-name-that-is-much-too-long-for-argo"
		Expect(validateNames(long)).To(MatchError(ContainSubstring("longer than 52")))

		reserved := t.DeepCopy()
		reserved.Spec.Lanes[0].Name = laneMaintenance
		Expect(validateNames(reserved)).To(MatchError(ContainSubstring("reserved")))
	})

	It("rejects lanes without exactly one trigger", func() {
		t := &orchestrationv1alpha1.Transformation{}
		Expect(k8sClient.Get(ctx, key, t)).To(Succeed())
		both := t.DeepCopy()
		both.Spec.Lanes[0].Schedule = "* * * * *"
		Expect(k8sClient.Update(ctx, both)).To(MatchError(ContainSubstring("exactly one of schedule or onChange")))

		neither := t.DeepCopy()
		neither.Spec.Lanes[1].Schedule = ""
		Expect(k8sClient.Update(ctx, neither)).To(MatchError(ContainSubstring("exactly one of schedule or onChange")))

		deadline := t.DeepCopy()
		deadline.Spec.Lanes[0].ActiveDeadlineSeconds = ptr.To(int64(10))
		Expect(k8sClient.Update(ctx, deadline)).NotTo(Succeed())
	})
})

func runnerContainerOf(cw *unstructured.Unstructured) corev1.Container {
	templates, _, _ := unstructured.NestedSlice(cw.Object, "spec", "workflowSpec", "templates")
	ExpectWithOffset(1, templates).To(HaveLen(1))
	raw, _, _ := unstructured.NestedMap(templates[0].(map[string]any), "container")
	c := corev1.Container{}
	ExpectWithOffset(1, runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &c)).To(Succeed())
	return c
}

func envMap(env []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		if e.ValueFrom == nil {
			out[e.Name] = e.Value
		}
	}
	return out
}
