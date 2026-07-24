package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAgentExplicitlyDisabled_True(t *testing.T) {
	f := false
	agents := []AgentEntry{
		{Name: "retro", Enabled: &f},
		{Source: "harness/code.yaml"},
	}
	assert.True(t, IsAgentExplicitlyDisabled(agents, "retro"))
}

func TestIsAgentExplicitlyDisabled_CaseInsensitive(t *testing.T) {
	f := false
	agents := []AgentEntry{{Name: "Retro", Enabled: &f}}
	assert.True(t, IsAgentExplicitlyDisabled(agents, "retro"))
	assert.True(t, IsAgentExplicitlyDisabled(agents, "RETRO"))
}

func TestIsAgentExplicitlyDisabled_EnabledAgent(t *testing.T) {
	tr := true
	agents := []AgentEntry{{Name: "retro", Source: "harness/retro.yaml", Enabled: &tr}}
	assert.False(t, IsAgentExplicitlyDisabled(agents, "retro"))
}

func TestIsAgentExplicitlyDisabled_NilEnabled(t *testing.T) {
	agents := []AgentEntry{{Name: "retro", Source: "harness/retro.yaml"}}
	assert.False(t, IsAgentExplicitlyDisabled(agents, "retro"))
}

func TestIsAgentExplicitlyDisabled_NotInConfig(t *testing.T) {
	f := false
	agents := []AgentEntry{{Name: "retro", Enabled: &f}}
	assert.False(t, IsAgentExplicitlyDisabled(agents, "triage"))
}

func TestIsAgentExplicitlyDisabled_EmptyConfig(t *testing.T) {
	assert.False(t, IsAgentExplicitlyDisabled(nil, "retro"))
}

func TestIsAgentExplicitlyDisabled_DerivedName(t *testing.T) {
	f := false
	agents := []AgentEntry{{Source: "harness/retro.yaml", Enabled: &f}}
	assert.True(t, IsAgentExplicitlyDisabled(agents, "retro"))
}

func TestIsAgentExplicitlyDisabled_DisableThenEnable(t *testing.T) {
	f := false
	tr := true
	agents := []AgentEntry{
		{Name: "retro", Enabled: &f},
		{Name: "retro", Source: "harness/custom-retro.yaml", Enabled: &tr},
	}
	assert.False(t, IsAgentExplicitlyDisabled(agents, "retro"))
}

func TestIsAgentExplicitlyDisabled_EnableThenDisable(t *testing.T) {
	f := false
	tr := true
	agents := []AgentEntry{
		{Name: "retro", Source: "harness/custom-retro.yaml", Enabled: &tr},
		{Name: "retro", Enabled: &f},
	}
	assert.True(t, IsAgentExplicitlyDisabled(agents, "retro"))
}
