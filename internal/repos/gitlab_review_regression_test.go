package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// landingScaffoldCommit returns a ScaffoldCommitFunc that simulates a
// direct commit actually landing on the default branch by writing the
// delivered files into fc.FileContents, so a subsequent read (e.g.
// GitLabUsesTypedDispatch) observes them. Contrast with a commit
// function that reports success without writing anything, simulating a
// branch-protection PR fallback that hasn't merged yet — see
// TestTypedActivationNotAppliedBeforeTemplatesLand.
func landingScaffoldCommit(fc *forge.FakeClient, owner, repo string) ScaffoldCommitFunc {
	return func(_ context.Context, _, _ string, files []forge.TreeFile, _ bool, _ bool) error {
		for _, f := range files {
			fc.FileContents[owner+"/"+repo+"/"+f.Path] = f.Content
		}
		return nil
	}
}

func TestTypedActivationMigratesRealManagedSchedules(t *testing.T) {
	for _, operation := range []string{"install", "converge", "dry-run"} {
		t.Run(operation, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			for i, spec := range PipelineScheduleSpecs() {
				require.Empty(t, spec.Variables, "canonical schedules must work under no_one_allowed")
				fc.PipelineSchedules["acme/widgets"] = append(fc.PipelineSchedules["acme/widgets"], forge.PipelineSchedule{
					ID: int64(i + 1), Description: spec.Description, Active: i == 0,
					Variables: map[string]string{forge.VarPollMode: "legacy"},
				})
			}
			if operation == "install" {
				cfg := baseCfg()
				cfg.Forge = ForgeGitLab
				_, err := Install(context.Background(), cfg, fc, landingScaffoldCommit(fc, cfg.Owner, cfg.Repo), noopProgress)
				require.NoError(t, err)
			} else {
				// The wrapper, agent, and poll templates are seeded
				// directly into FileContents (not passed as "pending"
				// below), representing a root CI contract and its
				// sibling scaffold files already committed on the
				// default branch in an earlier run — i.e. nothing is
				// left to commit before activation is safe. Activation
				// confirms all three (gitlabPollAndAgentTemplatesLanded),
				// not just the wrapper.
				wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
				require.NoError(t, err)
				fc.FileContents["acme/widgets/"+fullsendPipelineInclude] = wrapper
				agentTemplate, err := scaffold.GitLabPerRepoFile(fullsendAgentTemplatePath)
				require.NoError(t, err)
				fc.FileContents["acme/widgets/"+fullsendAgentTemplatePath] = agentTemplate
				pollTemplate, err := scaffold.GitLabPerRepoFile(fullsendPollTemplatePath)
				require.NoError(t, err)
				fc.FileContents["acme/widgets/"+fullsendPollTemplatePath] = pollTemplate
				// The root .gitlab-ci.yml must also already be committed
				// with the typed contract declared and forwarded — activation
				// defers otherwise (gitlabRootDeclaresDispatchInputs).
				mergedRoot, err := MergeGitLabCI(nil)
				require.NoError(t, err)
				fc.FileContents["acme/widgets/.gitlab-ci.yml"] = mergedRoot
				resolved := ResolvedConfig{Owner: "acme", Repo: "widgets", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}}
				dryRun := operation == "dry-run"
				_, actions := convergeGitLabRootCIFiles(context.Background(), resolved, ConvergeConfig{DryRun: dryRun}, noopProgress, nil)
				for _, action := range actions {
					require.NotEqual(t, "error", action.Action, action.Detail)
				}
				if !dryRun {
					// Mirrors convergeRepo's "nothing needed to be
					// committed" branch: convergeGitLabRootCIFiles only
					// validates, so the real activation is a separate,
					// explicit step.
					for _, a := range activateGitLabTypedDispatch(context.Background(), fc, resolved.Owner, resolved.Repo) {
						require.NotEqual(t, "error", a.Action, a.Detail)
					}
				}
			}
			for i, schedule := range fc.PipelineSchedules["acme/widgets"] {
				assert.Equal(t, i == 0, schedule.Active, "migration must preserve disabled schedules")
				if operation == "dry-run" {
					assert.NotEmpty(t, schedule.Variables)
				} else {
					assert.Empty(t, schedule.Variables)
				}
			}
		})
	}
}

