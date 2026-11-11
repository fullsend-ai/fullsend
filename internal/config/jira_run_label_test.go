package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestJiraRunLabel(t *testing.T) {
	for _, label := range []string{"", "ai-coding-agent", "contains space", "tab\tlabel", "line\nlabel", "control\x00", "unicode\u00a0space"} {
		t.Run(label, func(t *testing.T) {
			j := &JiraTrackingConfig{BaseURL: "https://example.atlassian.net", Project: "PROJ", RunLabel: label}
			cfg := &TrackingConfig{Jira: j}
			if label == "" || label == "ai-coding-agent" {
				require.NoError(t, validateTracking(cfg))
				require.NoError(t, j.ValidateRunLabel())
			} else {
				require.ErrorContains(t, validateTracking(cfg), "run_label")
				require.ErrorContains(t, j.ValidateRunLabel(), "run_label")
			}
			require.NoError(t, j.ValidateConnection(), "optional label must not block connection consumers")
			require.Equal(t, label, cloneTracking(cfg).Jira.RunLabel)
		})
	}
	var cfg TrackingConfig
	require.NoError(t, yaml.Unmarshal([]byte("jira:\n  base_url: https://example.atlassian.net\n  project: PROJ\n  run_label: ai-coding-agent\n"), &cfg))
	require.Equal(t, "ai-coding-agent", cfg.Jira.RunLabel)
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.Contains(t, string(data), "run_label: ai-coding-agent")
	require.ErrorContains(t, (&JiraTrackingConfig{RunLabel: "ai-coding-agent"}).ValidateConnection(), "base_url is required")
}

func TestValidateRunLabel_Nil(t *testing.T) {
	var cfg *JiraTrackingConfig
	require.NoError(t, cfg.ValidateRunLabel())
}
