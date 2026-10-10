package repos

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func adoptionCfg() ResolvedConfig {
	return ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub}
}

func desiredFor(t *testing.T, m *Manifest) []byte {
	t.Helper()
	return mustDesiredManaged(t, m, "acme", "api")
}

func TestAdoptionProposal_ShowsEntryAndDifference(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	existing := []byte("# hand-authored\n" +
		"version: \"1\"\n" +
		"roles:\n  - triage\n" +
		"runtime: pi\n" +
		"allowed_remote_resources:\n  - https://example.com/\n" +
		"kill_switch: false\n")

	got := adoptionProposal(adoptionCfg(), existing, desiredFor(t, m), nil, nil)

	// Every explicit setting, including values equal to code defaults,
	// moves into the proposed entry; shorthands stay top-level.
	for _, want := range []string{
		"proposed repos.yaml entry for acme/api",
		"- name: acme/api",
		"runtime: pi",
		"allowed_remote_resources:",
		"- https://example.com/",
		"config:",
		"version: \"1\"",
		"kill_switch: false",
	} {
		assert.Contains(t, got, want)
	}
	// The effective difference lists changed and removed settings.
	assert.Contains(t, got, "- kill_switch: false")
	assert.Contains(t, got, "+ kill_switch: true")
	assert.Contains(t, got, "- version: \"1\"   # removed: falls back to the base layer or code default")
	assert.Contains(t, got, "comments in the existing file are not carried")
	assert.Contains(t, got, "remove or replace "+preset.OverlayPath)
}

func TestAdoptionProposal_NoDifference(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	got := adoptionProposal(adoptionCfg(), []byte("kill_switch: true\n"), desiredFor(t, m), nil, nil)
	assert.Contains(t, got, "adoption only adds the ownership marker")
	assert.NotContains(t, got, "comments in the existing file")
}

func TestAdoptionProposal_BaseGatewayChangeIsShown(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	existing := []byte("kill_switch: true\n")
	currentBase := []byte("inference:\n  gateway:\n    url: https://old.example.com\n    audience: aud\n")
	proposedBase := []byte("inference:\n  gateway:\n    url: https://new.example.com\n    audience: aud\n")

	got := adoptionProposal(adoptionCfg(), existing, desiredFor(t, m), currentBase, proposedBase)

	// The file itself is unchanged, but the effective configuration is not.
	assert.Contains(t, got, "effective layered configuration change")
	assert.NotContains(t, got, "the effective configuration does not change")
	assert.Contains(t, got, "url: https://old.example.com")
	assert.Contains(t, got, "url: https://new.example.com")
}

func TestAdoptionProposal_EmptyFileAddsEverythingDesired(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	for _, existing := range [][]byte{{}, []byte("\n"), []byte("# only a comment\n"), []byte("~\n")} {
		got := adoptionProposal(adoptionCfg(), existing, desiredFor(t, m), nil, nil)
		assert.Contains(t, got, "- name: acme/api")
		assert.NotContains(t, got, "config:", "an empty file declares nothing to move")
		assert.Contains(t, got, "+ kill_switch: true")
	}
}

func TestAdoptionProposal_UnparseableAndNonMapping(t *testing.T) {
	m := newConvergeManifest("acme/api")
	desired := desiredFor(t, m)
	got := adoptionProposal(adoptionCfg(), []byte("roles: [unterminated\n"), desired, nil, nil)
	assert.Contains(t, got, "cannot be parsed")
	got = adoptionProposal(adoptionCfg(), []byte("- a\n- b\n"), desired, nil, nil)
	assert.Contains(t, got, "must be a YAML mapping")
	got = adoptionProposal(adoptionCfg(), []byte("kill_switch: false\n"), []byte("roles: [unterminated\n"), nil, nil)
	assert.Contains(t, got, "rendering the managed")
}

