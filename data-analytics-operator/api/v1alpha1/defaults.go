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

// NamespaceOrDefault returns the namespace Superset runs in.
func (s *AnalyticsSpec) NamespaceOrDefault() string {
	return stringOrDefault(s.Namespace, DefaultNamespace)
}

// ImageOrDefault returns the Superset image.
func (s *AnalyticsSpec) ImageOrDefault() string {
	return stringOrDefault(s.Image, DefaultImage)
}

// ImageOrDefault returns the Postgres image.
func (s *PostgresSpec) ImageOrDefault() string {
	return stringOrDefault(s.Image, DefaultPostgresImage)
}

// StorageSizeOrDefault returns the Postgres PVC size.
func (s *PostgresSpec) StorageSizeOrDefault() string {
	return stringOrDefault(s.StorageSize, DefaultPostgresStorage)
}

// DatabaseOrDefault returns the metadata database name.
func (s *PostgresSpec) DatabaseOrDefault() string {
	return stringOrDefault(s.Database, "superset")
}

// IsEmbedded reports whether Postgres should be operator-managed. Nil defaults to true.
func (s *PostgresSpec) IsEmbedded() bool {
	return s.Embedded == nil || *s.Embedded
}

// PortOrDefault returns the external Postgres port.
func (s *PostgresSpec) PortOrDefault() int32 {
	if s.Port == 0 {
		return 5432
	}
	return s.Port
}

// SSLModeOrDefault returns the sslmode for an external database.
func (s *PostgresSpec) SSLModeOrDefault() string {
	return stringOrDefault(s.SSLMode, "prefer")
}

// UsernameKeyOrDefault returns the Secret key for the database user.
func (s *PostgresCredentialsSecretRef) UsernameKeyOrDefault() string {
	return stringOrDefault(s.UsernameKey, "username")
}

// PasswordKeyOrDefault returns the Secret key for the database password.
func (s *PostgresCredentialsSecretRef) PasswordKeyOrDefault() string {
	return stringOrDefault(s.PasswordKey, "password")
}

// ImageOrDefault returns the Redis image.
func (s *RedisSpec) ImageOrDefault() string {
	return stringOrDefault(s.Image, DefaultRedisImage)
}

// TypeOrDefault returns the Service type.
func (s *ServiceSpec) TypeOrDefault() corev1.ServiceType {
	if s.Type == "" {
		return corev1.ServiceTypeClusterIP
	}
	return s.Type
}

// HostOrDefault returns the Trino coordinator host.
func (s *TrinoSpec) HostOrDefault() string {
	return stringOrDefault(s.Host, DefaultTrinoHost)
}

// TrinoSchemeOrDefault returns the scheme Superset uses for Trino. With auth
// enabled it defaults to https: Trino refuses authentication over plain HTTP.
func (s *AnalyticsSpec) TrinoSchemeOrDefault() string {
	if s.Trino.HTTPScheme != "" {
		return s.Trino.HTTPScheme
	}
	if s.Auth.IsEnabled() {
		return TrinoSchemeHTTPS
	}
	return DefaultTrinoScheme
}

// TrinoPortOrDefault returns the Trino coordinator port for the scheme.
func (s *AnalyticsSpec) TrinoPortOrDefault() int32 {
	if s.Trino.Port != 0 {
		return s.Trino.Port
	}
	if s.TrinoSchemeOrDefault() == TrinoSchemeHTTPS {
		return DefaultTrinoHTTPSPort
	}
	return DefaultTrinoPort
}

// TrinoCAOrDefault returns the CA ConfigMap to trust for Trino over https,
// with unset fields defaulted, or nil over http.
func (s *AnalyticsSpec) TrinoCAOrDefault() *ConfigMapKeyRef {
	if s.TrinoSchemeOrDefault() != TrinoSchemeHTTPS {
		return nil
	}
	ref := ConfigMapKeyRef{}
	if s.Trino.CAConfigMapRef != nil {
		ref = *s.Trino.CAConfigMapRef
	}
	ref.Name = stringOrDefault(ref.Name, DefaultTrinoCAConfigMap)
	ref.Namespace = stringOrDefault(ref.Namespace, DefaultTrinoNamespace)
	ref.Key = stringOrDefault(ref.Key, DefaultTrinoCAKey)
	return &ref
}

// CatalogOrDefault returns the Trino catalog.
func (s *TrinoSpec) CatalogOrDefault() string {
	return stringOrDefault(s.Catalog, DefaultTrinoCatalog)
}

// IsEnabled reports whether OIDC login is on. Nil defaults to false.
func (s *AuthSpec) IsEnabled() bool {
	return s != nil && s.Enabled != nil && *s.Enabled
}

// GroupsClaimOrDefault returns the claim that lists the user's groups.
func (s *OIDCSpec) GroupsClaimOrDefault() string {
	return stringOrDefault(s.GroupsClaim, "groups")
}

// SupersetClientIDKeyOrDefault returns the Secret key for the Superset client id.
func (s *CredentialsSecretRef) SupersetClientIDKeyOrDefault() string {
	if s == nil {
		return DefaultSupersetClientIDKey
	}
	return stringOrDefault(s.SupersetClientIDKey, DefaultSupersetClientIDKey)
}

// SupersetClientSecretKeyOrDefault returns the Secret key for the Superset client secret.
func (s *CredentialsSecretRef) SupersetClientSecretKeyOrDefault() string {
	if s == nil {
		return DefaultSupersetClientSecretKey
	}
	return stringOrDefault(s.SupersetClientSecretKey, DefaultSupersetClientSecretKey)
}

// TrinoClientIDKeyOrDefault returns the Secret key for the Trino client id.
func (s *CredentialsSecretRef) TrinoClientIDKeyOrDefault() string {
	if s == nil {
		return DefaultTrinoClientIDKey
	}
	return stringOrDefault(s.TrinoClientIDKey, DefaultTrinoClientIDKey)
}

// TrinoClientSecretKeyOrDefault returns the Secret key for the Trino client secret.
func (s *CredentialsSecretRef) TrinoClientSecretKeyOrDefault() string {
	if s == nil {
		return DefaultTrinoClientSecretKey
	}
	return stringOrDefault(s.TrinoClientSecretKey, DefaultTrinoClientSecretKey)
}

func stringOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
