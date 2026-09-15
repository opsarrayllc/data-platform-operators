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
	"net/url"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

const (
	supersetConfigKey    = "superset_config.py"
	supersetBootstrapKey = "bootstrap_trino.py"
	supersetDBName       = "superset"
)

func (r *DataPlatformReconciler) reconcileSupersetStack(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform, oidc oidcConfig) (bool, error) {
	if !dp.Spec.Superset.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionTrue, reasonDisabled, "Superset is disabled")
		return false, nil
	}

	ns := dp.Spec.Superset.NamespaceOrDefault()
	if err := r.ensureNamespace(ctx, dp, ns, componentSuperset); err != nil {
		return false, err
	}
	if err := r.reconcileSuperset(ctx, dp, oidc); err != nil {
		return false, err
	}
	return !conditionTrue(dp, dataplatformv1alpha1.ConditionSupersetReady), nil
}

func (r *DataPlatformReconciler) reconcileSuperset(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform, oidc oidcConfig) error {
	ns := dp.Spec.Superset.NamespaceOrDefault()
	dp.Status.SupersetEndpoint = clusterServiceURL(nameSuperset, ns, supersetPort)

	if err := r.ensureSupersetSecrets(ctx, dp, ns, oidc); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applySupersetPostgres(ctx, dp, ns, dp.Spec.Superset.Postgres); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applySupersetRedis(ctx, dp, ns); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}

	cfg, cfgHash, err := r.supersetConfig(ctx, dp, ns, oidc)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	bootstrap := supersetBootstrapTrino(dp)
	if err := r.applySupersetConfigMap(ctx, dp, ns, cfg, bootstrap); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applySupersetService(ctx, dp, ns); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if err := r.applySupersetDeployment(ctx, dp, ns, hashData(cfgHash, bootstrap)); err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}

	pgReady, err := r.statefulSetReady(ctx, ns, namePostgres)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if !pgReady {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonNotReady, "Superset Postgres is not ready")
		return nil
	}
	redisReady, err := r.deploymentReady(ctx, ns, nameSupersetRedis)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if !redisReady {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonNotReady, "Superset Redis is not ready")
		return nil
	}

	ready, err := r.deploymentReady(ctx, ns, nameSuperset)
	if err != nil {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonError, err.Error())
		return err
	}
	if !ready {
		setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionFalse, reasonNotReady, "Superset Deployment is not ready")
		return nil
	}
	setCondition(dp, dataplatformv1alpha1.ConditionSupersetReady, metav1.ConditionTrue, reasonReady, "Superset is ready")
	return nil
}

func (r *DataPlatformReconciler) ensureSupersetSecrets(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns string,
	oidc oidcConfig,
) error {
	password, err := randomHex(16)
	if err != nil {
		return err
	}
	if err := r.ensureGeneratedSecret(ctx, dp, secretPostgres, ns, componentSupersetPostgres, map[string][]byte{
		keyPostgresUsername: []byte(namePostgres),
		keyPostgresPassword: []byte(password),
		keyPostgresDatabase: []byte(supersetDBName),
	}); err != nil {
		return err
	}

	secretKey, err := randomHex(32)
	if err != nil {
		return err
	}
	if err := r.ensureGeneratedSecret(ctx, dp, secretSuperset, ns, componentSuperset, map[string][]byte{
		keySupersetSecretKey: []byte(secretKey),
	}); err != nil {
		return err
	}

	if !oidc.enabled {
		return nil
	}
	return r.ensureSupersetOIDCSecret(ctx, dp, ns, oidc)
}

func (r *DataPlatformReconciler) ensureSupersetOIDCSecret(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns string,
	oidc oidcConfig,
) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretSupersetOIDC, Namespace: ns}}
	labels := labelsFor(dp, componentSuperset)
	return r.apply(ctx, dp, secret, func() error {
		ensureLabels(secret, labels)
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[keyOIDCSupersetClientID] = []byte(oidc.supersetClientID)
		secret.Data[keyOIDCSupersetClientSecret] = []byte(oidc.supersetSecret)
		secret.Data[keyOIDCTrinoClientID] = []byte(oidc.trinoClientID)
		secret.Data[keyOIDCTrinoClientSecret] = []byte(oidc.trinoSecret)
		return nil
	})
}

