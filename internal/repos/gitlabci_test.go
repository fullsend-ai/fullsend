package repos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
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
	// dispatch was dropped from the required set in #7337.
	assert.NotContains(t, s, "- dispatch")
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

	// Fullsend stages appended. dispatch is obsolete (#7337) and must
	// not be newly installed.
	assert.NotContains(t, s, "- dispatch")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
}

func TestMergeGitLabCI_DoesNotAddObsoleteDispatchStage(t *testing.T) {
	existing := []byte(`stages:
  - build
  - test
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	assert.NotContains(t, s, "- dispatch", "dispatch must not be added after #7337")
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

	// Leftover dispatch from a pre-#7337 install is left in place by
	// merge (converge/uninstall strip it) and must not be duplicated.
	assert.Equal(t, 1, strings.Count(s, "- dispatch"), "dispatch stage should not be duplicated")

	// Current fullsend stages added.
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

	// Current fullsend stages already present — no duplicates.
	// Leftover dispatch is preserved (not duplicated) by merge.
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

func TestHasFullsendEntries_ObsoleteDispatchNotRequired(t *testing.T) {
	// After #7337, dispatch is no longer required for drift detection.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - poll
  - agent
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

	// Fullsend stages added to existing array. dispatch is obsolete.
	assert.NotContains(t, s, "- dispatch")
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

func TestStripObsoleteGitLabStages_RemovesDispatch(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)

	assert.NotContains(t, s, "- dispatch")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
	assert.Contains(t, s, "fullsend-pipeline.yml")
}

func TestStripObsoleteGitLabStages_NoObsoleteStage(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_NoStagesBlock(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_EmptyFile(t *testing.T) {
	result, changed, err := StripObsoleteGitLabStages(nil)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, result)
}

func TestStripObsoleteGitLabStages_InvalidYAML(t *testing.T) {
	_, _, err := StripObsoleteGitLabStages([]byte("not: valid: yaml: [["))
	require.Error(t, err)
}

func TestStripObsoleteGitLabStages_NoIncludePreservesDispatch(t *testing.T) {
	// Without the fullsend pipeline include this is not an enrolled
	// root CI file, so a dispatch stage is the repo's own.
	existing := []byte(`---
stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_LoneDispatchPreserved(t *testing.T) {
	// Include is present but none of fullsend's current stages are, so
	// dispatch is treated as the repo's own stage rather than a leftover
	// from mergeStages.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ReferencedByJobPreserved(t *testing.T) {
	// A merge-path-enrolled repo whose own job still runs on a stage
	// literally named "dispatch" must not have that stage stripped —
	// doing so would leave the job's stage: value out of stages: and
	// GitLab would reject the pipeline.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

notify:
  stage: dispatch
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_HiddenJobReferenceIgnored(t *testing.T) {
	// A hidden (`.`-prefixed) key is a template meant to be pulled in via
	// extends:, not a job that runs on its own — it should not block the
	// obsolete-stage migration.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template:
  stage: dispatch
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)
	assert.NotContains(t, s, "- dispatch")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
}

func TestStripObsoleteGitLabStages_PreservesUserStages(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - test
  - dispatch
  - poll
  - agent
  - deploy
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)
	assert.NotContains(t, s, "- dispatch")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, "- test")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
	assert.Contains(t, s, "- deploy")
}

