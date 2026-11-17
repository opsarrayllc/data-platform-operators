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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// ConditionTransformationReady is True when every lane's CronWorkflow is
	// in place in the Orchestration's namespace.
	ConditionTransformationReady = "Ready"

	DefaultTransformationImage     = "ghcr.io/opsarrayllc/data-platform-dbt-runner:0.12.0"
	DefaultTransformationGitRef    = "main"
	DefaultTransformationGitKey    = "token"
	DefaultTransformationDBTTarget = "prod"
	DefaultTrinoHost               = "trino.trino.svc"
	DefaultTrinoPort               = int32(8443)
	DefaultTrinoCatalog            = "lakekeeper"
	DefaultTrinoCAConfigMap        = "trino-ca"
	DefaultTrinoCANamespace        = "trino"
	DefaultTrinoCAKey              = "ca.crt"
	// DefaultTrinoTokenAudience matches the DataLake's
	// spec.auth.serviceAccountTokens.audience default, which Trino requires.
	DefaultTrinoTokenAudience     = "lakekeeper"
	DefaultLaneCommand            = LaneCommandRun
	DefaultPollSchedule           = "* * * * *"
	DefaultMaintenanceSchedule    = "0 */6 * * *"
	DefaultMaintenanceFileSize    = "128MB"
	DefaultMaintenanceRetention   = "7d"
	DefaultLaneDeadlineSeconds    = int64(3600)
	DefaultMaintenanceDeadlineSec = int64(3 * 3600)
)

// LaneCommand is the dbt command a lane runs.
// +kubebuilder:validation:Enum=run;build;test;snapshot;seed
type LaneCommand string

const (
	LaneCommandRun      LaneCommand = "run"
	LaneCommandBuild    LaneCommand = "build"
	LaneCommandTest     LaneCommand = "test"
	LaneCommandSnapshot LaneCommand = "snapshot"
	LaneCommandSeed     LaneCommand = "seed"
)

// TransformationSpec defines a dbt project that transforms lake data on
// Trino, and when each part of it runs.
type TransformationSpec struct {
	// orchestrationRef is the name of the Orchestration whose Argo Workflows
	// runs this project. Workflows run in that Orchestration's namespace.
	// Defaults to the only Orchestration in the cluster.
	// +optional
	OrchestrationRef string `json:"orchestrationRef,omitempty"`

	// suspend pauses every lane and maintenance without deleting them.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// image is the runner image: dbt-core, dbt-trino and the opsarray
	// entrypoint. Defaults to ghcr.io/opsarrayllc/data-platform-dbt-runner.
	// +optional
	Image string `json:"image,omitempty"`

	// project is where the dbt project lives.
	// +required
	Project ProjectSpec `json:"project"`

	// trino is how the runner reaches Trino. It signs in with its pod's
	// ServiceAccount token, so the DataLake must have
	// spec.auth.serviceAccountTokens set, and a grant for the ServiceAccount
	// reported in status.serviceAccount.
	// +optional
	Trino TrinoSpec `json:"trino,omitempty"`

	// target is the dbt target name, passed as --target. Defaults to "prod".
	// +optional
	Target string `json:"target,omitempty"`

	// vars are passed to dbt as --vars.
	// +optional
	Vars map[string]string `json:"vars,omitempty"`

	// env is added to the runner container after the operator's variables.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// resources for the runner container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// lanes are independent schedules, each running a dbt selection. Use a
	// fast onChange lane for near-live models and slower scheduled lanes for
	// heavy or daily ones. Lanes run independently; keep their selections
	// disjoint so two lanes never write the same table.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	// +listType=map
	// +listMapKey=name
	Lanes []LaneSpec `json:"lanes"`

	// maintenance compacts and expires snapshots of the Iceberg tables this
	// project writes. Frequent small writes leave many small files, which
	// slow every read until they are compacted.
	// +optional
	Maintenance *MaintenanceSpec `json:"maintenance,omitempty"`
}

// ProjectSpec locates the dbt project.
type ProjectSpec struct {
	// git is the repository holding the dbt project.
	// +required
	Git GitSource `json:"git"`
}

// GitSource is a Git repository the runner clones on every run.
type GitSource struct {
	// url is the HTTPS clone URL.
	// +required
	// +kubebuilder:validation:Pattern=`^https://`
	URL string `json:"url"`

	// ref is the branch or tag to check out. Defaults to "main".
	// +optional
	Ref string `json:"ref,omitempty"`

	// path is the directory holding dbt_project.yml, relative to the
	// repository root. Defaults to the root.
	// +optional
	Path string `json:"path,omitempty"`

	// credentialsSecretRef names a Secret in the Orchestration's namespace
	// holding an HTTPS access token. Omit it for public repositories.
	// +optional
	CredentialsSecretRef *SecretKeyRef `json:"credentialsSecretRef,omitempty"`
}

// SecretKeyRef selects a key of a Secret in the Orchestration's namespace.
type SecretKeyRef struct {
	// name of the Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// key holding the value. Defaults to "token".
	// +optional
	Key string `json:"key,omitempty"`
}

// TrinoSpec is how the runner reaches Trino.
type TrinoSpec struct {
	// host is Trino's HTTPS host. Defaults to trino.trino.svc.
	// +optional
	Host string `json:"host,omitempty"`

	// port is Trino's HTTPS port. Defaults to 8443.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// catalog is the Iceberg catalog dbt writes to. Defaults to "lakekeeper".
	// +optional
	Catalog string `json:"catalog,omitempty"`

	// caConfigMapRef locates the CA that signs Trino's certificate. The
	// operator copies it next to the workflows. Defaults to the data lake
	// operator's trino-ca ConfigMap (key ca.crt) in the trino namespace.
	// +optional
	CAConfigMapRef *ConfigMapKeyRef `json:"caConfigMapRef,omitempty"`

	// tokenAudience is the audience of the ServiceAccount token the runner
	// presents. It must match the DataLake's serviceAccountTokens.audience.
	// Defaults to "lakekeeper".
	// +optional
	TokenAudience string `json:"tokenAudience,omitempty"`
}

