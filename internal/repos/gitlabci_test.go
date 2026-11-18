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
	assert.Contains(t, s, debugTraceDenyRuleIf)
	assert.Contains(t, s, "when: never")
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "schedule"`)
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
	debugIdx := strings.Index(s, debugTraceDenyRuleIf)
	scheduleIdx := strings.Index(s, `$CI_PIPELINE_SOURCE == "schedule"`)
	require.NotEqual(t, -1, debugIdx)
	require.NotEqual(t, -1, scheduleIdx)
	assert.Less(t, debugIdx, scheduleIdx, "debug-trace deny must precede schedule admit")
}

func TestMergeGitLabCI_InstallsPipelineInputContract(t *testing.T) {
	result, err := MergeGitLabCI(nil)
	require.NoError(t, err)
	s := string(result)
	assert.Contains(t, s, "spec:")
	assert.Contains(t, s, "event_payload_chunk_08:")
	assert.Contains(t, s, "inputs:")
	assert.Contains(t, s, "stage: $[[ inputs.stage ]]")
}

func TestMergeGitLabCI_AddsInputContractToExistingRoot(t *testing.T) {
	existing := []byte(`include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
stages:
  - build
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)
	assert.Contains(t, s, "spec:")
	assert.Contains(t, s, "event_type: $[[ inputs.event_type ]]")
	assert.Contains(t, s, "- local: '.gitlab/ci/fullsend-pipeline.yml'")
	assert.Contains(t, s, "event_payload_chunk_08: $[[ inputs.event_payload_chunk_08 ]]")
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

	// Fullsend stages appended to existing stages array. dispatch
	// (dropped in #7337) is back for the webhook dispatcher (#7771).
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

func TestMergeGitLabCI_PrependsDebugTraceDeny(t *testing.T) {
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
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	debugIdx := strings.Index(s, debugTraceDenyRuleIf)
	require.NotEqual(t, -1, debugIdx, "merge must add the debug-trace deny rule")
	scheduleIdx := strings.Index(s, `$CI_PIPELINE_SOURCE == "schedule"`)
	require.NotEqual(t, -1, scheduleIdx)
	assert.Less(t, debugIdx, scheduleIdx, "debug-trace deny must be prepended before existing admit rules")
	assert.Contains(t, s, "when: never")
	assert.Equal(t, 1, strings.Count(s, debugTraceDenyRuleIf))
}

// --- MergeMissingGitLabDebugTraceRule tests ---

func TestMergeMissingGitLabDebugTraceRule_AddsMissingRule(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	require.True(t, changed, "expected the missing debug-trace rule to be added")
	s := string(result)

	debugIdx := strings.Index(s, debugTraceDenyRuleIf)
	require.NotEqual(t, -1, debugIdx, "expected the debug-trace deny rule to be added")
	scheduleIdx := strings.Index(s, `$CI_PIPELINE_SOURCE == "schedule"`)
	require.NotEqual(t, -1, scheduleIdx)
	assert.Less(t, debugIdx, scheduleIdx, "debug-trace deny must precede existing admit rules")
	assert.Contains(t, s, "when: never")
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "api"`, "existing admit rules must be preserved")
}

func TestMergeMissingGitLabDebugTraceRule_AlreadyPresentNoAction(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	assert.False(t, changed, "rule already present — nothing to merge")
	assert.Equal(t, existing, result)
}

func TestMergeMissingGitLabDebugTraceRule_WrongWhenBackfilled(t *testing.T) {
	// Same if: condition as the guard, but when: is missing (GitLab
	// defaults an omitted when: to "always", not "never"), so the rule
	// does nothing to deny a debug-trace pipeline. Must be replaced by a
	// correctly-shaped when: never rule, not treated as already present.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: ` + debugTraceDenyRuleIf + `
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	require.True(t, changed, "wrong when: must be treated as missing, not already present")
	s := string(result)

	assert.Equal(t, 1, strings.Count(s, debugTraceDenyRuleIf), "must not leave a duplicate if: condition")
	debugIdx := strings.Index(s, debugTraceDenyRuleIf)
	scheduleIdx := strings.Index(s, `$CI_PIPELINE_SOURCE == "schedule"`)
	require.NotEqual(t, -1, debugIdx)
	require.NotEqual(t, -1, scheduleIdx)
	assert.Less(t, debugIdx, scheduleIdx, "debug-trace deny must precede admit rules")
	assert.Contains(t, s, "when: never")
}

func TestMergeMissingGitLabDebugTraceRule_WrongOrderBackfilled(t *testing.T) {
	// Correctly-shaped if:/when: never rule, but positioned after an
	// admit rule — first-match evaluation would let the admit rule win
	// for a debug-trace pipeline. Must be repositioned, not treated as
	// already present.
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  name: 'fullsend $CI_PIPELINE_SOURCE $STAGE $RESOURCE_KEY'
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`)
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	require.True(t, changed, "wrong order must be treated as missing, not already present")
	s := string(result)

	assert.Equal(t, 1, strings.Count(s, debugTraceDenyRuleIf), "must not leave a duplicate if: condition")
	debugIdx := strings.Index(s, debugTraceDenyRuleIf)
	scheduleIdx := strings.Index(s, `$CI_PIPELINE_SOURCE == "schedule"`)
	require.NotEqual(t, -1, debugIdx)
	require.NotEqual(t, -1, scheduleIdx)
	assert.Less(t, debugIdx, scheduleIdx, "debug-trace deny must precede admit rules")
	assert.Contains(t, s, "when: never")
}

