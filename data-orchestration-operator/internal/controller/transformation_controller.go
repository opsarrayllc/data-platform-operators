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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	orchestrationv1alpha1 "github.com/opsarrayllc/data-orchestration-operator/api/v1alpha1"
)

// A Transformation becomes, in its Orchestration's namespace:
//   - a ServiceAccount the runner signs in to Trino as,
//   - a Role letting that account report task results and keep its state,
//   - a state ConfigMap where onChange lanes record what they last saw,
//   - a copy of Trino's CA,
//   - one Argo CronWorkflow per lane, plus one for maintenance.
//
// The runner image does the work; the operator only passes it settings
// through environment variables (see docs/transformations.md).
const (
	labelTransformation     = "dataplatform.opsarray.io/transformation"
	labelLane               = "dataplatform.opsarray.io/lane"
	componentTransformation = "transformation"
	laneMaintenance         = "maintenance"

	// Argo appends an 11-character suffix to a CronWorkflow's name for each
	// Workflow, and Workflow names feed pod names (63 characters).
	maxCronWorkflowName = 52

	// statusRefresh re-reads CronWorkflow status. The operator does not watch
	// CronWorkflows, because their CRD may not exist when it starts.
	statusRefresh = time.Minute

	runnerContainer = "runner"
	volumeToken     = "trino-token"
	volumeTrinoCA   = "trino-ca"
	volumeWork      = "work"
	volumeTmp       = "tmp"
	tokenDir        = "/var/run/secrets/opsarray"
	tokenFile       = "token"
	trinoCADir      = "/etc/trino-ca"
	workDir         = "/work"
	tokenSeconds    = int64(3600)
	uidRunner       = int64(65532)

	modeRun      = "run"
	modeOnChange = "on-change"
	modeMaintain = "maintain"
)

var cronWorkflowGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "CronWorkflow"}

// TransformationReconciler reconciles a Transformation object.
type TransformationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=transformations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=transformations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=transformations/finalizers,verbs=update
// +kubebuilder:rbac:groups=dataplatform.opsarray.io,resources=orchestrations,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps;serviceaccounts;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=argoproj.io,resources=cronworkflows;workflowtaskresults,verbs=get;list;watch;create;update;patch;delete

func (r *TransformationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	t := &orchestrationv1alpha1.Transformation{}
	if err := r.Get(ctx, req.NamespacedName, t); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	result, err := r.reconcile(ctx, t)
	if err != nil {
		setTransformationCondition(t, metav1.ConditionFalse, reasonError, err.Error())
	}
	if statusErr := r.patchTransformationStatus(ctx, t); statusErr != nil {
		log.Error(statusErr, "Failed to update Transformation status")
		return ctrl.Result{}, statusErr
	}
	return result, err
}

