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
	// ConditionReady is True when Superset can serve dashboards.
	ConditionReady = "Ready"
	// ConditionPostgresReady is True when Superset's metadata database is usable.
	ConditionPostgresReady = "PostgresReady"
	// ConditionRedisReady is True when Superset's Redis is ready.
	ConditionRedisReady = "RedisReady"
	// ConditionSupersetReady is True when the Superset Deployment is ready.
	ConditionSupersetReady = "SupersetReady"

	DefaultNamespace       = "superset"
	DefaultPostgresImage   = "postgres:17"
	DefaultPostgresStorage = "10Gi"
	DefaultRedisImage      = "redis:7.4-alpine"
	// DefaultImage is the operator-built image with authlib and the Trino
	// dialect baked in (see images/superset/Dockerfile).
	DefaultImage = "data-platform-superset:5.0.0"

	DefaultTrinoHost    = "trino.trino.svc"
	DefaultTrinoPort    = int32(8080)
	DefaultTrinoCatalog = "lakekeeper"
	DefaultTrinoScheme  = "http"

	// Group names match the groups the data lake operator imports into Keycloak.
	DefaultGroupPlatformAdmins = "platform-admins"
	DefaultGroupDataEngineers  = "data-engineers"
	DefaultGroupAnalysts       = "analysts"

	DefaultSupersetClientIDKey     = "supersetClientID"
	DefaultSupersetClientSecretKey = "supersetClientSecret"
	DefaultTrinoClientIDKey        = "trinoClientID"
	DefaultTrinoClientSecretKey    = "trinoClientSecret"
)

// AnalyticsSpec defines the desired state of Analytics.
type AnalyticsSpec struct {
	// namespace is where Superset, its Postgres, and its Redis run.
	// Defaults to "superset".
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// image is the Superset container image. Defaults to data-platform-superset:5.0.0,
	// which includes authlib and the Trino dialect.
	// +optional
	Image string `json:"image,omitempty"`

	// publicURL is the Superset URL browsers use (for example https://superset.example.com).
	// Required for the per-user Trino OAuth redirect when auth is enabled.
	// +optional
	PublicURL string `json:"publicURL,omitempty"`

	// postgres is Superset's metadata database.
	// +optional
	Postgres PostgresSpec `json:"postgres"`

	// redis stores sessions and the chart cache.
	// +optional
	Redis RedisSpec `json:"redis"`

	// resources are compute resource requirements for the Superset container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// extraEnv is appended to the Superset container.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`

	// service exposes the Superset HTTP port.
	// +optional
	Service ServiceSpec `json:"service"`

	// trino is the query engine Superset connects to.
	// +optional
	Trino TrinoSpec `json:"trino"`

	// auth configures how users sign in. When disabled, Superset uses its own
	// database login. When enabled, users sign in with OIDC and SQL runs as the
	// logged-in user via per-user Trino OAuth2.
	// +optional
	Auth AuthSpec `json:"auth"`
}

// PostgresSpec configures Superset's metadata database.
type PostgresSpec struct {
	// image is the Postgres container image.
	// +optional
	Image string `json:"image,omitempty"`

	// storageSize is the PVC size.
	// +optional
	StorageSize string `json:"storageSize,omitempty"`

	// database name. Defaults to "superset".
	// +optional
	Database string `json:"database,omitempty"`
}

// RedisSpec configures Redis for Superset sessions and cache.
type RedisSpec struct {
	// image is the Redis container image.
	// +optional
	Image string `json:"image,omitempty"`

	// resources are compute resource requirements for the Redis container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// ServiceSpec configures the Superset Service.
type ServiceSpec struct {
	// type is the Service type.
	// +optional
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`
}

// TrinoSpec points Superset at a Trino coordinator.
type TrinoSpec struct {
	// host is the coordinator Service hostname. Defaults to trino.trino.svc.
	// +optional
	Host string `json:"host,omitempty"`

	// port is the coordinator HTTP port. Defaults to 8080.
	// +optional
	Port int32 `json:"port,omitempty"`

	// catalog is the Trino catalog Superset queries. Defaults to lakekeeper.
	// +optional
	Catalog string `json:"catalog,omitempty"`

	// httpScheme is http or https. Defaults to http.
	// +optional
	HTTPScheme string `json:"httpScheme,omitempty"`
}

// AuthSpec configures Superset login.
type AuthSpec struct {
	// enabled turns on OIDC login. Defaults to false.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// oidc is required when enabled is true.
	// +optional
	OIDC OIDCSpec `json:"oidc"`
}

// OIDCSpec describes the identity provider Superset and Trino share.
type OIDCSpec struct {
	// issuer is the in-cluster issuer URL Superset uses for tokens and JWKS.
	// Example: http://keycloak.keycloak.svc:8080/realms/dataplatform.
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// publicIssuer is the issuer browsers use. Defaults to issuer.
	// Example: https://keycloak.data-platform.local/realms/dataplatform.
	// +optional
	PublicIssuer string `json:"publicIssuer,omitempty"`

	// credentialsSecretRef points at a Secret with the Superset and Trino
	// confidential-client credentials. The data lake operator writes these
	// into the Keycloak namespace Secret named oidc.
	// +optional
	CredentialsSecretRef *CredentialsSecretRef `json:"credentialsSecretRef,omitempty"`
}

// CredentialsSecretRef selects an existing Secret.
type CredentialsSecretRef struct {
	// name of the Secret.
	// +optional
	Name string `json:"name,omitempty"`

	// namespace of the Secret.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// supersetClientIDKey defaults to supersetClientID.
	// +optional
	SupersetClientIDKey string `json:"supersetClientIDKey,omitempty"`

	// supersetClientSecretKey defaults to supersetClientSecret.
	// +optional
	SupersetClientSecretKey string `json:"supersetClientSecretKey,omitempty"`

	// trinoClientIDKey defaults to trinoClientID.
	// +optional
	TrinoClientIDKey string `json:"trinoClientIDKey,omitempty"`

	// trinoClientSecretKey defaults to trinoClientSecret.
	// +optional
	TrinoClientSecretKey string `json:"trinoClientSecretKey,omitempty"`
}

// AnalyticsStatus defines the observed state of Analytics.
type AnalyticsStatus struct {
	// conditions represent the current state of the Analytics resource.
	//
	// Standard condition types include:
	// - "Ready": Superset is ready to serve dashboards
	// - "PostgresReady": the metadata database is usable
	// - "RedisReady": Redis is ready
	// - "SupersetReady": the Superset Deployment is ready
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// supersetEndpoint is the in-cluster HTTP URL of Superset.
	// +optional
	SupersetEndpoint string `json:"supersetEndpoint,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=analytics,scope=Cluster,singular=analytics
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Superset",type=string,JSONPath=".status.supersetEndpoint"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Analytics is the Schema for the analytics API.
type Analytics struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Analytics
	// +required
	Spec AnalyticsSpec `json:"spec"`

	// status defines the observed state of Analytics
	// +optional
	Status AnalyticsStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AnalyticsList contains a list of Analytics.
type AnalyticsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Analytics `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Analytics{}, &AnalyticsList{})
		return nil
	})
}