func TestStripObsoleteGitLabStages_StagesNotSequence(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages: build
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ScalarInclude(t *testing.T) {
	existing := []byte(`---
include: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - poll
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)
	assert.NotContains(t, s, "- dispatch")
	assert.Contains(t, s, "- poll")
}

func TestStripObsoleteGitLabStages_UnrelatedIncludePreservesDispatch(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/other.yml'

stages:
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_WhitespaceOnly(t *testing.T) {
	existing := []byte("   \n\t\n")
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_MappingInclude(t *testing.T) {
	existing := []byte(`---
include:
  local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - dispatch
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	require.True(t, changed)
	s := string(result)
	assert.NotContains(t, s, "- dispatch")
	assert.Contains(t, s, "- agent")
}

func TestStripObsoleteGitLabStages_NonMappingRoot(t *testing.T) {
	existing := []byte("- just a list\n")
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ExtendsHiddenTemplatePreserved(t *testing.T) {
	// A visible job with no stage: of its own that extends a hidden
	// template still compiles to that template's stage under GitLab's
	// extends deep-merge, so it must count as a live reference.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template:
  stage: dispatch

notify:
  extends: .dispatch_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_MergeKeyReferencePreserved(t *testing.T) {
	// A job that inherits stage: dispatch via the YAML merge key (<<:)
	// rather than extends: must also count as a live reference.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template: &dispatch_template
  stage: dispatch

notify:
  <<: *dispatch_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_AliasJobReferencePreserved(t *testing.T) {
	// A job whose entire value is an alias to a hidden template (rather
	// than its own mapping with extends:/<<:) must resolve through the
	// alias to find the inherited stage.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template: &dispatch_template
  stage: dispatch

notify: *dispatch_template
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_PagesJobReferencePreserved(t *testing.T) {
	// pages is GitLab's reserved Pages *job*, not a pipeline keyword, so
	// it must be scanned like any other job rather than skipped as
	// configuration.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

pages:
  stage: dispatch
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_OtherLocalIncludePreventsStrip(t *testing.T) {
	// A second local include can define jobs that stageReferencedByJob
	// can't see (only the root file's top-level keys are scanned), so
	// the migration must fail closed rather than risk stripping a stage
	// a job in that other file still references.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
  - local: '.gitlab/ci/other.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_AliasedStageValuePreserved(t *testing.T) {
	// A job's stage: value can itself be an alias (stage: *anchor) rather
	// than a plain scalar. The alias must be dereferenced to the anchored
	// scalar before comparing, otherwise the reference is invisible and
	// the stage gets stripped out from under the job.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_stage_name: &dispatch_stage_name dispatch

notify:
  stage: *dispatch_stage_name
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ReferenceTagStagePreserved(t *testing.T) {
	// GitLab's `!reference [...]` tag lets a job pull its stage: value
	// from elsewhere in the document. That value isn't a resolvable
	// scalar or alias, so the effective stage can't be determined from
	// the root file alone — treat it as a live reference and fail closed
	// rather than assume it's safe to strip.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template:
  stage: dispatch

notify:
  stage: !reference [.dispatch_template, stage]
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_MergeKeyAliasedSequencePreserved(t *testing.T) {
	// `<<: *seq` merges in a sequence of job mappings via a single alias
	// to the whole sequence, rather than a literal `<<: [*a, *b]` at the
	// use site. The alias must be dereferenced before its Kind is
	// classified — comparing Kind on the un-dereferenced AliasNode misses
	// that it resolves to a sequence and silently drops the referencing
	// items from the scan.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.other_job: &other_job
  script: echo other

.dispatch_job: &dispatch_job
  stage: dispatch

.merged_jobs: &merged_jobs
  - *other_job
  - *dispatch_job

notify:
  <<: *merged_jobs
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_MergeKeyReferenceTagPreserved(t *testing.T) {
	// GitLab's `<<: !reference [...]` form merges in a value pulled from
	// elsewhere in the document. Its Kind is a SequenceNode of
	// path-component scalars (not job mappings), so scanning it as a list
	// of merge targets would look at the wrong content. The `!reference`
	// tag must be recognized as unresolvable so the merge fails closed.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.t:
  stage: dispatch

notify:
  <<: !reference [.t]
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ExtendsReferenceTagPreserved(t *testing.T) {
	// GitLab's `extends: !reference [.setup, scaling]` form has the same
	// SequenceNode shape as an ordinary two-name extends list, but its
	// contents are a reference path (a nested lookup), not job/template
	// names. Parsing it as a name list would look up unrelated top-level
	// keys instead of recognizing the reference can't be resolved from
	// this scan — it must fail closed regardless of what the reference
	// would resolve to.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.setup:
  scaling:
    stage: dispatch

notify:
  extends: !reference [.setup, scaling]
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_JobReferenceTagPreserved(t *testing.T) {
	// GitLab's `!reference [...]` tag can stand in for a job's entire
	// value, not just a single field's — `notify: !reference
	// [.dispatch_template]` inlines the whole hidden template as this
	// job's body. That parses as a SequenceNode tagged !reference, not a
	// job MappingNode, so it can't be interpreted as job content here; the
	// effective stage can't be ruled out and must be treated as a live
	// reference rather than silently skipped.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template:
  stage: dispatch

notify: !reference [.dispatch_template]
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_DuplicateStageKeyPreserved(t *testing.T) {
	// gopkg.in/yaml.v3 preserves duplicate mapping keys as separate
	// Content pairs instead of collapsing them (the duplicate-key error
	// only applies when decoding into a Go map/struct), and GitLab/Psych's
	// YAML parser is last-wins: `stage: test` then `stage: dispatch` in
	// the same job compiles to stage: dispatch. A first-match lookup would
	// see only the first "stage" occurrence and wrongly conclude the job
	// runs on "test", missing the live reference to "dispatch".
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - test
  - dispatch
  - poll
  - agent

notify:
  stage: test
  stage: dispatch
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_DuplicateMergeKeyPreserved(t *testing.T) {
	// Two `<<:` keys on the same job is another case yaml.v3 preserves as
	// separate Content pairs rather than collapsing. Only the second
	// mapping supplies stage: dispatch; a first-match lookup would resolve
	// only the first `<<:` and miss the live reference carried by the
	// second.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.other_template: &other_template
  script: echo other

.dispatch_template: &dispatch_template
  stage: dispatch

notify:
  <<: *other_template
  <<: *dispatch_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_DuplicateExtendsPreserved(t *testing.T) {
	// Same duplicate-key shape as above but for extends:. Only the second
	// occurrence points at the template that sets stage: dispatch; a
	// first-match lookup would resolve only the first extends: and miss
	// the live reference carried by the second.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.other_template:
  script: echo other

.dispatch_template:
  stage: dispatch

notify:
  extends: .other_template
  extends: .dispatch_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_AliasedJobNamePreserved(t *testing.T) {
	// A top-level job can be keyed by an alias to an anchored scalar
	// (`*n: {...}`) rather than a plain literal key. stageReferencedByJob's
	// top-level scan must resolve — or fail closed on — such a key instead
	// of skipping it outright, since skipping would let an aliased job's
	// stage: dispatch go unnoticed.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.job_name: &job_name notify

*job_name:
  stage: dispatch
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_AliasedHiddenTemplateExtendsPreserved(t *testing.T) {
	// A hidden template's own top-level key can itself be an AliasNode
	// that resolves to a "."-prefixed scalar, rather than a literal ".name"
	// key. The scan loop recognizes and skips it as a template (it resolves
	// the alias, sees the "." prefix), but the extends-target lookup used
	// to require a literal yaml.ScalarNode key and would never index this
	// template under its resolved name — so a visible job extending it by
	// that name couldn't find it, and its stage: dispatch went unnoticed.
	// Both the lookup and the scan must classify keys identically.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.tpl_name: &tpl_name .dispatch_template

*tpl_name:
  stage: dispatch

notify:
  extends: .dispatch_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_RootMergeKeyPreventsStrip(t *testing.T) {
	// A root-level YAML merge key (<<: *jobs) injects the anchor
	// mapping's own keys — here a whole job definition — directly into
	// the document mapping. gopkg.in/yaml.v3 preserves "<<" as a literal
	// key rather than expanding it, so the injected job's stage: is
	// invisible to a scan that treats the merge target as a single job
	// body. Fail closed rather than risk stripping a stage the injected
	// job still references.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.jobs: &jobs
  notify:
    stage: dispatch
    script: echo hi

<<: *jobs
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_DuplicateTopLevelIncludeKeyPreserved(t *testing.T) {
	// Two top-level "include:" keys: gopkg.in/yaml.v3 preserves duplicate
	// mapping keys as separate Content pairs, and GitLab/Psych is
	// last-wins, so a first-match lookup (findMappingValue) would only see
	// the first — fullsend-owned — include and never notice the second,
	// customer-owned include that could define its own job on the
	// obsolete stage. The scanner can't fetch and check that other
	// include's content, so it must fail closed on the mere presence of a
	// duplicate "include:" key rather than assume only the first matters.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
include:
  - local: 'customer/other.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_AliasedSecondIncludeKeyPreserved(t *testing.T) {
	// A second top-level "include:" key can itself be an AliasNode
	// resolving to the scalar "include" rather than a literal "include:"
	// key. A literal .Value=="include" check would never match the alias
	// key and would silently miss this second include entirely;
	// classifyTopLevelKey resolves the alias first, so this is visible as
	// a duplicate "include:" key and the strip is refused.
	existing := []byte(`---
.inc_key: &inc_key include

include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

*inc_key:
  - local: 'customer/other.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_DuplicateLocalKeyInIncludePreserved(t *testing.T) {
	// A single include: mapping item with duplicate "local:" keys:
	// GitLab/Psych's last-wins semantics mean the second local: value is
	// the one that actually takes effect, but isFullsendPipelineInclude
	// can't assume which occurrence wins with confidence, so it must fail
	// closed (treat as not confirmed to be the fullsend pipeline include)
	// rather than match on the first "local:" occurrence.
	existing := []byte(`---
include:
  local: '.gitlab/ci/fullsend-pipeline.yml'
  local: 'customer/other.yml'

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_VariableInterpolatedStagePreserved(t *testing.T) {
	// A job's stage: value can use GitLab CI/CD variable interpolation
	// (`$NOTIFY_STAGE`) instead of a literal stage name. The node is still
	// a plain, untagged ScalarNode, but its .Value is the unevaluated
	// expression text, not the stage GitLab will actually assign — here,
	// "dispatch" per the variables: block. Comparing it as a literal would
	// wrongly conclude the job doesn't reference "dispatch".
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

variables:
  NOTIFY_STAGE: dispatch

notify:
  stage: $NOTIFY_STAGE
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ExtendsInterpolatedTemplatePreserved(t *testing.T) {
	// Like stage: $NOTIFY_STAGE, an extends: value can carry GitLab
	// pipeline-input interpolation instead of a literal template name
	// (`extends: $[[ inputs.template ]]`). The node is still a plain,
	// untagged ScalarNode, but its .Value is the unevaluated expression
	// text — not a real job/template name a lookup could ever match — so
	// nodeResolvesToStage's lookup[name] check would always miss and the
	// scanner would wrongly conclude the job extends nothing, even though
	// the interpolated target could be this hidden template with stage:
	// dispatch.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.dispatch_template:
  stage: dispatch

notify:
  extends: $[[ inputs.template ]]
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_ExtendsMixedInterpolatedSequencePreserved(t *testing.T) {
	// Same interpolation gap as above, but inside a sequence that also
	// contains an ordinary literal name. Every item must resolve to a
	// fully-literal scalar before the sequence can be trusted as a list of
	// real extends targets — one interpolated item is enough to make the
	// effective extends target(s) undeterminable, regardless of the other,
	// literal item.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.setup:
  script: echo setup

.dispatch_template:
  stage: dispatch

notify:
  extends:
    - .setup
    - $[[ inputs.template ]]
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_InterpolatedHiddenTemplateKeyPreserved(t *testing.T) {
	// A hidden-template key's own literal text can carry GitLab
	// pipeline-input interpolation (`.$[[ inputs.template ]]`). Its raw
	// .Value never string-matches the literal name a visible job's
	// extends: uses (here, ".dispatch_template"), so the interpolated
	// portion may evaluate to the very template being extended without the
	// scanner ever connecting the two. classifyTopLevelKey has no way to
	// prove the two names are, or aren't, the same template at pipeline
	// time, so the scan must fail closed rather than skip this key as an
	// ordinary, unrelated hidden template.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.$[[ inputs.template ]]:
  stage: dispatch

notify:
  extends: .dispatch_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_InterpolatedJobKeywordKeyPreserved(t *testing.T) {
	// A job-level key can itself carry GitLab pipeline-input interpolation
	// (`$[[ inputs.keyword ]]: dispatch`) and evaluate to "stage" at
	// pipeline time even though its raw .Value never string-matches
	// "stage" here. collectJobKeys must treat such a key as making the
	// whole job unclassifiable rather than silently drop it, or a job that
	// effectively sets stage: dispatch this way would be invisible to the
	// scan.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

notify:
  $[[ inputs.keyword ]]: dispatch
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_DollarBearingJobNameStillScanned(t *testing.T) {
	// classifyTopLevelKey must not be blanket-replaced with
	// isLiteralScalarValue: an ordinary, visible job whose *name* happens
	// to contain "$" (not a "."-prefixed hidden template, and not a
	// job-level keyword) is still a real, literal job name GitLab will run
	// as-is, and must still be scanned normally rather than treated as
	// unclassifiable. Its own stage: is a literal, non-obsolete name, so
	// the migration should still proceed.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

weird$job:
  stage: build
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotEqual(t, existing, result)
}

func TestStripObsoleteGitLabStages_InterpolatedTopLevelMappingIncludePreserved(t *testing.T) {
	// A second top-level key whose literal text carries interpolation
	// syntax (`$[[ inputs.extra ]]:`) can't be trusted as an ordinary job
	// name: classifyTopLevelKeys must treat it as unclassifiable so the
	// whole scan fails closed, rather than let stageReferencedByJob's scan
	// loop treat its mapping value ({local: customer/other.yml} — itself
	// shaped like a second include) as an ordinary job body with no
	// stage:, which would leave hasOtherInclude false and jobs from that
	// include unvisited.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

$[[ inputs.extra ]]:
  local: customer/other.yml
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_InterpolatedIncludeMappingKeyPreserved(t *testing.T) {
	// An include-mapping item can carry a second, interpolated key
	// alongside a legitimate "local: fullsend-pipeline.yml" — e.g.
	// `$[[ inputs.k ]]: customer/other.yml`. isFullsendPipelineInclude
	// must not silently skip that key via "name != local": since it isn't
	// a known-safe include-mapping key (and separately carries
	// interpolation syntax), the item can't be confirmed as solely the
	// fullsend include.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
    $[[ inputs.k ]]: customer/other.yml

stages:
  - build
  - dispatch
  - poll
  - agent
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestStripObsoleteGitLabStages_InterpolatedMergeKeyInjectedJobPreserved(t *testing.T) {
	// A top-level key whose name is itself interpolated
	// (`$[[ inputs.merge ]]: *jobs`) can alias to a mapping of further job
	// definitions, effectively injecting jobs the way a merge key would.
	// classifyTopLevelKeys must fail the whole scan closed on this key
	// rather than let it be scanned as a single ordinary job body (which
	// has no stage: of its own), silently missing the injected
	// "notify: {stage: dispatch}" job.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

.jobs: &jobs
  notify:
    stage: dispatch
    script: echo hi

$[[ inputs.merge ]]: *jobs
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_NotFound(t *testing.T) {
	// No wrapper file on the repo at all — nothing to pull in the
	// obsolete dispatch include.
	fc := forge.NewFakeClient()
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.False(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_ScalarInclude(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(`---
include: '.gitlab/ci/fullsend-dispatch.yml'
`)
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.True(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_MappingInclude(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-dispatch.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  - local: '.gitlab/ci/fullsend-poll.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "schedule"
`)
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.True(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_NoDispatch(t *testing.T) {
	// The current wrapper template: no dispatch include anywhere.
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(`---
include:
  - local: '.gitlab/ci/fullsend-poll.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "schedule"
  - local: '.gitlab/ci/fullsend-agent.yml'
    rules:
      - if: $CI_PIPELINE_SOURCE == "api" && $STAGE

stages:
  - poll
  - agent
`)
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.False(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_NoIncludeKey(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte(`---
stages:
  - poll
`)
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.False(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_EmptyContent(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte("   \n")
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.False(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_FetchErrorFailsClosed(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + fullsendPipelineInclude: errors.New("transient API error"),
	}
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.Error(t, err)
	assert.True(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_ParseErrorFailsClosed(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte("not: valid: yaml: [[")
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.Error(t, err)
	assert.True(t, got)
}

func TestGitlabPipelineWrapperStillIncludesDispatch_NonMappingRootFailsClosed(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+fullsendPipelineInclude] = []byte("- just a list\n")
	got, err := gitlabPipelineWrapperStillIncludesDispatch(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.True(t, got)
}

func TestStripObsoleteGitLabStages_ExtendsTargetAbsentFromRootPreserved(t *testing.T) {
	// A job's extends: can name a template that isn't a top-level key of
	// this root file at all — e.g. one defined in the included fullsend
	// pipeline wrapper or another local include. GitLab resolves extends:
	// against the whole merged pipeline configuration, not just this root
	// file, so an unresolvable name here must be treated as a live
	// reference rather than silently skipped as "not found".
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - dispatch
  - poll
  - agent

notify:
  extends: .missing_template
  script: echo hi
`)
	result, changed, err := StripObsoleteGitLabStages(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}
