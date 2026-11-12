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
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	analyticsv1alpha1 "github.com/opsarrayllc/data-analytics-operator/api/v1alpha1"
)

const (
	teableOIDCCallbackPath = "/api/auth/oidc/callback"
	teableOIDCProvider     = "oidc"
	volumeTmp              = "tmp"
)

// teableAESSlots are Teable's aes-128-cbc key and IV variables, which must be
// exactly 16 characters. SECRET_KEY covers JWT and session signing.
var teableAESSlots = []string{
	"BACKEND_ACCESS_TOKEN_ENCRYPTION_KEY",
	"BACKEND_ACCESS_TOKEN_ENCRYPTION_IV",
	"BACKEND_MAIL_ENCRYPTION_KEY",
	"BACKEND_MAIL_ENCRYPTION_IV",
	"BACKEND_DATA_DB_URL_ENCRYPTION_KEY",
	"BACKEND_DATA_DB_URL_ENCRYPTION_IV",
	"BACKEND_STORAGE_ENCRYPTION_KEY",
	"BACKEND_STORAGE_ENCRYPTION_IV",
}

// ensureBucketsScript creates both buckets and lets browsers read the public
// one anonymously. It is idempotent.
const ensureBucketsScript = `set -eu
mc alias set target "$S3_ENDPOINT" "$S3_ACCESS_KEY" "$S3_SECRET_KEY" >/dev/null
mc mb --ignore-existing "target/$PUBLIC_BUCKET" "target/$PRIVATE_BUCKET"
mc anonymous set download "target/$PUBLIC_BUCKET"`

func teablePostgres(a *analyticsv1alpha1.Analytics) postgresInstance {
	return postgresInstance{
		component:      componentTeablePostgres,
		name:           nameTeablePostgres,
		user:           nameTeable,
		secret:         secretTeablePostgres,
		externalSecret: secretTeablePostgresExternal,
		defaultDB:      analyticsv1alpha1.DefaultTeableDatabase,
		spec:           a.Spec.Teable.Postgres,
	}
}

