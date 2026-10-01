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
	// ConditionReady is True when the Argo server can serve the UI.
	ConditionReady = "Ready"
	// ConditionControllerReady is True when the workflow controller is ready.
	ConditionControllerReady = "ControllerReady"
	// ConditionServerReady is True when the Argo server is ready.
	ConditionServerReady = "ServerReady"

	DefaultNamespace       = "argo"
	DefaultControllerImage = "quay.io/argoproj/workflow-controller:v4.1.4"
	DefaultServerImage     = "quay.io/argoproj/argocli:v4.1.4"

	DefaultGroupPlatformAdmins = "platform-admins"
	DefaultGroupDataEngineers  = "data-engineers"
	DefaultGroupAnalysts       = "analysts"

	DefaultArgoClientIDKey     = "argoClientID"
	DefaultArgoClientSecretKey = "argoClientSecret"
)

// OrchestrationSpec defines the desired state of Orchestration.
type OrchestrationSpec struct {
	// namespace is where Argo Workflows runs. Defaults to "argo".
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// controllerImage is the workflow-controller image.
	// Defaults to quay.io/argoproj/workflow-controller:v4.1.4.
	// +optional
	ControllerImage string `json:"controllerImage,omitempty"`

	// serverImage is the argo-server image.
	// Defaults to quay.io/argoproj/argocli:v4.1.4.
	// +optional
	ServerImage string `json:"serverImage,omitempty"`

	// publicURL is the Argo UI URL browsers use.
	// Example: https://argo.data-platform.local. Required for the OIDC callback
	// when auth is enabled.
	// +optional
	PublicURL string `json:"publicURL,omitempty"`

	// service exposes the Argo server HTTP port.
	// +optional
	Service ServiceSpec `json:"service"`

	// auth configures how users sign in. When enabled, the UI logs in through
	// OIDC and a ServiceAccount rule maps the token's groups claim to what that
	// user can do.
	// +optional
	Auth AuthSpec `json:"auth"`
}

// ServiceSpec configures the Argo server Service.
type ServiceSpec struct {
	// type is the Service type.
	// +optional
	// +kubebuilder:default=ClusterIP
	Type corev1.ServiceType `json:"type,omitempty"`
}

// AuthSpec configures Argo login.
type AuthSpec struct {
	// enabled turns on OIDC login. Defaults to false.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// oidc is required when enabled is true.
	// +optional
	OIDC OIDCSpec `json:"oidc"`
}

// OIDCSpec describes the identity provider Argo uses.
type OIDCSpec struct {
	// issuer is the in-cluster issuer URL. Argo cannot split browser and
	// backchannel discovery, so the token issuer it trusts is publicIssuer.
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// publicIssuer is the issuer browsers and tokens use. This string is the
	// token's iss claim. Defaults to issuer.
	// Example: https://keycloak.data-platform.local/realms/dataplatform.
	// +optional
	PublicIssuer string `json:"publicIssuer,omitempty"`

	// credentialsSecretRef points at a Secret with the Argo confidential-client
	// credentials. The data lake operator writes these into the Keycloak
	// namespace Secret named oidc.
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

	// argoClientIDKey defaults to argoClientID.
	// +optional
	ArgoClientIDKey string `json:"argoClientIDKey,omitempty"`

	// argoClientSecretKey defaults to argoClientSecret.
	// +optional
	ArgoClientSecretKey string `json:"argoClientSecretKey,omitempty"`
}

// OrchestrationStatus defines the observed state of Orchestration.
type OrchestrationStatus struct {
	// conditions represent the current state of the Orchestration resource.
	//
	// Standard condition types include:
	// - "Ready": Argo is ready to run workflows
	// - "ControllerReady": the workflow controller is ready
	// - "ServerReady": the Argo server is ready
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// argoEndpoint is the in-cluster HTTP URL of the Argo server.
	// +optional
	ArgoEndpoint string `json:"argoEndpoint,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=orchestrations,scope=Cluster,singular=orchestration
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Argo",type=string,JSONPath=".status.argoEndpoint"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Orchestration is the Schema for the orchestrations API.
type Orchestration struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Orchestration
	// +required
	Spec OrchestrationSpec `json:"spec"`

	// status defines the observed state of Orchestration
	// +optional
	Status OrchestrationStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OrchestrationList contains a list of Orchestration.
type OrchestrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Orchestration `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Orchestration{}, &OrchestrationList{})
		return nil
	})
}
