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
	"hash/fnv"
	"maps"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// Debezium Server (spec.debezium) streams database changes into the warehouse
// through the LakeKeeper REST catalog with the memiiso Iceberg sink. Each
// source is a single-replica Deployment: two engines on one source would fight
// over its replication slot or binlog position. Offsets and schema history live
// in Iceberg tables, so a pod can be rescheduled without a volume.
const (
	componentDebezium   = "debezium"
	nameDebezium        = "debezium"
	labelDebeziumSource = "dataplatform.opsarray.io/debezium-source"
	keyDebeziumConfig   = "application.properties"
	// envDebeziumUser and envDebeziumPassword hold the source login. The
	// properties file refers to them, so the password never lands in it.
	envDebeziumUser     = "DEBEZIUM_SOURCE_DATABASE_USER"
	envDebeziumPassword = "DEBEZIUM_SOURCE_DATABASE_PASSWORD"
	debeziumConfigDir   = "/debezium/config"
	debeziumDataDir     = "/debezium/data"
	debeziumHTTPPort    = int32(8080)
	// annotationDoNotDisrupt stops Karpenter consolidating a running engine.
	// An engine stopped mid-batch can leave the replication slot confirmed
	// past its stored offset, and each restart replays from that offset.
	annotationDoNotDisrupt = "karpenter.sh/do-not-disrupt"
	// debeziumTerminationGracePeriod gives the engine time to commit its
	// last batch and offsets to Iceberg on shutdown.
	debeziumTerminationGracePeriod = int64(120)
	debeziumManagementPort         = int32(9000)
	portNameManagement             = "management"
	uidDebezium                    = int64(185)
	gidDebezium                    = int64(185)
	// debeziumServerIDBase keeps derived MySQL server ids clear of the small
	// numbers replicas usually take.
	debeziumServerIDBase  = 5400
	debeziumServerIDRange = 100000
	// propDebeziumUpsert is generated, and commonly overridden per source.
	propDebeziumUpsert = "debezium.sink.iceberg.upsert"
	// authTypeOAuth2 is the OAuth2 authentication type in Trino and Iceberg.
	authTypeOAuth2 = "oauth2"
)

// debeziumConnectorClasses maps spec connector names to Debezium classes.
var debeziumConnectorClasses = map[string]string{
	dataplatformv1alpha1.DebeziumConnectorMySQL:      "io.debezium.connector.mysql.MySqlConnector",
	dataplatformv1alpha1.DebeziumConnectorMariaDB:    "io.debezium.connector.mariadb.MariaDbConnector",
	dataplatformv1alpha1.DebeziumConnectorPostgreSQL: "io.debezium.connector.postgresql.PostgresConnector",
	dataplatformv1alpha1.DebeziumConnectorSQLServer:  "io.debezium.connector.sqlserver.SqlServerConnector",
}

func debeziumName(src *dataplatformv1alpha1.DebeziumSourceSpec) string {
	return nameDebezium + "-" + src.Name
}

func debeziumConfigSecretName(src *dataplatformv1alpha1.DebeziumSourceSpec) string {
	return debeziumName(src) + "-config"
}

func debeziumCredentialsSecretName(src *dataplatformv1alpha1.DebeziumSourceSpec) string {
	return debeziumName(src) + "-credentials"
}

// debeziumTopicPrefix is the Debezium topic prefix, which the sink turns into
// the start of every table name.
func debeziumTopicPrefix(src *dataplatformv1alpha1.DebeziumSourceSpec) string {
	return strings.ReplaceAll(src.Name, "-", "_")
}

func debeziumLabels(dp *dataplatformv1alpha1.DataLake, src *dataplatformv1alpha1.DebeziumSourceSpec) map[string]string {
	labels := labelsFor(dp, componentDebezium)
	labels[labelDebeziumSource] = src.Name
	return labels
}

// debeziumServerID derives a stable MySQL/MariaDB replication client id.
func debeziumServerID(dp *dataplatformv1alpha1.DataLake, src *dataplatformv1alpha1.DebeziumSourceSpec) int64 {
	if src.ServerID != nil {
		return *src.ServerID
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(dp.Name + "/" + src.Name))
	return debeziumServerIDBase + int64(h.Sum32()%debeziumServerIDRange)
}

