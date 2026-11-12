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
	// ConditionReady is True when Superset, and Teable if enabled, are ready.
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
	// DefaultImage is the Superset image with authlib and the Trino dialect
	// baked in (see images/superset/Dockerfile). CI publishes it, and
	// make kind-up builds and loads the same tag locally.
	DefaultImage = "ghcr.io/opsarrayllc/data-platform-superset:5.0.0"

	DefaultTrinoHost = "trino.trino.svc"
	DefaultTrinoPort = int32(8080)
	// DefaultTrinoHTTPSPort is the data lake operator's HTTPS listener, which
	// Trino has whenever the lake has an identity provider.
	DefaultTrinoHTTPSPort = int32(8443)
	// DefaultTrinoCAConfigMap is the CA ConfigMap the data lake operator
	// publishes next to Trino.
	DefaultTrinoCAConfigMap = "trino-ca"
	DefaultTrinoNamespace   = "trino"
	DefaultTrinoCAKey       = "ca.crt"
	DefaultTrinoCatalog     = "lakekeeper"
	DefaultTrinoScheme      = "http"
	TrinoSchemeHTTPS        = "https"

	// Group names match the groups the data lake operator imports into Keycloak.
	DefaultGroupPlatformAdmins = "platform-admins"
	DefaultGroupDataEngineers  = "data-engineers"
	DefaultGroupAnalysts       = "analysts"

	DefaultSupersetClientIDKey     = "supersetClientID"
	DefaultSupersetClientSecretKey = "supersetClientSecret"
	DefaultTrinoClientIDKey        = "trinoClientID"
	DefaultTrinoClientSecretKey    = "trinoClientSecret"
	DefaultTeableClientIDKey       = "teableClientID"
	DefaultTeableClientSecretKey   = "teableClientSecret"

	// ConditionTeableReady is True when Teable is disabled or its Deployment is ready.
	ConditionTeableReady = "TeableReady"
	// ConditionTeablePostgresReady is True when Teable's database is usable.
	ConditionTeablePostgresReady = "TeablePostgresReady"

	DefaultTeableNamespace = "teable"
	// DefaultTeableImage is a pinned Teable community release. Teable runs its
	// own database migrations on start.
	DefaultTeableImage         = "ghcr.io/teableio/teable:release.2026-11-06T11-23-32Z.3804"
	DefaultTeableDatabase      = "teable"
	DefaultTeablePublicBucket  = "teable-public"
	DefaultTeablePrivateBucket = "teable-private"
	DefaultTeableS3Region      = "us-east-1"
	// DefaultMcImage matches the data lake operator's MinIO client image;
	// upstream minio/mc is no longer anonymously pullable.
	DefaultMcImage = "docker.io/pgsty/mc:RELEASE.2026-09-16T00-00-00Z"
)

// AnalyticsSpec defines the desired state of Analytics.
type AnalyticsSpec struct {
	// namespace is where Superset, its Postgres, and its Redis run.
	// Defaults to "superset".
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// image is the Superset container image. Defaults to
	// ghcr.io/opsarrayllc/data-platform-superset:5.0.0, which includes authlib
	// and the Trino dialect.
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

	// teable deploys Teable, a collaborative spreadsheet app, next to
	// Superset. It reuses auth.oidc for sign-in. Teable data is reachable only
	// through Teable's own sharing, never through Trino or Superset.
	// +optional
	Teable TeableSpec `json:"teable"`
}

// TeableSpec configures the optional Teable deployment.
type TeableSpec struct {
	// enabled deploys Teable. Defaults to false.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// namespace is where Teable, its Postgres, and its Redis run.
	// Defaults to "teable".
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// image is the Teable container image.
	// +optional
	Image string `json:"image,omitempty"`

	// publicURL is the Teable URL browsers use (for example
	// https://teable.example.com). Required when enabled.
	// +optional
	PublicURL string `json:"publicURL,omitempty"`

	// postgres is Teable's database. Defaults to an embedded instance with a
	// database named "teable".
	// +optional
	Postgres PostgresSpec `json:"postgres"`

	// redis is Teable's cache and queue.
	// +optional
	Redis RedisSpec `json:"redis"`

	// storage is the S3-compatible object store for attachments.
	// +optional
	Storage TeableStorageSpec `json:"storage"`

	// resources are compute resource requirements for the Teable container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// extraEnv is appended to the Teable container.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`

	// service exposes the Teable HTTP port.
	// +optional
	Service ServiceSpec `json:"service"`
}

