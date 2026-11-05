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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

type postgresConn struct {
	Host     string
	Port     int32
	Database string
	Username string
	Password string
	SSLMode  string
	// Secret is the Secret in the component namespace that holds the username
	// and password under keyPostgresUsername and keyPostgresPassword. Pods read
	// credentials from it rather than from plain env values.
	Secret string
	// External is true when the database is not operator-managed.
	External bool
}

// credentialsHash changes when the username or password changes, so pod
// templates can roll when external credentials rotate.
func (c postgresConn) credentialsHash() string {
	return hashData(c.Username, c.Password)
}

// ensureDatabaseScript waits for the server, then creates the target database
// if it does not exist yet. The database name is restricted by the CRD pattern,
// so it is safe inside the double-quoted identifier.
const ensureDatabaseScript = `until pg_isready -q; do sleep 2; done
if psql -d "$TARGET_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then exit 0; fi
psql -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$TARGET_DB\""`

// ensureDatabaseContainer is the init container that creates an external
// component database before migrations run.
func ensureDatabaseContainer(image string, conn postgresConn) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "PGHOST", Value: conn.Host},
		{Name: "PGPORT", Value: strconv.Itoa(int(conn.Port))},
		{Name: "PGCONNECT_TIMEOUT", Value: "5"},
		{Name: "TARGET_DB", Value: conn.Database},
		secretEnv("PGUSER", conn.Secret, keyPostgresUsername),
		secretEnv("PGPASSWORD", conn.Secret, keyPostgresPassword),
	}
	if conn.SSLMode != "" {
		env = append(env, corev1.EnvVar{Name: "PGSSLMODE", Value: conn.SSLMode})
	}
	return corev1.Container{
		Name:            "ensure-database",
		Image:           image,
		Command:         []string{"sh", "-c", ensureDatabaseScript},
		Env:             env,
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

func (r *DataLakeReconciler) reconcilePostgres(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
) (postgresConn, error) {
	ns := dp.Spec.Lakekeeper.NamespaceOrDefault()
	spec := dp.Spec.Lakekeeper.Postgres

	if !spec.IsEmbedded() {
		conn, err := r.externalPostgres(ctx, dp, externalPostgresRequest{
			spec:      spec,
			field:     "lakekeeper.postgres",
			condition: dataplatformv1alpha1.ConditionPostgresReady,
			namespace: ns,
			component: componentPostgres,
			database:  spec.DatabaseOrDefault(),
		})
		if err != nil {
			return postgresConn{}, err
		}
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionTrue, reasonReady, "Using external Postgres")
		return conn, nil
	}

	if err := r.ensurePostgresSecret(ctx, dp, ns); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionFalse, reasonError, err.Error())
		return postgresConn{}, err
	}

	if err := r.applyPostgresService(ctx, dp, ns); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionFalse, reasonError, err.Error())
		return postgresConn{}, err
	}

	if err := r.applyPostgresStatefulSet(ctx, dp, ns, spec); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionFalse, reasonError, err.Error())
		return postgresConn{}, err
	}

	ready, err := r.statefulSetReady(ctx, ns, namePostgres)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionFalse, reasonError, err.Error())
		return postgresConn{}, err
	}
	conn := postgresConn{
		Host:     namePostgres,
		Port:     postgresPort,
		Database: spec.DatabaseOrDefault(),
		Username: namePostgres,
		SSLMode:  sslModeDisable,
		Secret:   secretPostgres,
	}
	if !ready {
		setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionFalse, reasonNotReady, "Postgres StatefulSet is not ready")
		return conn, nil
	}
	setCondition(dp, dataplatformv1alpha1.ConditionPostgresReady, metav1.ConditionTrue, reasonReady, "Postgres is ready")
	return conn, nil
}

// externalPostgresRequest describes one component's external database.
type externalPostgresRequest struct {
	spec dataplatformv1alpha1.PostgresSpec
	// field is the spec path used in error messages, such as "lakekeeper.postgres".
	field string
	// condition is set to False when the spec or credentials are unusable.
	condition string
	// namespace is the component namespace. The credentials Secret defaults to
	// it, and the operator-owned copy is written there.
	namespace string
	component string
	database  string
}