// reconcileDebezium deploys one Debezium Server per source once the warehouse
// and Debezium's catalog grant exist. It reports whether it is still progressing.
func (r *DataLakeReconciler) reconcileDebezium(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	store objectStore,
	oidc oidcConfig,
) (bool, error) {
	spec := &dp.Spec.Debezium
	ns := spec.NamespaceOrDefault()
	if !spec.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionTrue, reasonDisabled, "No Debezium sources are configured")
		return false, r.pruneDebeziumSources(ctx, dp, ns, nil)
	}
	if !dp.Spec.Lakekeeper.IsEnabled() {
		setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionFalse, reasonMissing, "spec.debezium requires LakeKeeper")
		return true, nil
	}
	if oidc.enabled && oidc.debeziumPrincipal == "" {
		setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionFalse, reasonMissing,
			"spec.debezium needs embedded Keycloak or spec.auth.serviceAccountTokens to authenticate to LakeKeeper")
		return true, nil
	}
	if !conditionTrue(dp, dataplatformv1alpha1.ConditionWarehouseReady) {
		setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionFalse, reasonNotReady, "Waiting for the warehouse")
		return true, nil
	}

	fail := func(err error) (bool, error) {
		setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionFalse, reasonError, err.Error())
		return false, err
	}
	if err := r.ensureNamespace(ctx, dp, ns, componentDebezium); err != nil {
		return fail(err)
	}
	// One account for every source: it is the LakeKeeper principal the
	// operator grants, and the identity to associate with a cloud role.
	if err := r.ensureServiceAccount(ctx, dp, ns, nameDebezium, componentDebezium, nil); err != nil {
		return fail(err)
	}
	if oidc.serviceAccounts.enabled {
		if err := r.applyTokenProxyConfigMap(ctx, dp, ns, componentDebezium); err != nil {
			return fail(err)
		}
	}

	keep := map[string]bool{}
	var notReady []string
	for i := range spec.Sources {
		src := &spec.Sources[i]
		keep[src.Name] = true
		if err := r.reconcileDebeziumSource(ctx, dp, ns, src, store, oidc); err != nil {
			return fail(fmt.Errorf("debezium source %s: %w", src.Name, err))
		}
		ready, err := r.deploymentReady(ctx, ns, debeziumName(src))
		if err != nil && !errors.IsNotFound(err) {
			return fail(err)
		}
		if !ready {
			notReady = append(notReady, src.Name)
		}
	}
	if err := r.pruneDebeziumSources(ctx, dp, ns, keep); err != nil {
		return fail(err)
	}
	if len(notReady) > 0 {
		setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionFalse, reasonNotReady,
			"Debezium sources are not ready: "+strings.Join(notReady, ", "))
		return true, nil
	}
	setCondition(dp, dataplatformv1alpha1.ConditionDebeziumReady, metav1.ConditionTrue, reasonReady,
		fmt.Sprintf("%d Debezium source(s) running", len(spec.Sources)))
	return false, nil
}

