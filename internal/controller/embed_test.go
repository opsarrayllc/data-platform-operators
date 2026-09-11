package controller

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedPoliciesArePresent guards the OPA bundle the manager ships.
// An empty embed.FS compiles, then OPA starts with no Trino policy.
func TestEmbeddedPoliciesArePresent(t *testing.T) {
	for _, dir := range opaPolicyDirs {
		entries, err := fs.ReadDir(opaPolicyFS, dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		rego := 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".rego") {
				rego++
			}
		}
		if rego == 0 {
			t.Errorf("%s has no embedded .rego files", dir)
		}
	}
}

// TestDockerignoreKeepsEmbeddedPolicies catches the e2e/image build failure
// where go:embed cannot find *.rego because .dockerignore only kept *.go.
func TestDockerignoreKeepsEmbeddedPolicies(t *testing.T) {
	raw, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	if !strings.Contains(string(raw), "!**/*.rego") {
		t.Error(".dockerignore does not re-include *.rego files the manager embeds")
	}
}
