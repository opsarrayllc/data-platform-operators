package controller

import (
	"strings"
	"testing"

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
	dp.Spec.Lakekeeper.Warehouse.Name = "keyme"
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
		"debezium.sink.iceberg.warehouse":             "keyme",
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
