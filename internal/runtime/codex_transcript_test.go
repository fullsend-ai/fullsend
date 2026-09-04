package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestCodexExtractTranscripts_DownloadsRollouts(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	rollout := r.codexSessionsDir() + "/2026/09/02/rollout-2026-09-02T10-00-00-abc123.jsonl"
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", "", rollout)

	outDir := filepath.Join(t.TempDir(), "transcripts")
	require.NoError(t, r.ExtractTranscripts("sb", "triage", outDir))

	log := readFileString(t, logPath)
	// Only plain .jsonl, and only regular files: the sessions directory is
	// agent-writable, and a plaintext file named x.jsonl.zst used to ship as
	// an artifact codexRedactFile then declined to rewrite.
	assert.Contains(t, log, "-type f -name '*.jsonl'")
	assert.NotContains(t, log, "*.jsonl.zst")
	assert.Contains(t, log, "download")
	// The local name is prefixed with the agent label, as for pi and Claude,
	// so several agents' transcripts can share one directory.
	_, err := os.Stat(filepath.Join(outDir, "triage-rollout-2026-09-02T10-00-00-abc123.jsonl"))
	assert.NoError(t, err)
}

func TestCodexExtractTranscripts_NoSessionsIsNotAnError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")

	outDir := filepath.Join(t.TempDir(), "transcripts")
	require.NoError(t, CodexRuntime{}.ExtractTranscripts("sb", "triage", outDir))
	assert.NotContains(t, readFileString(t, logPath), "download")
}

func TestCodexExtractDebugLog_OnlyWhenDebugIsOn(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1")
	local := filepath.Join(t.TempDir(), "codex-debug.log")

	require.NoError(t, CodexRuntime{}.ExtractDebugLog("sb", local, ""))
	assert.NotContains(t, readFileString(t, logPath), codexDebugLogFile)

	require.NoError(t, CodexRuntime{}.ExtractDebugLog("sb", local, "1"))
	assert.Contains(t, readFileString(t, logPath), codexDebugLogFile)
}

func TestCodexParseTranscriptErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Fixture names are .ndjson (the repo convention, PR B); the destinations
	// keep the .jsonl names the runtime actually writes, which is what
	// ParseTranscriptErrors scans for.
	copyFixture := func(name, dest string) {
		data, err := os.ReadFile(filepath.Join("testdata", "codex", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, dest), data, 0o644))
	}
	copyFixture("turn_failed.ndjson", "output.jsonl")
	copyFixture("basic_run.ndjson", "ok.jsonl")
	// A rollout session file is a different envelope; it must be skipped
	// rather than misread as a failed run.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout.jsonl"),
		[]byte(`{"type":"session_meta","payload":{"id":"abc"}}`+"\n"), 0o644))
	// Non-JSONL files are ignored entirely.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "last-message.txt"), []byte("done"), 0o644))

	got := CodexRuntime{}.ParseTranscriptErrors(dir)
	require.Len(t, got, 1)
	assert.Equal(t, "output.jsonl", got[0].Source)
	assert.True(t, got[0].IsError)
}

func TestCodexParseTranscriptErrors_MissingDir(t *testing.T) {
	t.Parallel()

	assert.Nil(t, CodexRuntime{}.ParseTranscriptErrors(filepath.Join(t.TempDir(), "nope")))
}

func TestCodexParseTranscriptFile(t *testing.T) {
	t.Parallel()

	te, ok := CodexRuntime{}.ParseTranscriptFile(filepath.Join("testdata", "codex", "turn_failed.ndjson"))
	require.True(t, ok)
	assert.True(t, te.IsError)

	_, ok = CodexRuntime{}.ParseTranscriptFile(filepath.Join(t.TempDir(), "absent.jsonl"))
	assert.False(t, ok)
}

// TestCodexRun_StreamVerdictOverridesExitZero is the runtime half of the
// exit-code contract: `codex exec` exits 0 on a failed turn, so a run whose
// stream reports a failure must still be reported as failed.
func TestCodexRun_StreamVerdictOverridesExitZero(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", filepath.Join("testdata", "codex", "turn_failed.ndjson"))

	outPath := filepath.Join(t.TempDir(), "output.jsonl")
	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "openai/gpt-5.6-luna",
		Effort:      "high",
		OutputPath:  outPath,
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)

	require.NoError(t, err)
	assert.Equal(t, 1, exit, "a stream-reported failure must override the zero exit")
	// The stream carries no model, so the runner supplies the resolved id.
	assert.Equal(t, "gpt-5.6-luna", metrics.Model)

	// The stream is tee'd so ParseTranscriptFile can reach the same verdict.
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestCodexRun_SuccessfulRunReportsMetrics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", filepath.Join("testdata", "codex", "basic_run.ndjson"))

	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gpt-5.6-luna",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Positive(t, metrics.InputTokens)
	assert.Positive(t, metrics.OutputTokens)
	// codex reports no cost, so it stays zero rather than being guessed at.
	assert.Zero(t, metrics.TotalCostUSD)
}