func (r *DataLakeReconciler) reconcileDebeziumSource(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns string,
	src *dataplatformv1alpha1.DebeziumSourceSpec,
	store objectStore,
	oidc oidcConfig,
) error {
	ref := src.CredentialsSecretRef
	refNS := stringOr(ref.Namespace, ns)
	user, err := r.getSecretData(ctx, ref.Name, refNS, ref.UsernameKeyOrDefault())
	if err != nil {
		return err
	}
	password, err := r.getSecretData(ctx, ref.Name, refNS, ref.PasswordKeyOrDefault())
	if err != nil {
		return err
	}
	labels := debeziumLabels(dp, src)

	// Copied so the pod can reference it whichever namespace it came from.
	creds := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: debeziumCredentialsSecretName(src), Namespace: ns}}
	if err := r.apply(ctx, dp, creds, func() error {
		ensureLabels(creds, labels)
		creds.Type = corev1.SecretTypeOpaque
		creds.Data = map[string][]byte{
			keyPostgresUsername: []byte(user),
			keyPostgresPassword: []byte(password),
		}
		return nil
	}); err != nil {
		return err
	}

	// A Secret, not a ConfigMap: with static S3 keys or a Keycloak client the
	// properties carry credentials.
	props := debeziumProperties(dp, src, store, oidc)
	cfg := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: debeziumConfigSecretName(src), Namespace: ns}}
	if err := r.apply(ctx, dp, cfg, func() error {
		ensureLabels(cfg, labels)
		cfg.Type = corev1.SecretTypeOpaque
		cfg.Data = map[string][]byte{keyDebeziumConfig: []byte(props)}
		return nil
	}); err != nil {
		return err
	}

	// Credentials are hashed too: env vars from a Secret only change on restart.
	cfgHash := hashData(props, user, password)
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: debeziumName(src), Namespace: ns}}
	return r.apply(ctx, dp, deploy, func() error {
		ensureLabels(deploy, labels)
		if deploy.CreationTimestamp.IsZero() {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}
		deploy.Spec.Replicas = ptr.To(int32(1))
		// Never run two engines against one source, even during a rollout.
		deploy.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = map[string]string{}
		}
		deploy.Spec.Template.Annotations[annotationConfigHash] = cfgHash
		deploy.Spec.Template.Annotations[annotationDoNotDisrupt] = strconv.FormatBool(true)
		deploy.Spec.Template.Labels = labels
		deploy.Spec.Template.Spec = debeziumPodSpec(dp.Spec.Debezium.ImageOrDefault(), src, oidc.serviceAccounts)
		return nil
	})
}

func stringOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func debeziumPodSpec(image string, src *dataplatformv1alpha1.DebeziumSourceSpec, sa serviceAccountTokens) corev1.PodSpec {
	credentials := debeziumCredentialsSecretName(src)
	env := make([]corev1.EnvVar, 0, 2+len(src.ExtraEnv))
	env = append(env,
		corev1.EnvVar{Name: envDebeziumUser, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: credentials},
			Key:                  keyPostgresUsername,
		}}},
		corev1.EnvVar{Name: envDebeziumPassword, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: credentials},
			Key:                  keyPostgresPassword,
		}}},
	)
	env = append(env, src.ExtraEnv...)
	health := func(path string) corev1.ProbeHandler {
		return corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Path: path,
			Port: intstr.FromInt32(debeziumManagementPort),
		}}
	}
	spec := corev1.PodSpec{
		ServiceAccountName:            nameDebezium,
		SecurityContext:               restrictedPodSecurity(uidDebezium, gidDebezium),
		TerminationGracePeriodSeconds: ptr.To(debeziumTerminationGracePeriod),
		Containers: []corev1.Container{{
			Name:  nameDebezium,
			Image: image,
			Ports: []corev1.ContainerPort{
				{Name: portNameHTTP, ContainerPort: debeziumHTTPPort},
				{Name: portNameManagement, ContainerPort: debeziumManagementPort},
			},
			Env: env,
			VolumeMounts: []corev1.VolumeMount{
				{Name: volumeConfig, MountPath: debeziumConfigDir, ReadOnly: true},
				{Name: volumeData, MountPath: debeziumDataDir},
				{Name: volumeTmp, MountPath: "/tmp"},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler:     health("/q/health/ready"),
				PeriodSeconds:    10,
				FailureThreshold: 3,
			},
			// Generous: an initial snapshot keeps the JVM busy, and a restart
			// only resumes from the last committed offset.
			LivenessProbe: &corev1.Probe{
				ProbeHandler:        health("/q/health/live"),
				InitialDelaySeconds: 60,
				PeriodSeconds:       20,
				FailureThreshold:    6,
			},
			Resources:       src.Resources,
			SecurityContext: restrictedContainerSecurity(uidDebezium, gidDebezium),
		}},
		Volumes: []corev1.Volume{
			{Name: volumeConfig, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: debeziumConfigSecretName(src)}}},
			{Name: volumeData, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: volumeTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	if sa.enabled {
		withTokenProxy(&spec, sa)
	}
	return spec
}

