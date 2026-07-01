package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeBuilder(name, sha string) (string, error) {
	return fmt.Sprintf("https://example.com/%s/%s.yaml#sha256=aaaa", sha, name), nil
}

func TestMergedAgents_ScaffoldOnly(t *testing.T) {
	result, err := MergedAgents([]string{"code", "triage"}, "abc123def456abc123def456abc123def456abc1", nil, fakeBuilder)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "code", result[0].Name)
	assert.False(t, result[0].IsConfig)
	assert.Equal(t, "triage", result[1].Name)
	assert.False(t, result[1].IsConfig)
}

func TestMergedAgents_ConfigOnly(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/custom.yaml"},
		{Source: "https://example.com/lint.yaml#sha256=bbbb"},
	}
	result, err := MergedAgents(nil, "", agents, nil)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "custom", result[0].Name)
	assert.True(t, result[0].IsConfig)
	assert.Equal(t, "lint", result[1].Name)
	assert.True(t, result[1].IsConfig)
}

func TestMergedAgents_ConfigOverridesScaffold(t *testing.T) {
	agents := []AgentEntry{
		{Source: "https://external.com/triage.yaml#sha256=cccc"},
	}
	result, err := MergedAgents([]string{"code", "triage"}, "abc123def456abc123def456abc123def456abc1", agents, fakeBuilder)
	require.NoError(t, err)
	require.Len(t, result, 2)

	assert.Equal(t, "code", result[0].Name)
	assert.False(t, result[0].IsConfig)

	assert.Equal(t, "triage", result[1].Name)
	assert.True(t, result[1].IsConfig)
	assert.Equal(t, "https://external.com/triage.yaml#sha256=cccc", result[1].Source)
}

func TestMergedAgents_ConfigAppendsNewAgent(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/lint.yaml"},
	}
	result, err := MergedAgents([]string{"code"}, "abc123def456abc123def456abc123def456abc1", agents, fakeBuilder)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "code", result[0].Name)
	assert.Equal(t, "lint", result[1].Name)
	assert.True(t, result[1].IsConfig)
}

func TestMergedAgents_BothEmpty(t *testing.T) {
	result, err := MergedAgents(nil, "", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestMergedAgents_CaseInsensitiveOverride(t *testing.T) {
	agents := []AgentEntry{
		{Name: "Code", Source: "https://example.com/code.yaml#sha256=dddd"},
	}
	result, err := MergedAgents([]string{"code"}, "abc123def456abc123def456abc123def456abc1", agents, fakeBuilder)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "Code", result[0].Name)
	assert.True(t, result[0].IsConfig)
}

func TestMergedAgents_SortedByName(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/zebra.yaml"},
		{Source: "harness/alpha.yaml"},
	}
	result, err := MergedAgents([]string{"middle"}, "abc123def456abc123def456abc123def456abc1", agents, fakeBuilder)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, "alpha", result[0].Name)
	assert.Equal(t, "middle", result[1].Name)
	assert.Equal(t, "zebra", result[2].Name)
}

func TestMergedAgents_NilBuilder(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/custom.yaml"},
	}
	result, err := MergedAgents([]string{"code", "triage"}, "abc123def456abc123def456abc123def456abc1", agents, nil)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "custom", result[0].Name)
}

func TestMergedAgents_EmptyCommitSHA(t *testing.T) {
	agents := []AgentEntry{
		{Source: "harness/custom.yaml"},
	}
	result, err := MergedAgents([]string{"code"}, "", agents, fakeBuilder)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "custom", result[0].Name)
}

func TestMergedAgents_ExplicitName(t *testing.T) {
	agents := []AgentEntry{
		{Name: "my-linter", Source: "harness/lint.yaml"},
	}
	result, err := MergedAgents(nil, "", agents, nil)
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "my-linter", result[0].Name)
	assert.Equal(t, "harness/lint.yaml", result[0].Source)
}

func TestMergedAgents_BuilderError(t *testing.T) {
	failBuilder := func(name, sha string) (string, error) {
		return "", fmt.Errorf("build failed for %s", name)
	}
	_, err := MergedAgents([]string{"code"}, "abc123def456abc123def456abc123def456abc1", nil, failBuilder)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "build failed for code")
}

func TestLookupMergedAgent_Found(t *testing.T) {
	agents := []MergedAgent{
		{Name: "code", Source: "url1"},
		{Name: "triage", Source: "url2"},
	}
	found := LookupMergedAgent(agents, "triage")
	require.NotNil(t, found)
	assert.Equal(t, "triage", found.Name)
}

func TestLookupMergedAgent_CaseInsensitive(t *testing.T) {
	agents := []MergedAgent{
		{Name: "Code", Source: "url1"},
	}
	found := LookupMergedAgent(agents, "code")
	require.NotNil(t, found)
	assert.Equal(t, "Code", found.Name)
}

func TestLookupMergedAgent_NotFound(t *testing.T) {
	agents := []MergedAgent{
		{Name: "code", Source: "url1"},
	}
	found := LookupMergedAgent(agents, "missing")
	assert.Nil(t, found)
}
