package controller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

func parseProperties(t *testing.T, s string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed property line %q", line)
		}
		out[k] = v
	}
	return out
}

func TestDebeziumPropertiesPostgresKeycloak(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	dp.Name = "lake"
	dp.Spec.Lakekeeper.Warehouse.Name = "acme"
	src := &dataplatformv1alpha1.DebeziumSourceSpec{
		Name:             "billing",
		Connector:        dataplatformv1alpha1.DebeziumConnectorPostgreSQL,
		Host:             "billing.db",
		Database:         "billing",
		TableIncludeList: []string{"public.invoices", "public.payments"},
		IcebergNamespace: "raw.billing",
		Upsert:           ptr.To(false),
		ExtraProperties:  map[string]string{"debezium.source.database.sslmode": "require", propDebeziumUpsert: "true"},
	}
	store := objectStore{Region: "us-east-1", AccessKeyID: "AKIA", SecretAccessKey: "shh", Endpoint: "http://minio.minio.svc:9000", PathStyleAccess: true}
	oidc := oidcConfig{
		enabled:          true,
		scope:            "lakekeeper",
		tokenURL:         "http://keycloak.keycloak.svc:8080/realms/dataplatform/protocol/openid-connect/token",
		debeziumClientID: "debezium",
		debeziumSecret:   "clientsecret",
	}
	props := parseProperties(t, debeziumProperties(dp, src, store, oidc))

	want := map[string]string{
		"debezium.source.connector.class":             "io.debezium.connector.postgresql.PostgresConnector",
		"debezium.source.database.port":               "5432",
		"debezium.source.database.dbname":             "billing",
		"debezium.source.plugin.name":                 "pgoutput",
		"debezium.source.slot.name":                   "debezium_billing",
		"debezium.source.publication.name":            "debezium_billing",
		"debezium.source.publication.autocreate.mode": "filtered",
		"debezium.source.offset.mismatch.strategy":    "trust_greater_lsn",
		// Heartbeats advance the slot when the captured tables are quiet.
		"debezium.source.heartbeat.interval.ms":       "60000",
		"debezium.source.heartbeat.action.query":      "SELECT pg_logical_emit_message(false, 'debezium-heartbeat', now()::text)",
		"debezium.source.message.prefix.exclude.list": "debezium-heartbeat",
		"debezium.source.table.include.list":          "public.invoices,public.payments",
		"debezium.source.database.sslmode":            "require",
		"debezium.sink.iceberg.warehouse":             "acme",
		"debezium.sink.iceberg.table-namespace":       "raw.billing",
		// extraProperties win over generated keys.
		"debezium.sink.iceberg.upsert":                               "true",
		"debezium.sink.iceberg.rest.auth.type":                       "oauth2",
		"debezium.sink.iceberg.oauth2-server-uri":                    oidc.tokenURL,
		"debezium.sink.iceberg.credential":                           "debezium:clientsecret",
		"debezium.sink.iceberg.scope":                                "lakekeeper",
		"debezium.sink.iceberg.s3.access-key-id":                     "AKIA",
		"debezium.sink.iceberg.s3.secret-access-key":                 "shh",
		"debezium.sink.iceberg.s3.endpoint":                          "http://minio.minio.svc:9000",
		"debezium.sink.iceberg.s3.path-style-access":                 "true",
		"debezium.source.offset.storage.iceberg.table-name":          "_debezium_offset_storage_billing",
		"debezium.source.schema.history.internal.iceberg.table-name": "_debezium_schema_history_billing",
	}
	for k, v := range want {
		if props[k] != v {
			t.Errorf("%s = %q, want %q", k, props[k], v)
		}
	}
	for _, k := range []string{"debezium.source.database.server.id", "debezium.sink.iceberg.header.X-Iceberg-Access-Delegation"} {
		if _, ok := props[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestDebeziumPropertiesSystemIdentityWithoutSTS(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	src := &dataplatformv1alpha1.DebeziumSourceSpec{Name: "a", Connector: dataplatformv1alpha1.DebeziumConnectorMariaDB, Host: "h"}
	store := objectStore{Region: "us-east-1", SystemIdentity: &dataplatformv1alpha1.S3SystemIdentitySpec{}}
	props := parseProperties(t, debeziumProperties(dp, src, store, oidcConfig{}))
	for _, k := range []string{
		"debezium.sink.iceberg.s3.access-key-id",
		"debezium.sink.iceberg.header.X-Iceberg-Access-Delegation",
		"debezium.sink.iceberg.rest.auth.type",
		"debezium.source.database.include.list",
	} {
		if _, ok := props[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
	if props["debezium.source.connector.class"] != "io.debezium.connector.mariadb.MariaDbConnector" {
		t.Errorf("connector.class = %q", props["debezium.source.connector.class"])
	}
}

func TestDebeziumServerID(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	dp.Name = "lake"
	a := &dataplatformv1alpha1.DebeziumSourceSpec{Name: "orders"}
	b := &dataplatformv1alpha1.DebeziumSourceSpec{Name: "inventory"}
	idA, idB := debeziumServerID(dp, a), debeziumServerID(dp, b)
	if idA != debeziumServerID(dp, a) {
		t.Fatal("server id is not stable")
	}
	if idA == idB {
		t.Fatalf("sources share server id %d", idA)
	}
	for _, id := range []int64{idA, idB} {
		if id < debeziumServerIDBase || id >= debeziumServerIDBase+debeziumServerIDRange {
			t.Fatalf("server id %d out of range", id)
		}
	}
	a.ServerID = ptr.To(int64(42))
	if debeziumServerID(dp, a) != 42 {
		t.Fatal("explicit server id ignored")
	}
}

// TestDebeziumPropertiesVendedCredentials covers an AWS warehouse with STS and
// ServiceAccount tokens: the sink asks LakeKeeper for table credentials and
// holds no keys or client secret of its own.
func TestDebeziumPropertiesVendedCredentials(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	src := &dataplatformv1alpha1.DebeziumSourceSpec{Name: "orders", Connector: dataplatformv1alpha1.DebeziumConnectorMySQL, Host: "h", Database: "orders"}
	store := objectStore{Region: "us-east-1", STSEnabled: true, SystemIdentity: &dataplatformv1alpha1.S3SystemIdentitySpec{}}
	oidc := oidcConfig{enabled: true, scope: "openid", serviceAccounts: serviceAccountTokens{enabled: true}}
	props := parseProperties(t, debeziumProperties(dp, src, store, oidc))
	if got := props["debezium.sink.iceberg.header.X-Iceberg-Access-Delegation"]; got != "vended-credentials" {
		t.Errorf("access delegation header = %q", got)
	}
	if got := props["debezium.sink.iceberg.credential"]; got != "debezium:serviceaccount" {
		t.Errorf("credential = %q", got)
	}
	for _, k := range []string{"debezium.sink.iceberg.s3.access-key-id", "debezium.sink.iceberg.s3.endpoint", "debezium.sink.iceberg.scope"} {
		if _, ok := props[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

// TestDebeziumPodSpecSnapshotExpiry covers the sidecar that expires the
// source's table snapshots: it runs the mounted program with the engine's
// image and configuration, and shares the token proxy with the engine.
func TestDebeziumPodSpecSnapshotExpiry(t *testing.T) {
	src := &dataplatformv1alpha1.DebeziumSourceSpec{Name: "billing", Connector: dataplatformv1alpha1.DebeziumConnectorPostgreSQL, Host: "h", Database: "billing"}
	spec := debeziumPodSpec("debezium:test", src, serviceAccountTokens{enabled: true}, 6*time.Hour)

	var expiry *corev1.Container
	names := map[string]bool{}
	for i := range spec.Containers {
		names[spec.Containers[i].Name] = true
		if spec.Containers[i].Name == containerSnapshotExpiry {
			expiry = &spec.Containers[i]
		}
	}
	if expiry == nil {
		t.Fatalf("no %s container in %v", containerSnapshotExpiry, names)
	}
	if !names[nameDebezium] || !names[volumeTokenProxy] {
		t.Errorf("containers = %v, want the engine and token proxy too", names)
	}
	if expiry.Image != "debezium:test" {
		t.Errorf("image = %q, want the engine's", expiry.Image)
	}
	script := strings.Join(expiry.Command, " ")
	for _, want := range []string{
		debeziumMaintenanceDir + "/" + keySnapshotExpirySource,
		debeziumConfigDir + "/" + keyDebeziumConfig,
		// Offset retention, data retention, snapshots always kept.
		"3600000 21600000 10",
		fmt.Sprintf("sleep %d & wait", snapshotExpiryDelay(src)),
		"trap 'exit 0' TERM",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("command missing %q:\n%s", want, script)
		}
	}
	mounts := map[string]string{}
	for _, m := range expiry.VolumeMounts {
		mounts[m.Name] = m.MountPath
	}
	if mounts[volumeConfig] != debeziumConfigDir || mounts[volumeDebeziumMaintenance] != debeziumMaintenanceDir {
		t.Errorf("mounts = %v", mounts)
	}
	found := false
	for _, v := range spec.Volumes {
		if v.Name == volumeDebeziumMaintenance && v.ConfigMap != nil && v.ConfigMap.Name == configMapDebeziumMaintenance {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s volume from ConfigMap %s", volumeDebeziumMaintenance, configMapDebeziumMaintenance)
	}
	if !strings.Contains(debeziumSnapshotExpirySource, "public class ExpireSnapshots") {
		t.Error("embedded expiry program is missing or renamed")
	}
}

// TestSnapshotExpiryDelay checks the first run is spread across sources but
// stable for each.
func TestSnapshotExpiryDelay(t *testing.T) {
	seen := map[int]bool{}
	for _, name := range []string{"sales", "crm", "billing", "orders-db", "inventory", "payments"} {
		src := &dataplatformv1alpha1.DebeziumSourceSpec{Name: name}
		d := snapshotExpiryDelay(src)
		if d < snapshotExpiryDelaySeconds || d >= snapshotExpiryDelaySeconds+snapshotExpiryJitterSeconds {
			t.Errorf("%s: delay %d out of range", name, d)
		}
		if d != snapshotExpiryDelay(&dataplatformv1alpha1.DebeziumSourceSpec{Name: name}) {
			t.Errorf("%s: delay is not stable", name)
		}
		seen[d] = true
	}
	if len(seen) < 5 {
		t.Errorf("delays %v are not spread out", seen)
	}
}

func TestDebeziumCompactionReady(t *testing.T) {
	sa := oidcConfig{enabled: true, serviceAccounts: serviceAccountTokens{enabled: true, audience: "lakekeeper"}}
	off := false
	cases := []struct {
		name string
		dp   func(*dataplatformv1alpha1.DataLake)
		oidc oidcConfig
		want bool
	}{
		{"service account tokens", func(*dataplatformv1alpha1.DataLake) {}, sa, true},
		{"no auth", func(*dataplatformv1alpha1.DataLake) {}, oidcConfig{}, false},
		{"keycloak clients", func(*dataplatformv1alpha1.DataLake) {}, oidcConfig{enabled: true}, false},
		{"trino off", func(dp *dataplatformv1alpha1.DataLake) { dp.Spec.Trino.Enabled = &off }, sa, false},
		{"disabled", func(dp *dataplatformv1alpha1.DataLake) { dp.Spec.Debezium.Maintenance.Compaction.Enabled = &off }, sa, false},
	}
	for _, c := range cases {
		dp := &dataplatformv1alpha1.DataLake{}
		c.dp(dp)
		if got, _ := debeziumCompactionReady(dp, c.oidc); got != c.want {
			t.Errorf("%s: ready = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDebeziumCompactionCronJobSpec(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	dp.Spec.Debezium.Sources = []dataplatformv1alpha1.DebeziumSourceSpec{
		{Name: "sales"}, {Name: "crm-db"}, {Name: "crm-replica", IcebergNamespace: "crm_db"},
	}
	dp.Spec.Debezium.Maintenance.Compaction.Schedule = "0 2 * * *"
	oidc := oidcConfig{enabled: true, serviceAccounts: serviceAccountTokens{enabled: true, audience: "lakekeeper"}}
	spec := debeziumCompactionCronJobSpec(dp, oidc, map[string]string{"app": "x"})

	if spec.Schedule != "0 2 * * *" || spec.ConcurrencyPolicy != "Forbid" {
		t.Errorf("schedule %q, concurrency %q", spec.Schedule, spec.ConcurrencyPolicy)
	}
	pod := spec.JobTemplate.Spec.Template.Spec
	if pod.ServiceAccountName != nameDebezium {
		t.Errorf("service account = %q", pod.ServiceAccountName)
	}
	c := pod.Containers[0]
	if c.Image != dataplatformv1alpha1.DefaultDebeziumCompactionImage || len(c.Args) != 1 || c.Args[0] != "maintain" {
		t.Errorf("image %q args %v", c.Image, c.Args)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	for k, want := range map[string]string{
		"MAINTAIN_NAMESPACES":          "crm_db,sales",
		"MAINTAIN_FILE_SIZE_THRESHOLD": "128MB",
		"MAINTAIN_SNAPSHOT_RETENTION":  "7d",
		"TRINO_HOST":                   "trino.trino.svc",
		"TRINO_PORT":                   "8443",
		"TRINO_CA_FILE":                debeziumCompactionCADir + "/" + keyTrinoCA,
		"TRINO_TOKEN_FILE":             debeziumCompactionTokenDir + "/token",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	var audience string
	for _, v := range pod.Volumes {
		if v.Projected != nil {
			audience = v.Projected.Sources[0].ServiceAccountToken.Audience
		}
	}
	if audience != "lakekeeper" {
		t.Errorf("token audience = %q", audience)
	}
}
