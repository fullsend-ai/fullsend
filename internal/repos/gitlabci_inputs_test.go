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
	"gopkg.in/yaml.v3"
)

func inputTestMapping(t *testing.T, content string) *yaml.Node {
	t.Helper()
	docs, err := decodeGitLabDocuments([]byte(content))
	require.NoError(t, err)
	require.Len(t, docs, 1)
	return docs[0].Content[0]
}

func TestGitLabInputMergeUnmergePreservesUserPipeline(t *testing.T) {
	for _, include := range []string{
		"include: .gitlab/ci/fullsend-pipeline.yml",
		"include:\n  - .gitlab/ci/fullsend-pipeline.yml\n  - local: user.yml",
		"include:\n  - local: .gitlab/ci/fullsend-pipeline.yml\n    inputs: {stage: old}\n  - local: user.yml",
	} {
		t.Run(include, func(t *testing.T) {
			original := include + `
stages: [build]
variables:
  USER_SETTING: keep
workflow:
  name: user pipeline
  rules:
    - if: $CI_PIPELINE_SOURCE == "push"
build-job:
  stage: build
  script: echo user
`
			merged, err := MergeGitLabCI([]byte(original))
			require.NoError(t, err)
			docs, err := decodeGitLabDocuments(merged)
			require.NoError(t, err)
			require.Len(t, docs, 2)
			root := docs[1].Content[0]
			includes := findMappingValue(root, "include")
			require.NotNil(t, includes)
			forwarding := findMappingValue(includes.Content[0], "inputs")
			require.NotNil(t, forwarding, "scalar and mapping includes must both forward inputs")
			assert.Len(t, forwarding.Content, 2*len(gitlabDispatchInputNames))
			assert.True(t, HasFullsendEntries(merged))
			again, err := MergeGitLabCI(merged)
			require.NoError(t, err)
			assert.Equal(t, string(merged), string(again), "migration must be idempotent")
			cleaned, err := UnmergeGitLabCI(merged)
			require.NoError(t, err)
			docs, err = decodeGitLabDocuments(cleaned)
			require.NoError(t, err)
			if strings.Contains(include, "user.yml") {
				// UnmergeGitLabCI cannot fetch and scan the surviving
				// user.yml include for a $STAGE consumer, so it
				// conservatively preserves the STAGE bridge and its
				// backing "stage" input rather than guessing — see the
				// review finding on included-file consumers.
				require.Len(t, docs, 2)
				header := docs[0].Content[0]
				stageInput := findMappingValue(findMappingValue(findMappingValue(header, "spec"), "inputs"), "stage")
				assert.NotNil(t, stageInput, "stage input must be preserved while an uninspected include survives")
				root = docs[1].Content[0]
				assert.Contains(t, string(cleaned), "$[[ inputs.stage ]]")
				assert.Contains(t, string(cleaned), "user.yml")
			} else {
				require.Len(t, docs, 1)
				root = docs[0].Content[0]
				assert.NotContains(t, string(cleaned), "inputs.stage")
			}
			assert.NotNil(t, findMappingValue(root, "build-job"))
			assert.Equal(t, "keep", findMappingValue(findMappingValue(root, "variables"), "USER_SETTING").Value)
			assert.Equal(t, "user pipeline", findMappingValue(findMappingValue(root, "workflow"), "name").Value)
			assert.NotContains(t, string(cleaned), fullsendPipelineInclude)
		})
	}
}

func TestMergeSpecInputsPreservesUserSettingsAndValidatesContract(t *testing.T) {
	fixture := "spec:\n  inputs:\n    stage: {type: string, default: ''}\n"
	for _, tc := range []struct {
		name, existing, errorText string
	}{
		{"missing spec", "other: keep", ""},
		{"missing inputs", "spec: {description: keep}", ""},
		{"unrelated input", "spec:\n  description: keep\n  inputs:\n    environment: {default: staging}", ""},
		{"matching collision", fixture, ""},
		{"conflicting collision", "spec:\n  inputs:\n    stage: {default: user}", "conflicts"},
		{"invalid header", "[one, two]", "header must be a mapping"},
		{"invalid spec", "spec: scalar", "spec: must be a mapping"},
		{"invalid inputs", "spec: {inputs: []}", "spec:inputs must be a mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := inputTestMapping(t, tc.existing)
			err := mergeSpecInputs(existing, inputTestMapping(t, fixture))
			if tc.errorText != "" {
				require.ErrorContains(t, err, tc.errorText)
				return
			}
			require.NoError(t, err)
			spec := findMappingValue(existing, "spec")
			assert.NotNil(t, findMappingValue(findMappingValue(spec, "inputs"), "stage"))
			if strings.Contains(tc.existing, "description") {
				assert.Equal(t, "keep", findMappingValue(spec, "description").Value)
			}
			if strings.Contains(tc.existing, "environment") {
				assert.NotNil(t, findMappingValue(findMappingValue(spec, "inputs"), "environment"))
			}
		})
	}
	_, err := MergeGitLabCI([]byte("spec:\n  inputs:\n    environment: {default: staging}\n---\njob: {script: 'echo $[[ inputs.environment ]]'}\n"))
	require.ErrorContains(t, err, "maximum is 20")
}

