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

func stringOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
