package binary

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsReleasedVersion(t *testing.T) {
	tests := []struct {
		version  string
		expected bool
	}{
		{"0.4.0", true},
		{"v0.4.0", true},
		{"1.0.0", true},
		{"dev", false},
		{"", false},
		{"0.4.0-3-gabcdef", false},
		{"0.4.0-vendored", false},
		{"0.4.0-crosscompiled", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsReleasedVersion(tt.version), "version=%q", tt.version)
		})
	}
}
