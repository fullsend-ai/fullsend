package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestBuildScaffoldPRMetadata_FreshInstall(t *testing.T) {
	fc := forge.NewFakeClient()
	// No guard variable → fresh install.
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "adds the fullsend scaffold files")
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithBothVersions(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/widget/"+forge.PerRepoGuardVar] = "true"
	fc.FileContents = map[string][]byte{
		"acme/widget/.github/workflows/fullsend.yaml": []byte(
			"uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123 # v0.25.2\n"),
	}

	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "chore: bump fullsend from v0.25.2 to v0.28.0", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "from v0.25.2 to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithNewVersionOnly(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/widget/"+forge.PerRepoGuardVar] = "true"
	// No workflow file → can't detect old version.

	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.CommitMsg)
	assert.Equal(t, "chore: bump fullsend to v0.28.0", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "to v0.28.0")
	assert.Equal(t, "fullsend/bump-v0.28.0", meta.Branch)
}

func TestBuildScaffoldPRMetadata_UpgradeWithNoVersions(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/widget/"+forge.PerRepoGuardVar] = "true"

	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "")

	assert.Equal(t, "chore: update fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "chore: update fullsend per-repo installation", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "updates the fullsend scaffold files")
	assert.Equal(t, DefaultScaffoldBranch, meta.Branch)
}

func TestBuildScaffoldPRMetadata_GuardVariableFalse(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["acme/widget/"+forge.PerRepoGuardVar] = "false"

	// Guard set to "false" should be treated as a fresh install (re-enable).
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
}

func TestBuildScaffoldPRMetadata_GuardCheckError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetRepoVariable"] = assert.AnError

	// Error checking guard → treated as fresh install (fail open for metadata).
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0")

	assert.Equal(t, "chore: initialize fullsend per-repo installation", meta.CommitMsg)
	assert.Equal(t, "fullsend/scaffold-install", meta.Branch)
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
}
