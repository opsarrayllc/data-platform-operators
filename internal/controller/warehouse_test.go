package controller

import "testing"

func TestWarehouseCreateJSONAccessKey(t *testing.T) {
	body := warehouseCreateJSON(WarehouseRequest{
		Name: "default", Bucket: "b", Region: "us-east-1", Flavor: "aws",
		AccessKeyID: "AKIA", SecretAccessKey: "secret",
	})
	cred := body["storage-credential"].(map[string]any)
	if cred["credential-type"] != "access-key" || cred["aws-access-key-id"] != "AKIA" {
		t.Fatalf("storage-credential = %v, want access-key with AKIA", cred)
	}
	if _, ok := body["storage-profile"].(map[string]any)["assume-role-arn"]; ok {
		t.Fatalf("storage-profile has assume-role-arn without a system identity")
	}
}

func TestWarehouseCreateJSONSystemIdentity(t *testing.T) {
	body := warehouseCreateJSON(WarehouseRequest{
		Name: "default", Bucket: "b", Region: "us-east-1", Flavor: "aws",
		STSEnabled: true, SystemIdentity: true,
		AssumeRoleARN: "arn:aws:iam::123456789012:role/warehouse",
		ExternalID:    "ext",
	})
	cred := body["storage-credential"].(map[string]any)
	if cred["credential-type"] != "aws-system-identity" || cred["external-id"] != "ext" {
		t.Fatalf("storage-credential = %v, want aws-system-identity with external-id", cred)
	}
	if _, ok := cred["aws-access-key-id"]; ok {
		t.Fatalf("storage-credential carries an access key: %v", cred)
	}
	profile := body["storage-profile"].(map[string]any)
	if profile["assume-role-arn"] != "arn:aws:iam::123456789012:role/warehouse" {
		t.Fatalf("storage-profile = %v, want assume-role-arn", profile)
	}

	direct := warehouseCreateJSON(WarehouseRequest{Name: "default", Bucket: "b", Region: "us-east-1", SystemIdentity: true})
	if _, ok := direct["storage-credential"].(map[string]any)["external-id"]; ok {
		t.Fatalf("storage-credential has an empty external-id")
	}
	if _, ok := direct["storage-profile"].(map[string]any)["assume-role-arn"]; ok {
		t.Fatalf("storage-profile has an empty assume-role-arn")
	}
}
