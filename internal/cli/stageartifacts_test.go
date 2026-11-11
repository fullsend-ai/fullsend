package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	stageTestRunDir = "fs-cod-abc123"
	// stageJiraEvent and stageGitHubEvent are valid normalized events
	// (the _normalized_event values of the dispatch payloads below).
	stageJiraEvent   = `{"repo":"org/repo","entity":{"kind":"work_item","id":10042,"key":"PROJ-1","url":"https://example.atlassian.net/browse/PROJ-1"},"transition":{"kind":"comment_added","comment":{"body":"/fs-code","command":"/fs-code"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":[]},"source":{"system":"jira","raw_type":"comment"}}`
	stageGitHubEvent = `{"repo":"org/repo","entity":{"kind":"work_item","id":10,"url":"https://github.com/org/repo/issues/10"},"transition":{"kind":"label_changed","label":{"name":"ready-to-code","action":"added"}},"actor":{"id":"u1","kind":"human","role":"write","is_entity_author":false},"state":{"labels":["ready-to-code"]},"source":{"system":"github","raw_type":"issues"}}`
)

// stageFixture lays out a fullsend dir with config, and a run output dir
// holding run-level metadata plus transcript files.
func stageFixture(t *testing.T, configYAML string) (fullsendDir, outputDir, stagedDir string) {
	t.Helper()
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITHUB_EVENT_NAME", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("CI_PROJECT_VISIBILITY", "")
	fullsendDir = t.TempDir()
	if configYAML != "" {
		require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(configYAML), 0o644))
	}
	outputDir = t.TempDir()
	run := filepath.Join(outputDir, stageTestRunDir)
	require.NoError(t, os.MkdirAll(filepath.Join(run, "iteration-1", "transcripts"), 0o755))
	for rel, content := range map[string]string{
		"metrics.json":                      `{}`,
		"run-telemetry.jsonl":               `{"resourceSpans":[]}` + "\n",
		"iteration-1/output.jsonl":          "conversation\n",
		"iteration-1/transcripts/t.jsonl":   "transcript\n",
		"iteration-1/output/agent-out.json": "{}",
	} {
		p := filepath.Join(run, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	stagedDir = filepath.Join(t.TempDir(), "staged")
	return fullsendDir, outputDir, stagedDir
}

func stagedFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	return out
}

func runStageCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	level, _, err := runStageCmdStderr(t, args...)
	return level, err
}

// runStageCmdStderr runs stage-artifacts and returns stdout (trimmed) and
// stderr separately: stdout must hold only the level.
func runStageCmdStderr(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(append([]string{"stage-artifacts"}, args...))
	err := cmd.Execute()
	return strings.TrimSpace(stdout.String()), stderr.String(), err
}

// Without a valid normalized event the source is unknown: the level is the
// strictest of content and every by_source entry, never the forge's level,
// even on a forge CI runner. A dispatch payload that names a source it
// cannot back makes the source unknown too.
func TestStageArtifactsCmd_UnknownSourceUsesStrictest(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		gitlabCI   bool
		payload    string
		want       string
		wantReason string
	}{
		{name: "missing event, jira none", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n    github: all\n", gitlabCI: true, want: "none", wantReason: "no valid normalized event"},
		{name: "missing event, only github metadata", config: "version: \"1\"\nartifacts:\n  by_source:\n    github: metadata\n", want: "metadata", wantReason: "no valid normalized event"},
		{name: "missing event, no artifacts key", config: "version: \"1\"\n", want: "all", wantReason: "no valid normalized event"},
		{
			name:       "payload claims an external source but fails validation",
			config:     "version: \"1\"\nartifacts:\n  by_source:\n    linear: none\n",
			payload:    `{"_normalized_event":{"source":{"system":"linear"}}}`,
			want:       "none",
			wantReason: `names source "linear" but its normalized event is invalid`,
		},
		{
			name:       "payload claims jira but fails validation",
			config:     "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n",
			payload:    `{"_normalized_event":{"source":{"system":"jira"}}}`,
			want:       "metadata",
			wantReason: `names source "jira" but its normalized event is invalid`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fullsendDir, outputDir, stagedDir := stageFixture(t, tc.config)
			if tc.gitlabCI {
				t.Setenv("GITLAB_CI", "true")
			}
			if tc.payload != "" {
				writeDispatchPayload(t, fullsendDir, tc.payload)
			}
			level, stderr, err := runStageCmdStderr(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
			require.NoError(t, err)
			assert.Equal(t, tc.want, level)
			assert.Contains(t, stderr, "Run artifact level "+tc.want+": run source unknown (")
			assert.Contains(t, stderr, tc.wantReason)
			assert.Contains(t, stderr, "using the strictest of artifacts.content and every artifacts.by_source entry")
		})
	}
}