// TestTypedActivationNotAppliedBeforeTemplatesLand guards the ordering
// bug where typed-dispatch activation (migrating managed schedule
// variables, enforcing the pipeline-variable override restriction)
// mutated live project state based on a wrapper that was only queued for
// commit, not yet confirmed on the protected default branch.
// ScaffoldCommitFunc can report success after only opening an upgrade MR
// (branch-protection fallback) rather than landing a direct commit, and
// can fail outright; either way, activation must not run until a later
// check confirms compatible templates actually landed, or a legacy
// poller is left stranded behind a restriction it can't satisfy.
//
// It also guards the companion post-install schedule-creation bug: unlike
// activation's live re-check, fresh-install schedule creation
// (setupGitLabPipelineSchedules, in internal/cli) must select transport
// from the pending wrapper this install queued (InstallResult.
// GitLabTypedDispatch), not a live re-read of the default branch — that
// read would see the prior (or, for a brand-new repo, absent) wrapper
// while the init MR is unmerged and wrongly create legacy
// FULLSEND_POLL_MODE schedule variables for what is actually a typed
// target.
func TestTypedActivationNotAppliedBeforeTemplatesLand(t *testing.T) {
	for _, scenario := range []string{"delivery failure", "unmerged upgrade MR"} {
		t.Run(scenario, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{
				ID: 1, Description: PipelineScheduleSpecs()[0].Description, Active: true,
				Variables: map[string]string{forge.VarPollMode: "legacy"},
			}}

			cfg := baseCfg()
			cfg.Forge = ForgeGitLab

			var commit ScaffoldCommitFunc
			if scenario == "delivery failure" {
				commit = func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
					return errors.New("branch protected: push rejected")
				}
			} else {
				// Reports success without writing anything, simulating a
				// branch-protection PR fallback that commitScaffold
				// opened successfully but that a human hasn't merged yet.
				commit = func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
					return nil
				}
			}

			result, err := Install(context.Background(), cfg, fc, commit, noopProgress)
			if scenario == "delivery failure" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.NotNil(t, result)
			assert.True(t, result.GitLabTypedDispatch,
				"the queued scaffold is the typed contract regardless of whether delivery landed, so post-install schedule setup must create variable-free schedules, not legacy ones derived from a live re-read")

			require.Len(t, fc.PipelineSchedules["acme/widgets"], 1)
			assert.Equal(t, map[string]string{forge.VarPollMode: "legacy"}, fc.PipelineSchedules["acme/widgets"][0].Variables,
				"schedule must not be migrated until compatible templates are confirmed on the default branch")
		})
	}
}

