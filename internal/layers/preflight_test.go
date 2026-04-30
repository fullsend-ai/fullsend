package layers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestPreflight_AllScopesPresent(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo", "delete_repo", "workflow"},
	}
	stack := NewStack(
		&mockLayer{name: "a", scopes: map[Operation][]string{OpInstall: {"repo", "workflow"}}},
		&mockLayer{name: "b", scopes: map[Operation][]string{OpInstall: {"repo"}}},
	)

	result, err := stack.Preflight(context.Background(), OpInstall, client)
	require.NoError(t, err)
	assert.True(t, result.OK())
	assert.Empty(t, result.Missing)
}

func TestPreflight_MissingScopes(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo"},
	}
	stack := NewStack(
		&mockLayer{name: "a", scopes: map[Operation][]string{OpUninstall: {"repo", "delete_repo"}}},
	)

	result, err := stack.Preflight(context.Background(), OpUninstall, client)
	require.NoError(t, err)
	assert.False(t, result.OK())
	assert.Equal(t, []string{"delete_repo"}, result.Missing)
	assert.Contains(t, result.Error(), "delete_repo")
	assert.Contains(t, result.Error(), "gh auth refresh")
}

func TestPreflight_NoScopesRequired(t *testing.T) {
	client := &forge.FakeClient{}
	stack := NewStack(
		&mockLayer{name: "a"}, // no scopes for any operation
	)

	result, err := stack.Preflight(context.Background(), OpAnalyze, client)
	require.NoError(t, err)
	assert.True(t, result.OK())
}

func TestPreflight_NilScopes_FineGrainedToken(t *testing.T) {
	// Fine-grained tokens return nil for GetTokenScopes.
	// Preflight should let the operation proceed (we can't validate).
	client := &forge.FakeClient{
		TokenScopes: nil,
	}
	stack := NewStack(
		&mockLayer{name: "a", scopes: map[Operation][]string{OpInstall: {"repo", "workflow"}}},
	)

	result, err := stack.Preflight(context.Background(), OpInstall, client)
	require.NoError(t, err)
	assert.True(t, result.OK(), "should pass when scopes can't be introspected")
	assert.True(t, result.Skipped, "should indicate preflight was skipped")
}

func TestPreflight_GetTokenScopesError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{"GetTokenScopes": errors.New("network error")},
	}
	stack := NewStack(
		&mockLayer{name: "a", scopes: map[Operation][]string{OpInstall: {"repo"}}},
	)

	_, err := stack.Preflight(context.Background(), OpInstall, client)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestPreflight_DeduplicatesScopes(t *testing.T) {
	client := &forge.FakeClient{
		TokenScopes: []string{"repo"},
	}
	// Both layers require "repo" — should only appear once in required.
	stack := NewStack(
		&mockLayer{name: "a", scopes: map[Operation][]string{OpInstall: {"repo"}}},
		&mockLayer{name: "b", scopes: map[Operation][]string{OpInstall: {"repo"}}},
	)

	result, err := stack.Preflight(context.Background(), OpInstall, client)
	require.NoError(t, err)
	assert.True(t, result.OK())
	assert.Equal(t, []string{"repo"}, result.Required)
}

func TestCollectRequiredScopes(t *testing.T) {
	stack := NewStack(
		&mockLayer{name: "a", scopes: map[Operation][]string{OpInstall: {"repo", "workflow"}}},
		&mockLayer{name: "b", scopes: map[Operation][]string{OpInstall: {"repo", "delete_repo"}}},
	)

	scopes := stack.CollectRequiredScopes(OpInstall)
	assert.ElementsMatch(t, []string{"repo", "workflow", "delete_repo"}, scopes)
}

func TestPreflightResult_Error(t *testing.T) {
	r := &PreflightResult{
		Required: []string{"repo", "delete_repo", "workflow"},
		Granted:  []string{"repo"},
		Missing:  []string{"delete_repo", "workflow"},
	}

	msg := r.Error()
	assert.Contains(t, msg, "delete_repo")
	assert.Contains(t, msg, "workflow")
	assert.Contains(t, msg, "gh auth refresh -s delete_repo,workflow")
	// Verify scope descriptions are included.
	assert.Contains(t, msg, "Why each scope is needed")
	assert.Contains(t, msg, "delete the .fullsend config repository")
	assert.Contains(t, msg, "GitHub Actions workflow files")
	// Verify fine-grained PAT guidance is included.
	assert.Contains(t, msg, "fine-grained personal access token")
}