func TestUnmergeGitLabCIKeepsUnrelatedHeaderAndBody(t *testing.T) {
	cleaned, err := UnmergeGitLabCI([]byte(`spec:
  description: user-owned
  inputs:
    environment: {default: staging}
    stage: {default: ''}
---
include: .gitlab/ci/fullsend-pipeline.yml
variables: {STAGE: '$[[ inputs.stage ]]', KEEP: value}
job: {script: echo keep}
`))
	require.NoError(t, err)
	assert.Contains(t, string(cleaned), "environment:")
	assert.Contains(t, string(cleaned), "user-owned")
	assert.Contains(t, string(cleaned), "echo keep")
	assert.Contains(t, string(cleaned), "KEEP: value")
	assert.Contains(t, string(cleaned), "stage:", "an unrelated user declaration with a managed name must survive")
	assert.NotContains(t, string(cleaned), "inputs.stage")
	cleaned, err = UnmergeGitLabCI([]byte("spec: {inputs: {environment: {default: staging}}}\n---\ninclude: .gitlab/ci/fullsend-pipeline.yml\n"))
	require.NoError(t, err)
	assert.Contains(t, string(cleaned), "environment", "a surviving user header must not be deleted with an empty body")
}

func TestMergeGitLabCIFailsForInvalidContracts(t *testing.T) {
	for _, tc := range []struct{ source, errorText string }{
		{"variables: []", "variables: is not a mapping"},
		{"variables: {STAGE: custom}", "STAGE variable conflicts"},
		{"[one, two]", "pipeline body must be a mapping"},
		{"job: [", "parsing existing"},
		{"spec: {}\n---\nother: {}\n---\njob: {}", "one pipeline body"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			_, err := MergeGitLabCI([]byte(tc.source))
			require.ErrorContains(t, err, tc.errorText)
		})
	}
	_, err := mergeGitLabCIWithWrapper([]byte("job: {}"), []byte("include: ["))
	require.ErrorContains(t, err, "parsing GitLab wrapper")
}

// TestMergeGitLabCIAcceptsExtendedStageBridge covers the review finding that
// the root STAGE compatibility check compared the variable node's scalar
// Value directly, which is always empty on a mapping node. That rejected a
// compatible extended variable declaration
// (STAGE: {value: '$[[ inputs.stage ]]', expand: false}) as a conflict, even
// though gitlabVariableBridgesInput already recognizes both the plain-scalar
// and extended forms as equivalent bridges.
func TestMergeGitLabCIAcceptsExtendedStageBridge(t *testing.T) {
	merged, err := MergeGitLabCI([]byte("variables:\n  STAGE: {value: '$[[ inputs.stage ]]', expand: false}\n  KEEP: value\n"))
	require.NoError(t, err)
	docs, err := decodeGitLabDocuments(merged)
	require.NoError(t, err)
	root := docs[len(docs)-1].Content[0]
	variables := findMappingValue(root, "variables")
	require.NotNil(t, variables)
	stage := findMappingValue(variables, "STAGE")
	require.NotNil(t, stage)
	assert.Equal(t, yaml.MappingNode, stage.Kind, "a compatible extended STAGE declaration must be preserved, not replaced with a plain scalar")
	assert.True(t, gitlabVariableBridgesInput(variables, "STAGE", "stage"))
	expand := findMappingValue(stage, "expand")
	require.NotNil(t, expand)
	assert.Equal(t, "false", expand.Value, "the preserved extended STAGE declaration must keep its expand: false setting")
	assert.Equal(t, "value", findMappingValue(variables, "KEEP").Value)
}