// TestActivationDefersOnStaleAgentOrPollTemplate guards the remaining gap
// in the typed-dispatch activation gate: GitLabUsesTypedDispatch only
// inspects the wrapper (fullsend-pipeline.yml). A repo can have an
// already-committed, valid typed wrapper while the sibling agent or poll
// template is stale — e.g. an unmerged repair MR that only touches one of
// those files. activateGitLabTypedDispatch must not migrate schedules or
// enforce the pipeline-variable restriction in that state; it must defer
// until a later run observes both sibling templates landed.
func TestActivationDefersOnStaleAgentOrPollTemplate(t *testing.T) {
	agentTemplate, err := scaffold.GitLabPerRepoFile(fullsendAgentTemplatePath)
	require.NoError(t, err)
	pollTemplate, err := scaffold.GitLabPerRepoFile(fullsendPollTemplatePath)
	require.NoError(t, err)
	staleAgentTemplate := []byte(strings.ReplaceAll(string(agentTemplate), "event_payload_chunk_00", "event_payload_record"))
	stalePollTemplate := []byte(strings.ReplaceAll(string(pollTemplate), "fullsend slash poll", "something else"))
	// A complete spec:inputs header (every required name still declared)
	// but a blanked-out EVENT_PAYLOAD_CHUNK_00 job-variable bridge — e.g.
	// an unmerged repair MR that only touched the declarations, not the
	// variables: block. gitlabWrapperHasDispatchInputs alone would treat
	// this as landed; gitlabAgentBridgesDispatchInputs must catch the
	// broken bridge so activation keeps deferring (#7850 review).
	unbridgedChunkAgentTemplate := []byte(strings.ReplaceAll(string(agentTemplate),
		"value: $[[ inputs.event_payload_chunk_00 ]]", "value: ''"))
	require.NotEqual(t, string(agentTemplate), string(unbridgedChunkAgentTemplate), "fixture must actually differ from the valid template")

	for _, tc := range []struct {
		name    string
		agent   []byte
		poll    []byte
		missing bool
	}{
		{name: "stale agent template", agent: staleAgentTemplate, poll: pollTemplate},
		{name: "stale poll template", agent: agentTemplate, poll: stalePollTemplate},
		{name: "agent template not yet delivered", agent: nil, poll: pollTemplate, missing: true},
		{name: "complete header but unbridged payload-chunk variable", agent: unbridgedChunkAgentTemplate, poll: pollTemplate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			root, err := scaffold.GitLabPerRepoFile(".gitlab-ci.yml")
			require.NoError(t, err)
			wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
			require.NoError(t, err)
			// Seed a landed, compatible root so this test exercises the
			// intended sibling agent/poll-template checks in
			// gitlabPollAndAgentTemplatesLanded rather than tripping the
			// separate (and here irrelevant) missing-root deferral in
			// gitlabRootDeclaresDispatchInputs.
			fc.FileContents["acme/widgets/.gitlab-ci.yml"] = root
			fc.FileContents["acme/widgets/"+fullsendPipelineInclude] = wrapper
			fc.FileContents["acme/widgets/"+fullsendPollTemplatePath] = tc.poll
			if !tc.missing {
				fc.FileContents["acme/widgets/"+fullsendAgentTemplatePath] = tc.agent
			}
			fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{
				ID: 1, Description: PipelineScheduleSpecs()[0].Description, Active: true,
				Variables: map[string]string{forge.VarPollMode: "legacy"},
			}}

			for _, a := range activateGitLabTypedDispatch(context.Background(), fc, "acme", "widgets") {
				require.NotEqual(t, "error", a.Action, a.Detail)
			}
			assert.Equal(t, forge.PipelineVarOverrideNoOneAllowed, fc.PipelineVarOverrideRoles["acme/widgets"],
				"restriction was already no_one_allowed and must not be touched by a deferred activation")
			assert.Equal(t, map[string]string{forge.VarPollMode: "legacy"}, fc.PipelineSchedules["acme/widgets"][0].Variables,
				"schedules must not be migrated while a sibling agent/poll template is stale or missing")
		})
	}
}

func TestScheduleMigrationFailsClosed(t *testing.T) {
	for _, method := range []string{"ListPipelineSchedules", "GetPipelineSchedule", "DeletePipelineScheduleVariable", "user-owned variable"} {
		t.Run(method, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{ID: 1, Description: PipelineScheduleSpecs()[0].Description, Variables: map[string]string{forge.VarPollMode: "slash"}}}
			if method == "user-owned variable" {
				fc.PipelineSchedules["acme/widgets"][0].Variables["KEEP"] = "value"
			} else {
				fc.Errors[method] = errors.New("API denied")
			}
			err := requireGitLabPipelineVariableRestriction(context.Background(), fc, "acme", "widgets", false)
			require.Error(t, err)
			assert.NotEmpty(t, fc.PipelineSchedules["acme/widgets"][0].Variables)
		})
	}
}

