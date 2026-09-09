package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// recordingSCM captures the last committed file so runtime tests can
// assert on the config that was written, not just that a commit happened.
type recordingSCM struct {
	fakeCleanupSCM
	lastPath    string
	lastContent []byte
}

func (r *recordingSCM) CommitFile(_ context.Context, _, _, path, _ string, content []byte) error {
	if r.commitFileErr != nil {
		return r.commitFileErr
	}
	r.commitFileCalled = true
	r.lastPath = path
	r.lastContent = content
	return nil
}

const perRepoDummyConfig = "version: \"1\"\nruntime: dummy\nroles:\n  - triage\n"

func TestGivenRepositoryRuntime_WritesRuntimeAndRecordsOriginal(t *testing.T) {
	t.Parallel()
	scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte(perRepoDummyConfig)}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver}

	require.NoError(t, givenRepositoryRuntime(w, "pi"))
	assert.True(t, w.RuntimeOverridden)
	assert.Equal(t, "dummy", w.RuntimeOriginal, "install-time runtime is remembered for cleanup")
	assert.Equal(t, filepath.Join(".fullsend", "config.yaml"), scmDriver.lastPath)
	assert.Contains(t, string(scmDriver.lastContent), "runtime: pi")

	// A second override in the same scenario keeps the first original.
	scmDriver.fileContent = scmDriver.lastContent
	require.NoError(t, givenRepositoryRuntime(w, "claude"))
	assert.Equal(t, "dummy", w.RuntimeOriginal)
}

func TestGivenRepositoryRuntime_RefusesNonDummyOriginal(t *testing.T) {
	t.Parallel()
	// No explicit runtime: ConfigRuntime resolves to the code default
	// ("claude"); recording that for restore would hand the slot a real
	// runtime after cleanup, so the step must refuse.
	scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte("version: \"1\"\nroles:\n  - triage\n")}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver}
	err := givenRepositoryRuntime(w, "pi")
	require.ErrorContains(t, err, "suite invariant")
	assert.False(t, scmDriver.commitFileCalled)
	assert.False(t, w.RuntimeOverridden)
}

func TestRestoreRuntime_DefaultsToDummyWhenOriginalUnset(t *testing.T) {
	t.Parallel()
	scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte("version: \"1\"\nruntime: pi\nroles:\n  - triage\n")}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver, RuntimeOverridden: true}
	require.NoError(t, RestoreRuntime(w))
	assert.Contains(t, string(scmDriver.lastContent), "runtime: dummy")
}

func TestGivenPiAgent_CommitsDefinitionWithFixtureInlined(t *testing.T) {
	t.Parallel()
	scmDriver := &recordingSCM{}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver, FixturesRoot: "e2e/behaviour"}
	doc := "---\nname: pi-smoke\ntools: Bash(ls), Write\n---\nWrite this:\n\n{{fixture:fixtures/triage/sufficient.json}}\n"
	require.NoError(t, givenRuntimeAgent(w, "pi-smoke", doc))
	assert.Equal(t, filepath.Join(".fullsend", "agents", "pi-smoke.md"), scmDriver.lastPath)
	body := string(scmDriver.lastContent)
	assert.True(t, strings.HasPrefix(body, "---\nname: pi-smoke"), body)
	assert.NotContains(t, body, "{{fixture:")
	assert.Contains(t, body, `"action": "sufficient"`, "fixture content is inlined verbatim")

	require.ErrorContains(t, givenRuntimeAgent(w, "x", "no frontmatter"), "frontmatter")
	require.ErrorContains(t, givenRuntimeAgent(w, "x", "---\nname: x\n---\n{{fixture:fixtures/nope.json}}"), "reading fixture")
	require.ErrorContains(t, givenRuntimeAgent(w, "../x", doc), "bare file name")
	require.ErrorContains(t, givenRuntimeAgent(&world.World{Org: "org", RepoName: "repo", SCM: scmDriver}, "x", doc), "FixturesRoot")
}

func TestGivenRepositoryRuntime_RejectsUnknownAndMissingRepo(t *testing.T) {
	t.Parallel()
	scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte(perRepoDummyConfig)}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver}
	err := givenRepositoryRuntime(w, "opencode")
	require.ErrorContains(t, err, "not one of")
	assert.False(t, scmDriver.commitFileCalled, "nothing committed for an unknown runtime")
	assert.False(t, w.RuntimeOverridden)

	require.ErrorContains(t, givenRepositoryRuntime(&world.World{SCM: scmDriver}, "pi"), "no repo configured")
}