func TestAdoptionProposal_NotesSettingsTheManifestCannotHold(t *testing.T) {
	m := newConvergeManifest("acme/api")
	got := adoptionProposal(adoptionCfg(), []byte("not_a_real_setting: true\n"), desiredFor(t, m), nil, nil)
	assert.Contains(t, got, "repos.yaml cannot hold these settings as written")
	assert.Contains(t, got, "not_a_real_setting")
}

func TestOverlayDifference_MultilineAndAdded(t *testing.T) {
	cur, err := overlayMapping([]byte("roles:\n  - triage\n  - fix\n"))
	require.NoError(t, err)
	next, err := overlayMapping([]byte("roles:\n  - triage\ncreate_issues:\n  allow_targets:\n    repos:\n      - fullsend-ai/fullsend\n"))
	require.NoError(t, err)
	got := overlayDifference(cur, next)
	assert.Contains(t, got, "- roles:")
	assert.Contains(t, got, "- fix")
	assert.Contains(t, got, "-     - fix", "removed list items keep their indentation")
	assert.Contains(t, got, "+ roles:")
	assert.Contains(t, got, "+ create_issues:")
	assert.Equal(t, "", overlayDifference(cur, cur))
}

func TestReadExistingFile(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	path := "acme/api/" + preset.OverlayPath

	data, found, err := readExistingFile(ctx, fc, "acme", "api", preset.OverlayPath)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, data)

	fc.FileContents[path] = []byte{}
	data, found, err = readExistingFile(ctx, fc, "acme", "api", preset.OverlayPath)
	require.NoError(t, err)
	assert.True(t, found, "a zero-byte file exists")
	assert.NotNil(t, data)
	assert.True(t, overlayNeedsAdoption(found, data))

	fc.FileContents[path] = nil
	_, found, err = readExistingFile(ctx, fc, "acme", "api", preset.OverlayPath)
	require.NoError(t, err)
	assert.True(t, found, "a nil-content file still exists")

	fc.FileContents[path] = []byte(managedConfigMarker)
	data, found, _ = readExistingFile(ctx, fc, "acme", "api", preset.OverlayPath)
	assert.False(t, overlayNeedsAdoption(found, data))
	assert.False(t, overlayNeedsAdoption(false, nil))

	fc.GetFileContentErrors = map[string]error{path: fmt.Errorf("boom")}
	_, _, err = readExistingFile(ctx, fc, "acme", "api", preset.OverlayPath)
	require.Error(t, err)
}