// A failed or merely planned restriction write must never leave newly
// delivered runnable templates under a weaker setting.
func TestRestrictionRequiredBeforeScaffoldDelivery(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.PipelineVarOverrideRoles["acme/widgets"] = forge.PipelineVarOverrideDeveloper
	fc.Errors["SetPipelineVariablesMinimumOverrideRole"] = errors.New("GitLab API unavailable")
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")

	cfg := baseCfg()
	cfg.Forge = ForgeGitLab

	committed := false
	_, err := Install(context.Background(), cfg, fc, func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
		committed = true
		return nil
	}, noopProgress)
	require.ErrorContains(t, err, "refusing to deliver runnable typed GitLab jobs")
	assert.False(t, committed, "neither direct delivery nor an asynchronous MR may be queued")
	assert.Empty(t, fc.Variables)
	assert.Empty(t, fc.CreatedSecrets)
	assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["acme/widgets"],
		"preflight must not change a legacy project's setting")
}

func TestUnmergeRetainsMatchingUserInputReferences(t *testing.T) {
	for _, reference := range []string{"$[[ inputs.event_type ]]", "$[[ inputs.event_type | expand_vars ]]", "$[[inputs.event_type]]"} {
		original := []byte("spec:\n  inputs:\n    event_type: {default: ''}\n---\nuser-job:\n  script: 'echo " + reference + "'\n")
		merged, err := MergeGitLabCI(original)
		require.NoError(t, err)
		cleaned, err := UnmergeGitLabCI(merged)
		require.NoError(t, err)
		docs, err := decodeGitLabDocuments(cleaned)
		require.NoError(t, err)
		require.Len(t, docs, 2)
		assert.NotNil(t, findMappingValue(findMappingValue(findMappingValue(docs[0].Content[0], "spec"), "inputs"), "event_type"))
		assert.Contains(t, string(cleaned), reference)
		assert.NotContains(t, string(cleaned), fullsendPipelineInclude)
	}
}

func TestUnmergePreservesAnchorsStillAliasedByUserConfig(t *testing.T) {
	original := []byte("variables:\n  STAGE: &stage_bridge '$[[ inputs.stage ]]'\n  USER_STAGE: *stage_bridge\n" +
		"user-job:\n  script: echo $USER_STAGE\n")
	merged, err := MergeGitLabCI(original)
	require.NoError(t, err)
	cleaned, err := UnmergeGitLabCI(merged)
	require.NoError(t, err)

	docs, err := decodeGitLabDocuments(cleaned)
	require.NoError(t, err, "cleaned output must remain valid YAML:\n%s", cleaned)
	require.Len(t, docs, 2)
	variables := findMappingValue(docs[1].Content[0], "variables")
	require.NotNil(t, variables)
	assert.NotNil(t, findMappingValue(variables, "STAGE"), "anchored bridge still aliased by USER_STAGE must be preserved")
	assert.NotNil(t, findMappingValue(variables, "USER_STAGE"))
	inputs := findMappingValue(findMappingValue(docs[0].Content[0], "spec"), "inputs")
	assert.NotNil(t, findMappingValue(inputs, "stage"))
	assert.NotContains(t, string(cleaned), fullsendPipelineInclude)
}

func TestGitLabOnlyPinnedTargetFailsWithoutMatchingClient(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	d := convergeDiscovery{repo: ResolvedRepo{Owner: "acme", Repo: "api"}, resolved: ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, FullsendRef: "v0.1.0", ForgeConfig: ForgeConfig{Client: fc}}}
	sc := &fakeScaffoldCommit{}
	result := convergeRepo(context.Background(), d, "", ConvergeConfig{}, nil, sc.fn(), noopProgress)
	require.ErrorContains(t, result.Error, "upstream GitHub client")
	assert.False(t, sc.called)
	assert.Empty(t, fc.Secrets)
	_, err := ExpectedScaffoldContent(context.Background(), d.resolved, DriftConfig{}, nil)
	require.ErrorContains(t, err, "matching pinned GitLab templates")
}

