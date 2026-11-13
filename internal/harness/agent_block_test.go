package harness

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func agentBlock(t *testing.T, doc string) *config.AgentHarness {
	t.Helper()
	var b config.AgentHarness
	require.NoError(t, yaml.Unmarshal([]byte(doc), &b))
	return &b
}

func loadWithBlocks(t *testing.T, path string, opts ComposeOpts, blocks ...string) (*Harness, error) {
	t.Helper()
	for _, b := range blocks {
		opts.Blocks = append(opts.Blocks, agentBlock(t, b))
	}
	if opts.Agent == "" {
		opts.Agent = "review"
	}
	h, _, err := LoadWithBase(context.Background(), path, opts)
	return h, err
}

const blockTestHarness = `
agent: agents/review.md
role: review
readonly_repo: true
timeout_minutes: 45
skills:
  - skills/pr-review
env:
  runner: { REVIEW_FINDING_SEVERITY_THRESHOLD: low }
  sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: low, TIMEOUT_SECONDS: "2700" }
overlays:
  - when: 'runtime.forge == "gitlab"'
    env:
      runner: { OVERLAY_ONLY: "1" }
`

func TestAgentBlock_OverridesDeclaredValues(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "review.yaml", blockTestHarness)

	h, err := loadWithBlocks(t, path, ComposeOpts{ForgePlatform: "gitlab"}, `
timeout_minutes: 90
skills: [skills/extra]
env:
  runner: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium }
  sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: medium, TIMEOUT_SECONDS: "5400" }
`)
	require.NoError(t, err)
	assert.Equal(t, 90, h.TimeoutMinutes)
	assert.Equal(t, "medium", h.Env.Runner["REVIEW_FINDING_SEVERITY_THRESHOLD"])
	assert.Equal(t, "medium", h.Env.Sandbox["REVIEW_FINDING_SEVERITY_THRESHOLD"])
	assert.Equal(t, "5400", h.Env.Sandbox["TIMEOUT_SECONDS"])
	assert.Equal(t, "1", h.Env.Runner["OVERLAY_ONLY"], "the harness's matching overlay still applies")
	assert.Equal(t, "agents/review.md", h.Agent, "fields the block does not set keep the harness value")
	assert.Equal(t, "review", h.Role)
	assert.True(t, h.RepoReadonly(), "the read-only checkout survives the block (#7360)")
	require.Len(t, h.Skills, 2, "lists concatenate as in base: composition")
	assert.Equal(t, "skills/extra", h.Skills[1].Source)
}

// A URL-sourced harness fetches its own paths from that URL; a block's paths stay local.
func TestAgentBlock_URLSourcedHarness(t *testing.T) {
	harnessContent := "agent: agents/triage.md\nrole: triage\npolicy: policies/triage.yaml\npre_script: scripts/pre-triage.sh\n"
	server, policy := setupScriptTestServer(t, []byte(harnessContent), map[string][]byte{
		"/scripts/pre-triage.sh": []byte("#!/bin/sh\n"),
		"/scripts/mine.sh":       []byte("#!/bin/sh\n"),
	})
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "triage.yaml", harnessContent)

	h, err := loadWithBlocks(t, path, ComposeOpts{
		WorkspaceRoot: filepath.Join(dir, "cache"),
		FetchPolicy:   policy,
		OrgAllowlist:  []string{server.URL + "/"},
		SourceURL:     server.URL + "/harness/triage.yaml",
	}, "pre_script: scripts/mine.sh")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(h.Policy), "the harness's own path is fetched: %s", h.Policy)
	assert.Equal(t, "scripts/mine.sh", h.PreScript)
}

func TestAgentBlock_TildeSetsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "review.yaml", blockTestHarness)

	h, err := loadWithBlocks(t, path, ComposeOpts{}, "env: { sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: ~ } }")
	require.NoError(t, err)
	v, ok := h.Env.Sandbox["REVIEW_FINDING_SEVERITY_THRESHOLD"]
	assert.True(t, ok, "~ does not remove the variable")
	assert.Empty(t, v, "~ sets it empty (ADR 0162 Decision 3)")
}