func (r *TransformationReconciler) reconcile(ctx context.Context, t *orchestrationv1alpha1.Transformation) (ctrl.Result, error) {
	o, err := r.orchestrationFor(ctx, t)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !conditionTrue(o, orchestrationv1alpha1.ConditionReady) {
		setTransformationCondition(t, metav1.ConditionFalse, reasonReconciling,
			fmt.Sprintf("Waiting for Orchestration %s to become ready", o.Name))
		return ctrl.Result{RequeueAfter: requeueWhileProgressing}, nil
	}

	ns := o.Spec.NamespaceOrDefault()
	sa := runnerName(t)
	t.Status.Namespace = ns
	t.Status.ServiceAccount = "system:serviceaccount:" + ns + ":" + sa

	if err := validateNames(t); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyRunnerIdentity(ctx, t, ns); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.applyTrinoCA(ctx, t, ns); err != nil {
		return ctrl.Result{}, err
	}

	desired := map[string]*unstructured.Unstructured{}
	for i := range t.Spec.Lanes {
		lane := &t.Spec.Lanes[i]
		cw, err := laneCronWorkflow(t, ns, lane)
		if err != nil {
			return ctrl.Result{}, err
		}
		desired[cw.GetName()] = cw
	}
	if m := t.Spec.Maintenance; m != nil {
		cw := maintenanceCronWorkflow(t, ns, m)
		desired[cw.GetName()] = cw
	}
	for _, cw := range desired {
		if err := r.applyCronWorkflow(ctx, t, cw); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.pruneCronWorkflows(ctx, t, ns, desired); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.readLaneStatus(ctx, t, ns); err != nil {
		return ctrl.Result{}, err
	}

	setTransformationCondition(t, metav1.ConditionTrue, reasonReady,
		fmt.Sprintf("%d CronWorkflow(s) in namespace %s", len(desired), ns))
	return ctrl.Result{RequeueAfter: statusRefresh}, nil
}

// orchestrationFor returns the referenced Orchestration, or the only one.
func (r *TransformationReconciler) orchestrationFor(ctx context.Context, t *orchestrationv1alpha1.Transformation) (*orchestrationv1alpha1.Orchestration, error) {
	if name := t.Spec.OrchestrationRef; name != "" {
		o := &orchestrationv1alpha1.Orchestration{}
		if err := r.Get(ctx, types.NamespacedName{Name: name}, o); err != nil {
			return nil, fmt.Errorf("orchestration %q: %w", name, err)
		}
		return o, nil
	}
	list := &orchestrationv1alpha1.OrchestrationList{}
	if err := r.List(ctx, list); err != nil {
		return nil, err
	}
	switch len(list.Items) {
	case 0:
		return nil, errors.New("no Orchestration exists; create one or set spec.orchestrationRef")
	case 1:
		return &list.Items[0], nil
	default:
		return nil, errors.New("several Orchestrations exist; set spec.orchestrationRef")
	}
}

func runnerName(t *orchestrationv1alpha1.Transformation) string {
	return "transform-" + t.Name
}

func stateConfigMapName(t *orchestrationv1alpha1.Transformation) string {
	return runnerName(t) + "-state"
}

func caConfigMapName(t *orchestrationv1alpha1.Transformation) string {
	return runnerName(t) + "-trino-ca"
}

func cronWorkflowName(t *orchestrationv1alpha1.Transformation, lane string) string {
	return t.Name + "-" + lane
}

func validateNames(t *orchestrationv1alpha1.Transformation) error {
	lanes := make([]string, 0, len(t.Spec.Lanes)+1)
	for _, l := range t.Spec.Lanes {
		if l.Name == laneMaintenance {
			return fmt.Errorf("lane name %q is reserved", laneMaintenance)
		}
		lanes = append(lanes, l.Name)
	}
	if t.Spec.Maintenance != nil {
		lanes = append(lanes, laneMaintenance)
	}
	for _, l := range lanes {
		if name := cronWorkflowName(t, l); len(name) > maxCronWorkflowName {
			return fmt.Errorf("CronWorkflow name %q is longer than %d characters; shorten the Transformation or lane name", name, maxCronWorkflowName)
		}
	}
	return nil
}

func transformationLabels(t *orchestrationv1alpha1.Transformation, lane string) map[string]string {
	labels := map[string]string{
		labelAppName:        runnerName(t),
		labelAppInstance:    t.Name,
		labelAppComponent:   componentTransformation,
		labelAppManagedBy:   managedBy,
		labelTransformation: t.Name,
	}
	if lane != "" {
		labels[labelLane] = lane
	}
	return labels
}

func (r *TransformationReconciler) apply(ctx context.Context, t *orchestrationv1alpha1.Transformation, obj client.Object, mutate func() error) error {
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if err := mutate(); err != nil {
			return err
		}
		return controllerutil.SetControllerReference(t, obj, r.Scheme)
	})
	return err
}

