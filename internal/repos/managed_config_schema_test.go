package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	schemaTestLegacySHA  = "1111111111111111111111111111111111111111"
	schemaTestCurrentSHA = "2222222222222222222222222222222222222222"
)

func schemaDeclKey(sha string) string {
	return shimOwner + "/" + shimRepo + "/" + config.ManagedConfigSchemaDeclarationPath + "@" + sha
}

func schemaAncestryKey(sha string) string {
	return shimOwner + "/" + shimRepo + "/" + trackerMappingCommit + "/" + sha
}

// schemaTestClient returns a fake upstream client where v0.1.0 is a legacy
// release predating the schema declaration and the tracker mapping, and
// v0.9.0 declares the tracker-mapping schema.
func schemaTestClient() *forge.FakeClient {
	fc := forge.NewFakeClient()
	fc.FileContentsAnyRef = nil
	fc.Refs[shimOwner+"/"+shimRepo+"/tags/v0.1.0"] = schemaTestLegacySHA
	fc.Refs[shimOwner+"/"+shimRepo+"/tags/v0.9.0"] = schemaTestCurrentSHA
	fc.CommitAncestry = map[string]string{schemaAncestryKey(schemaTestLegacySHA): "behind"}
	fc.FileContentsRef[schemaDeclKey(schemaTestCurrentSHA)] = []byte("# version\n2\n")
	return fc
}

func TestResolveManagedConfigSchema(t *testing.T) {
	ctx := context.Background()

	t.Run("unpinned uses binary schema", func(t *testing.T) {
		for _, ref := range []string{"", "v1.0.0", "abc1234"} {
			s := resolveManagedConfigSchema(ctx, ref, "abc1234", "v1.0.0", nil)
			assert.Equal(t, managedConfigSchema{}, s, ref)
			assert.Equal(t, config.ManagedConfigSchemaVersion, s.version())
		}
	})

	t.Run("legacy ref without declaration", func(t *testing.T) {
		s := resolveManagedConfigSchema(ctx, "v0.1.0", "", "", NewRefResolver(schemaTestClient()))
		assert.Equal(t, managedConfigSchema{Ref: "v0.1.0", Version: config.ManagedConfigSchemaLegacy}, s)
	})

	t.Run("undeclared ref containing tracker mapping", func(t *testing.T) {
		fc := schemaTestClient()
		fc.CommitAncestry[schemaAncestryKey(schemaTestCurrentSHA)] = "ahead"
		delete(fc.FileContentsRef, schemaDeclKey(schemaTestCurrentSHA))
		s := resolveManagedConfigSchema(ctx, "v0.9.0", "", "", NewRefResolver(fc))
		assert.Equal(t, config.ManagedConfigSchemaTrackerMapping, s.Version)
		assert.Empty(t, s.Unknown)
	})

	t.Run("declared schema", func(t *testing.T) {
		fc := schemaTestClient()
		s := resolveManagedConfigSchema(ctx, "v0.9.0", "", "", NewRefResolver(fc))
		assert.Equal(t, managedConfigSchema{Ref: "v0.9.0", Version: config.ManagedConfigSchemaTrackerMapping}, s)

		fc.FileContentsRef[schemaDeclKey(schemaTestCurrentSHA)] = []byte("3\n")
		s = resolveManagedConfigSchema(ctx, "v0.9.0", "", "", NewRefResolver(fc))
		assert.Equal(t, managedConfigSchema{Ref: "v0.9.0", Version: config.ManagedConfigSchemaVersion}, s)
	})

	unknown := map[string]struct {
		ref    string
		setup  func(fc *forge.FakeClient)
		reason string
		nilRes bool
	}{
		"no resolver":        {ref: "v0.1.0", nilRes: true, reason: "upstream GitHub client"},
		"unresolvable ref":   {ref: "v9.9.9", reason: "cannot be resolved or compared"},
		"newer schema":       {ref: "v0.9.0", setup: func(fc *forge.FakeClient) { fc.FileContentsRef[schemaDeclKey(schemaTestCurrentSHA)] = []byte("99\n") }, reason: "does not know"},
		"malformed decl":     {ref: "v0.9.0", setup: func(fc *forge.FakeClient) { fc.FileContentsRef[schemaDeclKey(schemaTestCurrentSHA)] = []byte("x\n") }, reason: "not a positive integer"},
		"decl read error":    {ref: "v0.9.0", setup: func(fc *forge.FakeClient) { fc.Errors = map[string]error{"GetFileContentAtRef": errors.New("boom")} }, reason: "boom"},
		"ancestry undecided": {ref: "v0.1.0", setup: func(fc *forge.FakeClient) { fc.CommitAncestry = nil }, reason: "cannot be resolved or compared"},
	}
	for name, tt := range unknown {
		t.Run(name, func(t *testing.T) {
			fc := schemaTestClient()
			if tt.setup != nil {
				tt.setup(fc)
			}
			resolver := NewRefResolver(fc)
			if tt.nilRes {
				resolver = nil
			}
			s := resolveManagedConfigSchema(ctx, tt.ref, "", "", resolver)
			assert.Equal(t, tt.ref, s.Ref)
			assert.Contains(t, s.Unknown, tt.reason)
			require.Error(t, s.unknownError())
		})
	}
}

