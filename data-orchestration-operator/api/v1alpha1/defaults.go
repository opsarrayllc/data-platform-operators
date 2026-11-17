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

import corev1 "k8s.io/api/core/v1"

// NamespaceOrDefault returns the namespace Argo runs in.
func (s *OrchestrationSpec) NamespaceOrDefault() string {
	return stringOrDefault(s.Namespace, DefaultNamespace)
}

// ControllerImageOrDefault returns the workflow-controller image.
func (s *OrchestrationSpec) ControllerImageOrDefault() string {
	return stringOrDefault(s.ControllerImage, DefaultControllerImage)
}

// ServerImageOrDefault returns the argo-server image.
func (s *OrchestrationSpec) ServerImageOrDefault() string {
	return stringOrDefault(s.ServerImage, DefaultServerImage)
}

// TypeOrDefault returns the Service type.
func (s *ServiceSpec) TypeOrDefault() corev1.ServiceType {
	if s.Type == "" {
		return corev1.ServiceTypeClusterIP
	}
	return s.Type
}

// GroupsClaimOrDefault returns the claim that lists the user's groups.
func (s *OIDCSpec) GroupsClaimOrDefault() string {
	return stringOrDefault(s.GroupsClaim, "groups")
}

// IsEnabled reports whether OIDC login is on. Nil defaults to false.
func (s *AuthSpec) IsEnabled() bool {
	return s != nil && s.Enabled != nil && *s.Enabled
}

// ArgoClientIDKeyOrDefault returns the Secret key for the Argo client id.
func (s *CredentialsSecretRef) ArgoClientIDKeyOrDefault() string {
	if s == nil {
		return DefaultArgoClientIDKey
	}
	return stringOrDefault(s.ArgoClientIDKey, DefaultArgoClientIDKey)
}

// ArgoClientSecretKeyOrDefault returns the Secret key for the Argo client secret.
func (s *CredentialsSecretRef) ArgoClientSecretKeyOrDefault() string {
	if s == nil {
		return DefaultArgoClientSecretKey
	}
	return stringOrDefault(s.ArgoClientSecretKey, DefaultArgoClientSecretKey)
}

// ImageOrDefault returns the runner image.
func (s *TransformationSpec) ImageOrDefault() string {
	return stringOrDefault(s.Image, DefaultTransformationImage)
}

// TargetOrDefault returns the dbt target.
func (s *TransformationSpec) TargetOrDefault() string {
	return stringOrDefault(s.Target, DefaultTransformationDBTTarget)
}

// RefOrDefault returns the Git ref to check out.
func (s *GitSource) RefOrDefault() string {
	return stringOrDefault(s.Ref, DefaultTransformationGitRef)
}

// KeyOrDefault returns the Secret key holding the token.
func (s *SecretKeyRef) KeyOrDefault() string {
	return stringOrDefault(s.Key, DefaultTransformationGitKey)
}

// HostOrDefault returns Trino's host.
func (s *TrinoSpec) HostOrDefault() string {
	return stringOrDefault(s.Host, DefaultTrinoHost)
}

// PortOrDefault returns Trino's HTTPS port.
func (s *TrinoSpec) PortOrDefault() int32 {
	if s.Port == 0 {
		return DefaultTrinoPort
	}
	return s.Port
}

// CatalogOrDefault returns the Iceberg catalog.
func (s *TrinoSpec) CatalogOrDefault() string {
	return stringOrDefault(s.Catalog, DefaultTrinoCatalog)
}

// TokenAudienceOrDefault returns the runner token's audience.
func (s *TrinoSpec) TokenAudienceOrDefault() string {
	return stringOrDefault(s.TokenAudience, DefaultTrinoTokenAudience)
}

// CARef returns caConfigMapRef with defaults filled in.
func (s *TrinoSpec) CARef() ConfigMapKeyRef {
	ref := ConfigMapKeyRef{}
	if s.CAConfigMapRef != nil {
		ref = *s.CAConfigMapRef
	}
	ref.Name = stringOrDefault(ref.Name, DefaultTrinoCAConfigMap)
	ref.Namespace = stringOrDefault(ref.Namespace, DefaultTrinoCANamespace)
	ref.Key = stringOrDefault(ref.Key, DefaultTrinoCAKey)
	return ref
}

// CommandOrDefault returns the lane's dbt command.
func (s *LaneSpec) CommandOrDefault() LaneCommand {
	if s.Command == "" {
		return DefaultLaneCommand
	}
	return s.Command
}

// ScheduleOrDefault returns the cron schedule the lane's CronWorkflow uses:
// its schedule, or its onChange poll schedule.
func (s *LaneSpec) ScheduleOrDefault() string {
	if s.OnChange != nil {
		return stringOrDefault(s.OnChange.PollSchedule, DefaultPollSchedule)
	}
	return s.Schedule
}

// ActiveDeadlineSecondsOrDefault returns the lane's run time limit.
func (s *LaneSpec) ActiveDeadlineSecondsOrDefault() int64 {
	if s.ActiveDeadlineSeconds == nil {
		return DefaultLaneDeadlineSeconds
	}
	return *s.ActiveDeadlineSeconds
}

// ScheduleOrDefault returns the maintenance schedule.
func (s *MaintenanceSpec) ScheduleOrDefault() string {
	return stringOrDefault(s.Schedule, DefaultMaintenanceSchedule)
}

// FileSizeThresholdOrDefault returns optimize's file size threshold.
func (s *MaintenanceSpec) FileSizeThresholdOrDefault() string {
	return stringOrDefault(s.FileSizeThreshold, DefaultMaintenanceFileSize)
}

// SnapshotRetentionOrDefault returns expire_snapshots' retention.
func (s *MaintenanceSpec) SnapshotRetentionOrDefault() string {
	return stringOrDefault(s.SnapshotRetention, DefaultMaintenanceRetention)
}

func stringOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
