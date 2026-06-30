package harness

import (
	"context"
	"fmt"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverRemoteAgents(t *testing.T) {
	ctx := context.Background()
	const (
		owner = "acme"
		repo  = ".fullsend"
		ref   = "main"
	)

	t.Run("multiple harnesses sorted by role", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "triage.yaml", Type: "file"},
			{Path: "code.yaml", Type: "file"},
			{Path: "review.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/triage.yaml@%s", owner, repo, ref)] = []byte("agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/code.yaml@%s", owner, repo, ref)] = []byte("agent: agents/code.md\nrole: coder\nslug: fs-coder\n")
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/review.yaml@%s", owner, repo, ref)] = []byte("agent: agents/review.md\nrole: review\nslug: fs-review\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 3)

		assert.Equal(t, "coder", agents[0].Role)
		assert.Equal(t, "fs-coder", agents[0].Slug)
		assert.Equal(t, "code.yaml", agents[0].Filename)

		assert.Equal(t, "review", agents[1].Role)
		assert.Equal(t, "triage", agents[2].Role)
	})

	t.Run("no harness directory returns nil nil", func(t *testing.T) {
		fc := forge.NewFakeClient()

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		assert.Nil(t, agents)
	})

	t.Run("skips files without role or slug", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "legacy.yaml", Type: "file"},
			{Path: "modern.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/legacy.yaml@%s", owner, repo, ref)] = []byte("agent: agents/legacy.md\n")
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/modern.yaml@%s", owner, repo, ref)] = []byte("agent: agents/modern.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
	})

	t.Run("role only without slug is included", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "partial.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/partial.yaml@%s", owner, repo, ref)] = []byte("agent: agents/partial.md\nrole: triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
		assert.Empty(t, agents[0].Slug)
	})

	t.Run("slug only without role is included", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "slug-only.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/slug-only.yaml@%s", owner, repo, ref)] = []byte("agent: agents/slug.md\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "fs-triage", agents[0].Slug)
		assert.Empty(t, agents[0].Role)
	})

	t.Run("malformed YAML returns multi-error with valid files", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "good.yaml", Type: "file"},
			{Path: "bad.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/good.yaml@%s", owner, repo, ref)] = []byte("agent: agents/good.md\nrole: triage\nslug: fs-triage\n")
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/bad.yaml@%s", owner, repo, ref)] = []byte(":\n  :\n    - [invalid yaml")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad.yaml")
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
	})

	t.Run("GetFileContentAtRef failure for one file returns multi-error", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "good.yaml", Type: "file"},
			{Path: "missing.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/good.yaml@%s", owner, repo, ref)] = []byte("agent: agents/good.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing.yaml")
		require.Len(t, agents, 1)
		assert.Equal(t, "triage", agents[0].Role)
	})

	t.Run("empty harness directory returns empty list", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{}

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		assert.Empty(t, agents)
	})

	t.Run("yml extension is discovered", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "agent.yml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/agent.yml@%s", owner, repo, ref)] = []byte("agent: agents/agent.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "agent.yml", agents[0].Filename)
	})

	t.Run("skips subdirectories", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "triage.yaml", Type: "file"},
			{Path: "subdir", Type: "dir"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/triage.yaml@%s", owner, repo, ref)] = []byte("agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
	})

	t.Run("skips non-YAML files", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "triage.yaml", Type: "file"},
			{Path: "readme.md", Type: "file"},
			{Path: "notes.txt", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/triage.yaml@%s", owner, repo, ref)] = []byte("agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
	})

	t.Run("same role sorted by filename", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "fix.yaml", Type: "file"},
			{Path: "code.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/fix.yaml@%s", owner, repo, ref)] = []byte("agent: agents/fix.md\nrole: coder\nslug: fs-coder\n")
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/code.yaml@%s", owner, repo, ref)] = []byte("agent: agents/code.md\nrole: coder\nslug: fs-coder-2\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 2)
		assert.Equal(t, "code.yaml", agents[0].Filename)
		assert.Equal(t, "fix.yaml", agents[1].Filename)
	})

	t.Run("path field is empty for remote agents", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "triage.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/triage.yaml@%s", owner, repo, ref)] = []byte("agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Empty(t, agents[0].Path)
	})

	t.Run("path prefix in entry is stripped to bare filename", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.DirContents[fmt.Sprintf("%s/%s/harness@%s", owner, repo, ref)] = []forge.DirectoryEntry{
			{Path: "harness/triage.yaml", Type: "file"},
		}
		fc.FileContentsRef[fmt.Sprintf("%s/%s/harness/triage.yaml@%s", owner, repo, ref)] = []byte("agent: agents/triage.md\nrole: triage\nslug: fs-triage\n")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		assert.Equal(t, "triage.yaml", agents[0].Filename)
	})

	t.Run("ListDirectoryContents error propagates", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Errors["ListDirectoryContents"] = fmt.Errorf("network error")

		agents, err := DiscoverRemoteAgents(ctx, fc, owner, repo, ref)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "listing harness directory")
		assert.Nil(t, agents)
	})
}
