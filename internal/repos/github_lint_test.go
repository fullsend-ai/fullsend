package repos

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	lintCallerKey  = "acme/api/" + scaffold.LintCallerPath
	userLintCaller = "name: fullsend lint\non: pull_request\njobs: {}\n"
)

// lintManifest declares acme/api with the given lint setting; nil omits it.
func lintManifest(lint *bool) *Manifest {
	m := newConvergeManifest("acme/api")
	m.GitHub.Repos[0].Lint = lint
	return m
}

// runLintConverge converges acme/api with the given lint setting.
func runLintConverge(t *testing.T, fc *forge.FakeClient, lint *bool, dryRun bool) (*ConvergeResult, *spyScaffoldCommit) {
	t.Helper()
	cfg := withoutInferenceInputs(convergeCfgWithDefaults(lintManifest(lint)))
	cfg.DryRun = dryRun
	spy := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	return &result.Results[0], spy
}

// stagedLintCaller returns the lint caller entry the spy received, if any.
func stagedLintCaller(spy *spyScaffoldCommit) (forge.TreeFile, bool) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	i := slices.IndexFunc(spy.files, func(f forge.TreeFile) bool { return f.Path == scaffold.LintCallerPath })
	if i < 0 {
		return forge.TreeFile{}, false
	}
	return spy.files[i], true
}

func managedLintCaller(t *testing.T, ref string) []byte {
	t.Helper()
	out, err := scaffold.RenderLintCaller(scaffold.RenderOptions{UpstreamRef: ref})
	require.NoError(t, err)
	return out
}

func TestConverge_GitHubLint_Established(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name       string
		lint       *bool
		existing   []byte
		dryRun     bool
		wantAction string // "" means no lint action is reported
		wantDetail string
		wantStaged bool
		wantDelete bool
	}{
		{name: "enabled and absent adds", lint: &on, wantAction: "add", wantStaged: true},
		{name: "enabled and outdated updates", lint: &on, existing: managedLintCaller(t, "v0.9.0"), wantAction: "update", wantStaged: true},
		{name: "enabled and current is idempotent", lint: &on, existing: managedLintCaller(t, "v1.0.0"), wantAction: "none"},
		{name: "enabled and user-owned is untouched", lint: &on, existing: []byte(userLintCaller), wantAction: "none", wantDetail: "not managed by fullsend"},
		{name: "enabled dry run stages nothing", lint: &on, dryRun: true, wantAction: "add", wantDetail: "would add"},
		{name: "disabled removes managed", lint: &off, existing: managedLintCaller(t, "v1.0.0"), wantAction: "delete", wantStaged: true, wantDelete: true},
		{name: "disabled dry run stages nothing", lint: &off, existing: managedLintCaller(t, "v1.0.0"), dryRun: true, wantAction: "delete", wantDetail: "would delete"},
		{name: "disabled and user-owned is untouched", lint: &off, existing: []byte(userLintCaller)},
		{name: "disabled and absent reports nothing", lint: &off},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := installedJiraRepo()
			if tt.existing != nil {
				fc.FileContents[lintCallerKey] = tt.existing
			}

			cr, spy := runLintConverge(t, fc, tt.lint, tt.dryRun)
			require.NoError(t, cr.Error)
			a, reported := findAction(cr.Actions, lintComponent)
			assert.Equal(t, tt.wantAction != "", reported)
			assert.Equal(t, tt.wantAction, a.Action)
			assert.Contains(t, a.Detail, tt.wantDetail)

			f, staged := stagedLintCaller(spy)
			require.Equal(t, tt.wantStaged, staged)
			if staged {
				assert.Equal(t, tt.wantDelete, f.Delete)
			}
			if staged && !f.Delete {
				assert.Equal(t, managedLintCaller(t, "v1.0.0"), f.Content, "the caller is pinned to fullsend_ref")
			}
		})
	}
}

