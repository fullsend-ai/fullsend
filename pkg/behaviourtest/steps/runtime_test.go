package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, givenPiAgent(w, "pi-smoke", doc))
	assert.Equal(t, filepath.Join(".fullsend", "agents", "pi-smoke.md"), scmDriver.lastPath)
	body := string(scmDriver.lastContent)
	assert.True(t, strings.HasPrefix(body, "---\nname: pi-smoke"), body)
	assert.NotContains(t, body, "{{fixture:")
	assert.Contains(t, body, `"action": "sufficient"`, "fixture content is inlined verbatim")

	require.ErrorContains(t, givenPiAgent(w, "x", "no frontmatter"), "frontmatter")
	require.ErrorContains(t, givenPiAgent(w, "x", "---\nname: x\n---\n{{fixture:fixtures/nope.json}}"), "reading fixture")
	require.ErrorContains(t, givenPiAgent(w, "../x", doc), "bare file name")
	require.ErrorContains(t, givenPiAgent(&world.World{Org: "org", RepoName: "repo", SCM: scmDriver}, "x", doc), "FixturesRoot")
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
