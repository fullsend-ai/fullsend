package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func parseAgents(t *testing.T, doc string) []AgentEntry {
	t.Helper()
	return parseAgentSettingsConfig(t, doc).AgentEntries()
}

func TestAgentHarness_DecodeAndSettings(t *testing.T) {
	agents := parseAgents(t, `
agents:
  - name: review
    harness:
      env:
        sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium }
  - name: code
    harness: {}
  - name: triage
    harness: ~
`)
	require.Len(t, agents, 3)
	assert.False(t, agents[0].Harness.IsEmpty())
	assert.True(t, agents[0].HasSettings(), "a non-empty block counts as a setting (ADR 0162 Decision 8)")
	assert.True(t, agents[0].IsOverrideOnly())
	assert.True(t, agents[1].Harness.IsEmpty())
	assert.False(t, agents[1].HasSettings(), "an empty block sets nothing")
	assert.Nil(t, agents[2].Harness)

	// Ten aliases to a list of ten aliases, four levels deep, expand past the node limit.
	bomb := "agents:\n  - name: review\n    harness:\n      a: &a [" + strings.Repeat("s,", 10) + "]\n"
	for _, level := range []string{"b: &b [" + strings.Repeat("*a,", 10), "c: &c [" + strings.Repeat("*b,", 10), "d: [" + strings.Repeat("*c,", 10)} {
		bomb += "      " + level + "]\n"
	}
	for doc, want := range map[string]string{
		"agents:\n  - name: review\n    harness: [env]\n":                                    "harness: must be a mapping",
		"agents:\n  - name: review\n    harness: &h { overlays: [ *h ] }\n":                  "a YAML anchor contains itself",
		"agents:\n  - name: review\n    harness: { <<: [ a ] }\n":                            "a merge key (<<) takes a mapping",
		"x: &a { image: a, image: b }\nagents:\n  - name: review\n    harness: { <<: *a }\n": `a merged mapping repeats the key "image"`,
		bomb: "too large once its YAML aliases are expanded",
	} {
		assert.ErrorContains(t, yaml.Unmarshal([]byte(doc), &perRepoConfig{}), want)
	}
}

func TestAgentHarness_RoundTrip(t *testing.T) {
	agents := parseAgents(t, `
agents:
  - name: code
    harness:
      timeout_minutes: 90
      env:
        sandbox: { TIMEOUT_SECONDS: "5400" }
`)
	out, err := yaml.Marshal(perRepoConfig{Agents: agents})
	require.NoError(t, err)
	again := parseAgents(t, string(out))
	require.Len(t, again, 1)
	data, err := yaml.Marshal(again[0].Harness)
	require.NoError(t, err)
	assert.Contains(t, string(data), "timeout_minutes: 90")
	assert.Contains(t, string(data), `TIMEOUT_SECONDS: "5400"`)
}

func TestAgentHarness_DecodeStrict(t *testing.T) {
	agents := parseAgents(t, "agents:\n  - name: code\n    harness: { timeout_minutes: 90, timeout_minute: 5 }\n")
	var out struct {
		TimeoutMinutes int    `yaml:"timeout_minutes"`
		Model          string `yaml:"model"`
	}
	assert.ErrorContains(t, agents[0].Harness.DecodeStrict(&out), "timeout_minute")

	// A merge key from an anchor outside the block resolves when the file is read; the block's own key wins.
	agents = parseAgents(t, "x-shared: &s { timeout_minutes: 30, model: opus }\nagents:\n  - name: code\n    harness:\n      <<: *s\n      timeout_minutes: 90\n")
	require.NoError(t, agents[0].Harness.DecodeStrict(&out))
	assert.Equal(t, 90, out.TimeoutMinutes)
	assert.Equal(t, "opus", out.Model)

	var empty *AgentHarness
	assert.NoError(t, empty.DecodeStrict(&out), "an absent block decodes to nothing")
}

func TestAgentHarness_ValidateExcludedFields(t *testing.T) {
	for _, tc := range []struct {
		name, block, want string
	}{
		{"base", "{ base: harness/review.yaml }", "harness.base is not allowed"},
		{"forge", "{ forge: { github: {} } }", "harness.forge is not allowed"},
		{"runner_env", "{ runner_env: { A: b } }", "harness.runner_env is not allowed"},
		{"overlay runner_env", "{ overlays: [ { when: \"true\" }, { when: \"true\", runner_env: { A: b } } ] }", "harness.overlays[1].runner_env is not allowed"},
		{"runner_env through a merge key", "{ skills: [ &m { runner_env: { A: b } } ], <<: *m }", "harness.runner_env is not allowed"},
		{"key with a control character", "{ env: { runner: { \"A\\n::warning::x\": b } } }", `harness: key "A\n::warning::x" has a control character`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agents := parseAgents(t, "agents:\n  - name: review\n    harness: "+tc.block+"\n")
			assert.ErrorContains(t, ValidateAgentEntries(agents, nil), tc.want)
		})
	}

	agents := parseAgents(t, "agents:\n  - name: review\n    harness: { schema_version: 1, overlays: [ { when: \"true\", env: { sandbox: { A: b } } } ] }\n")
	assert.NoError(t, agents[0].Harness.Validate())
}