func TestConvergeGitLabRootUsesEffectiveContract(t *testing.T) {
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	legacy := []byte("include:\n  - local: .gitlab/ci/fullsend-dispatch.yml\n  - local: .gitlab/ci/fullsend-agent.yml\nstages: [dispatch, agent]\n")
	for _, tc := range []struct {
		name                                  string
		pending, dryRun, blocked, unsupported bool
	}{
		{name: "queued wrapper", pending: true},
		{name: "queued dry run", pending: true, dryRun: true},
		{name: "pinned old wrapper"},
		{name: "unsafe override role", pending: true, blocked: true},
		{name: "unsupported override restriction", pending: true, unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.FileContents["acme/api/"+fullsendPipelineInclude] = legacy
			fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte("include: .gitlab/ci/fullsend-pipeline.yml\nstages: [build, dispatch, poll, agent]\njob: {stage: build, script: echo keep}\n")
			if tc.blocked {
				delete(fc.PipelineVarOverrideRoles, "acme/api")
			}
			if tc.unsupported {
				fc.Errors["GetPipelineVariablesMinimumOverrideRole"] = forge.ErrNotSupported
			}
			var pending []forge.TreeFile
			if tc.pending {
				pending = []forge.TreeFile{{Path: fullsendPipelineInclude, Content: wrapper}}
			}
			resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}}
			files, actions := convergeGitLabRootCIFiles(context.Background(), resolved, ConvergeConfig{DryRun: tc.dryRun}, noopProgress, pending)
			if tc.blocked || tc.unsupported {
				require.Empty(t, files)
				require.NotEmpty(t, actions)
				assert.Equal(t, "error", actions[len(actions)-1].Action)
			} else if !tc.pending {
				assert.Empty(t, files, "legacy pinned wrapper must not acquire typed root inputs or lose its dispatch stage")
			} else if tc.dryRun {
				assert.Empty(t, files)
				assert.NotEmpty(t, actions)
			} else {
				require.Len(t, files, 1)
				assert.Contains(t, string(files[0].Content), "inputs.stage")
				assert.Contains(t, string(files[0].Content), "echo keep")
				assert.NotContains(t, string(files[0].Content), "- dispatch")
			}
		})
	}
}

// TestConvergeGitLabRootRejectsTypedToLegacyTransition guards a forced
// typed-to-legacy transition: the committed root already carries the
// typed spec:inputs header and include:inputs forwarding map from a
// prior typed install, but the effective wrapper for this convergence
// (e.g. after re-pinning to an older release) is legacy and declares
// none of those inputs. convergeGitLabRootCIFiles must refuse rather
// than silently deliver the legacy wrapper alongside an untouched typed
// root, which would leave an invalid include (the root forwards inputs
// the legacy wrapper's include never declares). The project already
// being restricted (no_one_allowed) and running variable-free schedules
// — the state a prior typed activation would have left — must not mask
// the rejection.
func TestConvergeGitLabRootRejectsTypedToLegacyTransition(t *testing.T) {
	typedRoot, err := MergeGitLabCI([]byte("job: {stage: build, script: echo keep}\n"))
	require.NoError(t, err)
	legacyWrapper := []byte("include:\n  - local: .gitlab/ci/fullsend-dispatch.yml\n  - local: .gitlab/ci/fullsend-agent.yml\nstages: [dispatch, agent]\n")

	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/.gitlab-ci.yml"] = typedRoot
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = legacyWrapper
	fc.PipelineVarOverrideRoles["acme/api"] = forge.PipelineVarOverrideNoOneAllowed
	for i, spec := range PipelineScheduleSpecs() {
		fc.PipelineSchedules["acme/api"] = append(fc.PipelineSchedules["acme/api"], forge.PipelineSchedule{
			ID: int64(i + 1), Description: spec.Description,
		})
	}

	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}}
	files, actions := convergeGitLabRootCIFiles(context.Background(), resolved, ConvergeConfig{}, noopProgress, nil)
	assert.Empty(t, files)
	require.NotEmpty(t, actions)
	last := actions[len(actions)-1]
	assert.Equal(t, "error", last.Action)
	assert.Contains(t, last.Detail, "typed-to-legacy")
}