// TeableStorageSpec points Teable at an S3-compatible object store: AWS S3 in
// production, MinIO for development.
type TeableStorageSpec struct {
	// endpoint is the S3 API URL browsers use for presigned uploads and
	// downloads, for example https://s3.us-east-1.amazonaws.com or
	// https://minio-api.data-platform.local. Required when Teable is enabled.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// internalEndpoint is the S3 API URL Teable itself uses, for example
	// http://minio.minio.svc:9000. Defaults to endpoint.
	// +optional
	InternalEndpoint string `json:"internalEndpoint,omitempty"`

	// region of the buckets. Defaults to us-east-1.
	// +optional
	Region string `json:"region,omitempty"`

	// forcePathStyle uses https://endpoint/bucket/key addressing, which MinIO
	// needs. Defaults to false (virtual-hosted, as AWS S3 prefers).
	// +optional
	ForcePathStyle *bool `json:"forcePathStyle,omitempty"`

	// publicBucket holds avatars and other public assets and must allow
	// anonymous reads. Defaults to teable-public.
	// +optional
	PublicBucket string `json:"publicBucket,omitempty"`

	// privateBucket holds attachments, served through presigned URLs.
	// Defaults to teable-private.
	// +optional
	PrivateBucket string `json:"privateBucket,omitempty"`

	// publicURL is the browser URL of the public bucket, including the bucket.
	// Defaults to endpoint/publicBucket with path style, or
	// scheme://publicBucket.endpointHost otherwise.
	// +optional
	PublicURL string `json:"publicURL,omitempty"`

	// credentialsSecretRef locates the S3 access key pair. Required when
	// Teable is enabled.
	// +optional
	CredentialsSecretRef *S3CredentialsSecretRef `json:"credentialsSecretRef,omitempty"`

	// createBuckets creates both buckets if they are missing and allows
	// anonymous downloads from the public bucket, using the MinIO client.
	// Intended for development MinIO. Defaults to false; on AWS, create the
	// buckets with Terraform.
	// +optional
	CreateBuckets *bool `json:"createBuckets,omitempty"`
}