// reconcileTeable deploys Teable when spec.teable is enabled. Disabling it
// stops reconciliation but keeps the namespace and data; delete the namespace
// to remove Teable.
func (r *AnalyticsReconciler) reconcileTeable(ctx context.Context, a *analyticsv1alpha1.Analytics, oidc oidcConfig) error {
	spec := &a.Spec.Teable
	if !spec.IsEnabled() {
		a.Status.TeableEndpoint = ""
		meta.RemoveStatusCondition(&a.Status.Conditions, analyticsv1alpha1.ConditionTeableReady)
		meta.RemoveStatusCondition(&a.Status.Conditions, analyticsv1alpha1.ConditionTeablePostgresReady)
		return nil
	}
	fail := func(err error) error {
		setCondition(a, analyticsv1alpha1.ConditionTeableReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := validateTeable(spec); err != nil {
		return fail(err)
	}

	ns := spec.NamespaceOrDefault()
	a.Status.TeableEndpoint = clusterServiceURL(nameTeable, ns, teablePort)
	if err := r.ensureNamespace(ctx, a, ns, componentTeable); err != nil {
		return fail(err)
	}
	keys, err := teableGeneratedKeys()
	if err != nil {
		return fail(err)
	}
	if err := r.ensureGeneratedSecret(ctx, a, secretTeable, ns, componentTeable, keys); err != nil {
		return fail(err)
	}

	pg := teablePostgres(a)
	conn, err := r.reconcilePostgres(ctx, a, ns, pg)
	if err != nil {
		setCondition(a, analyticsv1alpha1.ConditionTeablePostgresReady, metav1.ConditionFalse, reasonError, err.Error())
		return fail(err)
	}
	dbHash, err := r.applyTeableDatabaseSecret(ctx, a, ns, conn)
	if err != nil {
		return fail(err)
	}
	storageHash, err := r.applyTeableStorageSecret(ctx, a, ns)
	if err != nil {
		return fail(err)
	}
	oidcHash := ""
	if oidc.enabled {
		if oidcHash, err = r.applyTeableOIDCSecret(ctx, a, ns, oidc); err != nil {
			return fail(err)
		}
	}
	if err := r.applyRedis(ctx, a, ns, nameTeableRedis, componentTeableRedis, spec.Redis); err != nil {
		return fail(err)
	}
	if err := r.applyTeableService(ctx, a, ns); err != nil {
		return fail(err)
	}
	env := teableEnv(a, oidc)
	envHash, err := json.Marshal(env)
	if err != nil {
		return fail(err)
	}
	cfgHash := hashData(string(envHash), dbHash, storageHash, oidcHash)
	if err := r.applyTeableDeployment(ctx, a, ns, conn, env, oidc.enabled, cfgHash); err != nil {
		return fail(err)
	}

	pgReady, err := r.postgresReady(ctx, ns, pg, conn)
	if err != nil {
		setCondition(a, analyticsv1alpha1.ConditionTeablePostgresReady, metav1.ConditionFalse, reasonError, err.Error())
		return fail(err)
	}
	if !pgReady {
		setCondition(a, analyticsv1alpha1.ConditionTeablePostgresReady, metav1.ConditionFalse, reasonNotReady, "Teable Postgres is not ready")
		setCondition(a, analyticsv1alpha1.ConditionTeableReady, metav1.ConditionFalse, reasonNotReady, "Teable Postgres is not ready")
		return nil
	}
	if conn.External {
		setCondition(a, analyticsv1alpha1.ConditionTeablePostgresReady, metav1.ConditionTrue, reasonReady, "Using external Postgres")
	} else {
		setCondition(a, analyticsv1alpha1.ConditionTeablePostgresReady, metav1.ConditionTrue, reasonReady, "Teable Postgres is ready")
	}

	redisReady, err := r.deploymentReady(ctx, ns, nameTeableRedis)
	if err != nil {
		return fail(err)
	}
	if !redisReady {
		setCondition(a, analyticsv1alpha1.ConditionTeableReady, metav1.ConditionFalse, reasonNotReady, "Teable Redis is not ready")
		return nil
	}
	ready, err := r.deploymentReady(ctx, ns, nameTeable)
	if err != nil {
		return fail(err)
	}
	if !ready {
		setCondition(a, analyticsv1alpha1.ConditionTeableReady, metav1.ConditionFalse, reasonNotReady, "Teable Deployment is not ready")
		return nil
	}
	setCondition(a, analyticsv1alpha1.ConditionTeableReady, metav1.ConditionTrue, reasonReady, "Teable is ready")
	return nil
}

func validateTeable(spec *analyticsv1alpha1.TeableSpec) error {
	if spec.PublicURL == "" {
		return fmt.Errorf("spec.teable.publicURL is required when Teable is enabled")
	}
	if spec.Storage.Endpoint == "" {
		return fmt.Errorf("spec.teable.storage.endpoint is required when Teable is enabled")
	}
	if spec.Storage.CredentialsSecretRef == nil || spec.Storage.CredentialsSecretRef.Name == "" {
		return fmt.Errorf("spec.teable.storage.credentialsSecretRef is required when Teable is enabled")
	}
	return nil
}

// teableGeneratedKeys returns fresh values for every key Teable needs pinned,
// keyed by the env var that reads it. They are written once and never rotated:
// changing them invalidates sessions and stored ciphertext.
func teableGeneratedKeys() (map[string][]byte, error) {
	secretKey, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	keys := map[string][]byte{"SECRET_KEY": []byte(secretKey)}
	for _, slot := range teableAESSlots {
		v, err := randomHex(8)
		if err != nil {
			return nil, err
		}
		keys[slot] = []byte(v)
	}
	return keys, nil
}

// applyTeableDatabaseSecret assembles PRISMA_DATABASE_URL with escaped
// credentials, so passwords with URL metacharacters still work. It returns a
// hash that changes when the URL does.
func (r *AnalyticsReconciler) applyTeableDatabaseSecret(
	ctx context.Context,
	a *analyticsv1alpha1.Analytics,
	ns string,
	conn postgresConn,
) (string, error) {
	user, err := r.getSecretData(ctx, conn.Secret, ns, keyPostgresUsername)
	if err != nil {
		return "", err
	}
	pass, err := r.getSecretData(ctx, conn.Secret, ns, keyPostgresPassword)
	if err != nil {
		return "", err
	}
	dbURL := teableDatabaseURL(conn, user, pass)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretTeableDatabase, Namespace: ns}}
	if err := r.apply(ctx, a, secret, func() error {
		ensureLabels(secret, labelsFor(a, componentTeable))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"PRISMA_DATABASE_URL": []byte(dbURL)}
		return nil
	}); err != nil {
		return "", err
	}
	return hashData(dbURL), nil
}

func teableDatabaseURL(conn postgresConn, user, pass string) string {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(conn.Host, strconv.Itoa(int(conn.Port))),
		Path:     "/" + conn.Database,
		RawQuery: url.Values{"sslmode": []string{conn.SSLMode}}.Encode(),
	}
	return u.String()
}