func TestConverge_GitHubLint_FreshInstall(t *testing.T) {
	on := true
	for _, dryRun := range []bool{false, true} {
		fc := newFakeClientForBatch("acme/api")
		cfg := convergeCfgWithDefaults(lintManifest(&on))
		cfg.DryRun = dryRun
		spy := &spyScaffoldCommit{}

		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "results: %+v", result.Results)
		a, _ := findAction(result.Results[0].Actions, lintComponent)
		assert.Equal(t, "add", a.Action)
		f, staged := stagedLintCaller(spy)
		assert.Equal(t, !dryRun, staged, "the caller joins the install commit, and a dry run writes nothing")
		if staged {
			assert.Equal(t, managedLintCaller(t, "v1.0.0"), f.Content)
		}
	}
}

// The release build some tests run as.
const (
	lintReleaseSHA = "2222222222222222222222222222222222222222"
	lintReleaseTag = "v2.0.0"
)

// serveLintWorkflow makes the reusable lint workflow exist at refs.
func serveLintWorkflow(fc *forge.FakeClient, refs ...string) {
	for _, ref := range refs {
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+reusableLintWorkflowPath+"@"+ref] = []byte("name: Fullsend lint\n")
	}
}

// lintInstall runs one install of acme/api, lands what it commits, and
// returns the result with the lines install printed about lint.
func lintInstall(t *testing.T, fc *forge.FakeClient, cfg ConvergeConfig) (*ConvergeResult, []string) {
	t.Helper()
	spy := &spyScaffoldCommit{}
	var printed []string
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), func(_, stage, msg string) {
		if stage == "lint" {
			printed = append(printed, msg)
		}
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	for _, f := range spy.files {
		if f.Delete {
			delete(fc.FileContents, "acme/api/"+f.Path)
		} else {
			fc.FileContents["acme/api/"+f.Path] = f.Content
		}
	}
	return &result.Results[0], printed
}

// lintStatusDrifts returns the drift repos status reports for acme/api.
func lintStatusDrifts(t *testing.T, fc *forge.FakeClient, m *Manifest, dcfg DriftConfig) []Drift {
	t.Helper()
	result, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil, dcfg)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	require.Empty(t, result.Repos[0].Error)
	return result.Repos[0].Drifts
}