func TestEffectiveGitLabWrapperReadFailures(t *testing.T) {
	fc := forge.NewFakeClient()
	content, err := effectiveGitLabWrapper(context.Background(), fc, "acme", "api", nil)
	require.NoError(t, err)
	assert.Nil(t, content)
	fc.Errors["GetFileContent"] = errors.New("permission denied")
	_, err = effectiveGitLabWrapper(context.Background(), fc, "acme", "api", nil)
	require.ErrorContains(t, err, "permission denied")
	for _, content := range []string{"spec: [", "include: []", "spec: []\n---\ninclude: []", "spec: {inputs: []}\n---\ninclude: []", "spec: {inputs: {stage: {}}}\n---\ninclude: []"} {
		assert.False(t, gitlabWrapperHasDispatchInputs([]byte(content)))
	}
}

func TestInstallGitLabRejectsUnsafeActivationBeforeScaffoldCommit(t *testing.T) {
	for _, mode := range []string{"", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(GitLabPipelineVarRestrictionEnv, mode)
			fc := newFakeClientWithRepo()
			delete(fc.PipelineVarOverrideRoles, "acme/widgets")
			cfg := baseCfg()
			cfg.Forge = ForgeGitLab
			sc := &fakeScaffoldCommit{}
			_, err := Install(context.Background(), cfg, fc, sc.fn(), noopProgress)
			require.ErrorContains(t, err, "refusing")
			assert.False(t, sc.called)
			assert.Empty(t, fc.Secrets, "must not deploy credentials with an unsafe root contract")
		})
	}
}

func TestConvergeGitLabRootMigrationErrors(t *testing.T) {
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	for _, tc := range []struct{ name, source, readPath string }{
		{name: "conflicting stage", source: "include: .gitlab/ci/fullsend-pipeline.yml\nvariables: {STAGE: user}\n"},
		{name: "wrapper unreadable", source: "include: .gitlab/ci/fullsend-pipeline.yml", readPath: fullsendPipelineInclude},
		{name: "root unreadable", readPath: ".gitlab-ci.yml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.FileContents["acme/api/.gitlab-ci.yml"] = []byte(tc.source)
			fc.FileContents["acme/api/"+fullsendPipelineInclude] = wrapper
			if tc.readPath != "" {
				fc.GetFileContentErrors = map[string]error{"acme/api/" + tc.readPath: errors.New("permission denied")}
			}
			resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}}
			files, actions := convergeGitLabRootCIFiles(context.Background(), resolved, ConvergeConfig{}, noopProgress, nil)
			require.Empty(t, files)
			require.NotEmpty(t, actions)
			assert.Equal(t, "error", actions[len(actions)-1].Action)
		})
	}
}

func TestConvergeGitLabRootMissingStillGatesTypedActivation(t *testing.T) {
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = wrapper
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}}
	files, actions := convergeGitLabRootCIFiles(context.Background(), resolved, ConvergeConfig{}, noopProgress, nil)
	require.Len(t, files, 1)
	assert.Contains(t, string(files[0].Content), "inputs.stage")
	assert.NotEmpty(t, actions)
	delete(fc.PipelineVarOverrideRoles, "acme/api")
	files, actions = convergeGitLabRootCIFiles(context.Background(), resolved, ConvergeConfig{}, noopProgress, nil)
	assert.Empty(t, files)
	require.NotEmpty(t, actions)
	assert.Equal(t, "error", actions[len(actions)-1].Action)
}

func TestGitLabPipelineRestrictionSettingFailure(t *testing.T) {
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{{ComponentName: "schedule:slash-poll"}})
	fc := forge.NewFakeClient()
	fc.Errors["SetPipelineVariablesMinimumOverrideRole"] = errors.New("permission denied")
	err := requireGitLabPipelineVariableRestriction(context.Background(), fc, "acme", "api", false)
	require.ErrorContains(t, err, "setting ci_pipeline_variables_minimum_override_role")
	assert.Empty(t, fc.PipelineVarOverrideRoles)
}

// incompleteTypedWrapper returns the embedded typed wrapper with one
// required dispatch input removed, simulating an alternate or damaged
// typed contract that is neither the supported legacy (variable-based)
// wrapper nor a valid typed one.
func incompleteTypedWrapper(t *testing.T) []byte {
	t.Helper()
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	docs, err := decodeGitLabDocuments(wrapper)
	require.NoError(t, err)
	require.Len(t, docs, 2)
	inputs := findMappingValue(findMappingValue(docs[0].Content[0], "spec"), "inputs")
	removeMappingKey(inputs, gitlabDispatchInputNames[len(gitlabDispatchInputNames)-1])
	out, err := marshalGitLabDocuments(docs)
	require.NoError(t, err)
	return out
}