func TestMergeMissingGitLabDebugTraceRule_NotFullsendOwnedNoAction(t *testing.T) {
	// No workflow.name — fullsend can't prove it owns this workflow block,
	// so a user's own configuration (missing the rule for its own reasons)
	// must not be modified.
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
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestMergeMissingGitLabDebugTraceRule_NoWorkflowBlockNoAction(t *testing.T) {
	existing := []byte(`---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'
`)
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestMergeMissingGitLabDebugTraceRule_EmptyFileNoAction(t *testing.T) {
	result, changed, err := MergeMissingGitLabDebugTraceRule(nil)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Nil(t, result)
}

func TestMergeMissingGitLabDebugTraceRule_InvalidYAMLReturnsError(t *testing.T) {
	existing := []byte("workflow: [1,2\n")
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.Error(t, err)
	assert.False(t, changed)
	assert.Nil(t, result)
}

func TestMergeMissingGitLabDebugTraceRule_CommentOnlyDocumentNoAction(t *testing.T) {
	// A comment-only file parses without error but yields no document
	// content node — must be treated as nothing-to-merge, not a panic on
	// an empty doc.Content slice.
	existing := []byte("# just a comment\n")
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
}

func TestMergeMissingGitLabDebugTraceRule_RootNotMappingNoAction(t *testing.T) {
	existing := []byte("- a\n- b\n")
	result, changed, err := MergeMissingGitLabDebugTraceRule(existing)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, existing, result)
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

func TestUnmergeGitLabCI_RemovesTriggerDispatcherRuleAndStage(t *testing.T) {
	// #7771: uninstall removes the webhook dispatcher's trigger admit
	// rule and dispatch stage along with the rest of fullsend's entries,
	// leaving the user's own stage and rule in place.
	existing := []byte(`---
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
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "push"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
    - if: ` + triggerDispatcherRuleIf + `
`)
	result, err := UnmergeGitLabCI(existing)
	require.NoError(t, err)
	require.NotNil(t, result)
	s := string(result)

	assert.NotContains(t, s, `"trigger"`)
	assert.NotContains(t, s, "- dispatch")
	assert.NotContains(t, s, "- poll")
	assert.NotContains(t, s, "- agent")
	assert.Contains(t, s, "- build")
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "push"`)
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

	// Fullsend stages appended, in order.
	assert.Contains(t, s, "- build\n  - test\n  - dispatch\n  - poll\n  - agent")
}

func TestMergeGitLabCI_AddsDispatcherStage(t *testing.T) {
	// #7771: the webhook dispatcher job runs on the dispatch stage. A
	// root stages: list without it would make GitLab reject every
	// source=trigger pipeline, so merge must add it.
	existing := []byte(`stages:
  - build
  - test
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	assert.Equal(t, 1, strings.Count(s, "- dispatch"), "dispatch must be added for the webhook dispatcher (#7771)")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
}

func TestMergeGitLabCI_AddsTriggerDispatcherRule(t *testing.T) {
	// #7771: an existing workflow: block gains the source=trigger admit
	// rule, after the debug-trace deny.
	existing := []byte(`---
workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "push"
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)
	s := string(result)

	require.Contains(t, s, triggerDispatcherRuleIf)
	assert.Equal(t, 1, strings.Count(s, triggerDispatcherRuleIf))
	assert.Less(t, strings.Index(s, debugTraceDenyRuleIf), strings.Index(s, triggerDispatcherRuleIf),
		"debug-trace deny must precede the trigger admit rule")

	// Idempotent: a second merge does not duplicate it.
	again, err := MergeGitLabCI(result)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(again), triggerDispatcherRuleIf))
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

	// An existing dispatch stage (current again since #7771) must not
	// be duplicated.
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
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
    - if: ` + childPipelineAgentRuleIf + `
    - if: ` + triggerDispatcherRuleIf + `
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

func TestHasFullsendEntries_DispatchStageRequired(t *testing.T) {
	// #7771 reinstated dispatch for the webhook dispatcher; a root
	// stages: list without it is drift the merge path repairs.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

stages:
  - build
  - poll
  - agent
`
	assert.False(t, HasFullsendEntries([]byte(yaml)))
}

func TestHasFullsendEntries_MissingTriggerDispatcherRule(t *testing.T) {
	// #7771: a fullsend workflow: block without the source=trigger
	// admit rule is drift — the dispatcher pipeline would never start.
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  rules:
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`
	assert.False(t, HasFullsendEntries([]byte(yaml)))
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
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
    - if: ` + childPipelineAgentRuleIf + `
    - if: ` + triggerDispatcherRuleIf + `
`
	assert.True(t, HasFullsendEntries([]byte(yaml)))
}

// An installation that predates child-pipeline dispatch (#8438) lacks the
// source=pipeline workflow rule; it is drift, so convergence adds the rule.
func TestHasFullsendEntries_MissingChildPipelineRule(t *testing.T) {
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
    - if: ` + debugTraceDenyRuleIf + `
      when: never
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
    - if: ` + triggerDispatcherRuleIf + `
`
	assert.False(t, HasFullsendEntries([]byte(yaml)),
		"workflow rules without the child-pipeline agent rule are drift")
}

// The child-pipeline rule admits only source=pipeline on the protected
// default branch with the stage input set.
func TestChildPipelineAgentRuleIf(t *testing.T) {
	assert.Contains(t, childPipelineAgentRuleIf, `$CI_PIPELINE_SOURCE == "pipeline"`)
	assert.Contains(t, childPipelineAgentRuleIf, `$CI_COMMIT_REF_PROTECTED == "true"`)
	assert.Contains(t, childPipelineAgentRuleIf, `$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH`)
	assert.Contains(t, childPipelineAgentRuleIf, `$STAGE`)
	assert.NotContains(t, childPipelineAgentRuleIf, "||")
	assert.Contains(t, fullsendWorkflowRules, workflowRule{If: childPipelineAgentRuleIf})
	assert.Contains(t, unmergeWorkflowRules, workflowRule{If: childPipelineAgentRuleIf})
}

func TestHasFullsendEntries_MissingDebugTraceDeny(t *testing.T) {
	yaml := `---
include:
  - local: '.gitlab/ci/fullsend-pipeline.yml'

workflow:
  auto_cancel:
    on_new_commit: none
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - if: $CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE
`
	assert.False(t, HasFullsendEntries([]byte(yaml)),
		"workflow rules without the debug-trace deny are drift")
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