// Validate runs the content check the harness package registers.
func TestAgentHarness_ValidateRunsRegisteredCheck(t *testing.T) {
	SetAgentHarnessCheck(func(*AgentHarness) error { return fmt.Errorf("content check ran") })
	t.Cleanup(func() { SetAgentHarnessCheck(nil) })
	agents := parseAgents(t, "agents:\n  - name: review\n    harness: { timeout_minutes: 30 }\n")
	assert.ErrorContains(t, ValidateAgentEntries(agents, nil), "content check ran")
}

func TestAgentHarness_Sets(t *testing.T) {
	agents := parseAgents(t, `
agents:
  - name: review
    harness:
      timeout_minutes: 30
`)
	h := agents[0].Harness
	assert.True(t, h.Sets("timeout_minutes"))
	assert.False(t, h.Sets("model"))
	var none *AgentHarness
	assert.False(t, none.Sets("model"))
}

func TestHarnessBlocksFor(t *testing.T) {
	agents := parseAgents(t, `
agents:
  - name: review
    harness: { timeout_minutes: 30 }
  - name: code
    enabled: false
    harness: { timeout_minutes: 90 }
`)
	assert.Len(t, HarnessBlocksFor(agents, "Review"), 1)
	assert.Nil(t, HarnessBlocksFor(agents, "code"), "a disabled entry's block has no effect")
	assert.Nil(t, HarnessBlocksFor(agents, "triage"))
	assert.NoError(t, ValidateAgentEntries(agents, nil), "a block on a disabled entry is not an error")
}

func TestValidateAgentEntries_OneControlPerSetting(t *testing.T) {
	const custom = "harness/custom.yaml"
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"model both", "agents:\n  - name: review\n    model: opus\n    harness: { model: sonnet }\n", "model is set both on the entry and in its harness: block"},
		{"effort both", "agents:\n  - name: review\n    effort: high\n    harness: { effort: low }\n", "effort is set both on the entry and in its harness: block"},
		{"model both through a merge key", "x-shared: &s { model: sonnet }\nagents:\n  - name: review\n    model: opus\n    harness: { <<: *s }\n", "model is set both on the entry and in its harness: block"},
		{"trigger both on a built-in agent", "agents:\n  - name: review\n    trigger: \"true\"\n    harness: { trigger: \"true\" }\n", "trigger is set both on the entry and in its harness: block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorContains(t, ValidateAgentEntries(parseAgents(t, tc.doc), nil), tc.want)
		})
	}

	for _, doc := range []string{
		"agents:\n  - name: review\n    model: opus\n    harness: { effort: high }\n",
		"agents:\n  - name: review\n    trigger: \"true\"\n    harness: { timeout_minutes: 30 }\n",
		"agents:\n  - source: " + custom + "\n    harness: { trigger: \"true\" }\n",
		// On a built-in agent the block's trigger loads; composition ignores it with a warning.
		"agents:\n  - name: review\n    harness: { trigger: \"true\" }\n",
	} {
		assert.NoError(t, ValidateAgentEntries(parseAgents(t, doc), nil), doc)
	}
}

func TestValidateAgentEntries_ModelBothAcrossLayers(t *testing.T) {
	// The check runs on the merged entry, so a base entry's model conflicts with an overlay block's.
	w, err := ParsePerRepoConfigWriterLayered(
		[]byte("agents:\n  - name: review\n    harness: { model: sonnet }\n"),
		[]byte("agents:\n  - name: review\n    model: opus\n"))
	require.NoError(t, err)
	assert.ErrorContains(t, w.Validate(), "model is set both on the entry and in its harness: block")
}

func TestValidateMergedManaged_HarnessInOneLayerOnly(t *testing.T) {
	var defaults, repo ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("agents:\n  - name: review\n    harness: { timeout_minutes: 30 }\n"), &defaults))
	require.NoError(t, yaml.Unmarshal([]byte("agents:\n  - name: review\n    model: opus\n"), &repo))
	assert.NoError(t, ValidateMergedManaged(MergeManaged(defaults.Writer(), repo.Writer())), "one layer may set the block")

	require.NoError(t, yaml.Unmarshal([]byte("agents:\n  - name: review\n    harness: { env: { sandbox: { A: b } } }\n"), &repo))
	err := ValidateMergedManaged(MergeManaged(defaults.Writer(), repo.Writer()))
	assert.ErrorContains(t, err, `agents entry "review": harness: is set in more than one repos.yaml config layer`)

	require.NoError(t, yaml.Unmarshal([]byte("agents:\n  - name: review\n    model: opus\n    harness: {}\n"), &repo))
	assert.NoError(t, ValidateMergedManaged(MergeManaged(defaults.Writer(), repo.Writer())), "an empty block is not a second layer")

	var emptyParent, child ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("agents:\n  - name: review\n    model: opus\n    harness: {}\n"), &emptyParent))
	require.NoError(t, yaml.Unmarshal([]byte("agents:\n  - name: review\n    harness: { timeout_minutes: 30 }\n"), &child))
	assert.NoError(t, ValidateMergedManaged(MergeManaged(emptyParent.Writer(), child.Writer())), "nor is an empty block in the parent layer")
}

func TestAgentHarness_EmptyBlock(t *testing.T) {
	var h AgentHarness
	out, err := yaml.Marshal(h)
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(out), "a block with no node writes an empty mapping")
	assert.NoError(t, (&h).Validate())
}