// debeziumProperties renders Debezium Server's application.properties. Every
// debezium.sink.iceberg.* key reaches the Iceberg catalog with the prefix
// removed, so the catalog and S3 settings mirror Trino's lakekeeper catalog.
func debeziumProperties(
	dp *dataplatformv1alpha1.DataLake,
	src *dataplatformv1alpha1.DebeziumSourceSpec,
	store objectStore,
	oidc oidcConfig,
) string {
	prefix := debeziumTopicPrefix(src)
	lkURL := clusterServiceURL(nameLakekeeper, dp.Spec.Lakekeeper.NamespaceOrDefault(), lakekeeperPort) + "/catalog"
	props := map[string]string{
		"quarkus.management.enabled": strconv.FormatBool(true),

		"debezium.source.connector.class":   debeziumConnectorClasses[src.Connector],
		"debezium.source.topic.prefix":      prefix,
		"debezium.source.database.hostname": src.Host,
		"debezium.source.database.port":     strconv.Itoa(int(src.PortOrDefault())),
		"debezium.source.database.user":     "${" + envDebeziumUser + "}",
		"debezium.source.database.password": "${" + envDebeziumPassword + "}",
		// Per-source table names, so sources may share a destination namespace.
		"debezium.source.offset.storage":                             "io.debezium.server.iceberg.offset.IcebergOffsetBackingStore",
		"debezium.source.offset.storage.iceberg.table-name":          "_debezium_offset_storage_" + prefix,
		"debezium.source.schema.history.internal":                    "io.debezium.server.iceberg.history.IcebergSchemaHistory",
		"debezium.source.schema.history.internal.iceberg.table-name": "_debezium_schema_history_" + prefix,

		// The sink creates tables from the event schemas, so keep them.
		"debezium.format.key":                  "connect",
		"debezium.format.value":                "connect",
		"debezium.format.key.schemas.enable":   strconv.FormatBool(true),
		"debezium.format.value.schemas.enable": strconv.FormatBool(true),
		// Flatten change events to row images plus change metadata columns.
		"debezium.transforms":                                       "unwrap",
		"debezium.transforms.unwrap.type":                           "io.debezium.transforms.ExtractNewRecordState",
		"debezium.transforms.unwrap.add.fields":                     "op,table,source.ts_ns,db,ts_ms",
		"debezium.transforms.unwrap.delete.tombstone.handling.mode": "rewrite",
		"debezium.transforms.unwrap.drop.tombstones":                strconv.FormatBool(true),
		"debezium.sink.type":                                        "iceberg",
		"debezium.sink.iceberg.catalog-name":                        dp.Spec.Trino.CatalogOrDefault(),
		"debezium.sink.iceberg.type":                                "rest",
		"debezium.sink.iceberg.uri":                                 lkURL,
		"debezium.sink.iceberg.warehouse":                           dp.Spec.Lakekeeper.Warehouse.NameOrDefault(),
		"debezium.sink.iceberg.table-namespace":                     src.IcebergNamespaceOrDefault(),
		propDebeziumUpsert:                                          strconv.FormatBool(src.IsUpsert()),
		"debezium.sink.iceberg.io-impl":                             "org.apache.iceberg.aws.s3.S3FileIO",
		"debezium.sink.iceberg.client.region":                       store.Region,
		"debezium.sink.iceberg.s3.path-style-access":                strconv.FormatBool(store.PathStyleAccess),
	}
	if src.TablePrefix != "" {
		props["debezium.sink.iceberg.table-prefix"] = src.TablePrefix
	}
	if src.SnapshotMode != "" {
		props["debezium.source.snapshot.mode"] = src.SnapshotMode
	}
	if len(src.TableIncludeList) > 0 {
		props["debezium.source.table.include.list"] = strings.Join(src.TableIncludeList, ",")
	}

	switch src.Connector {
	case dataplatformv1alpha1.DebeziumConnectorMySQL, dataplatformv1alpha1.DebeziumConnectorMariaDB:
		props["debezium.source.database.server.id"] = strconv.FormatInt(debeziumServerID(dp, src), 10)
		if src.Database != "" {
			props["debezium.source.database.include.list"] = src.Database
		}
	case dataplatformv1alpha1.DebeziumConnectorPostgreSQL:
		props["debezium.source.database.dbname"] = src.Database
		props["debezium.source.plugin.name"] = "pgoutput"
		// Named per source: two connectors sharing a slot lose changes.
		props["debezium.source.slot.name"] = "debezium_" + prefix
		props["debezium.source.publication.name"] = "debezium_" + prefix
		// "filtered" needs table ownership rather than superuser, which managed
		// Postgres (RDS, Cloud SQL) does not hand out.
		props["debezium.source.publication.autocreate.mode"] = "filtered"
		// The slot is confirmed as events reach Iceberg, but the offset is
		// stored only periodically, so after an unclean stop the slot can be
		// ahead of the offset. The default (no_validation) then fails on every
		// start asking for WAL the slot has released; resume from whichever
		// position is further along instead.
		props["debezium.source.offset.mismatch.strategy"] = "trust_greater_lsn"
	case dataplatformv1alpha1.DebeziumConnectorSQLServer:
		props["debezium.source.database.names"] = src.Database
	}

	switch {
	case store.STSEnabled:
		// LakeKeeper vends table-scoped credentials, as it does for Trino.
		props["debezium.sink.iceberg.header.X-Iceberg-Access-Delegation"] = "vended-credentials"
	case store.SystemIdentity == nil:
		props["debezium.sink.iceberg.s3.access-key-id"] = store.AccessKeyID
		props["debezium.sink.iceberg.s3.secret-access-key"] = store.SecretAccessKey
	}
	// With a system identity and no STS, S3FileIO falls back to the pod's AWS
	// credential chain: associate the debezium ServiceAccount with a role.
	if store.Endpoint != "" {
		props["debezium.sink.iceberg.s3.endpoint"] = store.Endpoint
	}

	if oidc.enabled {
		props["debezium.sink.iceberg.rest.auth.type"] = authTypeOAuth2
		props["debezium.sink.iceberg.oauth2-server-uri"] = oidc.machineTokenURL()
		if oidc.serviceAccounts.enabled {
			// The token sidecar ignores the credential, but the client needs
			// one to start the client-credentials flow.
			props["debezium.sink.iceberg.credential"] = nameDebezium + ":serviceaccount"
		} else {
			props["debezium.sink.iceberg.credential"] = oidc.debeziumClientID + ":" + oidc.debeziumSecret
			if oidc.scope != "" {
				props["debezium.sink.iceberg.scope"] = oidc.scope
			}
		}
	}

	maps.Copy(props, src.ExtraProperties)
	return renderProperties(props)
}