func TestCodexRun_RejectsForeignModelBeforeSpending(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "anthropic-vertex/claude-opus-4-6",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, err)
	assert.Equal(t, -1, exit)
	assert.Contains(t, err.Error(), "codex takes OpenAI model ids only")
	assert.NotContains(t, readFileString(t, logPath), "exec --json", "the run must not start")
}

// Security is a runner-side decision; a manifest without a hook plan while the
// runner says hooks are on means the wiring was lost, and running anyway would
// be the silently-unhooked failure ADR 0090 forbids.
func TestCodexRun_RefusesHooklessRunWhenSecurityIsOn(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	_, err := r.Run(context.Background(), RunParams{
		SandboxName:       "sb",
		RepoDir:           "/sandbox/workspace/repo",
		Model:             "gpt-5.6-luna",
		HooksSettingsPath: r.codexHooksPath(),
		Timeout:           time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "carries no hook plan")
	assert.NotContains(t, readFileString(t, logPath), "exec --json")
}

func TestCodexRun_AcceptsManifestHookPlan(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r,
		codexHooksManifestFor(r.codexHooksDir(), security.SandboxHookConfigFromHarness(&harness.Harness{})))
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", filepath.Join("testdata", "codex", "basic_run.ndjson"))

	exit, err := r.Run(context.Background(), RunParams{
		SandboxName:       "sb",
		RepoDir:           "/sandbox/workspace/repo",
		Model:             "gpt-5.6-luna",
		HooksSettingsPath: r.codexHooksPath(),
		Timeout:           time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Contains(t, readFileString(t, logPath), "--dangerously-bypass-hook-trust")
}

// seedCodexManifest puts a manifest into the fake openshell's store so
// readCodexManifest finds one, and records the artifact digests Bootstrap
// would have kept in the runner-held digests for that sandbox.
func seedCodexManifest(t *testing.T, storeDir string, r CodexRuntime, hooks *codexHooksManifest) {
	t.Helper()
	hashes := codexRunnerHeldDigestSet{
		ConfigTOML: "config0000000000000000000000000000000000000000000000000000000000",
		AgentModel: "openai/gpt-5.6-luna",
	}
	if hooks != nil {
		hashes.HooksJSON = "hooks00000000000000000000000000000000000000000000000000000000000"
		hashes.HookScripts = testCodexHookScripts()
	}
	recordRunnerHeldDigests("sb", hashes)
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })
	data, err := json.MarshalIndent(codexManifest{
		AgentName: "triage",
		// Deliberately different from the runner-held AgentModel: Run must
		// never read the model from this agent-writable, undigested file.
		Model:        "openai/gpt-9-tampered",
		CodexVersion: "0.152.1",
		Hooks:        hooks,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	name := r.codexManifestPath()
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, sanitizeStorePath(name)), data, 0o644))
}

func sanitizeStorePath(p string) string {
	out := make([]rune, 0, len(p))
	for _, r := range p {
		if r == '/' {
			r = '_'
		}
		out = append(out, r)
	}
	return string(out)
}

// TestCodexRun_FallsBackToTheAgentDefinitionModel pins codex to the same model
// fallback chain NeedsOpenAIProvider uses (EffectiveModel). Resolving it any
// other way would let the run call a model the provider decision did not
// account for.
func TestCodexRun_FallsBackToTheAgentDefinitionModel(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	// The runner-held state carries the agent definition's frontmatter model;
	// the run params name none. The manifest's own copy is deliberately not
	// consulted, so it is left saying something else below.
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", filepath.Join("testdata", "codex", "basic_run.ndjson"))

	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "gpt-5.6-luna", metrics.Model, "the runner-held openai/ spec, prefix stripped")
	assert.Contains(t, readFileString(t, logPath), "--model 'gpt-5.6-luna'")
}

