//go:build e2e

package e2etest

import (
	"os"
	"testing"
)

func TestBuildCLI(t *testing.T) {
	binary := BuildCLIBinary(t)
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary not found at %s: %v", binary, err)
	}
}

func TestBuildModuleBinary(t *testing.T) {
	binary := BuildModuleBinary(t, "github.com/fullsend-ai/fullsend")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary not found at %s: %v", binary, err)
	}
}
