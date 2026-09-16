package repos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeGitLabCI_NoExistingFile(t *testing.T) {
	result, err := MergeGitLabCI(nil)
	require.NoError(t, err)
	s := string(result)

	assert.Contains(t, s, "fullsend-pipeline.yml")
	assert.Contains(t, s, "workflow:")
	assert.Contains(t, s, "auto_cancel:")
	assert.Contains(t, s, "on_new_commit: none")
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`,
		"native MR dispatch was removed in #7322")
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
}

func TestMergeGitLabCI_EmptyFile(t *testing.T) {
	result, err := MergeGitLabCI([]byte(""))
	require.NoError(t, err)
	assert.Contains(t, string(result), "fullsend-pipeline.yml")
}

func TestMergeGitLabCI_ExistingWithoutWorkflow(t *testing.T) {
	existing := []byte(`---
stages:
  - build
  - test

build:
  stage: build
  script:
    - make build
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Original content preserved.
	assert.Contains(t, s, "stages:")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "- test")
	assert.Contains(t, s, "make build")

	// Fullsend include added.
	assert.Contains(t, s, "fullsend-pipeline.yml")

	// Fullsend stages appended to existing stages array.
	assert.Contains(t, s, "- dispatch")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")

	// No workflow block should be created when none existed.
	assert.NotContains(t, s, "workflow:")
}

func TestMergeGitLabCI_ExistingWithWorkflowRules(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/existing.yml'

workflow:
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
      when: always
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Original include preserved.
	assert.Contains(t, s, "existing.yml")

	// Fullsend include appended.
	assert.Contains(t, s, "fullsend-pipeline.yml")

	// Original workflow rule preserved.
	assert.Contains(t, s, `$CI_COMMIT_BRANCH == "main"`)

	// Merge request rule already exists — should not be duplicated.
	count := strings.Count(s, `$CI_PIPELINE_SOURCE == "merge_request_event"`)
	assert.Equal(t, 1, count, "merge_request_event rule should not be duplicated")

	// Missing fullsend rules appended.
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "api"`)

	// auto_cancel added.
	assert.Contains(t, s, "auto_cancel:")
	assert.Contains(t, s, "on_new_commit: none")
}

func TestMergeGitLabCI_ExistingWithAutoCancel(t *testing.T) {
	existing := []byte(`---
workflow:
  auto_cancel:
    on_new_commit: interruptible
  rules:
    - if: $CI_COMMIT_BRANCH
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// User's auto_cancel value preserved (not overwritten to "none").
	assert.Contains(t, s, "on_new_commit: interruptible")
	assert.NotContains(t, s, "on_new_commit: none")
}

func TestMergeGitLabCI_Idempotent(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Should not duplicate entries.
	assert.Equal(t, 1, strings.Count(s, "fullsend-pipeline.yml"))
	assert.Equal(t, 1, strings.Count(s, `$CI_PIPELINE_SOURCE == "merge_request_event"`))
	assert.Equal(t, 1, strings.Count(s, `$CI_PIPELINE_SOURCE == "schedule"`))
	assert.Equal(t, 1, strings.Count(s, `$CI_PIPELINE_SOURCE == "api"`))
}

func TestMergeGitLabCI_SingleIncludeScalar(t *testing.T) {
	existing := []byte(`---
include: 'other.yml'
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Original scalar include preserved (wrapped in sequence).
	assert.Contains(t, s, "other.yml")
	// Fullsend include appended.
	assert.Contains(t, s, "fullsend-pipeline.yml")
}

func TestUnmergeGitLabCI_FullsendOnly(t *testing.T) {
	// merge_request_event is intentionally excluded from
	// unmergeWorkflowRules (#7333) — it's GitLab's generic MR-pipeline
	// gate, not something fullsend can safely claim ownership of without
	// a provenance signal, so uninstall leaves it in place even though
	// every other entry here is fullsend's. This is the one-time leftover
	// tradeoff documented on unmergeWorkflowRules.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result, "merge_request_event rule survives, so the file is not fully empty")
	s := string(result)

	// Fullsend's current rules and auto_cancel removed.
	assert.NotContains(t, s, "fullsend-pipeline.yml")
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
	assert.NotContains(t, s, "auto_cancel")
	// merge_request_event left in place — no provenance signal.
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`)
}