func TestRestoreRuntime_PutsOriginalBack(t *testing.T) {
	t.Parallel()
	scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte("version: \"1\"\nruntime: pi\nroles:\n  - triage\n")}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver, RuntimeOverridden: true, RuntimeOriginal: "dummy"}
	require.NoError(t, RestoreRuntime(w))
	assert.Contains(t, string(scmDriver.lastContent), "runtime: dummy")
	assert.NotContains(t, string(scmDriver.lastContent), "runtime: pi")
}

func TestCleanupScenario_RestoresRuntimeOnlyWhenOverridden(t *testing.T) {
	t.Parallel()
	overridden := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte("version: \"1\"\nruntime: pi\nroles:\n  - triage\n")}}
	CleanupScenario(&world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: overridden, RuntimeOverridden: true, RuntimeOriginal: "dummy"})
	assert.True(t, overridden.commitFileCalled)
	assert.Contains(t, string(overridden.lastContent), "runtime: dummy")

	untouched := &recordingSCM{}
	CleanupScenario(&world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: untouched})
	assert.False(t, untouched.commitFileCalled, "no config commit when the runtime was not overridden")
}

func writeArtifact(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestRunMetricsAssertions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeArtifact(t, root, "agent-triage-1/metrics.json", `{"runtime":"pi","token_usage":{"input":120,"output":30},"iterations":1}`)
	w := &world.World{ArtifactDir: root}

	require.NoError(t, assertRunSelectedRuntime(w, "pi"))
	require.ErrorContains(t, assertRunSelectedRuntime(w, "dummy"), `runtime = "pi", want "dummy"`)
	require.NoError(t, assertRunMetricsReportTokens(w))

	empty := t.TempDir()
	writeArtifact(t, empty, "metrics.json", `{"runtime":"dummy","token_usage":{"input":0,"output":0}}`)
	require.ErrorContains(t, assertRunMetricsReportTokens(&world.World{ArtifactDir: empty}), "want input and output > 0")
}

func TestAssertPiTranscriptHasToolCall(t *testing.T) {
	t.Parallel()
	const header = `{"type":"session","version":3,"id":"abc","timestamp":"2026-08-22T10:00:00.000Z","cwd":"/r"}` + "\n"
	const toolCall = `{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"gh issue view 1"}}],"stopReason":"toolUse"}}` + "\n"
	const textOnly = `{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"stop"}}` + "\n"

	good := t.TempDir()
	writeArtifact(t, good, "agent-triage-1/metrics.json", `{"runtime":"pi"}`)
	writeArtifact(t, good, "agent-triage-1/iteration-1/transcripts/triage-2026-08-22T10-00-00-000Z_abc.jsonl", header+toolCall)
	// A Claude-shaped transcript in the same tree is ignored, not mistaken for pi.
	writeArtifact(t, good, "agent-triage-1/iteration-1/transcripts/triage-claude.jsonl", `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}`+"\n")
	require.NoError(t, assertPiTranscriptHasToolCall(&world.World{ArtifactDir: good}))

	noCall := t.TempDir()
	writeArtifact(t, noCall, "agent-triage-1/iteration-1/transcripts/triage-2026-08-22T10-00-00-000Z_abc.jsonl", header+textOnly)
	require.ErrorContains(t, assertPiTranscriptHasToolCall(&world.World{ArtifactDir: noCall}), "none records a toolCall")

	none := t.TempDir()
	writeArtifact(t, none, "agent-triage-1/iteration-1/output.jsonl", header+toolCall) // not under transcripts/
	require.ErrorContains(t, assertPiTranscriptHasToolCall(&world.World{ArtifactDir: none}), "no pi session transcript")

	assert.True(t, isPiSessionFile([]byte(header)))
	assert.False(t, isPiSessionFile([]byte(`{"type":"assistant"}`+"\n")))
	assert.False(t, isPiSessionFile(nil))
}

