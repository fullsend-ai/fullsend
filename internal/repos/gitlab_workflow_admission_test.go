package repos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// rulesNode parses a YAML rules: sequence.
func rulesNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	require.Len(t, doc.Content, 1)
	return doc.Content[0]
}

// The diagnostic for a rule that cannot be evaluated names the rule's
// condition, the input that is unknown at install time, and the remedy
// (#9248).
func TestWorkflowAdmissionProblem_NamesUnknownCondition(t *testing.T) {
	rules := rulesNode(t, `
- if: $CI_DEBUG_TRACE =~ /^(1|t|true)$/i
  when: never
- if: $CI_PIPELINE_SOURCE == "merge_request_event"
- if: $CI_COMMIT_BRANCH && $CI_OPEN_MERGE_REQUESTS
  when: never
- if: $CI_COMMIT_BRANCH
`)
	for _, source := range []string{"trigger", "pipeline", "schedule"} {
		got := workflowAdmissionProblemForEnv(rules, pipelineSourceEnv("main", source), source)
		assert.Contains(t, got, "admits protected-default-branch "+source+" pipelines")
		assert.Contains(t, got, "workflow rule 3 cannot be evaluated, so admission cannot be established")
		assert.Contains(t, got, "its condition `$CI_COMMIT_BRANCH && $CI_OPEN_MERGE_REQUESTS` depends on CI_OPEN_MERGE_REQUESTS, which is not known at install time")
		assert.Contains(t, got, "To fix, add a rule before workflow rule 3 that admits them")
		assert.NotContains(t, got, ";", "the diagnostic is one clause, not a semicolon-joined list")
	}
}

func TestRulesProblem_UnevaluableReasons(t *testing.T) {
	env := triggerPipelineEnv("main")
	problem := func(src string) string {
		rules := rulesNode(t, src)
		outcome, idx := evaluateRules(rules, env)
		require.Equal(t, ruleUnknown, outcome)
		return rulesProblem(rules, env, outcome, idx, "workflow rule")
	}

	cases := map[string]struct {
		rules   string
		want    string
		hasHint bool
	}{
		"several unknown variables": {
			rules:   "- if: $A == \"1\" || $B\n",
			want:    "its condition `$A == \"1\" || $B` depends on A, B, which are not known at install time",
			hasHint: true,
		},
		"repeated unknown variable is named once": {
			rules:   "- if: $A == \"1\" || $A == \"2\"\n",
			want:    "depends on A, which is not known at install time",
			hasHint: true,
		},
		"multi-line condition is shown on one line": {
			rules:   "- if: >-\n    $CI_COMMIT_BRANCH &&\n    $FREEZE\n",
			want:    "its condition `$CI_COMMIT_BRANCH && $FREEZE` depends on FREEZE",
			hasHint: true,
		},
		"interpolated condition": {
			rules:   "- if: '$CI_PIPELINE_SOURCE == \"$[[ inputs.source ]]\"'\n",
			want:    "uses $[[ inputs ]] interpolation, which is not resolved at install time",
			hasHint: true,
		},
		"unsupported syntax": {
			rules: "- if: $CI_PIPELINE_SOURCE ==\n",
			want:  "its condition `$CI_PIPELINE_SOURCE ==` uses syntax this check cannot evaluate",
		},
		"unknown variable with a malformed comparison": {
			rules: "- if: $FREEZE ==\n",
			want:  "its condition `$FREEZE ==` uses syntax this check cannot evaluate",
		},
		"unknown variable with a missing parenthesis": {
			rules: "- if: ($FREEZE == \"1\"\n",
			want:  "uses syntax this check cannot evaluate",
		},
		"unknown variable with trailing text": {
			rules: "- if: $FREEZE == \"1\" garbage\n",
			want:  "uses syntax this check cannot evaluate",
		},
		"unknown variable with an invalid regex": {
			rules: "- if: $FREEZE =~ /[/\n",
			want:  "its condition `$FREEZE =~ /[/` uses syntax this check cannot evaluate",
		},
		"changes clause": {
			rules:   "- changes: [Makefile]\n",
			want:    "its changes: clause depends on repository state that is not known at install time",
			hasHint: true,
		},
		"condition and exists clause": {
			rules:   "- if: $FREEZE\n  exists: [Makefile]\n",
			want:    "depends on FREEZE, which is not known at install time, and its exists: clause depends on repository state",
			hasHint: true,
		},
		"non-literal when": {
			rules: "- when: $[[ inputs.when ]]\n",
			want:  "its when: is not a literal value",
		},
		"non-string if": {
			rules: "- if: [a, b]\n",
			want:  "its if: is not a string",
		},
		"rule that is not a mapping": {
			rules: "- always\n",
			want:  "the rule is not a mapping",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := problem(tc.rules)
			assert.Contains(t, got, "(workflow rule 1 cannot be evaluated, so admission cannot be established: ")
			assert.Contains(t, got, tc.want)
			if tc.hasHint {
				assert.Contains(t, got, "To fix, add a rule before workflow rule 1 that admits them")
			} else {
				assert.NotContains(t, got, "To fix")
			}
		})
	}
}

// Repository-controlled conditions and rule keys must not carry terminal
// control sequences or line breaks into the diagnostic.
func TestRulesProblem_EscapesRepositoryText(t *testing.T) {
	env := triggerPipelineEnv("main")
	rules := rulesNode(t, "- if: \"$FREEZE == \\\"\\e]0;spoof\\a\\e[2K\\\"\"\n- \"x\\nfake line\\e[31m\": [Makefile]\n")
	for idx, want := range map[int]string{
		1: `its condition ` + "`" + `$FREEZE == "\x1b]0;spoof\a\x1b[2K"` + "`" + ` depends on FREEZE`,
		2: `its x\nfake line\x1b[31m: clause depends on repository state`,
	} {
		got := rulesProblem(rules, env, ruleUnknown, idx, "workflow rule")
		assert.Contains(t, got, want)
		for _, r := range got {
			assert.True(t, r == ' ' || (r > ' ' && r < 0x7f), "unexpected raw character %q", r)
		}
	}
}

func TestRulesProblem_OtherOutcomesUnchanged(t *testing.T) {
	env := triggerPipelineEnv("main")
	assert.Equal(t, "(workflow rule 2 rejects them first)", rulesProblem(nil, env, ruleRejected, 2, "workflow rule"))
	assert.Equal(t, "(no include rule matches them)", rulesProblem(nil, env, ruleNoMatch, 0, "include rule"))
	assert.Equal(t, "(the workflow rules cannot be evaluated, so admission cannot be established)", rulesProblem(nil, env, ruleUnknown, 0, "workflow rule"))
	assert.Empty(t, rulesProblem(nil, env, ruleAdmitted, 1, "workflow rule"))
	// Without a rule to inspect, the diagnostic keeps its generic form.
	assert.Equal(t, "(workflow rule 4 cannot be evaluated, so admission cannot be established)", rulesProblem(rulesNode(t, "- when: always\n"), env, ruleUnknown, 4, "workflow rule"))
}