// The standard GitHub stage jobs get no normalized event: a native forge
// event on GitHub Actions, with no payload claiming a source, is github.
// Anything else stays unknown.
func TestStageArtifactsCmd_NativeGitHubEvent(t *testing.T) {
	nativeEvent := `{"action":"created","issue":{"number":10},"comment":{"id":1,"body":"/fs-code"},"repository":{"full_name":"org/repo","visibility":"private"}}`
	tests := []struct {
		name       string
		config     string
		actions    string
		eventName  string
		eventBody  string
		payload    string
		want       string
		wantStderr string
	}{
		{name: "issue_comment ignores jira entry", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n", actions: "true", eventName: "issue_comment", eventBody: nativeEvent, want: "all", wantStderr: `run source "github" (native issue_comment event)`},
		{name: "native event uses github entry", config: "version: \"1\"\nartifacts:\n  by_source:\n    github: metadata\n    jira: none\n", actions: "true", eventName: "pull_request_target", eventBody: `{"pull_request":{"number":3}}`, want: "metadata", wantStderr: `run source "github" (native pull_request_target event)`},
		{name: "workflow_dispatch without payload is unknown", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n", actions: "true", eventName: "workflow_dispatch", eventBody: `{"inputs":{}}`, want: "metadata", wantStderr: "run source unknown (no valid normalized event)"},
		{name: "native event with invalid jira payload is unknown", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n", actions: "true", eventName: "issue_comment", eventBody: nativeEvent, payload: `{"_normalized_event":{"source":{"system":"jira"}}}`, want: "none", wantStderr: `names source "jira" but its normalized event is invalid`},
		{name: "native event with an unclaimed normalized event key is unknown", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n", actions: "true", eventName: "issues", eventBody: nativeEvent, payload: `{"_normalized_event":{}}`, want: "none", wantStderr: "run source unknown (no valid normalized event)"},
		{name: "event itself claims a source", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n", actions: "true", eventName: "issues", eventBody: `{"source":{"system":"jira"}}`, want: "none", wantStderr: "run source unknown"},
		{name: "GITHUB_ACTIONS unset is unknown", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n", eventName: "issue_comment", eventBody: nativeEvent, want: "metadata", wantStderr: "run source unknown (no valid normalized event)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fullsendDir, outputDir, stagedDir := stageFixture(t, tc.config)
			t.Setenv("GITHUB_ACTIONS", tc.actions)
			t.Setenv("GITHUB_EVENT_NAME", tc.eventName)
			ev := filepath.Join(t.TempDir(), "event.json")
			require.NoError(t, os.WriteFile(ev, []byte(tc.eventBody), 0o644))
			t.Setenv("GITHUB_EVENT_PATH", ev)
			if tc.payload != "" {
				writeDispatchPayload(t, fullsendDir, tc.payload)
			}
			level, stderr, err := runStageCmdStderr(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
			require.NoError(t, err)
			assert.Equal(t, tc.want, level)
			assert.Contains(t, stderr, tc.wantStderr)
		})
	}
}

// An unknown source that tightens the level beyond artifacts.content is a
// warning (and an annotation on GitHub Actions), not an info line.
func TestStageArtifactsCmd_UnknownSourceStricterThanContentWarns(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "schedule")
	level, stderr, err := runStageCmdStderr(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "metadata", level, "stdout holds only the level")
	assert.Contains(t, stderr, "::warning::Run artifact level metadata: run source unknown (no valid normalized event)")

	// When the unknown source does not tighten anything, it stays info.
	fullsendDir, outputDir, stagedDir = stageFixture(t, "version: \"1\"\nartifacts:\n  content: metadata\n")
	t.Setenv("GITHUB_ACTIONS", "true")
	_, stderr, err = runStageCmdStderr(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.NotContains(t, stderr, "::warning::Run artifact level")
}

// A valid event whose source differs from another dispatch payload's
// claim leaves the source unknown.
func TestStageArtifactsCmd_ConflictingPayloadSourceIsUnknown(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n")
	writeDispatchPayload(t, fullsendDir, `{"_normalized_event":`+stageGitHubEvent+`}`)
	ghEvent := filepath.Join(t.TempDir(), "gh-event.json")
	inner := strings.ReplaceAll(`{"_normalized_event":`+stageJiraEvent+`}`, `"`, `\"`)
	require.NoError(t, os.WriteFile(ghEvent, []byte(`{"inputs":{"event_payload":"`+inner+`"}}`), 0o644))
	t.Setenv("GITHUB_EVENT_PATH", ghEvent)
	level, stderr, err := runStageCmdStderr(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "none", level)
	assert.Contains(t, stderr, `names source "jira", not the run's source "github"`)
}

// An event source in another case still matches its lowercase key, and a
// by_source key that is not lowercase fails closed at staging.
func TestStageArtifactsCmd_SourceCase(t *testing.T) {
	t.Run("event source is lowercased", func(t *testing.T) {
		fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n")
		writeDispatchPayload(t, fullsendDir, `{"_normalized_event":`+strings.Replace(stageJiraEvent, `"system":"jira"`, `"system":"Jira"`, 1)+`}`)
		level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
		require.NoError(t, err)
		assert.Equal(t, "none", level)
	})
	t.Run("uppercase by_source key fails closed", func(t *testing.T) {
		fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\nartifacts:\n  by_source:\n    Jira: metadata\n")
		writeDispatchPayload(t, fullsendDir, `{"_normalized_event":`+stageJiraEvent+`}`)
		level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid artifacts.by_source key "Jira"`)
		assert.Empty(t, level)
		assert.Empty(t, stagedFiles(t, stagedDir))
	})
}

func TestStageArtifactsCmd_NoForgeFlag(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\n")
	_, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir, "--forge", "gitlab")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flag")
}

func TestDetectRepoVisibility_GitHub(t *testing.T) {
	writeEvent := func(t *testing.T, body string) string {
		p := filepath.Join(t.TempDir(), "event.json")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
		return p
	}
	tests := []struct {
		name string
		path func(t *testing.T) string
		want repoVisibility
	}{
		{name: "no GITHUB_EVENT_PATH", path: func(*testing.T) string { return "" }, want: repoVisibilityUnknown},
		{name: "unreadable file", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.json") }, want: repoVisibilityUnknown},
		{name: "bad JSON", path: func(t *testing.T) string { return writeEvent(t, "{not json") }, want: repoVisibilityUnknown},
		{name: "visibility internal", path: func(t *testing.T) string {
			return writeEvent(t, `{"repository":{"visibility":"internal","private":true}}`)
		}, want: repoVisibilityNotPrivate},
		{name: "visibility private", path: func(t *testing.T) string {
			return writeEvent(t, `{"repository":{"visibility":"private","private":true}}`)
		}, want: repoVisibilityPrivate},
		{name: "visibility public", path: func(t *testing.T) string {
			return writeEvent(t, `{"repository":{"visibility":"public","private":false}}`)
		}, want: repoVisibilityNotPrivate},
		{name: "only private true", path: func(t *testing.T) string { return writeEvent(t, `{"repository":{"private":true}}`) }, want: repoVisibilityPrivate},
		{name: "only private false", path: func(t *testing.T) string { return writeEvent(t, `{"repository":{"private":false}}`) }, want: repoVisibilityNotPrivate},
		{name: "no repository", path: func(t *testing.T) string { return writeEvent(t, `{}`) }, want: repoVisibilityUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITLAB_CI", "")
			t.Setenv("GITHUB_ACTIONS", "true")
			t.Setenv("GITHUB_EVENT_PATH", tc.path(t))
			assert.Equal(t, tc.want, detectRepoVisibility())
		})
	}
}

const stageWarningText = "Run started from jira: the run artifact publishes the full transcript"

func TestStageArtifactsCmd_PublicTranscriptWarning(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		event    string
		env      map[string]string
		ghaEvent string
		wantWarn bool
	}{
		{name: "jira with nothing set", event: stageJiraEvent, wantWarn: true},
		{name: "jira with only another source set", config: "version: \"1\"\nartifacts:\n  by_source:\n    linear: none\n", event: stageJiraEvent, wantWarn: true},
		{name: "explicit content all", config: "version: \"1\"\nartifacts:\n  content: all\n", event: stageJiraEvent},
		{name: "by_source jira all", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: all\n", event: stageJiraEvent},
		{name: "jira at metadata", config: "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n", event: stageJiraEvent},
		{name: "github source", event: stageGitHubEvent},
		{name: "private github repo", event: stageJiraEvent, env: map[string]string{"GITHUB_ACTIONS": "true"}, ghaEvent: `{"repository":{"private":true}}`},
		{name: "public github repo", event: stageJiraEvent, env: map[string]string{"GITHUB_ACTIONS": "true"}, ghaEvent: `{"repository":{"private":false}}`, wantWarn: true},
		{name: "github visibility unknown", event: stageJiraEvent, env: map[string]string{"GITHUB_ACTIONS": "true"}, ghaEvent: `{}`, wantWarn: true},
		{name: "private gitlab project", event: stageJiraEvent, env: map[string]string{"GITLAB_CI": "true", "CI_PROJECT_VISIBILITY": "private"}},
		{name: "internal gitlab project", event: stageJiraEvent, env: map[string]string{"GITLAB_CI": "true", "CI_PROJECT_VISIBILITY": "internal"}, wantWarn: true},
		{name: "gitlab visibility unknown", event: stageJiraEvent, env: map[string]string{"GITLAB_CI": "true"}, wantWarn: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fullsendDir, outputDir, stagedDir := stageFixture(t, tc.config)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.ghaEvent != "" {
				p := filepath.Join(t.TempDir(), "gha-event.json")
				require.NoError(t, os.WriteFile(p, []byte(tc.ghaEvent), 0o644))
				t.Setenv("GITHUB_EVENT_PATH", p)
			}
			eventFile := filepath.Join(t.TempDir(), "event.json")
			require.NoError(t, os.WriteFile(eventFile, []byte(tc.event), 0o644))
			level, stderr, err := runStageCmdStderr(t, "--output-dir", outputDir, "--artifact-dir", stagedDir,
				"--fullsend-dir", fullsendDir, "--event-file", eventFile)
			require.NoError(t, err)
			assert.NotContains(t, level, "Run started", "stdout holds only the level")
			if !tc.wantWarn {
				assert.NotContains(t, stderr, "Run started from")
				return
			}
			assert.Equal(t, "all", level)
			assert.Contains(t, stderr, stageWarningText)
			assert.Contains(t, stderr, "set artifacts.by_source.jira to metadata or none in config.yaml; set it to all to silence this warning.")
			if tc.env["GITHUB_ACTIONS"] == "true" {
				assert.Contains(t, stderr, "::warning::"+stageWarningText, "on GitHub Actions the warning is an annotation")
			} else {
				assert.NotContains(t, stderr, "::warning::")
			}
		})
	}
}

// The source comes from an event, so the warning sanitizes it before it
// reaches the CI log.
func TestPublicTranscriptWarning_SanitizesSource(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITHUB_EVENT_NAME", "")
	t.Setenv("GITLAB_CI", "")
	msg := publicTranscriptWarning(artifactResolution{level: "all", source: "evil::error::x\n\x1b[31m"})
	require.NotEmpty(t, msg)
	assert.NotContains(t, msg, "::")
	assert.NotContains(t, msg, "\n")
	assert.NotContains(t, msg, "\x1b")
}

func TestStageArtifactsCmd_UnsetStagesEverything(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t, "")
	level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "all", level)
	assert.ElementsMatch(t, stagedFiles(t, outputDir), stagedFiles(t, stagedDir))
}

func TestStageArtifactsCmd_ContentMetadata(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\nartifacts:\n  content: metadata\n")
	level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "metadata", level)
	assert.ElementsMatch(t, []string{
		stageTestRunDir + "/metrics.json",
		stageTestRunDir + "/run-telemetry.jsonl",
	}, stagedFiles(t, stagedDir))
}

func TestStageArtifactsCmd_BySourceFromEventFile(t *testing.T) {
	cfg := "version: \"1\"\nartifacts:\n  by_source:\n    jira: metadata\n"
	tests := []struct {
		name  string
		event string
		want  string
	}{
		{name: "jira run", event: stageJiraEvent, want: "metadata"},
		{name: "github run", event: stageGitHubEvent, want: "all"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fullsendDir, outputDir, stagedDir := stageFixture(t, cfg)
			eventFile := filepath.Join(t.TempDir(), "event.json")
			require.NoError(t, os.WriteFile(eventFile, []byte(tc.event), 0o644))
			level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir,
				"--fullsend-dir", fullsendDir, "--event-file", eventFile)
			require.NoError(t, err)
			assert.Equal(t, tc.want, level)
		})
	}
}

func TestStageArtifactsCmd_StricterContentWinsForDispatchedJiraRun(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t,
		"version: \"1\"\nartifacts:\n  content: none\n  by_source:\n    jira: metadata\n")
	writeDispatchPayload(t, fullsendDir, `{"_normalized_event":`+stageJiraEvent+`}`)
	level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "none", level)
	assert.Empty(t, stagedFiles(t, stagedDir))
}

func TestStageArtifactsCmd_DispatchedJiraRunUsesSourceLevel(t *testing.T) {
	fullsendDir, outputDir, stagedDir := stageFixture(t, "version: \"1\"\nartifacts:\n  by_source:\n    jira: none\n")
	writeDispatchPayload(t, fullsendDir, `{"_normalized_event":`+stageJiraEvent+`}`)
	level, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "none", level)
}

func TestStageArtifactsCmd_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		setup   func(t *testing.T, fullsendDir string) []string
		wantErr string
	}{
		{
			name:    "invalid level",
			config:  "version: \"1\"\nartifacts:\n  content: secret\n",
			wantErr: "resolving artifact level",
		},
		{
			name:    "malformed config",
			config:  "artifacts: [\n",
			wantErr: "resolving artifact level",
		},
		{
			name:   "unreadable event file",
			config: "version: \"1\"\n",
			setup: func(t *testing.T, _ string) []string {
				return []string{"--event-file", filepath.Join(t.TempDir(), "missing.json")}
			},
			wantErr: "reading event file",
		},
		{
			name:   "invalid event file",
			config: "version: \"1\"\n",
			setup: func(t *testing.T, _ string) []string {
				p := filepath.Join(t.TempDir(), "event.json")
				require.NoError(t, os.WriteFile(p, []byte(`{"source":{"system":"jira"}}`), 0o644))
				return []string{"--event-file", p}
			},
			wantErr: "parsing event file",
		},
		{
			name:    "uppercase by_source key",
			config:  "version: \"1\"\nartifacts:\n  by_source:\n    Jira: none\n",
			wantErr: "resolving artifact level",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fullsendDir, outputDir, stagedDir := stageFixture(t, tc.config)
			require.NoError(t, os.MkdirAll(stagedDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(stagedDir, "stale.txt"), []byte("old"), 0o644))
			args := []string{"--output-dir", outputDir, "--artifact-dir", stagedDir, "--fullsend-dir", fullsendDir}
			if tc.setup != nil {
				args = append(args, tc.setup(t, fullsendDir)...)
			}
			level, err := runStageCmd(t, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Empty(t, level)
			assert.Empty(t, stagedFiles(t, stagedDir), "nothing from output/ is staged")
		})
	}
}

func TestStageArtifactsCmd_RejectsOverlappingDirs(t *testing.T) {
	fullsendDir, outputDir, _ := stageFixture(t, "")
	before := stagedFiles(t, outputDir)
	_, err := runStageCmd(t, "--output-dir", outputDir, "--artifact-dir", filepath.Join(outputDir, "staged"),
		"--fullsend-dir", fullsendDir)
	require.Error(t, err)
	assert.Equal(t, before, stagedFiles(t, outputDir))
}

// action.yml builds the staged copy after eval-measure and uploads only
// that copy, skipping the upload for none or an unresolved level.
func TestActionYML_UploadsStagedCopy(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	require.NoError(t, err)
	s := string(b)
	measureIdx := strings.Index(s, "name: Eval measurements")
	stageIdx := strings.Index(s, "name: Stage fullsend artifacts")
	uploadIdx := strings.Index(s, "name: Upload fullsend artifacts")
	require.Greater(t, measureIdx, 0)
	require.Greater(t, stageIdx, measureIdx, "staging runs after eval-measure")
	require.Greater(t, uploadIdx, stageIdx, "upload runs after staging")

	stage := s[stageIdx:uploadIdx]
	assert.Contains(t, stage, "id: stage-artifacts")
	assert.Contains(t, stage, "if: always() && inputs.agent != '__install_only__'")
	assert.Contains(t, stage, "fullsend stage-artifacts")
	assert.Contains(t, stage, `--output-dir "${GITHUB_WORKSPACE}/output"`)
	assert.Contains(t, stage, `LEVEL="unresolved"`)
	assert.Contains(t, stage, `rm -rf -- "${STAGED_DIR}"`)
	// A CLI older than the command gets its own warning, still with no
	// upload; the probe runs only in the failure branch.
	elseIdx := strings.Index(stage, `LEVEL="unresolved"`)
	missingIdx := strings.Index(stage, "if ! command -v fullsend >/dev/null 2>&1; then")
	probeIdx := strings.Index(stage, "elif ! fullsend stage-artifacts --help >/dev/null 2>&1; then")
	require.Greater(t, missingIdx, elseIdx, "the binary check runs only after staging failed")
	require.Greater(t, probeIdx, missingIdx, "a missing binary is told apart before the version probe")
	assert.Contains(t, stage, "::warning::The fullsend CLI is not installed")
	assert.Contains(t, stage, "::warning::The installed fullsend CLI predates stage-artifacts, so no run artifact is uploaded. Set the version input")
	assert.Contains(t, stage, "::warning::Run artifact level could not be resolved; no run artifact is uploaded")

	upload := s[uploadIdx:]
	if end := strings.Index(upload, "\n    - name:"); end > 0 {
		upload = upload[:end]
	}
	assert.Contains(t, upload, `contains(fromJSON('["all","metadata"]'), steps.stage-artifacts.outputs.level)`)
	assert.Contains(t, upload, "path: ${{ steps.stage-artifacts.outputs.dir }}")
	assert.NotContains(t, upload, "github.workspace }}/output", "the raw output/ is never uploaded")
}
