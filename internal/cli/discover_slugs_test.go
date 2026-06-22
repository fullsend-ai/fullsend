package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestDiscoverAgentSlugs_HarnessFirst(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents = map[string][]forge.DirectoryEntry{
		"acme/.fullsend/harness@main": {
			{Path: "harness/triage.yaml", Type: "file"},
			{Path: "harness/coder.yaml", Type: "file"},
		},
	}
	client.FileContentsRef = map[string][]byte{
		"acme/.fullsend/harness/triage.yaml@main": []byte("role: triage\nslug: acme-triage\n"),
		"acme/.fullsend/harness/coder.yaml@main":  []byte("role: coder\nslug: acme-coder\n"),
	}

	cfg := &config.OrgConfig{
		Agents: []config.AgentEntry{
			{Role: "triage", Slug: "old-triage"},
		},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", cfg, printer)

	require.Len(t, slugs, 2)
	assert.Contains(t, slugs, "acme-triage")
	assert.Contains(t, slugs, "acme-coder")
	assert.NotContains(t, buf.String(), "agents: block")
}

func TestDiscoverAgentSlugs_FallsBackToAgentsBlock(t *testing.T) {
	client := forge.NewFakeClient()

	cfg := &config.OrgConfig{
		Agents: []config.AgentEntry{
			{Role: "triage", Slug: "acme-triage"},
			{Role: "coder", Slug: "acme-coder"},
		},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", cfg, printer)

	require.Len(t, slugs, 2)
	assert.Contains(t, slugs, "acme-triage")
	assert.Contains(t, slugs, "acme-coder")
	assert.Contains(t, buf.String(), "agents: block")
}

func TestDiscoverAgentSlugs_HarnessWithoutSlug_DerivesFromRole(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents = map[string][]forge.DirectoryEntry{
		"acme/.fullsend/harness@main": {
			{Path: "harness/triage.yaml", Type: "file"},
		},
	}
	client.FileContentsRef = map[string][]byte{
		"acme/.fullsend/harness/triage.yaml@main": []byte("role: triage\n"),
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", nil, printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "fullsend-ai-triage", slugs[0])
	assert.NotContains(t, buf.String(), "agents: block")
}

func TestDiscoverAgentSlugs_ConfigAgentWithoutSlug_DerivesFromRole(t *testing.T) {
	client := forge.NewFakeClient()

	cfg := &config.OrgConfig{
		Agents: []config.AgentEntry{
			{Role: "triage"},
		},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", cfg, printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "fullsend-ai-triage", slugs[0])
	assert.Contains(t, buf.String(), "agents: block")
}

func TestDiscoverAgentSlugs_NeitherSource_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", nil, printer)

	assert.Nil(t, slugs)
	assert.NotContains(t, buf.String(), "agents: block")
}

func TestDiscoverAgentSlugs_DeduplicatesSlugs(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents = map[string][]forge.DirectoryEntry{
		"acme/.fullsend/harness@main": {
			{Path: "harness/coder.yaml", Type: "file"},
			{Path: "harness/fix.yaml", Type: "file"},
		},
	}
	client.FileContentsRef = map[string][]byte{
		"acme/.fullsend/harness/coder.yaml@main": []byte("role: coder\nslug: acme-coder\n"),
		"acme/.fullsend/harness/fix.yaml@main":   []byte("role: fix\nslug: acme-coder\n"),
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", nil, printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "acme-coder", slugs[0])
}

func TestDiscoverAgentSlugs_EmptyAgentsBlock_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()

	cfg := &config.OrgConfig{
		Agents: []config.AgentEntry{},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", cfg, printer)

	assert.Nil(t, slugs)
	assert.NotContains(t, buf.String(), "agents: block")
}

func TestDiscoverAgentSlugs_PartialError_UsesValidAgents(t *testing.T) {
	client := forge.NewFakeClient()
	client.DirContents = map[string][]forge.DirectoryEntry{
		"acme/.fullsend/harness@main": {
			{Path: "harness/triage.yaml", Type: "file"},
			{Path: "harness/broken.yaml", Type: "file"},
		},
	}
	client.FileContentsRef = map[string][]byte{
		"acme/.fullsend/harness/triage.yaml@main": []byte("role: triage\nslug: acme-triage\n"),
		"acme/.fullsend/harness/broken.yaml@main": []byte("invalid: [yaml"),
	}

	cfg := &config.OrgConfig{
		Agents: []config.AgentEntry{
			{Role: "triage", Slug: "old-triage"},
		},
	}

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", cfg, printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "acme-triage", slugs[0])
	assert.Contains(t, buf.String(), "some harness files could not be read")
	assert.NotContains(t, buf.String(), "agents: block")
}
