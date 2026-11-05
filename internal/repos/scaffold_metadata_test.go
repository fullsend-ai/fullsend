package repos

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestUninstallPRMetadata(t *testing.T) {
	meta := UninstallPRMetadata()

	assert.Equal(t, "chore: remove fullsend workflow", meta.CommitMsg)
	assert.Equal(t, "chore: remove fullsend workflow", meta.PRTitle)
	assert.Contains(t, meta.PRBody, "removes the fullsend scaffold files")
	assert.Contains(t, meta.PRBody, "variables")
	// Reuses DefaultScaffoldBranch (not a distinct uninstall branch) so the
	// already-deployed per-repo shim exclusion (head.ref ==
	// "fullsend/scaffold-install") also covers uninstall PRs.
	assert.Equal(t, DefaultScaffoldBranch, meta.Branch)
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

// commandsNotInPerRepoCatalog lists commands reusable-dispatch.yml routes but
// that are deliberately omitted from the user-facing per-repo onboarding
// catalog.
var commandsNotInPerRepoCatalog = map[string]bool{}

// Route extraction is scoped to reusable-dispatch.yml's case-arm labels and
// catalog extraction to rendered "- `cmd`" bullets, so a command mentioned in a
// comment, URL, or prose on either side cannot spoof a match.
var (
	// dispatchCaseArmRE matches a case-arm label in reusable-dispatch.yml's
	// `case "${COMMAND}"` switch, e.g. "/fs-triage)".
	dispatchCaseArmRE = regexp.MustCompile(`(?m)^[ \t]*(/fs-[a-z0-9-]+(?:\|/fs-[a-z0-9-]+)*)\)`)
	// slashCommandRE matches a single /fs-* command token.
	slashCommandRE = regexp.MustCompile(`/fs-[a-z0-9-]+`)
	// catalogBulletRE matches a rendered onboarding-catalog bullet.
	catalogBulletRE = regexp.MustCompile("(?m)^- `(/fs-[a-z0-9-]+)`")
)

// routedDispatchCommands returns the set of slash commands reusable-dispatch.yml
// routes on, scoped to case-arm labels.
func routedDispatchCommands(dispatchStr string) map[string]bool {
	cmds := map[string]bool{}
	for _, arm := range dispatchCaseArmRE.FindAllStringSubmatch(dispatchStr, -1) {
		for _, cmd := range slashCommandRE.FindAllString(arm[1], -1) {
			cmds[cmd] = true
		}
	}
	return cmds
}

// catalogCommands returns the set of slash commands documented as bullets in an
// onboarding catalog block.
func catalogCommands(catalog string) map[string]bool {
	cmds := map[string]bool{}
	for _, m := range catalogBulletRE.FindAllStringSubmatch(catalog, -1) {
		cmds[m[1]] = true
	}
	return cmds
}

// TestPerRepoOnboardingCatalog guards the per-repo install PR body's
// slash-command catalog against drift from reusable-dispatch.yml's routing, in
// both directions.
func TestPerRepoOnboardingCatalog(t *testing.T) {
	dispatch, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)

	dispatchCmds := routedDispatchCommands(string(dispatch))
	require.NotEmpty(t, dispatchCmds, "expected dispatch.yml to route on /fs-* commands")

	catalogCmds := catalogCommands(gettingStartedCatalog)
	require.NotEmpty(t, catalogCmds, "expected the per-repo catalog to document /fs-* commands")

	// Forward: dispatch.yml commands must be documented (unless deliberately omitted).
	for cmd := range dispatchCmds {
		if commandsNotInPerRepoCatalog[cmd] {
			continue
		}
		assert.True(t, catalogCmds[cmd],
			"dispatch.yml routes on %s but the per-repo onboarding catalog does not document it "+
				"(add it to gettingStartedCatalog, or to commandsNotInPerRepoCatalog if intentional)", cmd)
	}

	// Reverse: every documented command must be routed by dispatch.yml.
	for cmd := range catalogCmds {
		assert.True(t, dispatchCmds[cmd],
			"per-repo onboarding catalog documents %s but dispatch.yml does not route on it", cmd)
	}
}

// TestFreshInstallBodyIncludesCatalog verifies the fresh-install PR body carries
// the Getting started catalog, so dropping the append is caught by CI.
func TestFreshInstallBodyIncludesCatalog(t *testing.T) {
	fc := forge.NewFakeClient()
	notInstalled := false
	meta := BuildScaffoldPRMetadata(context.Background(), fc, "acme", "widget", "v0.28.0",
		ScaffoldMetadataOpts{GuardInstalled: &notInstalled})
	assert.Contains(t, meta.PRBody, "## Getting started")
	assert.Contains(t, meta.PRBody, "`/fs-triage`")
}

func TestRuntimeSection(t *testing.T) {
	t.Parallel()
	def := RuntimeSection("")
	assert.Contains(t, def, "## Runtime")
	assert.Contains(t, def, "run on **claude**")
	assert.Contains(t, RuntimeSection("pi"), "run on **pi**")
	assert.Contains(t, def, "`runtime:` in `.fullsend/config.yaml`")
	assert.Contains(t, def, "fullsend run --runtime")
	assert.Contains(t, def, "`agents:` entry")
	assert.True(t, strings.HasPrefix(def, "\n\n"), "section must be appended after the body with a paragraph break")
}