// applyRunnerIdentity creates the runner's ServiceAccount, its state
// ConfigMap, and a Role covering what the Argo executor and the runner do.
func (r *TransformationReconciler) applyRunnerIdentity(ctx context.Context, t *orchestrationv1alpha1.Transformation, ns string) error {
	labels := transformationLabels(t, "")
	name := runnerName(t)

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := r.apply(ctx, t, sa, func() error {
		ensureLabels(sa, labels)
		return nil
	}); err != nil {
		return err
	}

	// The runner owns the data; only labels and ownership are managed here.
	state := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: stateConfigMapName(t), Namespace: ns}}
	if err := r.apply(ctx, t, state, func() error {
		ensureLabels(state, labels)
		return nil
	}); err != nil {
		return err
	}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := r.apply(ctx, t, role, func() error {
		ensureLabels(role, labels)
		role.Rules = runnerRules(stateConfigMapName(t))
		return nil
	}); err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	return r.apply(ctx, t, binding, func() error {
		ensureLabels(binding, labels)
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}
		binding.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}}
		return nil
	})
}

// runnerRules: the Argo executor reports results as WorkflowTaskResults, and
// the runner reads and updates its own state ConfigMap, nothing else.
func runnerRules(stateConfigMap string) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		rule("argoproj.io", []string{"workflowtaskresults"}, []string{"create", "patch"}),
		{
			APIGroups:     []string{""},
			Resources:     []string{"configmaps"},
			ResourceNames: []string{stateConfigMap},
			Verbs:         []string{"get", "update", "patch"},
		},
	}
}

// applyTrinoCA copies the CA that signs Trino's certificate next to the
// workflows, since a pod can only mount ConfigMaps from its own namespace.
func (r *TransformationReconciler) applyTrinoCA(ctx context.Context, t *orchestrationv1alpha1.Transformation, ns string) error {
	ref := t.Spec.Trino.CARef()
	src := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, src); err != nil {
		return fmt.Errorf("trino CA ConfigMap %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	caPEM := src.Data[ref.Key]
	if caPEM == "" {
		return fmt.Errorf("trino CA ConfigMap %s/%s has no key %s", ref.Namespace, ref.Name, ref.Key)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: caConfigMapName(t), Namespace: ns}}
	return r.apply(ctx, t, cm, func() error {
		ensureLabels(cm, transformationLabels(t, ""))
		cm.Data = map[string]string{orchestrationv1alpha1.DefaultTrinoCAKey: caPEM}
		return nil
	})
}

// runnerEnv is the settings every mode shares.
func runnerEnv(t *orchestrationv1alpha1.Transformation, ns, lane, mode string) []corev1.EnvVar {
	spec := &t.Spec
	env := []corev1.EnvVar{
		{Name: "TRANSFORM_NAME", Value: t.Name},
		{Name: "TRANSFORM_LANE", Value: lane},
		{Name: "TRANSFORM_MODE", Value: mode},
		{Name: "TRANSFORM_STATE_CONFIGMAP", Value: stateConfigMapName(t)},
		{Name: "TRANSFORM_NAMESPACE", Value: ns},
		{Name: "TRINO_HOST", Value: spec.Trino.HostOrDefault()},
		{Name: "TRINO_PORT", Value: strconv.Itoa(int(spec.Trino.PortOrDefault()))},
		{Name: "TRINO_CATALOG", Value: spec.Trino.CatalogOrDefault()},
		{Name: "TRINO_CA_FILE", Value: trinoCADir + "/" + orchestrationv1alpha1.DefaultTrinoCAKey},
		{Name: "TRINO_TOKEN_FILE", Value: tokenDir + "/" + tokenFile},
		{Name: "DBT_TARGET", Value: spec.TargetOrDefault()},
		{Name: "PROJECT_GIT_URL", Value: spec.Project.Git.URL},
		{Name: "PROJECT_GIT_REF", Value: spec.Project.Git.RefOrDefault()},
		{Name: "PROJECT_PATH", Value: spec.Project.Git.Path},
		{Name: "HOME", Value: workDir},
	}
	if ref := spec.Project.Git.CredentialsSecretRef; ref != nil {
		env = append(env, corev1.EnvVar{
			Name: "PROJECT_GIT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Key:                  ref.KeyOrDefault(),
			}},
		})
	}
	if len(spec.Vars) > 0 {
		vars, _ := json.Marshal(spec.Vars) // map[string]string always marshals
		env = append(env, corev1.EnvVar{Name: "DBT_VARS", Value: string(vars)})
	}
	return env
}