// TestFakeClientDeclaresCurrentSchema keeps the fake upstream's default
// schema declaration in step with the binary's.
func TestFakeClientDeclaresCurrentSchema(t *testing.T) {
	fc := forge.NewFakeClient()
	data, err := fc.GetFileContentAtRef(context.Background(), shimOwner, shimRepo, config.ManagedConfigSchemaDeclarationPath, "any")
	require.NoError(t, err)
	v, err := config.ParseManagedConfigSchemaDeclaration(data)
	require.NoError(t, err)
	assert.Equal(t, config.ManagedConfigSchemaVersion, v)
}

func TestSelectManagedConfigSchema(t *testing.T) {
	ctx := context.Background()
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	cfg, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	cfg.FullsendRef = "v0.1.0"

	// No schema-dependent keys: no lookup, even without a resolver.
	s := selectManagedConfigSchema(ctx, cfg, nil, "", "", nil)
	assert.Equal(t, managedConfigSchema{Ref: "v0.1.0"}, s)

	// An installed overlay with the tracker mapping needs the ref's schema.
	s = selectManagedConfigSchema(ctx, cfg, []byte("tracker:\n  github: {}\n"), "", "", NewRefResolver(schemaTestClient()))
	assert.Equal(t, config.ManagedConfigSchemaLegacy, s.Version)

	// Unpinned follows the running release.
	cfg.FullsendRef = "v1.0.0"
	assert.Equal(t, managedConfigSchema{}, selectManagedConfigSchema(ctx, cfg, nil, "abc", "v1.0.0", nil))

	// The desired body with a tracker needs the ref's schema.
	tm := trackerManifest(t)
	tcfg, ok := tm.ResolveConfig("acme", "api")
	require.True(t, ok)
	tcfg.FullsendRef = "v0.1.0"
	s = selectManagedConfigSchema(ctx, tcfg, nil, "", "", nil)
	assert.Contains(t, s.Unknown, "upstream GitHub client")

	// So does a harness: block, in the desired body or in the installed overlay.
	bcfg, ok := blockManifest(t).ResolveConfig("acme", "api")
	require.True(t, ok)
	bcfg.FullsendRef = "v0.9.0"
	s = selectManagedConfigSchema(ctx, bcfg, nil, "", "", NewRefResolver(schemaTestClient()))
	assert.Equal(t, config.ManagedConfigSchemaTrackerMapping, s.Version)
	cfg.FullsendRef = "v0.9.0"
	s = selectManagedConfigSchema(ctx, cfg, []byte("agents:\n  - name: review\n    harness: { timeout_minutes: 30 }\n"), "", "", NewRefResolver(schemaTestClient()))
	assert.Equal(t, config.ManagedConfigSchemaTrackerMapping, s.Version)
}

func blockManifest(t *testing.T) *Manifest {
	t.Helper()
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "agents:\n  - name: review\n    harness:\n      timeout_minutes: 30\n")
	return m
}

func trackerManifest(t *testing.T) *Manifest {
	t.Helper()
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "tracker:\n  github:\n    allowed_targets:\n      orgs: [acme]\n")
	return m
}

func TestDesiredManagedConfig_RendersRefSchema(t *testing.T) {
	m := trackerManifest(t)
	cfg, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)

	current, _, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(current), "tracker:\n    github:")
	assert.NotContains(t, string(current), "create_issues")

	cfg.ConfigSchema = managedConfigSchema{Ref: "v0.1.0", Version: config.ManagedConfigSchemaLegacy}
	legacy, _, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	assert.True(t, hasManagedConfigMarker(legacy), "the ownership marker is unchanged")
	assert.Contains(t, string(legacy), "tracker: github\ncreate_issues:")
	require.NoError(t, config.CheckManagedConfigSchemaCompat(legacy, config.ManagedConfigSchemaLegacy))

	cfg.ConfigSchema = managedConfigSchema{Ref: "v9.9.9", Unknown: "the ref cannot be resolved"}
	_, managed, err := desiredManagedConfig(cfg)
	assert.True(t, managed)
	require.ErrorContains(t, err, `fullsend_ref "v9.9.9" is unknown`)
}