// S3CredentialsSecretRef locates a Secret with an S3 access key pair.
type S3CredentialsSecretRef struct {
	// name of the Secret.
	Name string `json:"name"`

	// namespace of the Secret. Defaults to the Teable namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// accessKeyIDKey is the Secret key for the access key id.
	// +optional
	// +kubebuilder:default=AWS_ACCESS_KEY_ID
	AccessKeyIDKey string `json:"accessKeyIDKey,omitempty"`

	// secretAccessKeyKey is the Secret key for the secret access key.
	// +optional
	// +kubebuilder:default=AWS_SECRET_ACCESS_KEY
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`
}

// PostgresSpec configures an application database (Superset's metadata or
// Teable's data): either an operator-managed instance or a pointer at an
// existing server.
type PostgresSpec struct {
	// embedded deploys a single-replica Postgres in the application's namespace.
	// Defaults to true. Set false and fill host/credentials for an external
	// database. When false, the operator creates the database on that server if
	// it does not exist, so the credentials need CREATEDB or the database must
	// already exist.
	// +optional
	Embedded *bool `json:"embedded,omitempty"`

	// image is the Postgres container image. It also runs the ensure-database
	// init container for an external database.
	// +optional
	Image string `json:"image,omitempty"`

	// storageSize is the PVC size for embedded Postgres.
	// +optional
	StorageSize string `json:"storageSize,omitempty"`

	// database name. Defaults to "superset" for Superset and "teable" for Teable.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	Database string `json:"database,omitempty"`

	// host is required when embedded is false.
	// +optional
	Host string `json:"host,omitempty"`

	// port of the external database. Defaults to 5432.
	// +optional
	Port int32 `json:"port,omitempty"`

	// sslMode is passed to the Postgres driver (disable, prefer, require, ...).
	// Defaults to prefer for an external database.
	// +optional
	SSLMode string `json:"sslMode,omitempty"`

	// credentialsSecretRef locates username/password for an external database.
	// +optional
	CredentialsSecretRef *PostgresCredentialsSecretRef `json:"credentialsSecretRef,omitempty"`
}

// PostgresCredentialsSecretRef locates a Secret with username and password.
type PostgresCredentialsSecretRef struct {
	// name of the Secret.
	Name string `json:"name"`

	// namespace of the Secret. Defaults to the application's namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// usernameKey is the Secret key for the database user.
	// +optional
	// +kubebuilder:default=username
	UsernameKey string `json:"usernameKey,omitempty"`

	// passwordKey is the Secret key for the database password.
	// +optional
	// +kubebuilder:default=password
	PasswordKey string `json:"passwordKey,omitempty"`
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

	// port is the coordinator port. Defaults to 8443 for https and 8080 for http.
	// +optional
	Port int32 `json:"port,omitempty"`

	// catalog is the Trino catalog Superset queries. Defaults to lakekeeper.
	// +optional
	Catalog string `json:"catalog,omitempty"`

	// httpScheme is http or https. Defaults to https when auth is enabled,
	// because Trino only authenticates users over HTTPS, and http otherwise.
	// +optional
	// +kubebuilder:validation:Enum=http;https
	HTTPScheme string `json:"httpScheme,omitempty"`
	// caConfigMapRef locates the CA certificate that signs Trino's HTTPS
	// certificate. With https the operator copies it into the Superset
	// namespace and Superset verifies Trino against it. Defaults to the data
	// lake operator's trino-ca ConfigMap (key ca.crt) in the trino namespace.
	// +optional
	CAConfigMapRef *ConfigMapKeyRef `json:"caConfigMapRef,omitempty"`
}

// ConfigMapKeyRef selects a key of a ConfigMap in any namespace.
type ConfigMapKeyRef struct {
	// name of the ConfigMap.
	// +optional
	Name string `json:"name,omitempty"`
	// namespace of the ConfigMap. Defaults to trino.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// key holding the PEM certificate. Defaults to ca.crt.
	// +optional
	Key string `json:"key,omitempty"`
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
//
// For a Keycloak issuer (a path with /realms/), endpoints default to
// Keycloak's paths, so the backchannel can use the in-cluster issuer while
// browsers use publicIssuer. For any other issuer, such as JumpCloud, the
// operator reads endpoints from the issuer's discovery document. Set an
// endpoint field to override either.
type OIDCSpec struct {
	// issuer is the issuer URL Superset uses for tokens and JWKS, exactly as
	// the provider states it, including any trailing slash.
	// Example (Keycloak): http://keycloak.keycloak.svc:8080/realms/dataplatform.
	// Example (JumpCloud): https://oauth.id.jumpcloud.com/
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// publicIssuer is the issuer browsers use. Defaults to issuer.
	// Example: https://keycloak.data-platform.local/realms/dataplatform.
	// +optional
	PublicIssuer string `json:"publicIssuer,omitempty"`

	// authorizationEndpoint is the browser login URL. Defaults to Keycloak's
	// path on publicIssuer, or the discovered value.
	// +optional
	AuthorizationEndpoint string `json:"authorizationEndpoint,omitempty"`

	// tokenEndpoint is the token URL Superset calls. Defaults to Keycloak's
	// path on issuer, or the discovered value.
	// +optional
	TokenEndpoint string `json:"tokenEndpoint,omitempty"`

	// jwksURI is the signing key URL for ID tokens. Defaults to Keycloak's
	// path on issuer, or the discovered value.
	// +optional
	JWKSURI string `json:"jwksURI,omitempty"`

	// userinfoEndpoint returns the signed-in user's claims. Defaults to
	// Keycloak's path on issuer, or the discovered value.
	// +optional
	UserinfoEndpoint string `json:"userinfoEndpoint,omitempty"`

	// groupsClaim is the ID token or userinfo claim that lists the user's
	// groups. Group names map to Superset roles: platform-admins to Admin,
	// data-engineers to Alpha, analysts to Gamma. In JumpCloud, set the
	// application's group attribute name to this value. Defaults to "groups".
	// +optional
	GroupsClaim string `json:"groupsClaim,omitempty"`

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

	// teableClientIDKey defaults to teableClientID. Read only when Teable is enabled.
	// +optional
	TeableClientIDKey string `json:"teableClientIDKey,omitempty"`

	// teableClientSecretKey defaults to teableClientSecret. Read only when Teable is enabled.
	// +optional
	TeableClientSecretKey string `json:"teableClientSecretKey,omitempty"`
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
	// - "TeablePostgresReady": Teable's database is usable (Teable enabled only)
	// - "TeableReady": the Teable Deployment is ready (Teable enabled only)
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// supersetEndpoint is the in-cluster HTTP URL of Superset.
	// +optional
	SupersetEndpoint string `json:"supersetEndpoint,omitempty"`

	// teableEndpoint is the in-cluster HTTP URL of Teable, when enabled.
	// +optional
	TeableEndpoint string `json:"teableEndpoint,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=analytics,scope=Cluster,singular=analytics
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Superset",type=string,JSONPath=".status.supersetEndpoint"
// +kubebuilder:printcolumn:name="Teable",type=string,JSONPath=".status.teableEndpoint"
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