func (r *DataPlatformReconciler) applySupersetPostgres(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns string,
	spec dataplatformv1alpha1.PostgresSpec,
) error {
	qty, err := resource.ParseQuantity(spec.StorageSizeOrDefault())
	if err != nil {
		return fmt.Errorf("invalid superset postgres storageSize: %w", err)
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: namePostgres, Namespace: ns}}
	labels := labelsFor(dp, componentSupersetPostgres)
	if err := r.apply(ctx, dp, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       namePostgres,
			Port:       postgresPort,
			TargetPort: intstr.FromInt32(postgresPort),
		}}
		return nil
	}); err != nil {
		return err
	}

	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: namePostgres, Namespace: ns}}
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
				{Name: envPostgresDB, Value: supersetPostgresDatabase(spec)},
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

func supersetPostgresDatabase(spec dataplatformv1alpha1.PostgresSpec) string {
	if spec.Database != "" {
		return spec.Database
	}
	return supersetDBName
}

func (r *DataPlatformReconciler) applySupersetRedis(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform, ns string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: nameSupersetRedis, Namespace: ns}}
	labels := labelsFor(dp, componentSupersetRedis)
	if err := r.apply(ctx, dp, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "redis",
			Port:       redisPort,
			TargetPort: intstr.FromInt32(redisPort),
		}}
		return nil
	}); err != nil {
		return err
	}

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameSupersetRedis, Namespace: ns}}
	return r.apply(ctx, dp, deploy, func() error {
		ensureLabels(deploy, labels)
		if deploy.CreationTimestamp.IsZero() {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		deploy.Spec.Replicas = ptr.To(int32(1))
		deploy.Spec.Template.Labels = labels
		deploy.Spec.Template.Spec.SecurityContext = restrictedPodSecurity(uidRedis, gidRedis)
		deploy.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:  nameSupersetRedis,
			Image: dp.Spec.Superset.Redis.ImageOrDefault(),
			Ports: []corev1.ContainerPort{{Name: "redis", ContainerPort: redisPort}},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(redisPort)},
				},
				PeriodSeconds: 5,
			},
			Resources:       dp.Spec.Superset.Redis.Resources,
			SecurityContext: restrictedContainerSecurity(uidRedis, gidRedis),
		}}
		return nil
	})
}

func (r *DataPlatformReconciler) applySupersetConfigMap(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns, config, bootstrap string,
) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapSuperset, Namespace: ns}}
	labels := labelsFor(dp, componentSuperset)
	return r.apply(ctx, dp, cm, func() error {
		ensureLabels(cm, labels)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[supersetConfigKey] = config
		cm.Data[supersetBootstrapKey] = bootstrap
		return nil
	})
}

func (r *DataPlatformReconciler) applySupersetService(ctx context.Context, dp *dataplatformv1alpha1.DataPlatform, ns string) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: nameSuperset, Namespace: ns}}
	labels := labelsFor(dp, componentSuperset)
	return r.apply(ctx, dp, svc, func() error {
		ensureLabels(svc, labels)
		svc.Spec.Type = dp.Spec.Superset.Service.TypeOrDefault()
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       portNameHTTP,
			Port:       supersetPort,
			TargetPort: intstr.FromInt32(supersetPort),
		}}
		return nil
	})
}

func (r *DataPlatformReconciler) applySupersetDeployment(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns, cfgHash string,
) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: nameSuperset, Namespace: ns}}
	labels := labelsFor(dp, componentSuperset)
	return r.apply(ctx, dp, deploy, func() error {
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
		deploy.Spec.Template.Spec.SecurityContext = restrictedPodSecurity(uidSuperset, gidSuperset)
		deploy.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:            nameSuperset,
			Image:           dp.Spec.Superset.ImageOrDefault(),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"/bin/sh", "-c"},
			Args: []string{strings.Join([]string{
				"set -eu",
				"superset db upgrade",
				"# init seeds FAB roles; ignore UniqueViolation on re-runs",
				"superset init || true",
				"python /app/pythonpath/" + supersetBootstrapKey,
				"exec /usr/bin/run-server.sh",
			}, "\n")},
			Ports: []corev1.ContainerPort{{Name: portNameHTTP, ContainerPort: supersetPort}},
			Env: append([]corev1.EnvVar{
				{Name: "SUPERSET_CONFIG_PATH", Value: "/app/pythonpath/" + supersetConfigKey},
				// K8s injects SUPERSET_PORT=tcp://... from the Service name; that
				// breaks run-server.sh / gunicorn. Pin a numeric port.
				{Name: "SUPERSET_PORT", Value: strconv.Itoa(int(supersetPort))},
				{Name: "SERVER_PORT", Value: strconv.Itoa(int(supersetPort))},
				{
					Name: "SUPERSET_SECRET_KEY",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretSuperset},
							Key:                  keySupersetSecretKey,
						},
					},
				},
				{
					Name: "DB_PASS",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretPostgres},
							Key:                  keyPostgresPassword,
						},
					},
				},
				{
					Name: "SUPERSET_OIDC_CLIENT_ID",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretSupersetOIDC},
							Key:                  keyOIDCSupersetClientID,
							Optional:             ptr.To(true),
						},
					},
				},
				{
					Name: "SUPERSET_OIDC_CLIENT_SECRET",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretSupersetOIDC},
							Key:                  keyOIDCSupersetClientSecret,
							Optional:             ptr.To(true),
						},
					},
				},
				{
					Name: "TRINO_OIDC_CLIENT_ID",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretSupersetOIDC},
							Key:                  keyOIDCTrinoClientID,
							Optional:             ptr.To(true),
						},
					},
				},
				{
					Name: "TRINO_OIDC_CLIENT_SECRET",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: secretSupersetOIDC},
							Key:                  keyOIDCTrinoClientSecret,
							Optional:             ptr.To(true),
						},
					},
				},
			}, dp.Spec.Superset.ExtraEnv...),
			VolumeMounts: []corev1.VolumeMount{{
				Name:      volumeConfig,
				MountPath: "/app/pythonpath",
			}},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(supersetPort),
					},
				},
				InitialDelaySeconds: 60,
				PeriodSeconds:       10,
			},
			Resources:       dp.Spec.Superset.Resources,
			SecurityContext: restrictedContainerSecurity(uidSuperset, gidSuperset),
		}}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{{
			Name: volumeConfig,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapSuperset},
				},
			},
		}}
		return nil
	})
}