// TestGitLabOnlyReleaseDefaultRefDoesNotRequireClient guards a second
// invocation of a GitLab-only convergence (no GitHub client configured)
// after a first successful unpinned install already wrote the resolved
// release-default ref back into the manifest as gitlab.fullsend_ref
// (runReposInstall). resolved.FullsendRef is no longer empty on this
// second run, but it still matches the running release's own
// UpstreamRef/UpstreamTag, so it must not be treated like an explicit
// pin to a different release requiring an upstream GitHub client —
// contrast with TestGitLabOnlyPinnedTargetFailsWithoutMatchingClient,
// where FullsendRef genuinely differs.
func TestGitLabOnlyReleaseDefaultRefDoesNotRequireClient(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	d := convergeDiscovery{repo: ResolvedRepo{Owner: "acme", Repo: "api"}, resolved: ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, FullsendRef: "v1.0.0", ForgeConfig: ForgeConfig{Client: fc}}}
	sc := &fakeScaffoldCommit{}
	cfg := ConvergeConfig{UpstreamRef: "v1.0.0", UpstreamTag: "v1.0.0"}
	result := convergeRepo(context.Background(), d, "", cfg, nil, sc.fn(), noopProgress)
	if result.Error != nil && strings.Contains(result.Error.Error(), "upstream GitHub client") {
		t.Fatalf("a FullsendRef matching the running release's own UpstreamRef/UpstreamTag must not require a GitHub client, got: %v", result.Error)
	}
	assert.True(t, sc.called, "convergence must proceed to scaffold delivery instead of being rejected by the GitHub-client guard")
}

func TestGitLabInstallRejectsUnmatchedPinsAndVendor(t *testing.T) {
	for _, vendor := range []bool{false, true} {
		cfg := baseCfg()
		cfg.Forge = ForgeGitLab
		cfg.UpstreamRef = "v0.1.0"
		// Pinned marks this ref as an explicit manifest pin (as opposed to
		// a release-default ref with no pin), which is what requires
		// matching PrebuiltScaffoldFiles below.
		cfg.Pinned = true
		cfg.VendorBinary = vendor
		fc := newFakeClientWithRepo()
		sc := &fakeScaffoldCommit{}
		_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
		require.Error(t, err)
		assert.False(t, sc.called)
		assert.Empty(t, fc.Secrets)
	}
}

func TestInstalledTypedContractMustSupportLiteralScheduledDispatch(t *testing.T) {
	fc := forge.NewFakeClient()
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = wrapper
	typed, err := GitLabUsesTypedDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.True(t, typed)
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(strings.ReplaceAll(string(wrapper), "# fullsend-input-contract: literal-scheduled-v2", ""))
	_, err = GitLabUsesTypedDispatch(context.Background(), fc, "acme", "api")
	require.ErrorContains(t, err, "upgrade the pin")
	fc.Errors["GetFileContent"] = errors.New("denied")
	_, err = GitLabUsesTypedDispatch(context.Background(), fc, "acme", "api")
	require.ErrorContains(t, err, "denied")
}