func TestDesiredManagedConfig_LegacyUnsupportedSetting(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "tracker:\n  gitlab:\n    allowed_targets:\n      groups: [g]\n")
	cfg, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	cfg.ConfigSchema = managedConfigSchema{Ref: "v0.1.0", Version: config.ManagedConfigSchemaLegacy}
	_, _, err := desiredManagedConfig(cfg)
	require.ErrorContains(t, err, "legacy configuration schema")
}

// A harness: block is rendered for a ref that reads it and refused for an older one.
func TestDesiredManagedConfig_HarnessBlockNeedsCurrentSchema(t *testing.T) {
	cfg, ok := blockManifest(t).ResolveConfig("acme", "api")
	require.True(t, ok)

	current, _, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(current), "timeout_minutes: 30")

	cfg.ConfigSchema = managedConfigSchema{Ref: "v0.9.0", Version: config.ManagedConfigSchemaTrackerMapping}
	_, managed, err := desiredManagedConfig(cfg)
	assert.True(t, managed)
	require.ErrorContains(t, err, `fullsend_ref "v0.9.0": agents entry "review": a harness: block cannot be expressed`)
}

func TestCheckManagedConfigDrift_RefSchema(t *testing.T) {
	ctx := context.Background()
	m := trackerManifest(t)
	overlayKey := "acme/api/" + preset.OverlayPath

	legacyCfg := func(fc *forge.FakeClient) ResolvedConfig {
		cfg := managedResolved(t, fc, m, "acme", "api")
		cfg.ConfigSchema = managedConfigSchema{Ref: "v0.1.0", Version: config.ManagedConfigSchemaLegacy}
		return cfg
	}

	t.Run("legacy-shaped file at legacy ref is current and compatible", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := legacyCfg(fc)
		desired, _, err := desiredManagedConfig(cfg)
		require.NoError(t, err)
		fc.FileContents[overlayKey] = desired
		status := RepoStatus{Installed: true}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Empty(t, status.Error)
		assert.Empty(t, status.Drifts)
		assert.Equal(t, ConfigCompatible, status.ConfigCompatibility)
		assert.Equal(t, "v0.1.0", status.ConfigSchemaRef)
	})

	t.Run("mapping-shaped file at legacy ref is incompatible", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := legacyCfg(fc)
		current := cfg
		current.ConfigSchema = managedConfigSchema{}
		mapped, _, err := desiredManagedConfig(current)
		require.NoError(t, err)
		fc.FileContents[overlayKey] = mapped
		status := RepoStatus{Installed: true}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Empty(t, status.Error)
		assert.Equal(t, ConfigIncompatible, status.ConfigCompatibility)
		assert.Contains(t, status.ConfigCompatibilityReason, "tracker mapping")
		require.Len(t, status.Drifts, 1)
		assert.Equal(t, preset.OverlayPath, status.Drifts[0].Field)
	})

	t.Run("installed harness: block at an older ref is incompatible", func(t *testing.T) {
		fc := forge.NewFakeClient()
		installed, _, err := desiredManagedConfig(managedResolved(t, fc, blockManifest(t), "acme", "api"))
		require.NoError(t, err)
		fc.FileContents[overlayKey] = installed
		cfg := legacyCfg(fc)
		cfg.ConfigSchema = managedConfigSchema{Ref: "v0.9.0", Version: config.ManagedConfigSchemaTrackerMapping}
		status := RepoStatus{Installed: true}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Empty(t, status.Error)
		assert.Equal(t, ConfigIncompatible, status.ConfigCompatibility)
		assert.Contains(t, status.ConfigCompatibilityReason, "harness: block")
	})

	t.Run("desired harness: block at an older ref is an error naming the entry", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := managedResolved(t, fc, blockManifest(t), "acme", "api")
		cfg.ConfigSchema = managedConfigSchema{Ref: "v0.9.0", Version: config.ManagedConfigSchemaTrackerMapping}
		status := RepoStatus{Installed: true}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Contains(t, status.Error, `agents entry "review": a harness: block cannot be expressed`)
	})

	t.Run("unknown schema is reported, file not judged current", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := managedResolved(t, fc, m, "acme", "api")
		desired, _, err := desiredManagedConfig(cfg)
		require.NoError(t, err)
		fc.FileContents[overlayKey] = desired
		cfg.ConfigSchema = managedConfigSchema{Ref: "v9.9.9", Unknown: "the ref cannot be resolved"}
		status := RepoStatus{Installed: true}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Empty(t, status.Error)
		assert.Equal(t, ConfigUnknown, status.ConfigCompatibility)
		assert.Equal(t, "v9.9.9", status.ConfigSchemaRef)
		assert.Equal(t, "the ref cannot be resolved", status.ConfigCompatibilityReason)
		require.Len(t, status.Drifts, 1)
		assert.Contains(t, status.Drifts[0].Actual, "compatibility unknown")
	})

	t.Run("unknown schema before install without overlay reports nothing", func(t *testing.T) {
		fc := forge.NewFakeClient()
		cfg := managedResolved(t, fc, m, "acme", "api")
		cfg.ConfigSchema = managedConfigSchema{Ref: "v9.9.9", Unknown: "x"}
		status := RepoStatus{}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Empty(t, status.Drifts)
		assert.Empty(t, status.ConfigCompatibility)
	})

	t.Run("unknown schema before install read error", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.Errors = map[string]error{"GetFileContent": errors.New("boom")}
		cfg := managedResolved(t, fc, m, "acme", "api")
		cfg.ConfigSchema = managedConfigSchema{Ref: "v9.9.9", Unknown: "x"}
		status := RepoStatus{}
		checkManagedConfigDrift(ctx, cfg, &status)
		assert.Contains(t, status.Error, "boom")
	})
}

