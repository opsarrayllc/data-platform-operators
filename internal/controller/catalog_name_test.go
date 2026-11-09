package controller

import (
	"context"
	"strings"
	"testing"

	dataplatformv1alpha1 "github.com/opsarrayllc/data-platform-operator/api/v1alpha1"
)

// TestTrinoCatalogName checks that spec.trino.catalog names the warehouse
// catalog file and reserves that name from extraCatalogs.
func TestTrinoCatalogName(t *testing.T) {
	dp := &dataplatformv1alpha1.DataLake{}
	dp.Spec.Trino.Catalog = "keyme"
	dp.Spec.Lakekeeper.Warehouse.Name = "keyme"
	dp.Spec.Trino.ExtraCatalogs = map[string]string{
		"keyme":      "connector.name=memory\n",
		"lakekeeper": "connector.name=memory\n",
	}
	r := &DataLakeReconciler{}
	data, _ := r.trinoCatalogData(context.Background(), dp, objectStore{Region: "us-east-1"}, oidcConfig{})

	props := string(data["keyme.properties"])
	if !strings.Contains(props, "connector.name=iceberg") || !strings.Contains(props, "iceberg.rest-catalog.warehouse=keyme") {
		t.Fatalf("keyme.properties is not the warehouse catalog:\n%s", props)
	}
	// "lakekeeper" is an ordinary name once the warehouse catalog is renamed.
	if string(data["lakekeeper.properties"]) != "connector.name=memory\n" {
		t.Errorf("lakekeeper.properties = %q, want the extraCatalogs entry", data["lakekeeper.properties"])
	}

	dp.Spec.Trino.Catalog = ""
	if dp.Spec.Trino.CatalogOrDefault() != dataplatformv1alpha1.DefaultTrinoCatalog {
		t.Errorf("default catalog = %q", dp.Spec.Trino.CatalogOrDefault())
	}
}