// An existing zero-byte .fullsend/config.yaml is an existing, markerless
// file that requires adoption, not an absent one (#8218).
func TestZeroByteOverlayRequiresAdoption(t *testing.T) {
	ctx := context.Background()
	path := "acme/api/" + preset.OverlayPath

	t.Run("preflight", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents[path] = []byte{}
		err := PreflightManagedConfig(ctx, m, newTestClientFactory(fc), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "adoption required")
	})
	t.Run("writeback leaves manifest alone", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents[path] = []byte{}
		names, err := EnsureManagedConfigDefaults(ctx, ManagedConfigWritebackConfig{Manifest: m}, newTestClientFactory(fc), nil)
		require.NoError(t, err)
		assert.Empty(t, names, "an existing overlay is not a first install")
	})
	t.Run("converge on an installed repo", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := forge.NewFakeClient()
		fc.FileContents[path] = []byte{}
		resolved := managedResolved(t, fc, m, "acme", "api")
		files, actions := convergeManagedConfigFiles(ctx, resolved, desiredFor(t, m), false, noopProgress)
		assert.Empty(t, files, "the zero-byte file must not be overwritten")
		require.Len(t, actions, 1)
		assert.Equal(t, ActionAdoptionRequired, actions[0].Action)
	})
	t.Run("converge read error", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := forge.NewFakeClient()
		fc.GetFileContentErrors = map[string]error{path: fmt.Errorf("boom")}
		resolved := managedResolved(t, fc, m, "acme", "api")
		files, actions := convergeManagedConfigFiles(ctx, resolved, desiredFor(t, m), false, noopProgress)
		assert.Empty(t, files)
		require.Len(t, actions, 1)
		assert.Equal(t, "error", actions[0].Action)
	})
	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.FileContents[path] = []byte{}
		m := newConvergeManifest("acme/api")
		sc := &spyScaffoldCommit{}
		result, err := Converge(ctx, convergeCfgWithDefaults(m), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Error(t, result.Results[0].Error, "a fresh install blocked on adoption fails")
		assert.Contains(t, result.Results[0].Error.Error(), "adoption required")
		assert.False(t, result.Results[0].Installed)
		assert.Empty(t, sc.files, "adoption-required install must not commit any scaffold")
		assertNoForgeWrites(t, fc)
		var sawAdoption bool
		for _, a := range result.Results[0].Actions {
			if a.Component == preset.OverlayPath && strings.Contains(a.Detail, "adoption required") {
				sawAdoption = true
			}
		}
		assert.True(t, sawAdoption, "actions: %v", result.Results[0].Actions)
	})
	t.Run("status without components", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := forge.NewFakeClient()
		fc.FileContents[path] = []byte{}
		result, err := Status(ctx, m, newTestClientFactory(fc), 2, []string{"acme/api"})
		require.NoError(t, err)
		r := result.Repos[0]
		require.False(t, r.Installed)
		require.Len(t, r.Drifts, 1)
		assert.Equal(t, preset.OverlayPath, r.Drifts[0].Field)
		assert.Contains(t, r.Drifts[0].Expected, "adoption required")
		assert.NotEmpty(t, r.Drifts[0].Detail)
	})
	t.Run("effective overlay keeps the existing file", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := forge.NewFakeClient()
		fc.FileContents[path] = []byte{}
		resolved := managedResolved(t, fc, m, "acme", "api")
		d := convergeDiscovery{resolved: resolved, repo: ResolvedRepo{Owner: "acme", Repo: "api"}, managedConfig: desiredFor(t, m)}
		got, err := effectiveConfigOverlay(ctx, d)
		require.NoError(t, err)
		assert.Equal(t, []byte{}, got)

		fc.GetFileContentErrors = map[string]error{path: fmt.Errorf("boom")}
		_, err = effectiveConfigOverlay(ctx, d)
		require.Error(t, err)
	})
}

// Status reports overlay drift for every managed repo, including one with
// no Fullsend components, and shows how to adopt a markerless overlay.
func TestStatus_NoComponentsOverlayDrift(t *testing.T) {
	ctx := context.Background()
	path := "acme/api/" + preset.OverlayPath

	run := func(t *testing.T, m *Manifest, fc *forge.FakeClient) RepoStatus {
		t.Helper()
		result, err := Status(ctx, m, newTestClientFactory(fc), 2, []string{"acme/api"})
		require.NoError(t, err)
		require.Len(t, result.Repos, 1)
		require.Empty(t, result.Repos[0].Error)
		require.False(t, result.Repos[0].Installed)
		return result.Repos[0]
	}

	t.Run("markerless overlay shows entry and difference", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
		fc := forge.NewFakeClient()
		fc.FileContents[path] = []byte("kill_switch: false\nversion: \"1\"\n")
		r := run(t, m, fc)
		require.Len(t, r.Drifts, 1)
		d := r.Drifts[0]
		assert.Equal(t, "managed configuration (adoption required)", d.Expected)
		assert.Contains(t, d.Detail, "proposed repos.yaml entry for acme/api")
		assert.Contains(t, d.Detail, "version: \"1\"")
		assert.Contains(t, d.Detail, "- kill_switch: false")
		assert.Contains(t, d.Detail, "+ kill_switch: true")
	})
	t.Run("marked overlay with different content", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
		fc := forge.NewFakeClient()
		fc.FileContents[path] = []byte(managedConfigMarker + "kill_switch: false\n")
		r := run(t, m, fc)
		require.Len(t, r.Drifts, 1)
		assert.Equal(t, "installed content differs", r.Drifts[0].Actual)
		assert.Empty(t, r.Drifts[0].Detail)
	})
	t.Run("matching overlay", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := forge.NewFakeClient()
		fc.FileContents[path] = desiredFor(t, m)
		assert.Empty(t, run(t, m, fc).Drifts)
	})
	t.Run("missing overlay is not drift before install", func(t *testing.T) {
		assert.Empty(t, run(t, newConvergeManifest("acme/api"), forge.NewFakeClient()).Drifts)
	})
	t.Run("overlay read error", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		fc := forge.NewFakeClient()
		fc.GetFileContentErrors = map[string]error{path: fmt.Errorf("boom")}
		result, err := Status(ctx, m, newTestClientFactory(fc), 2, []string{"acme/api"})
		require.NoError(t, err)
		assert.Contains(t, result.Repos[0].Error, "reading "+preset.OverlayPath)
	})
}

