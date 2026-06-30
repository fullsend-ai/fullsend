//go:build e2e

package admin

import (
	"os"
	"testing"
)

func TestBuildCLI(t *testing.T) {
	binary := buildCLIBinary(t)
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary not found at %s: %v", binary, err)
	}
}