// The caller is pinned to the shim's ref, and status agrees with install from
// any CLI build.
func TestGitHubLint_CallerFollowsTheShim(t *testing.T) {
	const shaPin = "cccccccccccccccccccccccccccccccccccccccc"
	on := true
	release := DriftConfig{UpstreamRef: lintReleaseSHA, UpstreamTag: lintReleaseTag}
	laterRelease := DriftConfig{UpstreamRef: "3333333333333333333333333333333333333333", UpstreamTag: "v2.1.0"}
	tests := []struct {
		name        string
		shimRef     string // ref of the installed shim
		fullsendRef string // manifest fullsend_ref
		build       DriftConfig
		wantCaller  string
		// statusBuild is the CLI build that runs status and a second install.
		statusBuild DriftConfig
		// targetLacksWorkflow removes the reusable workflow from the
		// manifest's fullsend_ref.
		targetLacksWorkflow bool
	}{
		{name: "no fullsend_ref, release build", shimRef: "v1.0.0", build: release, wantCaller: "@v1.0.0\n", statusBuild: release},
		{name: "no fullsend_ref, dev build", shimRef: "v1.0.0", wantCaller: "@v1.0.0\n"},
		{name: "refused downgrade", shimRef: "v1.1.0", fullsendRef: "v1.0.0", wantCaller: "@v1.1.0\n"},
		{name: "refused downgrade to a ref without the workflow", shimRef: "v1.1.0", fullsendRef: "v1.0.0", wantCaller: "@v1.1.0\n", targetLacksWorkflow: true},
		{name: "upgrade in the same run", shimRef: "v0.9.0", fullsendRef: "v1.0.0", wantCaller: "@v1.0.0\n"},
		{name: "commit pin, later CLI build", shimRef: shaPin, fullsendRef: shaPin, build: release, wantCaller: "@" + shaPin + "\n", statusBuild: laterRelease},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := installedJiraRepo()
			populateScaffoldContent(t, fc, "acme", "api", tt.shimRef, "https://mint.example.com")
			serveLintWorkflow(fc, tt.shimRef, lintReleaseSHA, "main")
			if tt.targetLacksWorkflow {
				delete(fc.FileContentsRef, shimOwner+"/"+shimRepo+"/"+reusableLintWorkflowPath+"@"+tt.fullsendRef)
			}
			m := lintManifest(&on)
			m.GitHub.FullsendRef = tt.fullsendRef
			cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
			cfg.UpstreamRef, cfg.UpstreamTag = tt.build.UpstreamRef, tt.build.UpstreamTag

			cr, _ := lintInstall(t, fc, cfg)
			require.NoError(t, cr.Error)
			assert.Contains(t, string(fc.FileContents[lintCallerKey]), "reusable-fullsend-lint.yml"+tt.wantCaller)
			assert.Contains(t, string(fc.FileContents["acme/api/.github/workflows/fullsend.yaml"]), "reusable-dispatch.yml"+tt.wantCaller,
				"the shim is on the same ref")

			for _, d := range lintStatusDrifts(t, fc, m, tt.statusBuild) {
				assert.NotEqual(t, scaffold.LintCallerPath, d.Field, "status must not report lint drift: %+v", d)
			}
			cfg.UpstreamRef, cfg.UpstreamTag = tt.statusBuild.UpstreamRef, tt.statusBuild.UpstreamTag
			cr, _ = lintInstall(t, fc, cfg)
			a, _ := findAction(cr.Actions, lintComponent)
			assert.Equal(t, "none", a.Action, "a second install changes nothing: %s", a.Detail)
		})
	}
}

// A shim that ends up on a ref without the reusable workflow gets no caller.
func TestGitHubLint_ShimRefWithoutReusableWorkflow(t *testing.T) {
	on := true
	for name, shimRef := range map[string]string{
		"shim stays ahead of fullsend_ref": "v1.1.0",
		"shim is upgraded to fullsend_ref": "v0.9.0",
	} {
		t.Run(name, func(t *testing.T) {
			fc := installedJiraRepo()
			populateScaffoldContent(t, fc, "acme", "api", shimRef, "https://mint.example.com")
			delete(fc.FileContentsRef, shimOwner+"/"+shimRepo+"/"+reusableLintWorkflowPath+"@v1.0.0")
			wantRef := "v1.0.0"
			if shimRef == "v1.1.0" {
				wantRef = shimRef
			}

			cr, _ := lintInstall(t, fc, withoutInferenceInputs(convergeCfgWithDefaults(lintManifest(&on))))
			require.ErrorContains(t, cr.Error, "pinned to \""+wantRef+"\", which does not include "+reusableLintWorkflowPath)
			assert.NotContains(t, fc.FileContents, lintCallerKey)
		})
	}
}