// pruneDebeziumSources deletes the Deployments and Secrets of sources no
// longer in the spec. It leaves the source database's replication slot or
// publication, and the Iceberg tables, in place.
func (r *DataLakeReconciler) pruneDebeziumSources(
	ctx context.Context,
	dp *dataplatformv1alpha1.DataLake,
	ns string,
	keep map[string]bool,
) error {
	log := logf.FromContext(ctx)
	opts := []client.ListOption{
		client.InNamespace(ns),
		client.MatchingLabels{labelAppComponent: componentDebezium, labelAppInstance: dp.Name},
		client.HasLabels{labelDebeziumSource},
	}
	deploys := &appsv1.DeploymentList{}
	if err := r.List(ctx, deploys, opts...); err != nil {
		return err
	}
	for i := range deploys.Items {
		d := &deploys.Items[i]
		if keep[d.Labels[labelDebeziumSource]] {
			continue
		}
		log.Info("Deleting Debezium Deployment", "name", d.Name, "namespace", d.Namespace)
		if err := client.IgnoreNotFound(r.Delete(ctx, d)); err != nil {
			return err
		}
	}
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, opts...); err != nil {
		return err
	}
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if keep[s.Labels[labelDebeziumSource]] {
			continue
		}
		log.Info("Deleting Debezium Secret", "name", s.Name, "namespace", s.Namespace)
		if err := client.IgnoreNotFound(r.Delete(ctx, s)); err != nil {
			return err
		}
	}
	return nil
}