func (r *DataPlatformReconciler) supersetConfig(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataPlatform,
	ns string,
	oidc oidcConfig,
) (string, string, error) {
	password, err := r.getSecretData(ctx, secretPostgres, ns, keyPostgresPassword)
	if err != nil {
		return "", "", err
	}
	dbURI := fmt.Sprintf(
		"postgresql+psycopg2://%s:%s@%s:%d/%s",
		namePostgres,
		url.QueryEscape(password),
		namePostgres,
		postgresPort,
		supersetDBName,
	)
	redisURL := fmt.Sprintf("redis://%s:%d/0", nameSupersetRedis, redisPort)
	trinoHost := fmt.Sprintf("trino.%s.svc", dp.Spec.Trino.NamespaceOrDefault())
	trinoURI := fmt.Sprintf("trino://%s:%d/%s", trinoHost, trinoPort, dataplatformv1alpha1.DefaultTrinoCatalog)

	publicURL := strings.TrimRight(dp.Spec.Superset.PublicURL, "/")
	// Browser hits the public authorize URL; Superset's server-side discovery,
	// token, and JWKS calls must use the in-cluster issuer. The public hostname
	// only resolves on the host (/etc/hosts), so using it from the pod hangs.
	browserIssuer := oidc.issuer
	if oidc.publicIssuer != "" {
		browserIssuer = oidc.publicIssuer
	}
	authURL := oidcAuthURL(browserIssuer)
	tokenURL := oidc.tokenURL
	jwksURL := oidcJWKSURL(oidc.issuer)
	// Flask-AppBuilder's Keycloak handler GETs "{api_base_url}openid-connect/userinfo".
	// api_base must end at "/protocol/" so the path is .../protocol/openid-connect/userinfo.
	apiBase := strings.TrimRight(oidc.issuer, "/") + "/protocol/"

	var b strings.Builder
	b.WriteString("# Generated by data-platform-operator. Do not edit.\n")
	b.WriteString("import os\n")
	b.WriteString("from flask_appbuilder.security.manager import AUTH_DB, AUTH_OAUTH\n\n")
	b.WriteString(fmt.Sprintf("SQLALCHEMY_DATABASE_URI = %q\n", dbURI))
	b.WriteString("SECRET_KEY = os.environ.get(\"SUPERSET_SECRET_KEY\", \"change-me\")\n")
	b.WriteString(fmt.Sprintf("REDIS_URL = %q\n", redisURL))
	b.WriteString("CACHE_CONFIG = {\n")
	b.WriteString("    \"CACHE_TYPE\": \"RedisCache\",\n")
	b.WriteString("    \"CACHE_DEFAULT_TIMEOUT\": 300,\n")
	b.WriteString("    \"CACHE_KEY_PREFIX\": \"superset_\",\n")
	b.WriteString("    \"CACHE_REDIS_URL\": REDIS_URL,\n")
	b.WriteString("}\n")
	b.WriteString("DATA_CACHE_CONFIG = CACHE_CONFIG\n")
	b.WriteString("FILTER_STATE_CACHE_CONFIG = CACHE_CONFIG\n")
	b.WriteString("EXPLORE_FORM_DATA_CACHE_CONFIG = CACHE_CONFIG\n")
	b.WriteString("RATELIMIT_ENABLED = True\n")
	b.WriteString("RATELIMIT_STORAGE_URI = REDIS_URL\n")
	b.WriteString("ENABLE_PROXY_FIX = True\n")
	b.WriteString("FEATURE_FLAGS = {\"EMBEDDED_SUPERSET\": False}\n")
	b.WriteString(fmt.Sprintf("TRINO_SQLALCHEMY_URI = %q\n\n", trinoURI))

	if oidc.enabled {
		b.WriteString("AUTH_TYPE = AUTH_OAUTH\n")
		b.WriteString("AUTH_USER_REGISTRATION = True\n")
		b.WriteString("AUTH_USER_REGISTRATION_ROLE = \"Gamma\"\n")
		b.WriteString("AUTH_ROLES_SYNC_AT_LOGIN = True\n")
		b.WriteString("AUTH_ROLES_MAPPING = {\n")
		b.WriteString(fmt.Sprintf("    %q: [\"Admin\"],\n", dataplatformv1alpha1.DefaultGroupPlatformAdmins))
		b.WriteString(fmt.Sprintf("    %q: [\"Alpha\"],\n", dataplatformv1alpha1.DefaultGroupDataEngineers))
		b.WriteString(fmt.Sprintf("    %q: [\"Gamma\"],\n", dataplatformv1alpha1.DefaultGroupAnalysts))
		b.WriteString("}\n")
		b.WriteString("OAUTH_PROVIDERS = [{\n")
		b.WriteString("    \"name\": \"keycloak\",\n")
		b.WriteString("    \"icon\": \"fa-key\",\n")
		b.WriteString("    \"token_key\": \"access_token\",\n")
		b.WriteString("    \"remote_app\": {\n")
		b.WriteString("        \"client_id\": os.environ.get(\"SUPERSET_OIDC_CLIENT_ID\", \"superset\"),\n")
		b.WriteString("        \"client_secret\": os.environ.get(\"SUPERSET_OIDC_CLIENT_SECRET\", \"\"),\n")
		b.WriteString("        \"client_kwargs\": {\"scope\": \"openid profile email\"},\n")
		b.WriteString(fmt.Sprintf("        \"api_base_url\": %q,\n", apiBase))
		// Do not set server_metadata_url: Keycloak discovery advertises the
		// public token endpoint, and Authlib would overwrite access_token_url
		// with that unreachable host.
		b.WriteString(fmt.Sprintf("        \"access_token_url\": %q,\n", tokenURL))
		b.WriteString(fmt.Sprintf("        \"authorize_url\": %q,\n", authURL))
		b.WriteString(fmt.Sprintf("        \"jwks_uri\": %q,\n", jwksURL))
		b.WriteString("    },\n")
		b.WriteString("}]\n")
		if publicURL != "" {
			b.WriteString(fmt.Sprintf("DATABASE_OAUTH2_REDIRECT_URI = %q\n", publicURL+"/api/v1/database/oauth2/"))
		}
		b.WriteString("DATABASE_OAUTH2_CLIENTS = {\n")
		b.WriteString("    \"Trino\": {\n")
		b.WriteString("        \"id\": os.environ.get(\"TRINO_OIDC_CLIENT_ID\", \"trino\"),\n")
		b.WriteString("        \"secret\": os.environ.get(\"TRINO_OIDC_CLIENT_SECRET\", \"\"),\n")
		b.WriteString("        \"scope\": \"openid profile email\",\n")
		if publicURL != "" {
			b.WriteString(fmt.Sprintf("        \"redirect_uri\": %q,\n", publicURL+"/api/v1/database/oauth2/"))
		}
		b.WriteString(fmt.Sprintf("        \"authorization_request_uri\": %q,\n", authURL))
		b.WriteString(fmt.Sprintf("        \"token_request_uri\": %q,\n", tokenURL))
		b.WriteString("        \"request_content_type\": \"data\",\n")
		b.WriteString("    },\n")
		b.WriteString("}\n")
		// Superset's Trino engine sets X-Trino-User from the Superset login name.
		// Trino OAuth principal-field is `sub`, and OPA maps that to oidc~{sub}
		// for LakeKeeper. Rewrite the Trino user to the JWT subject so catalog
		// grants match.
		b.WriteString("def DB_CONNECTION_MUTATOR(uri, params, username, security_manager, source):\n")
		b.WriteString("    import base64, json\n")
		b.WriteString("    connect_args = params.setdefault(\"connect_args\", {})\n")
		b.WriteString("    session = connect_args.get(\"http_session\")\n")
		b.WriteString("    auth = None\n")
		b.WriteString("    if session is not None:\n")
		b.WriteString("        auth = session.headers.get(\"Authorization\") or session.headers.get(\"authorization\")\n")
		b.WriteString("    if isinstance(auth, str) and auth.lower().startswith(\"bearer \"):\n")
		b.WriteString("        token = auth.split(\" \", 1)[1].strip()\n")
		b.WriteString("        try:\n")
		b.WriteString("            payload = token.split(\".\")[1]\n")
		b.WriteString("            payload += \"=\" * (-len(payload) % 4)\n")
		b.WriteString("            claims = json.loads(base64.urlsafe_b64decode(payload.encode(\"ascii\")))\n")
		b.WriteString("            if sub := claims.get(\"sub\"):\n")
		b.WriteString("                connect_args[\"user\"] = sub\n")
		b.WriteString("        except Exception:\n")
		b.WriteString("            pass\n")
		b.WriteString("    return uri, params\n")
	} else {
		b.WriteString("AUTH_TYPE = AUTH_DB\n")
	}

	cfg := b.String()
	return cfg, hashData(cfg), nil
}