func laneEnv(lane *orchestrationv1alpha1.LaneSpec) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "DBT_COMMAND", Value: string(lane.CommandOrDefault())},
		{Name: "DBT_SELECT", Value: lane.Select},
	}
	if lane.Exclude != "" {
		env = append(env, corev1.EnvVar{Name: "DBT_EXCLUDE", Value: lane.Exclude})
	}
	if lane.FullRefresh {
		env = append(env, corev1.EnvVar{Name: "DBT_FULL_REFRESH", Value: "true"})
	}
	if lane.OnChange != nil {
		sources := slices.Clone(lane.OnChange.Sources)
		slices.Sort(sources)
		env = append(env, corev1.EnvVar{Name: "WATCH_SOURCES", Value: strings.Join(sources, ",")})
	}
	return env
}

func laneCronWorkflow(t *orchestrationv1alpha1.Transformation, ns string, lane *orchestrationv1alpha1.LaneSpec) (*unstructured.Unstructured, error) {
	schedule := lane.ScheduleOrDefault()
	if schedule == "" {
		return nil, fmt.Errorf("lane %q has no schedule", lane.Name)
	}
	mode := modeRun
	if lane.OnChange != nil {
		mode = modeOnChange
	}
	env := append(runnerEnv(t, ns, lane.Name, mode), laneEnv(lane)...)
	env = append(env, t.Spec.Env...)
	return cronWorkflow(t, ns, lane.Name, cronWorkflowSettings{
		schedule: schedule,
		timezone: lane.Timezone,
		suspend:  t.Spec.Suspend || lane.Suspend,
		deadline: lane.ActiveDeadlineSecondsOrDefault(),
		mode:     mode,
		env:      env,
	}), nil
}

func maintenanceCronWorkflow(t *orchestrationv1alpha1.Transformation, ns string, m *orchestrationv1alpha1.MaintenanceSpec) *unstructured.Unstructured {
	namespaces := slices.Clone(m.Namespaces)
	slices.Sort(namespaces)
	env := append(runnerEnv(t, ns, laneMaintenance, modeMaintain),
		corev1.EnvVar{Name: "MAINTAIN_NAMESPACES", Value: strings.Join(namespaces, ",")},
		corev1.EnvVar{Name: "MAINTAIN_FILE_SIZE_THRESHOLD", Value: m.FileSizeThresholdOrDefault()},
		corev1.EnvVar{Name: "MAINTAIN_SNAPSHOT_RETENTION", Value: m.SnapshotRetentionOrDefault()},
	)
	env = append(env, t.Spec.Env...)
	return cronWorkflow(t, ns, laneMaintenance, cronWorkflowSettings{
		schedule: m.ScheduleOrDefault(),
		suspend:  t.Spec.Suspend || m.Suspend,
		deadline: orchestrationv1alpha1.DefaultMaintenanceDeadlineSec,
		mode:     modeMaintain,
		env:      env,
	})
}

type cronWorkflowSettings struct {
	schedule string
	timezone string
	suspend  bool
	deadline int64
	mode     string
	env      []corev1.EnvVar
}