// A dry run reports the caller change that a planned fullsend_ref upgrade
// brings, and none when only a thin caller is behind the shim.
func TestGitHubLint_DryRunRefUpgrade(t *testing.T) {
	on := true
	tests := []struct {
		name, shimRef, wantAction string
		staleThinCaller           bool
		// unpinnedOpenAIWIF runs a release build on an openai-wif repo with
		// no fullsend_ref: install targets the release commit.
		unpinnedOpenAIWIF bool
	}{
		{name: "shim moves", shimRef: "v0.9.0", wantAction: "update"},
		{name: "shim moves to the release of an unpinned openai-wif repo", shimRef: "v1.0.0", unpinnedOpenAIWIF: true, wantAction: "update"},
		{name: "only a thin caller moves", shimRef: "v1.0.0", staleThinCaller: true, wantAction: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := installedJiraRepo()
			populateScaffoldContent(t, fc, "acme", "api", tt.shimRef, "https://mint.example.com")
			fc.FileContents[lintCallerKey] = managedLintCaller(t, tt.shimRef)
			if tt.staleThinCaller {
				// The tag resolves to a commit, so the target ref is
				// written differently from the tag-pinned shim.
				fc.Refs[shimOwner+"/"+shimRepo+"/tags/v1.0.0"] = lintReleaseSHA
				serveLintWorkflow(fc, lintReleaseSHA)
				key := "acme/api/.github/workflows/prioritize.yml"
				fc.FileContents[key] = []byte(strings.ReplaceAll(string(fc.FileContents[key]), "@v1.0.0", "@v0.9.0"))
			}
			m := lintManifest(&on)
			cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
			if tt.unpinnedOpenAIWIF {
				m.Defaults.Inference.Auth = InferenceAuthOpenAIWIF
				m.GitHub.FullsendRef = ""
				setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
				serveOpenAIWIFWorkflows(fc, lintReleaseSHA, true)
				serveLintWorkflow(fc, lintReleaseSHA)
				cfg.UpstreamRef, cfg.UpstreamTag = lintReleaseSHA, lintReleaseTag
			}
			cfg.DryRun = true

			cr, _ := lintInstall(t, fc, cfg)
			require.NoError(t, cr.Error)
			ref, _ := findAction(cr.Actions, "ref")
			require.Equal(t, "upgrade", ref.Action, "the fixture must plan a ref upgrade")
			a, _ := findAction(cr.Actions, lintComponent)
			assert.Equal(t, tt.wantAction, a.Action)
		})
	}
}

// A vendored shim has no ref. The caller must not change with the CLI build,
// and status, which lacks install's openai-wif ref override, must agree.
func TestGitHubLint_VendoredCallerIsStableAcrossBuilds(t *testing.T) {
	on, yes := true, true
	release := DriftConfig{UpstreamRef: lintReleaseSHA, UpstreamTag: lintReleaseTag}
	laterRelease := DriftConfig{UpstreamRef: "3333333333333333333333333333333333333333", UpstreamTag: "v2.1.0"}
	tests := []struct {
		name, auth, fullsendRef string
	}{
		{name: "pinned to the release commit", auth: InferenceAuthVertexWIF, fullsendRef: lintReleaseSHA},
		{name: "no fullsend_ref", auth: InferenceAuthVertexWIF},
		{name: "openai-wif with no fullsend_ref", auth: InferenceAuthOpenAIWIF},
	}
	for _, tt := range tests {
		auth := tt.auth
		t.Run(tt.name, func(t *testing.T) {
			fc := installedJiraRepo()
			putVendoredCallers(t, fc, "acme", "api", lintReleaseSHA)
			serveLintWorkflow(fc, lintReleaseSHA, laterRelease.UpstreamRef)
			m := lintManifest(&on)
			m.GitHub.Repos[0].Vendor = &yes
			m.GitHub.FullsendRef = tt.fullsendRef
			builds := []DriftConfig{release, laterRelease}
			if auth == InferenceAuthOpenAIWIF {
				m.Defaults.Inference.Auth = auth
				setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
				serveOpenAIWIFWorkflows(fc, lintReleaseSHA, true)
				serveOpenAIWIFWorkflows(fc, laterRelease.UpstreamRef, true)
			}
			for i, build := range builds {
				cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
				cfg.UpstreamRef, cfg.UpstreamTag = build.UpstreamRef, build.UpstreamTag
				cr, _ := lintInstall(t, fc, cfg)
				require.NoError(t, cr.Error)
				a, _ := findAction(cr.Actions, lintComponent)
				assert.Equal(t, []string{"add", "none"}[i], a.Action, "install %d from %s", i, build.UpstreamTag)
				for _, d := range lintStatusDrifts(t, fc, m, build) {
					assert.NotEqual(t, scaffold.LintCallerPath, d.Field, "status must not report lint drift: %+v", d)
				}
			}
		})
	}
}

