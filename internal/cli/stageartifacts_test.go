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
	cmd := newRootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"stage-artifacts"}, args...))
	err := cmd.Execute()
	return strings.TrimSpace(stdout.String()), err
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
			name:   "dispatch payload claims jira without a valid event",
			config: "version: \"1\"\n",
			setup: func(t *testing.T, fullsendDir string) []string {
				writeDispatchPayload(t, fullsendDir, `{"_normalized_event":{"source":{"system":"jira"}}}`)
				return nil
			},
			wantErr: "declares a Jira source",
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

	upload := s[uploadIdx:]
	if end := strings.Index(upload, "\n    - name:"); end > 0 {
		upload = upload[:end]
	}
	assert.Contains(t, upload, `contains(fromJSON('["all","metadata"]'), steps.stage-artifacts.outputs.level)`)
	assert.Contains(t, upload, "path: ${{ steps.stage-artifacts.outputs.dir }}")
	assert.NotContains(t, upload, "github.workspace }}/output", "the raw output/ is never uploaded")
}