func TestUnmergeGitLabCI_FullsendOnlyWithName(t *testing.T) {
	// Simulates uninstalling a file generated by newGitLabCI(). The
	// fullsend-generated workflow.name proves the block is fullsend's, so
	// the obsolete merge_request_event rule fullsend installed in earlier
	// versions is removed alongside fullsend's current rules, name, and
	// auto_cancel. With every fullsend-owned element gone, the file is
	// fully empty.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	assert.Empty(t, result, "all elements are fullsend-owned, so the file is fully emptied")
}

func TestUnmergeGitLabCI_ObsoleteRulePreservedWithoutProvenance(t *testing.T) {
	// A merge-path enrollment: fullsend appended its rules into a
	// pre-existing workflow block but never set workflow.name. Without that
	// provenance marker the obsolete merge_request_event rule cannot be
	// distinguished from a repo's own MR gate, so it is preserved while
	// fullsend's current rules are removed.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result, "merge_request_event rule survives, so the file is not fully empty")
	s := string(result)

	assert.NotContains(t, s, "fullsend-pipeline.yml")
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`)
}

func TestUnmergeGitLabCI_PreservesUserWorkflowName(t *testing.T) {
	// A user-provided workflow.name that does not start with the
	// fullsend prefix should be preserved during unmerge.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'my custom pipeline'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	// User's workflow name preserved.
	assert.Contains(t, s, "my custom pipeline")
	// Fullsend content removed.
	assert.NotContains(t, s, "fullsend-pipeline.yml")
}

func TestUnmergeGitLabCI_MixedContent(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/existing.yml'
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build

workflow:
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	// User content preserved.
	assert.Contains(t, s, "existing.yml")
	assert.Contains(t, s, "stages:")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, `$CI_COMMIT_BRANCH == "main"`)

	// Fullsend content removed. merge_request_event is not fullsend's to
	// remove here — no provenance signal distinguishes it from the
	// user's own MR gate (see unmergeWorkflowRules) — so it survives
	// alongside the user's other rule.
	assert.NotContains(t, s, "fullsend-pipeline.yml")
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
}

func TestUnmergeGitLabCI_Empty(t *testing.T) {
	result, err := UnmergeGitLabCI(nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestUnmergeGitLabCI_PreservesUserAutoCancel(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  auto_cancel:
    on_new_commit: interruptible
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	// User's auto_cancel value preserved.
	assert.Contains(t, s, "on_new_commit: interruptible")
	// User's rule preserved.
	assert.Contains(t, s, `$CI_COMMIT_BRANCH == "main"`)
	// merge_request_event has no provenance signal distinguishing it from
	// a user-owned rule, so it survives (see unmergeWorkflowRules).
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`)
	// Fullsend's current rules removed.
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
}

func TestUnmergeGitLabCI_RemovesSingleInclude(t *testing.T) {
	// When fullsend include is the only one in a scalar form.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
stages:
  - build
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	assert.NotContains(t, s, "fullsend-pipeline.yml")
	assert.NotContains(t, s, "include:")
	assert.Contains(t, s, "stages:")
}

