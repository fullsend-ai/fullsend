package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", printer)

	require.Len(t, slugs, 2)
	assert.Contains(t, slugs, "acme-triage")
	assert.Contains(t, slugs, "acme-coder")
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

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "fullsend-ai-triage", slugs[0])
}

func TestDiscoverAgentSlugs_NeitherSource_ReturnsNil(t *testing.T) {
	client := forge.NewFakeClient()

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", printer)

	assert.Nil(t, slugs)
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

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "acme-coder", slugs[0])
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

	var buf strings.Builder
	printer := ui.New(&buf)

	slugs := discoverAgentSlugs(context.Background(), client, "acme", ".fullsend", "main", "fullsend-ai", printer)

	require.Len(t, slugs, 1)
	assert.Equal(t, "acme-triage", slugs[0])
	assert.Contains(t, buf.String(), "some harness files could not be read")
}