func TestAgentBlock_LayersInOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "review.yaml", blockTestHarness)

	h, err := loadWithBlocks(t, path, ComposeOpts{},
		"timeout_minutes: 60\nenv: { sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: high } }",
		"timeout_minutes: 90")
	require.NoError(t, err)
	assert.Equal(t, 90, h.TimeoutMinutes, "config.yaml's block wins over config.base.yaml's")
	assert.Equal(t, "high", h.Env.Sandbox["REVIEW_FINDING_SEVERITY_THRESHOLD"], "config.base.yaml's value stays when config.yaml does not replace it")
}

func TestAgentBlock_DeclaredNames(t *testing.T) {
	dir := t.TempDir()
	writeTestHarness(t, dir, "ancestor.yaml", `
agent: agents/review.md
role: review
env:
  sandbox: { ANCESTOR_ONLY: "1" }
forge:
  github: {}
  gitlab:
    env:
      sandbox: { GITLAB_ONLY: "1" }
`)
	path := writeTestHarness(t, dir, "review.yaml", "base: ancestor.yaml\n"+blockTestHarness)

	for _, tc := range []struct {
		name, block string
	}{
		{"a name only a base: ancestor declares", "env: { sandbox: { ANCESTOR_ONLY: \"2\" } }"},
		{"a name only another forge's block declares", "env: { sandbox: { GITLAB_ONLY: \"2\" } }"},
		{"a name only a non-matching overlay declares", "env: { runner: { OVERLAY_ONLY: \"2\" } }"},
		{"a declared name in the block's own overlay", "overlays: [ { when: 'true', env: { sandbox: { REVIEW_FINDING_SEVERITY_THRESHOLD: high } } } ]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithBlocks(t, path, ComposeOpts{ForgePlatform: "github"}, tc.block)
			assert.NoError(t, err)
		})
	}

	for _, tc := range []struct {
		name, block, want string
	}{
		{"an undeclared name", "env: { sandbox: { REVIEW_UX_ENABLED: \"false\" } }", `agents entry "review": harness.env.sandbox.REVIEW_UX_ENABLED: the "review" harness does not declare this variable in env.sandbox`},
		{"a name declared in the other section", "env: { runner: { TIMEOUT_SECONDS: \"1\" } }", `harness.env.runner.TIMEOUT_SECONDS: the "review" harness does not declare this variable in env.runner`},
		{"an undeclared name in the block's overlay", "overlays: [ { when: 'true', env: { runner: { NEW: x } } } ]", `harness.overlays[0].env.runner.NEW: the "review" harness does not declare`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithBlocks(t, path, ComposeOpts{ForgePlatform: "github"}, tc.block)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestAgentBlock_ReservedName(t *testing.T) {
	saved := reservedSandboxKey
	t.Cleanup(func() { SetReservedSandboxKeyCheck(saved) })
	SetReservedSandboxKeyCheck(func(k string) bool { return strings.EqualFold(k, "PATH") })

	dir := t.TempDir()
	path := writeTestHarness(t, dir, "review.yaml", `
agent: agents/review.md
role: review
env:
  runner: { PATH: /usr/bin }
`)
	_, err := loadWithBlocks(t, path, ComposeOpts{}, "env: { runner: { PATH: /tmp } }")
	assert.ErrorContains(t, err, "harness.env.runner.PATH: fullsend reserves this name in the sandbox")
}

func TestAgentBlock_RejectsKeys(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "review.yaml", blockTestHarness)

	for _, tc := range []struct {
		name, block, want string
	}{
		{"unknown nested key", "env: { sandbx: { A: b } }", "sandbx"},
		{"base", "base: other.yaml", "harness.base is not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadWithBlocks(t, path, ComposeOpts{}, tc.block)
			assert.ErrorContains(t, err, tc.want)
		})
	}

	_, err := loadWithBlocks(t, path, ComposeOpts{}, "schema_version: 1\ntimeout_minutes: 30")
	assert.NoError(t, err, "a block may declare the version of the harness it builds on")
}