// TestGitLabIncompleteTypedContractIsRejectedNotTreatedAsLegacy guards the
// fail-open bug where typed-transport detection (gitlabWrapperHasDispatchInputs)
// required every one of the 20 current input names, so a wrapper missing
// even one was silently classified the same as a genuinely legacy
// (variable-based) wrapper — skipping compatibility validation and the
// required no_one_allowed restriction instead of being rejected.
func TestGitLabIncompleteTypedContractIsRejectedNotTreatedAsLegacy(t *testing.T) {
	incomplete := incompleteTypedWrapper(t)

	assert.False(t, gitlabWrapperHasDispatchInputs(incomplete), "missing a required input must not count as a complete typed contract")
	assert.True(t, gitlabWrapperHasPartialDispatchInputs(incomplete), "a spec:inputs attempt missing a required name must be flagged partial")
	require.ErrorContains(t, requireCompleteGitLabDispatchContract(incomplete), "incomplete")

	fc := newFakeClientForBatch("acme/api")
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = incomplete
	typed, err := GitLabUsesTypedDispatch(context.Background(), fc, "acme", "api")
	require.ErrorContains(t, err, "incomplete")
	assert.False(t, typed)

	_, err = mergeGitLabCIWithWrapper(nil, incomplete)
	require.ErrorContains(t, err, "incomplete")
}

// TestMergeGitLabCICommentsOnlyRootUsesSuppliedWrapper guards the bug where
// a comments-only (or otherwise entirely empty after decode) existing root
// fell back to newGitLabCI's embedded typed-input default, discarding the
// actually-supplied wrapper. Installing a supported legacy wrapper onto
// such a root must not introduce a typed spec:inputs header the legacy
// wrapper never declares.
func TestMergeGitLabCICommentsOnlyRootUsesSuppliedWrapper(t *testing.T) {
	legacyWrapper := []byte("include: []\n")
	merged, err := mergeGitLabCIWithWrapper([]byte("# just a comment, no actual pipeline content\n"), legacyWrapper)
	require.NoError(t, err)
	assert.Contains(t, string(merged), fullsendPipelineInclude)
	assert.NotContains(t, string(merged), "spec:")
}

// TestActivationDefersUntilRootForwardsDispatchInputs guards the activation
// gap where ActivateGitLabTypedDispatch checked the committed wrapper and
// sibling agent/poll templates but not whether the root .gitlab-ci.yml
// itself declares and forwards the pipeline-input contract. A typed
// wrapper can land while the root migration is still an unmerged repair
// MR; activation must defer rather than migrate schedules and enforce the
// restriction against that incompatible root.
func TestActivationDefersUntilRootForwardsDispatchInputs(t *testing.T) {
	fc := newFakeClientWithRepo()
	for _, path := range []string{fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		fc.FileContents["acme/widgets/"+path] = content
	}
	// No .gitlab-ci.yml committed at all: the root migration is still an
	// unmerged repair MR, even though the wrapper and sibling templates
	// already landed.
	fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{
		ID: 1, Description: PipelineScheduleSpecs()[0].Description,
		Variables: map[string]string{forge.VarPollMode: "legacy"},
	}}

	result, err := ActivateGitLabTypedDispatch(context.Background(), fc, "acme", "widgets", false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", result.Action)
	assert.Equal(t, "legacy", fc.PipelineSchedules["acme/widgets"][0].Variables[forge.VarPollMode],
		"activation must not migrate schedules while the root hasn't landed the forwarding contract")
}