// TestInstalledTypedWrapperMustForwardEveryDispatchInput guards the
// wrapper→agent include hop: a wrapper that keeps its input declarations
// and contract marker but forwards a payload chunk as a blank or stale
// value must be rejected as incompatible, not treated as a valid typed
// wrapper, so activation cannot migrate schedules before the repair lands.
func TestInstalledTypedWrapperMustForwardEveryDispatchInput(t *testing.T) {
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	good := "event_payload_chunk_00: $[[ inputs.event_payload_chunk_00 ]]"
	require.Contains(t, string(wrapper), good)

	for name, forwarded := range map[string]string{
		"blank chunk":     "event_payload_chunk_00: ''",
		"mismatched name": "event_payload_chunk_00: $[[ inputs.event_payload_chunk_01 ]]",
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			broken := strings.Replace(string(wrapper), good, forwarded, 1)
			require.NotEqual(t, string(wrapper), broken)
			fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(broken)
			_, err := GitLabUsesTypedDispatch(context.Background(), fc, "acme", "api")
			require.ErrorIs(t, err, errGitLabIncompatibleWrapper)
			require.ErrorContains(t, err, "forward every dispatch input")
		})
	}

	t.Run("missing forwarding key", func(t *testing.T) {
		fc := forge.NewFakeClient()
		broken := strings.Replace(string(wrapper), "      "+good+"\n", "", 1)
		require.NotEqual(t, string(wrapper), broken)
		fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(broken)
		_, err := GitLabUsesTypedDispatch(context.Background(), fc, "acme", "api")
		require.ErrorIs(t, err, errGitLabIncompatibleWrapper)
	})
}

// TestConvergeSchedulesGatesOnRestrictionBeforeMutating guards the ordering
// bug where convergeSchedules created missing schedules and reactivated
// disabled ones for a typed installation before convergeGitLabRootCIFiles'
// later live-restriction check could report a weaker-than-required policy.
// Those schedule mutations were not rolled back, so a scheduled pipeline
// could resume credential-bearing polling under the weaker policy ahead of
// the required no_one_allowed restriction. convergeSchedules must now
// check the restriction itself, before either mutation, when the
// effective wrapper is already typed.
func TestConvergeSchedulesGatesOnRestrictionBeforeMutating(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = wrapper
	// The agent/poll templates and root CI contract must already be
	// landed so this test isolates the restriction gate rather than
	// tripping the (now nonfatal) template-readiness deferral checked
	// earlier in convergeSchedules.
	agentTemplate, err := scaffold.GitLabPerRepoFile(fullsendAgentTemplatePath)
	require.NoError(t, err)
	fc.FileContents["acme/api/"+fullsendAgentTemplatePath] = agentTemplate
	pollTemplate, err := scaffold.GitLabPerRepoFile(fullsendPollTemplatePath)
	require.NoError(t, err)
	fc.FileContents["acme/api/"+fullsendPollTemplatePath] = pollTemplate
	mergedRoot, err := MergeGitLabCI(nil)
	require.NoError(t, err)
	fc.FileContents["acme/api/.gitlab-ci.yml"] = mergedRoot
	// Weaker than the no_one_allowed typed activation requires.
	fc.PipelineVarOverrideRoles["acme/api"] = forge.PipelineVarOverrideDeveloper
	specs := PipelineScheduleSpecs()
	require.True(t, len(specs) >= 2, "test assumes at least one disabled and one missing schedule can be modeled")
	fc.PipelineSchedules["acme/api"] = []forge.PipelineSchedule{
		{ID: 1, Description: specs[0].Description, Active: false},
	}
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}}
	components := []ComponentStatus{
		{Name: specs[0].ComponentName, Present: true},  // disabled — reactivation candidate
		{Name: specs[1].ComponentName, Present: false}, // missing — creation candidate
	}

	actions := convergeSchedules(context.Background(), resolved, components, false, true, noopProgress)

	require.NotEmpty(t, actions)
	for _, a := range actions {
		assert.Equal(t, "error", a.Action, a.Detail)
	}
	assert.Empty(t, fc.UpdatedScheduleIDs, "must not reactivate a disabled schedule before the restriction is established")
	assert.Len(t, fc.PipelineSchedules["acme/api"], 1, "must not create a missing schedule before the restriction is established")
	assert.False(t, fc.PipelineSchedules["acme/api"][0].Active, "the pre-existing disabled schedule must remain untouched")
}