// cronWorkflow renders an Argo CronWorkflow running the runner once per
// schedule tick. Forbid keeps one run per lane at a time; a tick that finds
// the previous run still going is skipped, which is what a fast poll lane
// wants. Lanes run concurrently; Iceberg's optimistic commits keep tables
// consistent when maintenance and a lane touch the same table.
func cronWorkflow(t *orchestrationv1alpha1.Transformation, ns, lane string, s cronWorkflowSettings) *unstructured.Unstructured {
	labels := transformationLabels(t, lane)
	sa := runnerName(t)
	container := corev1.Container{
		Name:            runnerContainer,
		Image:           t.Spec.ImageOrDefault(),
		Args:            []string{s.mode},
		Env:             s.env,
		Resources:       t.Spec.Resources,
		SecurityContext: restrictedContainerSecurity(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeToken, MountPath: tokenDir, ReadOnly: true},
			{Name: volumeTrinoCA, MountPath: trinoCADir, ReadOnly: true},
			{Name: volumeWork, MountPath: workDir},
			{Name: volumeTmp, MountPath: "/tmp"},
		},
	}
	podSecurity := restrictedPodSecurity()
	podSecurity.RunAsUser = ptr.To(uidRunner)
	podSecurity.RunAsGroup = ptr.To(uidRunner)
	podSecurity.FSGroup = ptr.To(uidRunner)
	volumes := []corev1.Volume{
		{
			Name: volumeToken,
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Audience:          t.Spec.Trino.TokenAudienceOrDefault(),
					ExpirationSeconds: ptr.To(tokenSeconds),
					Path:              tokenFile,
				}}},
			}},
		},
		{
			Name: volumeTrinoCA,
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: caConfigMapName(t)},
			}},
		},
		{Name: volumeWork, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: volumeTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}

	workflowSpec := map[string]any{
		"entrypoint":            "main",
		"serviceAccountName":    sa,
		"activeDeadlineSeconds": s.deadline,
		"securityContext":       toMap(podSecurity),
		"volumes":               toSlice(volumes),
		"podGC":                 map[string]any{"strategy": "OnPodSuccess"},
		"ttlStrategy": map[string]any{
			"secondsAfterSuccess": int64(3600),
			"secondsAfterFailure": int64(7 * 24 * 3600),
		},
		"podMetadata": map[string]any{"labels": toAnyMap(labels)},
		"templates": []any{map[string]any{
			"name":      "main",
			"container": toMap(&container),
		}},
	}

	spec := map[string]any{
		"schedules":                  []any{s.schedule},
		"concurrencyPolicy":          "Forbid",
		"startingDeadlineSeconds":    int64(60),
		"successfulJobsHistoryLimit": int64(3),
		"failedJobsHistoryLimit":     int64(5),
		"suspend":                    s.suspend,
		"workflowMetadata":           map[string]any{"labels": toAnyMap(labels)},
		"workflowSpec":               workflowSpec,
	}
	if s.timezone != "" {
		spec["timezone"] = s.timezone
	}

	cw := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	cw.SetGroupVersionKind(cronWorkflowGVK)
	cw.SetName(cronWorkflowName(t, lane))
	cw.SetNamespace(ns)
	cw.SetLabels(labels)
	return cw
}

// toMap converts a typed Kubernetes object to the plain map unstructured
// objects hold, with int64 numbers as the API server returns them, so
// unchanged CronWorkflows compare equal and are not rewritten.
func toMap(v any) map[string]any {
	out, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
	return out
}

func toSlice[T any](items []T) []any {
	out := make([]any, 0, len(items))
	for i := range items {
		out = append(out, toMap(&items[i]))
	}
	return out
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// applyCronWorkflow replaces the spec, labels and owner of a CronWorkflow,
// keeping its status and anything Argo adds to metadata.
func (r *TransformationReconciler) applyCronWorkflow(ctx context.Context, t *orchestrationv1alpha1.Transformation, desired *unstructured.Unstructured) error {
	cw := &unstructured.Unstructured{}
	cw.SetGroupVersionKind(cronWorkflowGVK)
	cw.SetName(desired.GetName())
	cw.SetNamespace(desired.GetNamespace())
	return r.apply(ctx, t, cw, func() error {
		labels := cw.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		maps.Copy(labels, desired.GetLabels())
		cw.SetLabels(labels)
		cw.Object["spec"] = desired.Object["spec"]
		return nil
	})
}

// pruneCronWorkflows deletes this Transformation's CronWorkflows for lanes
// that no longer exist, including after the Orchestration namespace changed.
func (r *TransformationReconciler) pruneCronWorkflows(ctx context.Context, t *orchestrationv1alpha1.Transformation, ns string, desired map[string]*unstructured.Unstructured) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(cronWorkflowGVK.GroupVersion().WithKind(cronWorkflowGVK.Kind + "List"))
	if err := r.List(ctx, list, client.MatchingLabels{
		labelTransformation: t.Name,
		labelAppManagedBy:   managedBy,
	}); err != nil {
		return err
	}
	for i := range list.Items {
		cw := &list.Items[i]
		if _, keep := desired[cw.GetName()]; keep && cw.GetNamespace() == ns {
			continue
		}
		if !metav1.IsControlledBy(cw, t) {
			continue
		}
		logf.FromContext(ctx).Info("Deleting CronWorkflow for removed lane", "namespace", cw.GetNamespace(), "name", cw.GetName())
		if err := client.IgnoreNotFound(r.Delete(ctx, cw)); err != nil {
			return err
		}
	}
	return nil
}