func TestEnsureManagedConfigDefaults_ExplicitRolesEqualToInheritedArePersisted(t *testing.T) {
	ctx := context.Background()
	roles := []string{"triage", "fix"}

	t.Run("equal to defaults.config roles", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "roles: [triage, fix]\ncreate_issues:\n  allow_targets:\n    repos: [fullsend-ai/fullsend]\n")
		names, err := EnsureManagedConfigDefaults(ctx, ManagedConfigWritebackConfig{
			Manifest: m, Roles: roles, RolesExplicit: true,
		}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"acme/api"}, names)
		declared, ok := m.GitHub.Repos[0].Config.ExplicitRoles()
		require.True(t, ok, "the entry must declare the explicit roles itself")
		assert.Equal(t, roles, declared)
	})
	t.Run("equal to code default roles", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		def, _ := m.ResolveConfig("acme", "api")
		names, err := EnsureManagedConfigDefaults(ctx, ManagedConfigWritebackConfig{
			Manifest: m, Roles: def.Managed.ConfigRoles(), RolesExplicit: true,
		}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"acme/api"}, names)
		_, ok := m.GitHub.Repos[0].Config.ExplicitRoles()
		assert.True(t, ok)
	})
	t.Run("entry that already declares them is unchanged", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.GitHub.Repos[0].Config = mustManagedConfig(t, "roles: [triage, fix]\ncreate_issues:\n  allow_targets:\n    repos: [fullsend-ai/fullsend]\n")
		names, err := EnsureManagedConfigDefaults(ctx, ManagedConfigWritebackConfig{
			Manifest: m, Roles: roles, RolesExplicit: true,
		}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Empty(t, names)
	})
	t.Run("glob-covered repo gets its own entry", func(t *testing.T) {
		m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{
			Repos: []RepoEntry{{Name: "acme/*", Config: mustManagedConfig(t, "roles: [triage, fix]\n")}},
		}}
		names, err := EnsureManagedConfigDefaults(ctx, ManagedConfigWritebackConfig{
			Manifest: m, RepoFilter: []string{"acme/api"}, Roles: roles, RolesExplicit: true,
		}, newTestClientFactory(newFakeClientForBatch("acme/api")), nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"acme/api"}, names)
		require.Len(t, m.GitHub.Repos, 2)
		_, ok := m.GitHub.Repos[1].Config.ExplicitRoles()
		assert.True(t, ok)
	})
}

func TestManagedConfigExplicitRoles(t *testing.T) {
	var zero = newConvergeManifest("acme/api").GitHub.Repos[0].Config
	_, ok := zero.ExplicitRoles()
	assert.False(t, ok, "an omitted config declares no roles")
	_, ok = mustManagedConfig(t, "kill_switch: true\n").ExplicitRoles()
	assert.False(t, ok, "a block without roles declares none")
	got, ok := mustManagedConfig(t, "roles: [fix]\n").ExplicitRoles()
	assert.True(t, ok)
	assert.Equal(t, []string{"fix"}, got)
}