// Each lintPin rule for a shim without an upstream ref (a vendored install).
func TestLintPin_WithoutShimRef(t *testing.T) {
	const commit = "cccccccccccccccccccccccccccccccccccccccc"
	vendoredShim := []byte("jobs:\n  dispatch:\n    uses: ./.github/workflows/reusable-dispatch.yml\n")
	installed := managedLintCaller(t, "v1.5.0")
	tests := []struct {
		name, fullsendRef string
		installed         []byte
		wantRef, wantTag  string
	}{
		{name: "unset, new caller uses the release", wantRef: lintReleaseSHA, wantTag: lintReleaseTag},
		{name: "unset, installed caller keeps its pin", installed: installed, wantRef: "v1.5.0"},
		{name: "fullsend_ref wins over an installed caller", fullsendRef: "v1.0.0", installed: installed, wantRef: "v1.0.0", wantTag: "v1.0.0"},
		{name: "the release commit carries no CLI tag", fullsendRef: lintReleaseSHA, wantRef: lintReleaseSHA},
		{name: "commit carries no CLI tag", fullsendRef: commit, wantRef: commit},
		{name: "branch carries no CLI tag", fullsendRef: "main", wantRef: "main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lintPin(context.Background(), vendoredShim, tt.installed, tt.fullsendRef, lintReleaseSHA, lintReleaseTag, nil)
			assert.Equal(t, scaffold.RenderOptions{UpstreamRef: tt.wantRef, UpstreamTag: tt.wantTag}, got)
		})
	}
	pinned := []byte("    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc1234  # v1.2.3\n")
	assert.Equal(t, scaffold.RenderOptions{UpstreamRef: "abc1234", UpstreamTag: "v1.2.3"},
		lintPin(context.Background(), pinned, installed, "v9.9.9", lintReleaseSHA, lintReleaseTag, nil), "a shim ref wins")
}

// A failed read of the installed shim is reported as such, not as a ref
// without the reusable workflow.
func TestCheckGitHubLintWorkflow_ShimReadError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{}
	fcfg := GitHubForgeConfig()
	for _, path := range fcfg.WorkflowPaths {
		fc.GetFileContentErrors["acme/api/"+path] = errors.New("502 bad gateway")
	}
	fcfg.Client = fc
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, Lint: true, FullsendRef: "v1.0.0", ForgeConfig: fcfg}

	err := checkGitHubLintWorkflow(context.Background(), resolved, ConvergeConfig{}, NewRefResolver(fc))
	require.ErrorContains(t, err, "502 bad gateway")
	assert.NotContains(t, err.Error(), "upgrade fullsend_ref")
}

// Install prints what it does to the caller, but not the routine "current".
func TestConverge_GitHubLint_PrintsProgress(t *testing.T) {
	on := true
	tests := []struct {
		name     string
		existing []byte
		dryRun   bool
		want     string // "" means nothing is printed
	}{
		{name: "dry run add", dryRun: true, want: "would add " + scaffold.LintCallerPath},
		{name: "add", want: "add " + scaffold.LintCallerPath},
		{name: "user-owned", existing: []byte(userLintCaller), want: "not managed by fullsend"},
		{name: "current", existing: managedLintCaller(t, "v1.0.0")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := installedJiraRepo()
			if tt.existing != nil {
				fc.FileContents[lintCallerKey] = tt.existing
			}
			cfg := withoutInferenceInputs(convergeCfgWithDefaults(lintManifest(&on)))
			cfg.DryRun = tt.dryRun
			_, printed := lintInstall(t, fc, cfg)
			if tt.want == "" {
				assert.Empty(t, printed)
				return
			}
			require.Len(t, printed, 1)
			assert.Contains(t, printed[0], tt.want)
		})
	}
}