// applyTeableStorageSecret copies the S3 key pair into the Teable namespace
// under the env var names Teable reads.
func (r *AnalyticsReconciler) applyTeableStorageSecret(ctx context.Context, a *analyticsv1alpha1.Analytics, ns string) (string, error) {
	ref := a.Spec.Teable.Storage.CredentialsSecretRef
	secretNS := ref.Namespace
	if secretNS == "" {
		secretNS = ns
	}
	accessKey, err := r.getSecretData(ctx, ref.Name, secretNS, ref.AccessKeyIDKeyOrDefault())
	if err != nil {
		return "", err
	}
	secretKey, err := r.getSecretData(ctx, ref.Name, secretNS, ref.SecretAccessKeyKeyOrDefault())
	if err != nil {
		return "", err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretTeableStorage, Namespace: ns}}
	if err := r.apply(ctx, a, secret, func() error {
		ensureLabels(secret, labelsFor(a, componentTeable))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			"BACKEND_STORAGE_S3_ACCESS_KEY": []byte(accessKey),
			"BACKEND_STORAGE_S3_SECRET_KEY": []byte(secretKey),
		}
		return nil
	}); err != nil {
		return "", err
	}
	return hashData(accessKey, secretKey), nil
}

func (r *AnalyticsReconciler) applyTeableOIDCSecret(
	ctx context.Context,
	a *analyticsv1alpha1.Analytics,
	ns string,
	oidc oidcConfig,
) (string, error) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretTeableOIDC, Namespace: ns}}
	if err := r.apply(ctx, a, secret, func() error {
		ensureLabels(secret, labelsFor(a, componentTeable))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			"BACKEND_OIDC_CLIENT_ID":     []byte(oidc.teableClientID),
			"BACKEND_OIDC_CLIENT_SECRET": []byte(oidc.teableSecret),
		}
		return nil
	}); err != nil {
		return "", err
	}
	return hashData(oidc.teableClientID, oidc.teableSecret), nil
}

func (r *AnalyticsReconciler) applyTeableService(ctx context.Context, a *analyticsv1alpha1.Analytics, ns string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: nameTeable, Namespace: ns}}
	labels := labelsFor(a, componentTeable)
	return r.apply(ctx, a, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Selector = labels
		svc.Spec.Type = a.Spec.Teable.Service.TypeOrDefault()
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       portNameHTTP,
			Port:       teablePort,
			TargetPort: intstr.FromInt32(teablePort),
		}}
		return nil
	})
}

// teableEnv is the Teable container's plain env. Secrets arrive via envFrom.
func teableEnv(a *analyticsv1alpha1.Analytics, oidc oidcConfig) []corev1.EnvVar {
	spec := a.Spec.Teable
	storage := spec.Storage
	publicURL := strings.TrimRight(spec.PublicURL, "/")
	pathStyle := strconv.FormatBool(storage.IsForcePathStyle())
	env := []corev1.EnvVar{
		{Name: "PUBLIC_ORIGIN", Value: publicURL},
		{Name: "PORT", Value: strconv.Itoa(int(teablePort))},
		{Name: "BACKEND_CACHE_PROVIDER", Value: "redis"},
		{Name: "BACKEND_CACHE_REDIS_URI", Value: fmt.Sprintf("redis://%s:%d/0", nameTeableRedis, redisPort)},
		{Name: "BACKEND_STORAGE_PROVIDER", Value: "s3"},
		{Name: "BACKEND_STORAGE_S3_REGION", Value: storage.RegionOrDefault()},
		{Name: "BACKEND_STORAGE_S3_ENDPOINT", Value: storage.Endpoint},
		{Name: "BACKEND_STORAGE_S3_INTERNAL_ENDPOINT", Value: storage.InternalEndpointOrDefault()},
		{Name: "BACKEND_STORAGE_S3_FORCE_PATH_STYLE", Value: pathStyle},
		// Teable turns path style off for a separate internal endpoint unless
		// told otherwise, which MinIO rejects.
		{Name: "BACKEND_STORAGE_S3_INTERNAL_FORCE_PATH_STYLE", Value: pathStyle},
		{Name: "BACKEND_STORAGE_PUBLIC_BUCKET", Value: storage.PublicBucketOrDefault()},
		{Name: "BACKEND_STORAGE_PRIVATE_BUCKET", Value: storage.PrivateBucketOrDefault()},
		{Name: "BACKEND_STORAGE_PUBLIC_URL", Value: storage.PublicURLOrDefault()},
	}
	if oidc.enabled {
		env = append(env,
			corev1.EnvVar{Name: "SOCIAL_AUTH_PROVIDERS", Value: teableOIDCProvider},
			// The identity provider is the only way in, as with Superset.
			corev1.EnvVar{Name: "PASSWORD_LOGIN_DISABLED", Value: "true"},
			// passport-openidconnect checks the ID token's iss strictly.
			// Keycloak stamps its public hostname, so this is publicIssuer
			// even though tokens are fetched over the backchannel.
			corev1.EnvVar{Name: "BACKEND_OIDC_ISSUER", Value: oidc.publicIssuer},
			corev1.EnvVar{Name: "BACKEND_OIDC_AUTHORIZATION_URL", Value: oidc.authURL},
			corev1.EnvVar{Name: "BACKEND_OIDC_TOKEN_URL", Value: oidc.tokenURL},
			corev1.EnvVar{Name: "BACKEND_OIDC_USER_INFO_URL", Value: oidc.userinfoURL},
			corev1.EnvVar{Name: "BACKEND_OIDC_CALLBACK_URL", Value: publicURL + teableOIDCCallbackPath},
			// Teable refuses sign-ins without an email claim.
			corev1.EnvVar{Name: "BACKEND_OIDC_OTHER", Value: `{"scope":["openid","profile","email"]}`},
		)
	}
	return append(env, spec.ExtraEnv...)
}