func TestCodexRun_RequiresAModelWhenNothingNamesOne(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	// Clear the runner-held model so neither side names one. (Emptying the
	// manifest's copy would prove nothing: Run does not read it.)
	held, ok := lookupRunnerHeldDigests("sb")
	require.True(t, ok)
	held.AgentModel = ""
	recordRunnerHeldDigests("sb", held)
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	_, runErr := r.Run(t.Context(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "no model was named")
	assert.NotContains(t, readFileString(t, logPath), "exec --json")
}

// TestCodexRun_RefusesInconsistentHookWiring pins the invariant between the
// runner's own signal and what Bootstrap recorded. Both derive from the
// harness's SecurityEnabled() today; a refactor that split them would drop the
// hooks.json digest from the guard while the adapter still loaded, and nothing
// would fail.
func TestCodexRun_RefusesInconsistentHookWiring(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r,
		codexHooksManifestFor(r.codexHooksDir(), security.SandboxHookConfigFromHarness(&harness.Harness{})))
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1")

	// Bootstrap recorded no hooks.json digest, but the runner says hooks are on.
	recordRunnerHeldDigests("sb", codexRunnerHeldDigestSet{ConfigTOML: "deadbeef"})
	t.Cleanup(func() { forgetRunnerHeldDigests("sb") })

	_, err := r.Run(t.Context(), RunParams{
		SandboxName:       "sb",
		RepoDir:           "/sandbox/workspace/repo",
		Model:             "gpt-5-mini",
		HooksSettingsPath: r.codexHooksPath(),
		Timeout:           time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook wiring is inconsistent")
	assert.NotContains(t, readFileString(t, logPath), "exec --json")
}

// Run cannot fall back to the manifest for the digests, so a sandbox this
// runner never bootstrapped is refused rather than run unguarded.
func TestCodexRun_RefusesWithoutRunnerHeldDigests(t *testing.T) {
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), storeDir, "codex-cli 0.152.1")
	forgetRunnerHeldDigests("sb-never-bootstrapped")

	_, err := r.Run(t.Context(), RunParams{
		SandboxName: "sb-never-bootstrapped",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gpt-5-mini",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no runner-held config digests")
}

// TestCodexExtractTranscripts_DiscardsSpoofedFiles covers the agent-writable
// sessions directory: a `.jsonl` there is a claim, not a fact.
func TestCodexExtractTranscripts_DiscardsSpoofedFiles(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	spoof := r.codexSessionsDir() + "/rollout-planted.jsonl"
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", "", spoof)

	outDir := filepath.Join(t.TempDir(), "transcripts")
	require.NoError(t, r.ExtractTranscripts("sb", "smoke", outDir))

	// The fake writes a non-rollout body for a path containing "planted", so
	// nothing is kept.
	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a file that is not a codex rollout must not ship as the transcript")
}

func TestCodexIsRolloutFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}

	require.NoError(t, codexIsRolloutFile(write("ok.jsonl",
		`{"type":"session_meta","payload":{"id":"abc"}}`+"\n")))
	// A leading blank line is tolerated.
	require.NoError(t, codexIsRolloutFile(write("blank.jsonl",
		"\n"+`{"type":"response_item","payload":{}}`+"\n")))

	for name, body := range map[string]string{
		// The tee'd stream uses dotted names and is a different artifact.
		"stream.jsonl": `{"type":"thread.started","thread_id":"t1"}` + "\n",
		"plain.jsonl":  "not json at all\n",
		"empty.jsonl":  "",
		"other.jsonl":  `{"type":"something_else"}` + "\n",
	} {
		err := codexIsRolloutFile(write(name, body))
		assert.Error(t, err, "%s must be refused", name)
	}
}