// Dry and live runs both treat the caller as a pending scaffold change, so
// both keep an obsolete inference secret until it merges.
func TestConverge_GitHubLint_DryRunKeepsObsoleteSecretLikeLiveRun(t *testing.T) {
	on := true
	for _, dryRun := range []bool{true, false} {
		fc := installedJiraRepo()
		populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")
		fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
		cfg := withoutInferenceInputs(convergeCfgWithDefaults(lintManifest(&on)))
		cfg.Direct = false
		cfg.DryRun = dryRun

		cr, _ := lintInstall(t, fc, cfg)
		require.NoError(t, cr.Error)
		a, ok := findAction(cr.Actions, "secret:"+forge.SecretOpenAIAPIKey)
		require.True(t, ok)
		assert.Equal(t, "none", a.Action, "dryRun=%v: %s", dryRun, a.Detail)
		assert.Contains(t, a.Detail, "obsolete")
	}
}

func TestConverge_GitHubLint_ReadErrorFailsRun(t *testing.T) {
	on := true
	fc := installedJiraRepo()
	fc.GetFileContentErrors = map[string]error{lintCallerKey: errors.New("api down")}

	cr, spy := runLintConverge(t, fc, &on, false)
	require.ErrorContains(t, cr.Error, "api down")
	_, staged := stagedLintCaller(spy)
	assert.False(t, staged)
}

// A first install fails before any write when its ref lacks the reusable
// workflow, unless lint is off or the caller is user-owned.
func TestConverge_GitHubLint_PinWithoutReusableWorkflow(t *testing.T) {
	on := true
	tests := []struct {
		name      string
		lint      *bool
		userOwned bool
		wantFail  bool
	}{
		{name: "lint on", lint: &on, wantFail: true},
		{name: "lint on with a user-owned caller", lint: &on, userOwned: true},
		{name: "lint omitted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			delete(fc.FileContentsRef, shimOwner+"/"+shimRepo+"/"+reusableLintWorkflowPath+"@v1.0.0")
			if tt.userOwned {
				fc.FileContents[lintCallerKey] = []byte(userLintCaller)
			}
			spy := &spyScaffoldCommit{}

			result, err := Converge(context.Background(), convergeCfgWithDefaults(lintManifest(tt.lint)), newTestClientFactory(fc), spy.fn(), noopProgress)
			require.NoError(t, err)
			if !tt.wantFail {
				require.Empty(t, result.Failed(), "results: %+v", result.Results)
				return
			}
			require.Len(t, result.Failed(), 1)
			assert.ErrorContains(t, result.Results[0].Error, reusableLintWorkflowPath)
			assert.ErrorContains(t, result.Results[0].Error, "upgrade fullsend_ref")
			assert.Empty(t, fc.CreatedSecrets, "install must fail before writing credentials")
			assert.Empty(t, spy.files, "install must fail before delivering files")
		})
	}
}

func TestConvergeLint_UnmanagedForge(t *testing.T) {
	var printed []string
	actions := convergeLint(context.Background(), ResolvedConfig{Forge: ForgeGitLab, Lint: true},
		lintDelivery{progress: func(detail string) { printed = append(printed, detail) }})
	require.Len(t, actions, 1)
	assert.Equal(t, "none", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "does not manage the lint check on gitlab yet")
	assert.Equal(t, []string{actions[0].Detail}, printed, "install tells the operator")
	assert.Empty(t, convergeLint(context.Background(), ResolvedConfig{Forge: ForgeGitLab}, lintDelivery{}))

	// Status reports no lint drift for a forge install does not manage.
	var status RepoStatus
	gitlab := ResolvedConfig{Forge: ForgeGitLab, Lint: true, ForgeConfig: ForgeConfig{Client: forge.NewFakeClient()}}
	checkLintDrift(context.Background(), gitlab, DriftConfig{}, nil, &status)
	assert.Empty(t, status.Drifts)
}

