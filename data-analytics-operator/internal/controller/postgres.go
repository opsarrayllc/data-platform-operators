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
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

// postgresInstance names one application's database objects, so Superset and
// Teable share the embedded/external Postgres logic.
type postgresInstance struct {
	component string
	// name is the embedded StatefulSet and Service, and the in-cluster host.
	name string
	// user is the embedded superuser the application connects as.
	user string
	// secret holds generated credentials for the embedded instance.
	secret string
	// externalSecret holds credentials copied from an external database's Secret.
	externalSecret string
	defaultDB      string
	spec           analyticsv1alpha1.PostgresSpec
}

func supersetPostgres(a *analyticsv1alpha1.Analytics) postgresInstance {
	return postgresInstance{
		component:      componentSupersetPostgres,
		name:           namePostgres,
		user:           namePostgres,
		secret:         secretPostgres,
		externalSecret: secretPostgresExternal,
		defaultDB:      "superset",
		spec:           a.Spec.Postgres,
	}
}

func (p postgresInstance) database() string {
	return p.spec.DatabaseOr(p.defaultDB)
}

// postgresConn is an application's database connection. Credentials are
// read by pods from Secret, never from plain env values or the ConfigMap.
type postgresConn struct {
	Host     string
	Port     int32
	Database string
	SSLMode  string
	// Secret holds keyPostgresUsername and keyPostgresPassword in the application namespace.
	Secret string
	// External is true when the database is not operator-managed.
	External bool
	// CredentialsHash changes when external credentials rotate.
	CredentialsHash string
}

// ensureDatabaseScript waits for the server, then creates the target database
// if it does not exist yet. The database name is restricted by the CRD pattern,
// so it is safe inside the double-quoted identifier.
const ensureDatabaseScript = `until pg_isready -q; do sleep 2; done
if psql -d "$TARGET_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then exit 0; fi
psql -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$TARGET_DB\""`

// reconcilePostgres deploys an embedded database, or copies an external
// database's credentials into the application namespace.
func (r *AnalyticsReconciler) reconcilePostgres(
	ctx context.Context,
	a *analyticsv1alpha1.Analytics,
	ns string,
	p postgresInstance,
) (postgresConn, error) {
	spec := p.spec
	if spec.IsEmbedded() {
		password, err := randomHex(16)
		if err != nil {
			return postgresConn{}, err
		}
		if err := r.ensureGeneratedSecret(ctx, a, p.secret, ns, p.component, map[string][]byte{
			keyPostgresUsername: []byte(p.user),
			keyPostgresPassword: []byte(password),
			keyPostgresDatabase: []byte(p.database()),
		}); err != nil {
			return postgresConn{}, err
		}
		if err := r.applyPostgres(ctx, a, ns, p); err != nil {
			return postgresConn{}, err
		}
		return postgresConn{
			Host:     p.name,
			Port:     postgresPort,
			Database: p.database(),
			SSLMode:  "disable",
			Secret:   p.secret,
		}, nil
	}

	if spec.Host == "" {
		return postgresConn{}, fmt.Errorf("postgres.host is required when embedded is false")
	}
	if spec.CredentialsSecretRef == nil || spec.CredentialsSecretRef.Name == "" {
		return postgresConn{}, fmt.Errorf("postgres.credentialsSecretRef is required when embedded is false")
	}
	ref := spec.CredentialsSecretRef
	secretNS := ref.Namespace
	if secretNS == "" {
		secretNS = ns
	}
	user, err := r.getSecretData(ctx, ref.Name, secretNS, ref.UsernameKeyOrDefault())
	if err != nil {
		return postgresConn{}, err
	}
	pass, err := r.getSecretData(ctx, ref.Name, secretNS, ref.PasswordKeyOrDefault())
	if err != nil {
		return postgresConn{}, err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: p.externalSecret, Namespace: ns}}
	if err := r.apply(ctx, a, secret, func() error {
		ensureLabels(secret, labelsFor(a, p.component))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			keyPostgresUsername: []byte(user),
			keyPostgresPassword: []byte(pass),
		}
		return nil
	}); err != nil {
		return postgresConn{}, err
	}
	return postgresConn{
		Host:            spec.Host,
		Port:            spec.PortOrDefault(),
		Database:        p.database(),
		SSLMode:         spec.SSLModeOrDefault(),
		Secret:          p.externalSecret,
		External:        true,
		CredentialsHash: hashData(user, pass),
	}, nil
}

// supersetDatabaseEnv is what superset_config.py reads to build
// SQLALCHEMY_DATABASE_URI.
func supersetDatabaseEnv(conn postgresConn) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "DB_HOST", Value: conn.Host},
		{Name: "DB_PORT", Value: strconv.Itoa(int(conn.Port))},
		{Name: "DB_NAME", Value: conn.Database},
		{Name: "DB_SSLMODE", Value: conn.SSLMode},
		secretEnv("DB_USER", conn.Secret, keyPostgresUsername),
		secretEnv("DB_PASS", conn.Secret, keyPostgresPassword),
	}
}

// postgresReady reports whether the database can take connections: always for
// an external server, or once the embedded StatefulSet is ready.
func (r *AnalyticsReconciler) postgresReady(ctx context.Context, ns string, p postgresInstance, conn postgresConn) (bool, error) {
	if conn.External {
		return true, nil
	}
	return r.statefulSetReady(ctx, ns, p.name)
}

// ensureDatabaseContainer is the init container that creates an external
// database before the application runs its migrations.
func ensureDatabaseContainer(image string, conn postgresConn) corev1.Container {
	return corev1.Container{
		Name:    "ensure-database",
		Image:   image,
		Command: []string{"sh", "-c", ensureDatabaseScript},
		Env: []corev1.EnvVar{
			{Name: "PGHOST", Value: conn.Host},
			{Name: "PGPORT", Value: strconv.Itoa(int(conn.Port))},
			{Name: "PGSSLMODE", Value: conn.SSLMode},
			{Name: "PGCONNECT_TIMEOUT", Value: "5"},
			{Name: "TARGET_DB", Value: conn.Database},
			secretEnv("PGUSER", conn.Secret, keyPostgresUsername),
			secretEnv("PGPASSWORD", conn.Secret, keyPostgresPassword),
		},
		SecurityContext: restrictedContainerSecurity(uidPostgres, gidPostgres),
	}
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secret},
				Key:                  key,
			},
		},
	}
}