// Config validation rejects a malformed block before any harness loads.
func TestAgentBlock_ConfigValidationChecksContent(t *testing.T) {
	for block, want := range map[string]string{
		"{ timeout_minute: 90 }":                        "timeout_minute",
		"{ timeout_minutes: [a, b] }":                   "cannot unmarshal",
		"{ schema_version: 2 }":                         "schema_version 2 is not supported",
		"{ effort: ultra }":                             `harness: effort "ultra" is not valid`,
		"{ overlays: [ { when: 'this is not CEL' } ] }": "harness: overlays[0].when",
	} {
		var entries []config.AgentEntry
		require.NoError(t, yaml.Unmarshal([]byte("- name: review\n  harness: "+block+"\n"), &entries))
		assert.ErrorContains(t, config.ValidateAgentEntries(entries, nil), want, block)
	}

	var entries []config.AgentEntry
	require.NoError(t, yaml.Unmarshal([]byte("- name: review\n  harness: { validation_loop: { schema: a.json }, overlays: [ { when: \"true\", validation_loop: { schema: b.json } } ] }\n"), &entries))
	assert.NoError(t, config.ValidateAgentEntries(entries, nil), "the harness below supplies what a block leaves out, here the loop's script")
}

func TestAgentBlock_TriggerOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "custom.yaml", `
agent: agents/custom.md
role: custom
trigger: 'event.transition.kind == "opened"'
`)
	const blockTrigger = `trigger: 'event.transition.kind == "label_changed"'`
	h, err := loadWithBlocks(t, path, ComposeOpts{Agent: "custom"}, blockTrigger)
	require.NoError(t, err)
	assert.Equal(t, `event.transition.kind == "label_changed"`, h.Trigger)
	assert.NotContains(t, diagnosticCodes(h.Lint()), CodeBlockTriggerIgnored)

	// On a built-in agent name the block's trigger is not evaluated (ADR 0162 Decision 6).
	h, err = loadWithBlocks(t, path, ComposeOpts{Agent: "Triage"}, "timeout_minutes: 30", blockTrigger)
	require.NoError(t, err)
	assert.Equal(t, `event.transition.kind == "opened"`, h.Trigger)
	var warning string
	for _, d := range h.Lint() {
		if d.Code == CodeBlockTriggerIgnored {
			warning = d.Message
		}
	}
	assert.Contains(t, warning, `agents entry "Triage"`)
}

func TestAgentBlock_ForgePlatformStillRequired(t *testing.T) {
	for name, forge := range map[string]string{
		"platform not configured": "gitlab",
		"unrecognized forge key":  "bitbucket",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeTestHarness(t, t.TempDir(), "review.yaml", "agent: agents/review.md\nrole: review\nforge:\n  "+forge+":\n    env:\n      sandbox: { A: b }\n")
			_, plain := loadWithBlocks(t, path, ComposeOpts{ForgePlatform: "github"})
			_, withBlock := loadWithBlocks(t, path, ComposeOpts{ForgePlatform: "github"}, "timeout_minutes: 30")
			require.Error(t, plain)
			require.EqualError(t, withBlock, plain.Error())
		})
	}
}

func TestAgentBlock_HarnessErrorsReadAsWithoutBlock(t *testing.T) {
	dir := t.TempDir()
	path := writeTestHarness(t, dir, "review.yaml", `
agent: agents/review.md
role: review
overlays:
  - when: 'this is not CEL'
    env: { sandbox: { A: b } }
`)
	_, err := loadWithBlocks(t, path, ComposeOpts{}, "timeout_minutes: 30")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "base", "the agent's own harness is not a base: harness to its user")
}

// A block does not inherit the runtime fetch fields (ADR 0158); Lint names the ones it leaves out.
func TestAgentBlock_DropsRuntimeFetchWarns(t *testing.T) {
	const plain = "agent: agents/custom.md\nrole: custom\n"
	const fetch = "allowed_remote_resources: [https://raw.githubusercontent.com/org/skills/]\nallow_runtime_fetch: true\n"
	tests := []struct {
		name    string
		harness string
		blocks  []string
		fetchOn bool
		dropped string // fields the warning names; empty for no warning
	}{
		{"no block", plain + fetch, nil, true, ""},
		{"block without the fields", plain + fetch, []string{"timeout_minutes: 30"}, false, "allow_runtime_fetch, allowed_remote_resources"},
		{"block sets them again", plain + fetch, []string{fetch}, true, ""},
		{"only the cap is lost", plain + fetch + "max_runtime_fetches: 50\n", []string{fetch}, true, "max_runtime_fetches"},
		{"second block drops what the first set", plain, []string{fetch, "timeout_minutes: 30"}, false, "allow_runtime_fetch, allowed_remote_resources"},
		{"block turns fetch off on purpose", plain + fetch, []string{"allow_runtime_fetch: false"}, false, ""},
		{"a later block does not undo that", plain + fetch, []string{"allow_runtime_fetch: false", "timeout_minutes: 30"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTestHarness(t, t.TempDir(), "custom.yaml", tt.harness)
			h, err := loadWithBlocks(t, path, ComposeOpts{}, tt.blocks...)
			require.NoError(t, err)
			assert.Equal(t, tt.fetchOn, h.AllowRuntimeFetch)
			var messages []string
			for _, d := range h.Lint() {
				if d.Code == CodeBlockDropsRuntimeFetch {
					messages = append(messages, d.Message)
				}
			}
			if tt.dropped == "" {
				assert.Empty(t, messages)
				return
			}
			require.Len(t, messages, 1)
			assert.Contains(t, messages[0], "leaves out "+tt.dropped+",")
		})
	}
}