// badlyForwardedRoot returns the embedded root .gitlab-ci.yml with one
// forwarding value blanked out: the include:inputs map still has every
// required key, but event_payload_chunk_00 is forwarded as an empty
// string instead of $[[ inputs.event_payload_chunk_00 ]], silently
// dropping that payload chunk. This simulates an unmerged repair MR that
// added the missing key without fixing its value.
func badlyForwardedRoot(t *testing.T) []byte {
	t.Helper()
	root, err := scaffold.GitLabPerRepoFile(".gitlab-ci.yml")
	require.NoError(t, err)
	docs, err := decodeGitLabDocuments(root)
	require.NoError(t, err)
	require.Len(t, docs, 2)
	body := docs[1].Content[0]
	include := findMappingValue(body, "include")
	items := include.Content
	if include.Kind != yaml.SequenceNode {
		items = []*yaml.Node{include}
	}
	found := false
	for _, item := range items {
		if !isFullsendPipelineInclude(item) {
			continue
		}
		inputs := findMappingValue(item, "inputs")
		value := findMappingValue(inputs, "event_payload_chunk_00")
		require.NotNil(t, value)
		value.Value = ""
		found = true
	}
	require.True(t, found, "embedded root must declare the fullsend pipeline include")
	out, err := marshalGitLabDocuments(docs)
	require.NoError(t, err)
	return out
}

// TestActivationDefersOnBadlyForwardedRootInput guards the gap where
// gitlabRootDeclaresDispatchInputs checked only that each dispatch input
// name was present as a key in the root's include:inputs forwarding map,
// not that its value actually forwarded the root's own matching input. A
// committed root containing all required keys with an empty
// event_payload_chunk_00 value passed even though it drops the first
// payload chunk; activation must keep deferring until the repair that
// fixes the forwarded value actually lands.
func TestActivationDefersOnBadlyForwardedRootInput(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.FileContents["acme/widgets/.gitlab-ci.yml"] = badlyForwardedRoot(t)
	for _, path := range []string{fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath} {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		fc.FileContents["acme/widgets/"+path] = content
	}
	fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{
		ID: 1, Description: PipelineScheduleSpecs()[0].Description,
		Variables: map[string]string{forge.VarPollMode: "legacy"},
	}}

	result, err := ActivateGitLabTypedDispatch(context.Background(), fc, "acme", "widgets", false)
	require.NoError(t, err)
	assert.Equal(t, "deferred", result.Action)
	assert.Equal(t, "legacy", fc.PipelineSchedules["acme/widgets"][0].Variables[forge.VarPollMode],
		"activation must not migrate schedules while the root's forwarding map drops a required input")
}

// TestGitLabAgentBridgeRequiresExplicitNoExpand guards the landed-agent
// check: every dispatch bridge must be an extended mapping with an explicit
// boolean expand: false, otherwise caller-controlled metadata could be
// variable-expanded before dispatch authentication. The root STAGE
// compatibility check (gitlabVariableBridgesInput) stays permissive.
func TestGitLabAgentBridgeRequiresExplicitNoExpand(t *testing.T) {
	agent, err := scaffold.GitLabPerRepoFile(fullsendAgentTemplatePath)
	require.NoError(t, err)
	require.True(t, gitlabAgentBridgesDispatchInputs(agent), "the shipped agent template must satisfy the strict check")

	const bridge = "      value: $[[ inputs.event_payload_chunk_00 ]]\n      expand: false\n"
	require.Contains(t, string(agent), bridge)
	for name, replacement := range map[string]string{
		"scalar bridge":       "      $[[ inputs.event_payload_chunk_00 ]]\n",
		"missing expand":      "      value: $[[ inputs.event_payload_chunk_00 ]]\n",
		"expand true":         "      value: $[[ inputs.event_payload_chunk_00 ]]\n      expand: true\n",
		"string expand false": "      value: $[[ inputs.event_payload_chunk_00 ]]\n      expand: 'false'\n",
	} {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(string(agent), "EVENT_PAYLOAD_CHUNK_00:\n"+bridge, "EVENT_PAYLOAD_CHUNK_00:\n"+replacement, 1)
			if name == "scalar bridge" {
				content = strings.Replace(string(agent), "EVENT_PAYLOAD_CHUNK_00:\n"+bridge, "EVENT_PAYLOAD_CHUNK_00: $[[ inputs.event_payload_chunk_00 ]]\n", 1)
			}
			require.NotEqual(t, string(agent), content, "fixture must differ from the shipped template")
			assert.False(t, gitlabAgentBridgesDispatchInputs([]byte(content)))
		})
	}

	// Root STAGE compatibility remains permissive for scalar and extended forms.
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("STAGE: $[[ inputs.stage ]]\nOTHER: {value: '$[[ inputs.stage ]]'}\n"), &doc))
	variables := doc.Content[0]
	assert.True(t, gitlabVariableBridgesInput(variables, "STAGE", "stage"))
	assert.True(t, gitlabVariableBridgesInput(variables, "OTHER", "stage"))
}