func TestStatus_LintDrift(t *testing.T) {
	on := true
	// current is what install writes for the status fixture's fullsend_ref.
	current := managedLintCaller(t, "v2.3.0")
	tests := []struct {
		name     string
		lint     *bool
		existing []byte
		readErr  error
		want     *Drift
		wantErr  string
	}{
		{name: "enabled and current", lint: &on, existing: current},
		{name: "enabled and missing", lint: &on, want: &Drift{Field: scaffold.LintCallerPath, Expected: "present", Actual: "missing"}},
		{name: "enabled and outdated", lint: &on, existing: managedLintCaller(t, "v2.1.0"),
			want: &Drift{Field: scaffold.LintCallerPath, Expected: "current template", Actual: "installed content differs"}},
		{name: "enabled and user-owned", lint: &on, existing: []byte(userLintCaller)},
		{name: "omitted and managed", existing: current,
			want: &Drift{Field: scaffold.LintCallerPath, Expected: "absent", Actual: "present (lint is not enabled)"}},
		{name: "omitted and absent"},
		{name: "read error", lint: &on, readErr: errors.New("api down"), wantErr: "api down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			m := newTestManifest()
			m.GitHub.Repos = m.GitHub.Repos[:1]
			m.GitHub.Lint = tt.lint
			populateInstalledRepo(t, fc, "acme-corp", "api-server", "v2.3.0", "https://mint.example.com", "us-central1")
			key := "acme-corp/api-server/" + scaffold.LintCallerPath
			if tt.existing != nil {
				fc.FileContents[key] = tt.existing
			}
			if tt.readErr != nil {
				fc.GetFileContentErrors = map[string]error{key: tt.readErr}
			}

			result, err := Status(context.Background(), m, newTestClientFactory(fc), 1, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			if tt.wantErr != "" {
				assert.Contains(t, result.Repos[0].Error, tt.wantErr)
				return
			}
			require.Empty(t, result.Repos[0].Error)
			if tt.want == nil {
				assert.Empty(t, result.Repos[0].Drifts)
			} else {
				assert.Equal(t, []Drift{*tt.want}, result.Repos[0].Drifts)
			}
		})
	}
}

func TestUninstall_RemovesManagedLintCaller(t *testing.T) {
	managed := managedLintCaller(t, "v1.0.0")
	tests := []struct {
		name        string
		content     []byte
		readErr     error
		wantDeleted bool
	}{
		{name: "managed", content: managed, wantDeleted: true},
		{name: "user-owned", content: []byte(userLintCaller)},
		{name: "absent"},
		{name: "read error", content: managed, readErr: errors.New("api down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newInstalledFakeClient("acme/api")
			if tt.content != nil {
				client.FileContents[lintCallerKey] = tt.content
			}
			if tt.readErr != nil {
				client.GetFileContentErrors = map[string]error{lintCallerKey: tt.readErr}
			}

			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest:       testManifest("acme/api"),
				Repos:          []string{"acme/api"},
				Direct:         true,
				MaxConcurrency: 1,
			}, newTestClientFactory(client), uninstallCommitFn(client), nil)
			require.NoError(t, err)
			require.Len(t, results, 1)
			if tt.readErr == nil {
				assert.True(t, results[0].Success, "uninstall error: %v", results[0].Error)
			} else {
				assert.ErrorContains(t, results[0].Error, "checking managed lint workflow before uninstall")
				assert.Empty(t, client.DeletedSecrets, "uninstall must stop before deleting credentials")
			}
			assert.Equal(t, tt.wantDeleted, slices.Contains(collectDeletedPaths(client), scaffold.LintCallerPath))
			if !tt.wantDeleted && tt.content != nil {
				assert.Equal(t, tt.content, client.FileContents[lintCallerKey])
			}
		})
	}
}