func diagnosticCodes(diags []Diagnostic) []string {
	codes := make([]string, 0, len(diags))
	for _, d := range diags {
		codes = append(codes, d.Code)
	}
	return codes
}

// A block that leaves a field alone does not change what lint reports for the
// agent's own harness, or the forge sections lint can read, whether or not a
// forge is selected.
func TestAgentBlock_LintFieldsUnchanged(t *testing.T) {
	// One of everything lint flags in a forge section or an overlay.
	const section = `
    runner_env: { OLD: "1" }
    env:
      runner: { ISSUE: "${GITHUB_ISSUE_URL}" }
      sandbox: { S: x }
    host_files:
      - { src: "${GITHUB_ISSUE_URL}/x.env", dest: /sandbox/.env.d/x.env, expand: true }
    validation_loop: { script: scripts/v.sh, max_iterations: 2, preflight_check: scripts/check.sh }
`
	harnesses := map[string]string{
		"top level":     "env:\n  runner: { GITHUB_ISSUE_URL: \"${GITHUB_ISSUE_URL}\", X: a }\n",
		"forge section": "env:\n  runner: { X: a }\nforge:\n  github:" + section,
		"with a base":   "base: base.yaml\nenv:\n  runner: { X: a }\nforge:\n  github:" + section,
		"overlay":       "env:\n  runner: { X: a }\noverlays:\n  - when: runtime.forge == \"github\"" + section,
	}
	for name, body := range harnesses {
		dir := t.TempDir()
		writeTestHarness(t, dir, "base.yaml", "agent: agents/review.md\nrole: review\n")
		path := writeTestHarness(t, dir, "review.yaml", "agent: agents/review.md\nrole: review\n"+body)
		for _, forge := range []string{"", "github"} {
			opts := ComposeOpts{ForgePlatform: forge}
			plain, err := loadWithBlocks(t, path, opts)
			require.NoError(t, err)
			want := plain.Lint()
			require.NotEmpty(t, want)
			for _, blocks := range [][]string{
				{"env: { runner: { X: b } }"},
				{"env: { runner: { X: b } }", "env: { runner: { X: c } }"},
				// Its overlay cannot coexist with the harness's forge section.
				{"overlays:\n  - when: runtime.forge == \"github\"\n    env:\n      runner: { X: b }\n"},
			} {
				h, err := loadWithBlocks(t, path, opts, blocks...)
				require.NoError(t, err, "%s, forge %q, blocks %q", name, forge, blocks)
				assert.Equal(t, want, h.Lint(), "%s, forge %q, blocks %q", name, forge, blocks)
				assert.Equal(t, plain.Forge, h.Forge, "%s, forge %q, blocks %q", name, forge, blocks)
			}
		}
	}
}

// With blocks, the last block's runtime fetch declaration is the one kept.
func TestRuntimeFetchLayer(t *testing.T) {
	path := writeTestHarness(t, t.TempDir(), "custom.yaml", "agent: agents/custom.md\nrole: custom\nallow_runtime_fetch: true\nallowed_remote_resources: [https://example.com/a/]\n")

	h, err := RuntimeFetchLayer(path, "custom", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com/a/"}, h.AllowedRemoteResources)

	h, err = RuntimeFetchLayer(path, "custom", []*config.AgentHarness{
		agentBlock(t, "allowed_remote_resources: [https://example.com/b/]"),
		agentBlock(t, "timeout_minutes: 30"),
	})
	require.NoError(t, err)
	assert.Empty(t, h.AllowedRemoteResources)
	assert.False(t, h.AllowRuntimeFetch)
}