func (r *AnalyticsReconciler) applyTeableDeployment(
	ctx context.Context,
	a *analyticsv1alpha1.Analytics,
	ns string,
	conn postgresConn,
	env []corev1.EnvVar,
	oidcEnabled bool,
	cfgHash string,
) error {
	spec := a.Spec.Teable
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameTeable, Namespace: ns}}
	labels := labelsFor(a, componentTeable)
	envFrom := []corev1.EnvFromSource{
		secretEnvFrom(secretTeable),
		secretEnvFrom(secretTeableDatabase),
		secretEnvFrom(secretTeableStorage),
	}
	if oidcEnabled {
		envFrom = append(envFrom, secretEnvFrom(secretTeableOIDC))
	}
	return r.apply(ctx, a, deploy, func() error {
		ensureLabels(deploy, labels)
		if deploy.CreationTimestamp.IsZero() {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		deploy.Spec.Replicas = ptr.To(int32(1))
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[annotationConfigHash] = cfgHash
		deploy.Spec.Template.Labels = labels
		pod := &deploy.Spec.Template.Spec
		// Service links would inject TEABLE_PORT=tcp://... and friends.
		pod.EnableServiceLinks = ptr.To(false)
		pod.SecurityContext = restrictedPodSecurity(uidTeable, gidTeable)
		pod.InitContainers = nil
		if conn.External {
			pod.InitContainers = append(pod.InitContainers, ensureDatabaseContainer(spec.Postgres.ImageOrDefault(), conn))
		}
		if spec.Storage.ShouldCreateBuckets() {
			pod.InitContainers = append(pod.InitContainers, ensureBucketsContainer(spec.Storage))
		}
		pod.Containers = []corev1.Container{{
			Name:            nameTeable,
			Image:           spec.ImageOrDefault(),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports:           []corev1.ContainerPort{{Name: portNameHTTP, ContainerPort: teablePort}},
			Env:             env,
			EnvFrom:         envFrom,
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(teablePort),
					},
				},
				InitialDelaySeconds: 20,
				PeriodSeconds:       10,
			},
			Resources:       spec.Resources,
			SecurityContext: restrictedContainerSecurity(uidTeable, gidTeable),
		}}
		pod.Volumes = nil
		if spec.Storage.ShouldCreateBuckets() {
			pod.Volumes = []corev1.Volume{{
				Name:         volumeTmp,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}}
		}
		return nil
	})
}

// ensureBucketsContainer creates the buckets with the MinIO client before
// Teable starts. It reads the key pair from the copied storage Secret.
func ensureBucketsContainer(storage analyticsv1alpha1.TeableStorageSpec) corev1.Container {
	return corev1.Container{
		Name:    "ensure-buckets",
		Image:   analyticsv1alpha1.DefaultMcImage,
		Command: []string{"sh", "-c", ensureBucketsScript},
		Env: []corev1.EnvVar{
			{Name: "S3_ENDPOINT", Value: storage.InternalEndpointOrDefault()},
			{Name: "PUBLIC_BUCKET", Value: storage.PublicBucketOrDefault()},
			{Name: "PRIVATE_BUCKET", Value: storage.PrivateBucketOrDefault()},
			// mc writes its alias config here; the image's home is not writable.
			{Name: "MC_CONFIG_DIR", Value: "/tmp/.mc"},
			secretEnv("S3_ACCESS_KEY", secretTeableStorage, "BACKEND_STORAGE_S3_ACCESS_KEY"),
			secretEnv("S3_SECRET_KEY", secretTeableStorage, "BACKEND_STORAGE_S3_SECRET_KEY"),
		},
		VolumeMounts:    []corev1.VolumeMount{{Name: volumeTmp, MountPath: "/tmp"}},
		SecurityContext: restrictedContainerSecurity(uidTeable, gidTeable),
	}
}

func secretEnvFrom(name string) corev1.EnvFromSource {
	return corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}},
	}
}