func TestUnmergeGitLabCI_NoFullsendContent(t *testing.T) {
	existing := []byte(`---
stages:
  - build

build:
  script:
    - make
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	// All original content preserved.
	assert.Contains(t, s, "stages:")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "make")
}

func TestMergeGitLabCI_InvalidYAML(t *testing.T) {
	_, err := MergeGitLabCI([]byte(":\n  invalid: [yaml\n"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parsing existing")
}

func TestMergeGitLabCI_WorkflowNotMapping(t *testing.T) {
	existing := []byte(`---
workflow: "simple"
`)
	_, err := MergeGitLabCI(existing)
	assert.Error(t, err, "workflow: as scalar should be an error")
}

func TestMergeGitLabCI_WorkflowWithoutRules(t *testing.T) {
	existing := []byte(`---
workflow:
  name: "my project"
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Workflow name preserved.
	assert.Contains(t, s, "my project")
	// Rules added (native MR dispatch removed in #7322).
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`)
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	// auto_cancel added.
	assert.Contains(t, s, "auto_cancel:")
}

func TestMergeGitLabCI_WorkflowRulesNotSequence(t *testing.T) {
	// Edge case: workflow.rules as a scalar (unusual but possible).
	existing := []byte(`---
workflow:
  rules: "always"
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	// Should not crash, rules left as-is since not a sequence.
	assert.Contains(t, string(result), "workflow:")
}

func TestMergeGitLabCI_IncludeAsMappingEntry(t *testing.T) {
	// Single include as a mapping (not in a sequence).
	existing := []byte(`---
include:
  local: 'other.yml'
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)
	assert.Contains(t, s, "other.yml")
	assert.Contains(t, s, "fullsend-pipeline.yml")
}

func TestMergeGitLabCI_StagesAddedToExistingArray(t *testing.T) {
	existing := []byte(`stages:
  - build
  - test
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Original stages preserved.
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "- test")

	// Fullsend stages appended.
	assert.Contains(t, s, "- dispatch")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
}

func TestMergeGitLabCI_StagesDeduplicatesExisting(t *testing.T) {
	existing := []byte(`stages:
  - build
  - dispatch
  - test
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// dispatch already present — should not be duplicated.
	assert.Equal(t, 1, strings.Count(s, "- dispatch"), "dispatch stage should not be duplicated")

	// Other fullsend stages added.
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
}

func TestMergeGitLabCI_NoWorkflowBlockCreatedWhenAbsent(t *testing.T) {
	existing := []byte(`stages:
  - build
job1:
  script: echo hi
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Fullsend include added.
	assert.Contains(t, s, "fullsend-pipeline.yml")

	// No workflow block should be created — fullsend's jobs
	// self-filter via their own rules:.
	assert.NotContains(t, s, "workflow:")
}

func TestMergeGitLabCI_NoStagesKeyLeftAlone(t *testing.T) {
	// When no stages: key exists, fullsend's stages come from the
	// included pipeline file and do not need to be in the root.
	existing := []byte(`job1:
  script: echo hi
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// No stages: key added.
	assert.NotContains(t, s, "stages:")
}

func TestMergeGitLabCI_StagesIdempotent(t *testing.T) {
	existing := []byte(`stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// All fullsend stages already present — no duplicates.
	assert.Equal(t, 1, strings.Count(s, "- dispatch"))
	assert.Equal(t, 1, strings.Count(s, "- poll"))
	assert.Equal(t, 1, strings.Count(s, "- agent"))
}

func TestUnmergeGitLabCI_RemovesFullsendStages(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - test
  - dispatch
  - poll
  - agent
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	// Fullsend stages removed.
	assert.NotContains(t, s, "- dispatch")
	assert.NotContains(t, s, "- poll")
	assert.NotContains(t, s, "- agent")

	// User stages preserved.
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "- test")
}

func TestUnmergeGitLabCI_RemovesStagesKeyWhenEmpty(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - poll
  - agent
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)

	// Everything was fullsend-only, file should be nil.
	assert.Nil(t, result, "file should be nil when only fullsend content remains")
}

// --- HasFullsendEntries tests ---

func TestHasFullsendEntries_AllPresent(t *testing.T) {
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

workflow:
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_MissingInclude(t *testing.T) {
	yaml := `---
stages:
  - build
  - dispatch
  - poll
  - agent

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`
	assert.False(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_MissingStagesWhenKeyExists(t *testing.T) {
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - test
`
	assert.False(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_NoStagesKey(t *testing.T) {
	// When no stages: key exists, the included pipeline provides them.
	// This is not drift.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_WithoutObsoleteMRRule(t *testing.T) {
	// After #7322, merge_request_event is no longer required.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - poll
  - agent

workflow:
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_MissingWorkflowRules(t *testing.T) {
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
`
	assert.False(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_NoWorkflowBlock(t *testing.T) {
	// When no workflow: block exists, fullsend's jobs self-filter.
	// This is not drift.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - poll
  - agent
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_EmptyFile(t *testing.T) {
	assert.False(t, HasFullsendEntries([]byte("")))
	assert.False(t, HasFullsendEntries(nil))
}

func TestHasFullsendEntries_InvalidYAML(t *testing.T) {
	assert.False(t, HasFullsendEntries([]byte(":\n  invalid: [yaml\n")))
}

func TestHasFullsendEntries_WorkflowWithoutRules(t *testing.T) {
	// workflow: exists but has no rules: key — MergeGitLabCI would
	// add rules, so this is drift.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: "my project"
`
	assert.False(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_PartialStagesMissing(t *testing.T) {
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
`
	// Missing "agent" stage.
	assert.False(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_IncludeAsScalar(t *testing.T) {
	// Include as a plain scalar matching the fullsend path.
	yaml := `---
include: '.gitlab/ci/fullsend-pipeline.yml'
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_IncludeWithExtraEntries(t *testing.T) {
	// Fullsend include is present alongside user includes.
	yaml := `---
include:
  - local: '.gitlab/ci/existing.yml'
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

func TestMergeGitLabCI_PreservesComments(t *testing.T) {
	existing := []byte(`---
# My project CI configuration
stages:
  - build
  - test

# Build job
build:
  stage: build
  script:
    - make build
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	// Comments should be preserved by yaml.Node API.
	assert.Contains(t, s, "# My project CI configuration")
	assert.Contains(t, s, "# Build job")

	// Fullsend stages added to existing array.
	assert.Contains(t, s, "- dispatch")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
}

func TestStripObsoleteGitLabWorkflowRules_RemovesObsoleteRule(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)

	assert.NotContains(t, s, `merge_request_event`)
	// Current fullsend rules, name, and auto_cancel are preserved.
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
	assert.Contains(t, s, "fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY")
	assert.Contains(t, s, "on_new_commit: none")
	assert.Contains(t, s, "fullsend-pipeline.yml")
}

func TestStripObsoleteGitLabWorkflowRules_NoObsoleteRule(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabWorkflowRules_NoWorkflowBlock(t *testing.T) {
	existing := []byte(`---
stages:
  - build
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabWorkflowRules_EmptyFile(t *testing.T) {
	result, changed, err := StripObsoleteGitLabWorkflowRules(nil)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, result)
}

func TestStripObsoleteGitLabWorkflowRules_InvalidYAML(t *testing.T) {
	_, _, err := StripObsoleteGitLabWorkflowRules([]byte("not: valid: yaml: [["))
	require.Error(t, err)
}

func TestStripObsoleteGitLabWorkflowRules_PreservesUserRules(t *testing.T) {
	// No workflow.name and none of fullsend's current required rules are
	// present, so nothing marks this merge_request_event rule as
	// fullsend-owned. It must survive untouched.
	existing := []byte(`---
workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_BRANCH == "main"
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabWorkflowRules_StripsFullsendManagedRuleByName(t *testing.T) {
	// workflow.name carries the fullsend-generated prefix, establishing
	// provenance even without the current rule set present.
	existing := []byte(`---
workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_COMMIT_BRANCH == "main"
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)
	assert.NotContains(t, s, "merge_request_event")
	assert.Contains(t, s, `$CI_COMMIT_BRANCH == "main"`)
	assert.Contains(t, s, "fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY")
}

func TestStripObsoleteGitLabWorkflowRules_PreservesUserRuleCoexistingWithFullsendRuleSet(t *testing.T) {
	// No workflow.name (as on an already-enrolled repo merged via
	// mergeWorkflowRules, which never sets it). Fullsend's full current
	// required rule set (schedule + api) is present alongside the
	// merge_request_event rule, but mergeWorkflowRules appends its rules
	// into ANY pre-existing workflow block unconditionally — so this
	// co-presence does not prove fullsend added the merge_request_event
	// rule specifically. It could equally be the repo owner's own
	// independently-configured MR gate that happened to coexist with
	// fullsend's rules after enrollment. Without a stronger provenance
	// signal (the fullsend workflow.name prefix), the rule must survive.
	existing := []byte(`---
workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabWorkflowRules_RemovesEmptyRulesKey(t *testing.T) {
	// The obsolete rule is the only entry in workflow.rules. Removing it
	// must drop the rules: key entirely rather than leaving behind
	// rules: [], which GitLab treats as "never run".
	existing := []byte(`---
workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
`)
	result, changed, err := StripObsoleteGitLabWorkflowRules(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)
	assert.NotContains(t, s, "merge_request_event")
	assert.NotContains(t, s, "rules:")
	assert.Contains(t, s, "fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY")
}