// ConfigMapKeyRef selects a key of a ConfigMap in any namespace.
type ConfigMapKeyRef struct {
	// name of the ConfigMap. Defaults to trino-ca.
	// +optional
	Name string `json:"name,omitempty"`
	// namespace of the ConfigMap. Defaults to trino.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// key holding the PEM certificate. Defaults to ca.crt.
	// +optional
	Key string `json:"key,omitempty"`
}

// LaneSpec is one schedule running one dbt selection.
// +kubebuilder:validation:XValidation:rule="has(self.schedule) != has(self.onChange)",message="set exactly one of schedule or onChange"
type LaneSpec struct {
	// name identifies the lane. The lane's CronWorkflow is named
	// <transformation>-<lane>.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// command is the dbt command. Defaults to "run".
	// +optional
	Command LaneCommand `json:"command,omitempty"`

	// select is passed to dbt as --select, e.g. "tag:live".
	// +required
	// +kubebuilder:validation:MinLength=1
	Select string `json:"select"`

	// exclude is passed to dbt as --exclude.
	// +optional
	Exclude string `json:"exclude,omitempty"`

	// fullRefresh passes --full-refresh, rebuilding incremental models.
	// +optional
	FullRefresh bool `json:"fullRefresh,omitempty"`

	// schedule is a cron expression; the lane runs on it.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule,omitempty"`

	// onChange runs the lane soon after its source tables change.
	// +optional
	OnChange *OnChangeSpec `json:"onChange,omitempty"`

	// timezone for the schedule, e.g. America/New_York. Defaults to UTC.
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// suspend pauses this lane.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// activeDeadlineSeconds stops a run that takes longer. Defaults to 3600.
	// +optional
	// +kubebuilder:validation:Minimum=60
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`
}

// OnChangeSpec polls source tables and runs the lane only when one has a
// new Iceberg snapshot since the last successful run.
type OnChangeSpec struct {
	// sources are Iceberg namespaces ("atlantis") or tables
	// ("atlantis.atlantis_public_orders") in the Trino catalog to watch.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9_]+(\.[a-z0-9_]+)*$`
	Sources []string `json:"sources"`

	// pollSchedule is how often to check for changes, as a cron expression.
	// A check with no change exits without running dbt. Defaults to every
	// minute.
	// +optional
	PollSchedule string `json:"pollSchedule,omitempty"`
}

// MaintenanceSpec is periodic Iceberg table upkeep.
type MaintenanceSpec struct {
	// schedule is a cron expression. Defaults to every six hours.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// namespaces are the Iceberg namespaces whose tables are maintained,
	// normally the ones this project writes.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9_]+(\.[a-z0-9_]+)*$`
	Namespaces []string `json:"namespaces"`

	// fileSizeThreshold: files smaller than this are rewritten by optimize.
	// Defaults to 128MB.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(B|kB|MB|GB)$`
	FileSizeThreshold string `json:"fileSizeThreshold,omitempty"`

	// snapshotRetention is how long expired snapshots are kept for time
	// travel, as a Trino duration. Defaults to 7d.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+(m|h|d)$`
	SnapshotRetention string `json:"snapshotRetention,omitempty"`

	// suspend pauses maintenance.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// TransformationStatus defines the observed state of Transformation.
type TransformationStatus struct {
	// conditions represent the current state of the Transformation.
	//
	// - "Ready": every lane's CronWorkflow is in place
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// namespace is where the workflows run.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// serviceAccount is the runner's identity, as
	// system:serviceaccount:<namespace>:<name>. Grant it Iceberg
	// namespaces through the DataLake's serviceAccountTokens.grants.
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// lanes report each lane's CronWorkflow, including maintenance.
	// +listType=map
	// +listMapKey=name
	// +optional
	Lanes []LaneStatus `json:"lanes,omitempty"`
}

// LaneStatus is copied from the lane's CronWorkflow status.
type LaneStatus struct {
	// name of the lane, or "maintenance".
	Name string `json:"name"`

	// cronWorkflow is the CronWorkflow's name.
	// +optional
	CronWorkflow string `json:"cronWorkflow,omitempty"`

	// lastScheduledTime is when the lane last started a workflow.
	// +optional
	LastScheduledTime *metav1.Time `json:"lastScheduledTime,omitempty"`

	// succeeded counts successful workflows.
	// +optional
	Succeeded int64 `json:"succeeded,omitempty"`

	// failed counts failed workflows.
	// +optional
	Failed int64 `json:"failed,omitempty"`

	// active is how many of the lane's workflows are running.
	// +optional
	Active int32 `json:"active,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=transformations,scope=Cluster,singular=transformation
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=".status.namespace"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Transformation is the Schema for the transformations API. It runs a dbt
// project on Trino through Argo CronWorkflows, one per lane.
type Transformation struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Transformation
	// +required
	Spec TransformationSpec `json:"spec"`

	// status defines the observed state of Transformation
	// +optional
	Status TransformationStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TransformationList contains a list of Transformation.
type TransformationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Transformation `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Transformation{}, &TransformationList{})
		return nil
	})
}
