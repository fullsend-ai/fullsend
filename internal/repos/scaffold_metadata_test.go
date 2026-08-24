package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestBuildScaffoldPRMetadata_FreshInstall(t *testing.T) {
	fc := forge.NewFakeClient()
	notInstalled := false
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &notInstalled})

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "adds the fullsend scaffold files")
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_FreshInstallNoOpts(t *testing.T) {
	fc := forge.NewFakeClient()
	// No opts → defaults to fresh install.
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithBothVersions(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents = map[string][]byte{
		"acme/widget/.github/workflows/fullsend.yaml": []byte(
			"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123 # v0.25.2\n"),
	}
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "from v0.25.2 to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithNewVersionOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithNoVersions(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: update fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "chore: update fullsend per-repo installation", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "updates the fullsend scaffold files")
	assert.Equal(t, DefaultScaffoldBranch, meta.Branch)
}

func TestBuildScaffoldPRMetadata_NilGuardDefaultsFresh(t *testing.T) {
	fc := forge.NewFakeClient()
	// GuardInstalled nil → defaults to fresh install.
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{})

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedGuardInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed})

	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedGuardNotInstalled(t *testing.T) {
	fc := forge.NewFakeClient()
	notInstalled := false
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &notInstalled})

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedOldVersion(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed, OldVersion: "v0.25.2"})

	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.CommitMsg)
	assert.Contains(t, meta.PRBody, "from v0.25.2 to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_PreFetchedBothGuardAndVersion(t *testing.T) {
	fc := forge.NewFakeClient()
	installed := true
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &installed, OldVersion: "v0.24.0"})

	assert.Equal(t, "chore: bump fullsend from v0.24.0 to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestDetectExistingVersion(t *testing.T) {
	t.Run("version comment found", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"name: fullsend\non:\n  workflow_dispatch:\njobs:\n  dispatch:\n    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@deadbeef # v0.25.2\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "v0.25.2", v)
	})

	t.Run("no version comment", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"name: fullsend\non:\n  workflow_dispatch:\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "", v)
	})

	t.Run("file not found", func(t *testing.T) {
		fc := forge.NewFakeClient()
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "", v)
	})

	t.Run("prerelease version", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc # v1.0.0-rc.1\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "v1.0.0-rc.1", v)
	})

	t.Run("hyphenated prerelease version", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents = map[string][]byte{
			"acme/widget/.github/workflows/fullsend.yaml": []byte(
				"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc # v1.0.0-alpha-1\n"),
		}
		v := detectExistingVersion(context.Background(), fc, "acme", "widget")
		assert.Equal(t, "v1.0.0-alpha-1", v)
	})
}

func TestRuntimeSection(t *testing.T) {
	t.Parallel()
	def := RuntimeSection("")
	assert.Contains(t, def, "## Runtime")
	assert.Contains(t, def, "run on **claude**")
	assert.Contains(t, RuntimeSection("pi"), "run on **pi**")
	assert.Contains(t, def, "`runtime:` in `.fullsend/config.yaml`")
	assert.Contains(t, def, "fullsend run --runtime")
	assert.True(t, strings.HasPrefix(def, "\n\n"), "section must be appended after the body with a paragraph break")
}