func TestRequireWritableManifestForNewRepos(t *testing.T) {
	ctx := context.Background()
	url := "https://example.com/repos.yaml"
	newManifest := func() *Manifest {
		return &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{MintURL: "https://mint.example.com"}}
	}

	t.Run("writable manifest passes", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/new")
		require.NoError(t, RequireWritableManifestForNewRepos(ctx, newManifest(), "", ForgeGitHub,
			[]RepoEntry{{Name: "acme/new"}}, newTestClientFactory(fc), nil, false))
		require.NoError(t, RequireWritableManifestForNewRepos(ctx, newManifest(), url, ForgeGitHub,
			nil, newTestClientFactory(fc), nil, false), "nothing to add")
	})
	t.Run("https manifest suggests the exact entry", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/new")
		err := RequireWritableManifestForNewRepos(ctx, newManifest(), url, ForgeGitHub,
			[]RepoEntry{{Name: "acme/new"}}, newTestClientFactory(fc), []string{"triage", "fix"}, true)
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "read-only")
		assert.Contains(t, msg, "no changes were made")
		for _, want := range []string{"github.repos", "- name: acme/new", "config:", "create_issues:", "- fullsend-ai/fullsend", "roles:", "- fix"} {
			assert.Contains(t, msg, want)
		}
	})
	t.Run("remotely loaded manifest is read-only without a path", func(t *testing.T) {
		m := newManifest()
		m.sourceRemote = true
		err := RequireWritableManifestForNewRepos(ctx, m, "", ForgeGitHub,
			[]RepoEntry{{Name: "acme/new"}}, newTestClientFactory(newFakeClientForBatch("acme/new")), nil, false)
		require.Error(t, err)
	})
	t.Run("an existing overlay and globs get no injected config", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/new")
		fc.FileContents["acme/new/"+preset.OverlayPath] = []byte(managedConfigMarker + "{}\n")
		err := RequireWritableManifestForNewRepos(ctx, newManifest(), url, ForgeGitHub,
			[]RepoEntry{{Name: "acme/new"}, {Name: "other/*"}, {Name: "malformed"}}, newTestClientFactory(fc), nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "- name: acme/new")
		assert.Contains(t, err.Error(), "- name: other/*")
		assert.NotContains(t, err.Error(), "create_issues")
	})
	t.Run("overlay read error", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/new")
		fc.GetFileContentErrors = map[string]error{"acme/new/" + preset.OverlayPath: fmt.Errorf("boom")}
		err := RequireWritableManifestForNewRepos(ctx, newManifest(), url, ForgeGitHub,
			[]RepoEntry{{Name: "acme/new"}}, newTestClientFactory(fc), nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
	t.Run("client factory error", func(t *testing.T) {
		err := RequireWritableManifestForNewRepos(ctx, newManifest(), url, ForgeGitHub,
			[]RepoEntry{{Name: "acme/new"}}, failingFactory{}, nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no client")
	})
}

func TestHasYAMLComments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"no comments", "kill_switch: true\n", false},
		{"whole-line comment", "# note\nkill_switch: true\n", true},
		{"inline comment", "kill_switch: true # maintenance restriction\n", true},
		{"nested inline comment", "inference:\n  gateway:\n    url: https://x # why\n", true},
		{"foot comment", "kill_switch: true\n# trailing\n", true},
		{"comment-only document", "# nothing else\n", true},
		{"hash inside block scalar", "description: |\n  line one\n  # not a comment\n", false},
		{"hash inside quoted scalar", "description: \"a # b\"\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasYAMLComments([]byte(tt.in)))
		})
	}
}

func TestProposedManifestEntry_ExpandsAliasesBeforeRelocation(t *testing.T) {
	existing := []byte("agents:\n  - name: code\n    runtime: &rt pi\n" +
		"runtime: *rt\n")
	current, err := overlayMapping(existing)
	require.NoError(t, err)

	got := proposedManifestEntry(adoptionCfg(), current)

	// The anchor is dropped and the alias expanded, so the suggested
	// entry parses on its own with the aliased value intact.
	assert.NotContains(t, got, "*rt")
	assert.NotContains(t, got, "&rt")
	var parsed []map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "pi", parsed[0]["runtime"])
	_, hasConfig := parsed[0]["config"]
	assert.True(t, hasConfig)
}