// externalPostgres reads the credentials for an external database and copies
// them into an operator-owned Secret in the component namespace, so pods can
// reference them without the password appearing in a pod spec.
func (r *DataLakeReconciler) externalPostgres(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	req externalPostgresRequest,
) (postgresConn, error) {
	fail := func(reason string, err error) (postgresConn, error) {
		setCondition(dp, req.condition, metav1.ConditionFalse, reason, err.Error())
		return postgresConn{}, err
	}
	spec := req.spec
	if spec.Host == "" {
		return fail(reasonMissing, fmt.Errorf("%s.host is required when embedded is false", req.field))
	}
	if spec.CredentialsSecretRef == nil || spec.CredentialsSecretRef.Name == "" {
		return fail(reasonMissing, fmt.Errorf("%s.credentialsSecretRef is required when embedded is false", req.field))
	}
	ref := spec.CredentialsSecretRef
	secretNS := ref.Namespace
	if secretNS == "" {
		secretNS = req.namespace
	}
	user, err := r.getSecretData(ctx, ref.Name, secretNS, ref.UsernameKeyOrDefault())
	if err != nil {
		return fail(reasonError, err)
	}
	pass, err := r.getSecretData(ctx, ref.Name, secretNS, ref.PasswordKeyOrDefault())
	if err != nil {
		return fail(reasonError, err)
	}
	if err := r.ensureExternalPostgresSecret(ctx, dp, req.namespace, req.component, user, pass); err != nil {
		return fail(reasonError, err)
	}
	return postgresConn{
		Host:     spec.Host,
		Port:     spec.PortOrDefault(),
		Database: req.database,
		Username: user,
		Password: pass,
		SSLMode:  spec.SSLMode,
		Secret:   secretPostgresExternal,
		External: true,
	}, nil
}

// ensureExternalPostgresSecret keeps the operator-owned copy of external
// credentials current, so a rotated password reaches the pods.
func (r *DataLakeReconciler) ensureExternalPostgresSecret(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns, component, user, pass string,
) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretPostgresExternal, Namespace: ns}}
	return r.apply(ctx, dp, secret, func() error {
		ensureLabels(secret, labelsFor(dp, component))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			keyPostgresUsername: []byte(user),
			keyPostgresPassword: []byte(pass),
		}
		return nil
	})
}

func (r *DataLakeReconciler) ensurePostgresSecret(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns string,
) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretPostgres, Namespace: ns}, secret)
	if err == nil {
		return nil
	}
	password, err := randomHex(16)
	if err != nil {
		return err
	}
	db := dp.Spec.Lakekeeper.Postgres.DatabaseOrDefault()
	return r.ensureGeneratedSecret(ctx, dp, secretPostgres, ns, componentPostgres, map[string][]byte{
		keyPostgresUsername: []byte(namePostgres),
		keyPostgresPassword: []byte(password),
		keyPostgresDatabase: []byte(db),
	})
}

func (r *DataLakeReconciler) applyPostgresService(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns string,
) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: namePostgres, Namespace: ns}}
	labels := labelsFor(dp, componentPostgres)
	return r.apply(ctx, dp, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       namePostgres,
			Port:       postgresPort,
			TargetPort: intstr.FromInt32(postgresPort),
		}}
		return nil
	})
}

func (r *DataLakeReconciler) applyPostgresStatefulSet(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns string,
	spec dataplatformv1alpha1.PostgresSpec,
) error {
	qty, err := resource.ParseQuantity(spec.StorageSizeOrDefault())
	if err != nil {
		return fmt.Errorf("invalid postgres storageSize: %w", err)
	}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: namePostgres, Namespace: ns}}
	labels := labelsFor(dp, componentPostgres)
	return r.apply(ctx, dp, sts, func() error {
		ensureLabels(sts, labels)
		if sts.CreationTimestamp.IsZero() {
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
			sts.Spec.ServiceName = namePostgres
			sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: volumeData},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: qty},
					},
				},
			}}
		}
		sts.Spec.Replicas = ptr.To(int32(1))
		sts.Spec.Template.Labels = labels
		sts.Spec.Template.Spec.SecurityContext = restrictedPodSecurity(uidPostgres, gidPostgres)
		sts.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:  namePostgres,
			Image: spec.ImageOrDefault(),
			Ports: []corev1.ContainerPort{{Name: namePostgres, ContainerPort: postgresPort}},
			Env: []corev1.EnvVar{
				{Name: envPostgresUser, Value: namePostgres},
				{
					Name: envPostgresPassword,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretPostgres},
							Key:                  keyPostgresPassword,
						},
					},
				},
				{Name: envPostgresDB, Value: spec.DatabaseOrDefault()},
				{Name: keyPGDATA, Value: pgDataPath},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: volumeData, MountPath: pgDataMountPath}},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(postgresPort)},
				},
				PeriodSeconds: 5,
			},
			SecurityContext: restrictedContainerSecurity(uidPostgres, gidPostgres),
		}}
		return nil
	})
}

func (r *DataLakeReconciler) statefulSetReady(ctx context.Context, ns, name string) (bool, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, sts); err != nil {
		return false, err
	}
	return sts.Status.ReadyReplicas >= 1, nil
}
