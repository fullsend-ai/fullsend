package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReconcileStatusCmd_RequiredFlags(t *testing.T) {
	cmd := newReconcileStatusCmd()

	for _, name := range []string{"repo", "number", "run-id"} {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "flag %q should exist", name)
	}
}

func TestNewReconcileStatusCmd_ReasonFlagDefault(t *testing.T) {
	cmd := newReconcileStatusCmd()

	reason := cmd.Flags().Lookup("reason")
	require.NotNil(t, reason)
	assert.Equal(t, "terminated", reason.DefValue)
}

func TestNewReconcileStatusCmd_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing token",
			args:    []string{"--repo", "org/repo", "--number", "7", "--run-id", "run-1"},
			wantErr: "--token or GITHUB_TOKEN required",
		},
		{
			name:    "invalid number",
			args:    []string{"--repo", "org/repo", "--number", "0", "--run-id", "run-1", "--token", "tok"},
			wantErr: "--number must be a positive integer",
		},
		{
			name:    "invalid repo format",
			args:    []string{"--repo", "noslash", "--number", "7", "--run-id", "run-1", "--token", "tok"},
			wantErr: "--repo must be in owner/repo format",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newReconcileStatusCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