func supersetBootstrapTrino(dp *dataplatformv1alpha1.DataPlatform) string {
	trinoHost := fmt.Sprintf("trino.%s.svc", dp.Spec.Trino.NamespaceOrDefault())
	trinoURI := fmt.Sprintf("trino://%s:%d/%s", trinoHost, trinoPort, dataplatformv1alpha1.DefaultTrinoCatalog)
	var b strings.Builder
	b.WriteString("# Generated by data-platform-operator. Seeds the managed Trino database.\n")
	b.WriteString("from superset.app import create_app\n\n")
	b.WriteString("app = create_app()\n")
	b.WriteString("with app.app_context():\n")
	b.WriteString("    from superset import db\n")
	b.WriteString("    from superset.models.core import Database\n\n")
	b.WriteString("    name = \"Trino\"\n")
	b.WriteString(fmt.Sprintf("    uri = %q\n", trinoURI))
	b.WriteString("    existing = db.session.query(Database).filter_by(database_name=name).one_or_none()\n")
	b.WriteString("    if existing is None:\n")
	b.WriteString("        database = Database(database_name=name, sqlalchemy_uri=uri)\n")
	b.WriteString("        database.set_sqlalchemy_uri(uri)\n")
	b.WriteString("        db.session.add(database)\n")
	b.WriteString("    else:\n")
	b.WriteString("        database = existing\n")
	b.WriteString("        database.set_sqlalchemy_uri(uri)\n")
	// Superset only attaches the per-user OAuth Bearer token when impersonation
	// is on (see Database._get_sqla_engine → update_impersonation_config).
	// Without it, schema/table listing keeps re-raising OAuth2RedirectError
	// even after a successful /api/v1/database/oauth2/ callback.
	b.WriteString("    database.impersonate_user = True\n")
	b.WriteString("    extra = database.get_extra() or {}\n")
	b.WriteString("    extra[\"allows_virtual_table_explore\"] = True\n")
	b.WriteString("    # Mark OAuth2 so SQL Lab triggers the per-user Trino token dance.\n")
	b.WriteString("    engine_params = extra.get(\"engine_params\") or {}\n")
	b.WriteString("    connect_args = engine_params.get(\"connect_args\") or {}\n")
	b.WriteString("    connect_args[\"http_scheme\"] = \"http\"\n")
	b.WriteString("    engine_params[\"connect_args\"] = connect_args\n")
	b.WriteString("    extra[\"engine_params\"] = engine_params\n")
	b.WriteString("    database.extra = __import__(\"json\").dumps(extra)\n")
	b.WriteString("    # OAuth2 for Trino comes from DATABASE_OAUTH2_CLIENTS (keyed by engine\n")
	b.WriteString("    # name \"Trino\"). Do not store a partial oauth2_client_info blob —\n")
	b.WriteString("    # Superset validates that as a full client config and fails.\n")
	b.WriteString("    try:\n")
	b.WriteString("        encrypted = database.get_encrypted_extra() or {}\n")
	b.WriteString("        if \"oauth2_client_info\" in encrypted:\n")
	b.WriteString("            del encrypted[\"oauth2_client_info\"]\n")
	b.WriteString("            database.encrypted_extra = __import__(\"json\").dumps(encrypted) if encrypted else None\n")
	b.WriteString("    except Exception:\n")
	b.WriteString("        pass\n")
	b.WriteString("    db.session.commit()\n")
	b.WriteString("    print(\"Ensured Trino database connection\", flush=True)\n")
	return b.String()
}