func TestAssertCodexStreamHasToolCall(t *testing.T) {
	t.Parallel()
	const started = `{"type":"thread.started","thread_id":"01a062d8-3c06-78f1-95f2-0fe3e261d47f"}` + "\n"
	const command = `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls .","aggregated_output":"","exit_code":0,"status":"completed"}}` + "\n"
	const textOnly = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"done"}}` + "\n"
	const completed = `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}` + "\n"

	good := t.TempDir()
	writeArtifact(t, good, "agent-triage-1/metrics.json", `{"runtime":"codex"}`)
	writeArtifact(t, good, "agent-triage-1/iteration-1/output.jsonl", started+command+completed)
	// A pi stream in the same tree is ignored, not mistaken for codex.
	writeArtifact(t, good, "agent-triage-2/iteration-1/output.jsonl",
		`{"type":"session","version":3,"id":"abc"}`+"\n")
	require.NoError(t, assertCodexStreamHasToolCall(&world.World{ArtifactDir: good}))

	noCall := t.TempDir()
	writeArtifact(t, noCall, "agent-triage-1/iteration-1/output.jsonl", started+textOnly+completed)
	require.ErrorContains(t, assertCodexStreamHasToolCall(&world.World{ArtifactDir: noCall}),
		"none records a command_execution item")

	none := t.TempDir()
	// A codex stream that is not named output.jsonl is not the artifact.
	writeArtifact(t, none, "agent-triage-1/iteration-1/transcripts/rollout.jsonl", started+command)
	require.ErrorContains(t, assertCodexStreamHasToolCall(&world.World{ArtifactDir: none}),
		"no codex output.jsonl stream")

	// An announced command is not a completed one, and a command the hooks
	// declined is a tool call the agent never got to make. Neither proves
	// the agent ran a tool, so neither may satisfy the step.
	const inProgress = `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"ls .","aggregated_output":"","exit_code":null,"status":"in_progress"}}` + "\n"
	const declinedCmd = `{"type":"item.completed","item":{"id":"item_2","type":"command_execution","command":"rm -rf /","aggregated_output":"","exit_code":null,"status":"declined"}}` + "\n"
	for name, body := range map[string]string{
		"only announced": started + inProgress + completed,
		"only declined":  started + declinedCmd + completed,
	} {
		dir := t.TempDir()
		writeArtifact(t, dir, "agent-triage-1/iteration-1/output.jsonl", body)
		require.ErrorContains(t, assertCodexStreamHasToolCall(&world.World{ArtifactDir: dir}),
			"none records a command_execution item", name)
	}

	assert.True(t, isCodexStreamFile([]byte(started)))
	assert.True(t, isCodexStreamFile([]byte(completed)))
	// Rollout session files use underscored inner names inside event_msg
	// envelopes, so they never look like a --json capture.
	assert.False(t, isCodexStreamFile(
		[]byte(`{"type":"event_msg","payload":{"type":"item_completed"}}`+"\n")))
	// Nor does a codex event name nested inside another envelope's payload,
	// which the old substring scan would have accepted.
	assert.False(t, isCodexStreamFile(
		[]byte(`{"type":"wrapper","inner":{"type":"thread.started"}}`+"\n")))
	assert.False(t, isCodexStreamFile([]byte("not json at all\n")))
	assert.False(t, isCodexStreamFile(nil))
}

func TestGivenRepositoryAgentSettings_WritesEntriesAndSnapshotsAgents(t *testing.T) {
	t.Parallel()
	existing := perRepoDummyConfig + "agents:\n  - source: harness/lint.yaml\n    effort: high\n"
	scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte(existing)}}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver}

	require.NoError(t, givenRepositoryAgentSettings(w, "triage:\n  runtime: claude\ncode:\n  runtime: dummy\nlint:\n  model: haiku\n"))
	assert.True(t, w.AgentsOverridden)
	assert.Equal(t, []config.AgentEntry{{Source: "harness/lint.yaml", Effort: "high"}}, w.AgentsOriginal,
		"pre-scenario agents are remembered for cleanup")
	assert.Equal(t, filepath.Join(".fullsend", "config.yaml"), scmDriver.lastPath)
	written, err := config.ParsePerRepoConfig(scmDriver.lastContent)
	require.NoError(t, err)
	assert.Equal(t, "dummy", written.(config.PerRepoConfigReader).ConfigRuntime(), "repo-wide key untouched")
	lint, ok := config.AgentSettingsFor(written.AgentEntries(), "lint")
	require.True(t, ok)
	assert.Equal(t, config.AgentEntry{Source: "harness/lint.yaml", Effort: "high", Model: "haiku"}, lint, "settings land on the sourced entry")
	triage, ok := config.AgentSettingsFor(written.AgentEntries(), "triage")
	require.True(t, ok)
	assert.Equal(t, config.AgentEntry{Name: "triage", Runtime: "claude"}, triage, "built-ins get a name-only entry")
}

func TestGivenRepositoryAgentSettings_RejectsWhatTheRunnerWouldReject(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{"coder:\n  runtime: dummy\n", "triage:\n  runtime: opencode\n", "triage:\n  effort: turbo\n", ""} {
		scmDriver := &recordingSCM{fakeCleanupSCM: fakeCleanupSCM{fileContent: []byte(perRepoDummyConfig)}}
		w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scmDriver}
		err := givenRepositoryAgentSettings(w, doc)
		require.Error(t, err, "doc %q", doc)
		assert.False(t, scmDriver.commitFileCalled, "doc %q", doc)
	}
}
