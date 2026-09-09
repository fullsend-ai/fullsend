package behaviourtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuiteOptionsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    SuiteOptions
		wantErr string
	}{
		{
			name: "valid",
			opts: SuiteOptions{FeaturePaths: []string{"features"}, FixturesRoot: "e2e/behaviour"},
		},
		{
			name:    "missing fixtures root",
			opts:    SuiteOptions{FeaturePaths: []string{"features"}},
			wantErr: "FixturesRoot is required",
		},
		{
			name:    "whitespace fixtures root",
			opts:    SuiteOptions{FeaturePaths: []string{"features"}, FixturesRoot: "   "},
			wantErr: "FixturesRoot is required",
		},
		{
			name:    "missing feature paths",
			opts:    SuiteOptions{FixturesRoot: "e2e/behaviour"},
			wantErr: "FeaturePaths is required",
		},
		{
			name:    "empty feature path entry",
			opts:    SuiteOptions{FeaturePaths: []string{"features", ""}, FixturesRoot: "e2e/behaviour"},
			wantErr: "FeaturePaths[1] is empty",
		},
		{
			name:    "whitespace feature path entry",
			opts:    SuiteOptions{FeaturePaths: []string{"  "}, FixturesRoot: "e2e/behaviour"},
			wantErr: "FeaturePaths[0] is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.opts.validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
