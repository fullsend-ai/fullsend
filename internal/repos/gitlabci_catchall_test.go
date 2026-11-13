package repos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// catchAllWorkflowCI is the workflow block from #9404: a repo's own admit
// rules followed by a catch-all `when: never`.
const catchAllWorkflowCI = `workflow:
  rules:
    # Run on merge request pipelines
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
    # Run automatically on statistic pipelines
    - if: '$STATISTIC_PIPELINE == "true"'
    # Don't run in other cases
    - when: never
`

// workflowRuleSummaries returns each workflow:rules entry of a root
// .gitlab-ci.yml as "if|when" in order. The root may carry a spec:
// header document, so the last document is the one decoded.
func workflowRuleSummaries(t *testing.T, content []byte) []string {
	t.Helper()
	docs, err := decodeGitLabDocuments(content)
	require.NoError(t, err)
	require.NotEmpty(t, docs)
	var doc struct {
		Workflow struct {
			Rules []struct {
				If   string `yaml:"if"`
				When string `yaml:"when"`
			} `yaml:"rules"`
		} `yaml:"workflow"`
	}
	require.NoError(t, docs[len(docs)-1].Decode(&doc))
	out := make([]string, 0, len(doc.Workflow.Rules))
	for _, r := range doc.Workflow.Rules {
		out = append(out, r.If+"|"+r.When)
	}
	return out
}

func TestMergeGitLabCI_AdmitRulesPrecedeCatchAllNever(t *testing.T) {
	result, err := MergeGitLabCI([]byte(catchAllWorkflowCI))
	require.NoError(t, err)

	assert.Equal(t, []string{
		debugTraceDenyRuleIf + "|never",
		`$CI_PIPELINE_SOURCE == "merge_request_event"|`,
		`$STATISTIC_PIPELINE == "true"|`,
		`$CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"|`,
		`$CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE|`,
		childPipelineAgentRuleIf + "|",
		triggerDispatcherRuleIf + "|",
		"|never",
	}, workflowRuleSummaries(t, result))
}

func TestMergeGitLabCI_CatchAllNeverIdempotent(t *testing.T) {
	first, err := MergeGitLabCI([]byte(catchAllWorkflowCI))
	require.NoError(t, err)
	second, err := MergeGitLabCI(first)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))
}

func TestMergeGitLabCI_CatchAllNeverUnmergeRestoresRules(t *testing.T) {
	merged, err := MergeGitLabCI([]byte(catchAllWorkflowCI))
	require.NoError(t, err)
	restored, err := UnmergeGitLabCI(merged)
	require.NoError(t, err)
	assert.Equal(t, workflowRuleSummaries(t, []byte(catchAllWorkflowCI)),
		workflowRuleSummaries(t, restored))
}

func TestMergeGitLabCI_CatchAllNeverExistingAdmitRuleNotDuplicated(t *testing.T) {
	existing := []byte(`workflow:
  rules:
    - if: $CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"
    - when: never
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)

	assert.Equal(t, []string{
		debugTraceDenyRuleIf + "|never",
		`$CI_PIPELINE_SOURCE == "schedule" && $CI_COMMIT_REF_PROTECTED == "true"|`,
		`$CI_PIPELINE_SOURCE == "api" && $CI_COMMIT_REF_PROTECTED == "true" && $STAGE|`,
		childPipelineAgentRuleIf + "|",
		triggerDispatcherRuleIf + "|",
		"|never",
	}, workflowRuleSummaries(t, result))
}

func TestMergeGitLabCI_ConditionalNeverIsNotCatchAll(t *testing.T) {
	// A when: never rule gated by if:, changes:, or exists: only denies
	// matching pipelines, so admit rules are still appended at the end.
	existing := []byte(`workflow:
  rules:
    - if: $CI_COMMIT_TAG
      when: never
    - changes:
        - docs/**/*
      when: never
    - exists:
        - SKIP_CI
      when: never
    - if: $CI_PIPELINE_SOURCE == "push"
`)
	result, err := MergeGitLabCI(existing)
	require.NoError(t, err)

	got := workflowRuleSummaries(t, result)
	require.Len(t, got, 9)
	assert.Equal(t, `$CI_PIPELINE_SOURCE == "push"|`, got[4])
	assert.Equal(t, triggerDispatcherRuleIf+"|", got[8])
}

func TestIsCatchAllNeverRule(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want bool
	}{
		{"bare when never", "when: never", true},
		{"when never with variables", "when: never\nvariables:\n  X: y", true},
		{"when always", "when: always", false},
		{"if gated", "if: $X\nwhen: never", false},
		{"changes gated", "changes: [a]\nwhen: never", false},
		{"exists gated", "exists: [a]\nwhen: never", false},
		{"no when", "variables:\n  X: y", false},
		{"scalar", "never", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.rule), &doc))
			assert.Equal(t, tt.want, isCatchAllNeverRule(doc.Content[0]))
		})
	}
}