func TestConvergeManagedConfigFiles_LegacyRefRewritesMappingFile(t *testing.T) {
	ctx := context.Background()
	m := trackerManifest(t)
	fc := forge.NewFakeClient()
	cfg := managedResolved(t, fc, m, "acme", "api")
	mapped, _, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	fc.FileContents["acme/api/"+preset.OverlayPath] = mapped

	cfg.ConfigSchema = managedConfigSchema{Ref: "v0.1.0", Version: config.ManagedConfigSchemaLegacy}
	legacy, _, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	files, actions := convergeManagedConfigFiles(ctx, cfg, legacy, false, func(string, string, string) {})
	require.Len(t, files, 1, "actions: %+v", actions)
	assert.Equal(t, legacy, files[0].Content)
	assert.True(t, hasManagedConfigMarker(files[0].Content))
}

// A schema-neutral desired configuration still resolves the pinned ref's
// schema when the installed overlay carries a schema-dependent key, so
// install fails before any write when that schema is unknown, as status
// reports compatibility unknown for the same repository.
func TestConverge_SchemaNeutralDesiredWithTrackerOverlayResolvesSchema(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.GitHub.FullsendRef = "v2.0.0"
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))

	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v2.0.0", "https://mint.example.com")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("tracker:\n  github: {}\n")
	// The pinned ref's schema declaration cannot be read.
	fc.Errors = map[string]error{"GetFileContentAtRef": errors.New("upstream unavailable")}

	committed := false
	commitFn := func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
		committed = true
		return nil
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), commitFn, noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Error(t, result.Results[0].Error)
	assert.Contains(t, result.Results[0].Error.Error(), "is unknown")
	assert.False(t, committed)
	assertNoForgeWrites(t, fc)
}

// overlayReadCounter counts GetFileContent reads of the installed overlay.
type overlayReadCounter struct {
	forge.Client
	reads int
}

func (c *overlayReadCounter) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if path == preset.OverlayPath {
		c.reads++
	}
	return c.Client.GetFileContent(ctx, owner, repo, path)
}

// Selecting the pinned ref's configuration schema reuses the installed
// overlay convergence reads anyway, so a pinned repository costs no more
// overlay reads than one following the running release, while a
// schema-dependent installed overlay is still seen by the selection.
func TestConverge_PinnedSchemaSelectionAddsNoOverlayRead(t *testing.T) {
	run := func(t *testing.T, pinned, installed bool) (reads int, err error) {
		t.Helper()
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
		cfg := convergeCfgWithDefaults(m)
		if installed {
			cfg = withoutInferenceInputs(cfg)
		}
		if !pinned {
			cfg.UpstreamRef = m.GitHub.FullsendRef
		}
		fc := newFakeClientForBatch("acme/api")
		if installed {
			markFullyInstalled(fc, "acme", "api")
			populateScaffoldContent(t, fc, "acme", "api", m.GitHub.FullsendRef, "https://mint.example.com")
			fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("tracker:\n  github: {}\n")
		}
		counter := &overlayReadCounter{Client: fc}
		sc := &fakeScaffoldCommit{}
		result, convErr := Converge(context.Background(), cfg, newTestClientFactory(counter), sc.fn(), noopProgress)
		require.NoError(t, convErr)
		require.Len(t, result.Results, 1)
		return counter.reads, result.Results[0].Error
	}

	for _, tc := range []struct {
		name      string
		installed bool
	}{
		{"fresh install", false},
		{"installed repository with a schema-dependent overlay", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinnedReads, pinnedErr := run(t, true, tc.installed)
			releaseReads, releaseErr := run(t, false, tc.installed)
			assert.Equal(t, releaseReads, pinnedReads, "pinned and release-following overlay reads")
			assert.Equal(t, releaseErr != nil, pinnedErr != nil, "pinned error: %v, release error: %v", pinnedErr, releaseErr)
			assert.Positive(t, pinnedReads)
		})
	}
}
