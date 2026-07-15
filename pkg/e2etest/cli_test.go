//go:build e2e || behaviour

package e2etest

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestModuleDir_Invalid(t *testing.T) {
	_, err := moduleDir("github.com/fullsend-ai/fullsend/not-a-real-module-path")
	require.Error(t, err)
}