// TestCodexRun_EmptyStreamIsAnError covers the interrupted-turn shape: codex
// can exit 0 having emitted no terminal event, and a run with no completed
// turn is a failure however the process exited.
func TestCodexRun_EmptyStreamIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"empty stream":     "",
		"truncated stream": `{"type":"thread.started","thread_id":"t1"}` + "\n" + `{"type":"turn.started"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "openshell.log")
			storeDir := t.TempDir()
			r := CodexRuntime{}
			seedCodexManifest(t, storeDir, r, nil)
			fixture := filepath.Join(t.TempDir(), "stream.jsonl")
			require.NoError(t, os.WriteFile(fixture, []byte(body), 0o644))
			fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", fixture)

			exit, err := r.Run(t.Context(), RunParams{
				SandboxName: "sb",
				RepoDir:     "/sandbox/workspace/repo",
				Model:       "gpt-5-mini",
				Timeout:     time.Minute,
			}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

			require.NoError(t, err)
			assert.Equal(t, 1, exit, "the fake exits 0; a stream with no completed turn must still fail the run")
		})
	}
}

func TestCodexValidSessionPath(t *testing.T) {
	t.Parallel()

	dir := CodexRuntime{}.codexSessionsDir()
	require.NoError(t, codexValidSessionPath(dir, dir+"/2026/09/02/rollout-x.jsonl"))

	for name, path := range map[string]string{
		"outside the sessions dir":       "/sandbox/workspace/.env",
		"a sibling with the same prefix": dir + "-evil/rollout.jsonl",
		"the directory itself":           dir,
		"a parent segment":               dir + "/../workspace/.env",
		"a control character":            dir + "/roll\tout.jsonl",
		"a newline":                      dir + "/roll\nout.jsonl",
	} {
		assert.Error(t, codexValidSessionPath(dir, path), "%s must be refused", name)
	}
}

// The extraction path is a chain of things that can fail against a sandbox
// that is misbehaving or hostile; none of them may leave a half-written
// artifact behind or abort the rest of the run.
func TestCodexExtractTranscripts_FailurePaths(t *testing.T) {
	r := CodexRuntime{}

	t.Run("an unwritable output dir is an error", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		// A file where the directory should be: MkdirAll cannot proceed.
		blocked := filepath.Join(t.TempDir(), "transcripts")
		require.NoError(t, os.WriteFile(blocked, []byte("x"), 0o644))

		err := r.ExtractTranscripts("sb", "smoke", blocked)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "creating output dir")
	})

	// A `find` that exits non-zero is not an error: the command carries
	// `|| true` because find reports a non-zero status for an unreadable
	// subdirectory, which is not a reason to fail the run. What is an error is
	// not being able to reach the sandbox at all.
	t.Run("an unreachable sandbox is an error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())

		err := r.ExtractTranscripts("sb", "smoke", filepath.Join(t.TempDir(), "out"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "finding transcripts")
	})

	t.Run("a path outside the sessions dir is skipped, not downloaded", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "openshell.log")
		// `find` is made to name the workspace .env — the shape a crafted
		// entry or a followed symlink would have.
		fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1", "",
			sandbox.SandboxWorkspace+"/.env")

		outDir := filepath.Join(t.TempDir(), "out")
		require.NoError(t, r.ExtractTranscripts("sb", "smoke", outDir))
		assert.NotContains(t, readFileString(t, logPath), "download",
			"a path outside the sessions directory must never reach a download")
		entries, err := os.ReadDir(outDir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("a download that fails leaves nothing behind", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "openshell.log")
		rollout := r.codexSessionsDir() + "/rollout-x.jsonl"
		fakeOpenshellCodex(t, logPath, t.TempDir(), "codex-cli 0.152.1", "", rollout)
		t.Setenv("FULLSEND_TEST_FAIL_MATCH", "download")

		outDir := filepath.Join(t.TempDir(), "out")
		require.NoError(t, r.ExtractTranscripts("sb", "smoke", outDir),
			"one failed transcript must not fail the run")
		entries, err := os.ReadDir(outDir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestCodexExtractDebugLog_FailurePaths(t *testing.T) {
	r := CodexRuntime{}

	t.Run("a download that fails is reported", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		t.Setenv("FULLSEND_TEST_FAIL_MATCH", "download")

		err := r.ExtractDebugLog("sb", filepath.Join(t.TempDir(), "codex-debug.log"), "1")
		require.Error(t, err)
	})

	// codex logs at error level by default and raises with RUST_LOG, so this
	// file carries request bodies and command text — the same material
	// output.jsonl does, and it is uploaded the same way.
	t.Run("a credential-shaped line is masked", func(t *testing.T) {
		fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.152.1")
		local := filepath.Join(t.TempDir(), "codex-debug.log")
		t.Setenv("FULLSEND_TEST_DOWNLOAD_BODY", "ERROR authorization: Bearer "+codexTestSecret)

		require.NoError(t, r.ExtractDebugLog("sb", local, "1"))
		got, err := os.ReadFile(local)
		require.NoError(t, err)
		assert.NotContains(t, string(got), codexTestSecret)
		assert.Contains(t, string(got), "ERROR", "only the credential is masked")
	})

}

// TestCodexRun_IgnoresATamperedManifestModel is the point of holding the model
// on the runner side: the manifest lives in the agent-writable config
// directory and carries no digest, so reading the model from it would let an
// agent move a validation retry onto a different model — and a different cost
// tier — than the run was authorised for.
func TestCodexRun_IgnoresATamperedManifestModel(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil) // manifest says gpt-9-tampered
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1",
		filepath.Join("testdata", "codex", "basic_run.ndjson"))

	metrics := &RunMetrics{}
	exit, err := r.Run(t.Context(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, "gpt-5.6-luna", metrics.Model, "the runner-held model, not the manifest's")
	log := readFileString(t, logPath)
	assert.Contains(t, log, "--model 'gpt-5.6-luna'")
	assert.NotContains(t, log, "gpt-9-tampered")
}