// readLaneStatus copies each CronWorkflow's status into the Transformation.
func (r *TransformationReconciler) readLaneStatus(ctx context.Context, t *orchestrationv1alpha1.Transformation, ns string) error {
	names := make([]string, 0, len(t.Spec.Lanes)+1)
	for _, l := range t.Spec.Lanes {
		names = append(names, l.Name)
	}
	if t.Spec.Maintenance != nil {
		names = append(names, laneMaintenance)
	}
	statuses := make([]orchestrationv1alpha1.LaneStatus, 0, len(names))
	for _, lane := range names {
		st := orchestrationv1alpha1.LaneStatus{Name: lane, CronWorkflow: cronWorkflowName(t, lane)}
		cw := &unstructured.Unstructured{}
		cw.SetGroupVersionKind(cronWorkflowGVK)
		err := r.Get(ctx, types.NamespacedName{Name: st.CronWorkflow, Namespace: ns}, cw)
		if apierrors.IsNotFound(err) {
			statuses = append(statuses, st)
			continue
		}
		if err != nil {
			return err
		}
		if s, ok, _ := unstructured.NestedString(cw.Object, "status", "lastScheduledTime"); ok && s != "" {
			if ts, err := time.Parse(time.RFC3339, s); err == nil {
				st.LastScheduledTime = &metav1.Time{Time: ts}
			}
		}
		st.Succeeded = nestedNumber(cw.Object, "status", "succeeded")
		st.Failed = nestedNumber(cw.Object, "status", "failed")
		if active, ok, _ := unstructured.NestedSlice(cw.Object, "status", "active"); ok {
			st.Active = int32(len(active))
		}
		statuses = append(statuses, st)
	}
	t.Status.Lanes = statuses
	return nil
}

// nestedNumber reads an integer field that JSON decoding may have made an
// int64 or a float64.
func nestedNumber(obj map[string]any, fields ...string) int64 {
	v, ok, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func setTransformationCondition(t *orchestrationv1alpha1.Transformation, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&t.Status.Conditions, metav1.Condition{
		Type:               orchestrationv1alpha1.ConditionTransformationReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: t.Generation,
	})
}

func (r *TransformationReconciler) patchTransformationStatus(ctx context.Context, t *orchestrationv1alpha1.Transformation) error {
	latest := &orchestrationv1alpha1.Transformation{}
	if err := r.Get(ctx, types.NamespacedName{Name: t.Name}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	latest.Status = t.Status
	return r.Status().Update(ctx, latest)
}

// SetupWithManager sets up the controller with the Manager. CronWorkflows
// are not watched (their CRD arrives with the Orchestration); status is
// refreshed by requeueing instead.
func (r *TransformationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&orchestrationv1alpha1.Transformation{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&orchestrationv1alpha1.Orchestration{}, handler.EnqueueRequestsFromMapFunc(r.transformationsForOrchestration)).
		Named("transformation").
		Complete(r)
}

// transformationsForOrchestration requeues every Transformation when an
// Orchestration changes, since any of them may default to it.
func (r *TransformationReconciler) transformationsForOrchestration(ctx context.Context, _ client.Object) []reconcile.Request {
	list := &orchestrationv1alpha1.TransformationList{}
	if err := r.List(ctx, list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, t := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: t.Name}})
	}
	return reqs
}
