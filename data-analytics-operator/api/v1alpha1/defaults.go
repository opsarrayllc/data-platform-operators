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

// PortOrDefault returns the Trino coordinator port.
func (s *TrinoSpec) PortOrDefault() int32 {
	if s.Port == 0 {
		return DefaultTrinoPort
	}
	return s.Port
}

// CatalogOrDefault returns the Trino catalog.
func (s *TrinoSpec) CatalogOrDefault() string {
	return stringOrDefault(s.Catalog, DefaultTrinoCatalog)
}

// SchemeOrDefault returns the Trino HTTP scheme.
func (s *TrinoSpec) SchemeOrDefault() string {
	return stringOrDefault(s.HTTPScheme, DefaultTrinoScheme)
}

// IsEnabled reports whether OIDC login is on. Nil defaults to false.
func (s *AuthSpec) IsEnabled() bool {
	return s != nil && s.Enabled != nil && *s.Enabled
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
