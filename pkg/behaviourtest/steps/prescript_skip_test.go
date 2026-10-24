package steps

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/gitlabci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeSkipArtifactCI answers DownloadNamedArtifactFromRun by writing files
// (relative path → content) into dest/<artifact name>/, where the real CI
// drivers extract a named artifact, or returning err.
type fakeSkipArtifactCI struct {
	ci.Driver
	files map[string]string
	err   error

	gotRunID int
	gotName  string
}

func (f *fakeSkipArtifactCI) DownloadNamedArtifactFromRun(_ context.Context, _, _ string, runID int, name, dest string) error {
	f.gotRunID = runID
	f.gotName = name
	if f.err != nil {
		return f.err
	}
	for rel, content := range f.files {
		p := filepath.Join(dest, name, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const skipTestRunDir = "fs-pre-0a1b2c"

// runTelemetry returns one OTLP JSON line holding a root run span with
// the given extra attributes JSON (comma-prefixed, or empty).
func runTelemetry(spanName, extraAttrs string) string {
	return fmt.Sprintf(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"cccccccccccccccccccccccccccccccc","spanId":"1111111111111111","name":%q,"startTimeUnixNano":"1000000000","endTimeUnixNano":"2000000000","attributes":[{"key":"fullsend.agent","value":{"stringValue":"pre"}}%s]}]}]}]}`+"\n", spanName, extraAttrs)
}

const skippedAttrTrue = `,{"key":"fullsend.prescript.skipped","value":{"boolValue":true}}`
const skippedAttrFalse = `,{"key":"fullsend.prescript.skipped","value":{"boolValue":false}}`

func skipWorld(fake *fakeSkipArtifactCI) *world.World {
	return &world.World{
		Org:         "org",
		RepoName:    "repo",
		WorkflowRun: &forge.WorkflowRun{ID: 42},
		CI:          fake,
	}
}

func TestThenHarnessRunSkippedByPreScript(t *testing.T) {
	telemetry := filepath.Join(skipTestRunDir, "run-telemetry.jsonl")
	agentResult := filepath.Join(skipTestRunDir, "iteration-1", "output", "agent-result.json")
	behaviourResults := filepath.Join(skipTestRunDir, "iteration-1", "output", "behaviour-results.json")

	tests := []struct {
		name    string
		fake    *fakeSkipArtifactCI
		wantErr []string
	}{
		{
			name: "skipped run",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				telemetry: runTelemetry("run", skippedAttrTrue),
			}},
		},
		{
			name: "agent ran",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				telemetry:        runTelemetry("run", skippedAttrFalse),
				agentResult:      `{"target_branch":"main"}`,
				behaviourResults: `{}`,
			}},
			wantErr: []string{
				`harness "pre" run 42 was not skipped by its pre-script`,
				"fullsend.prescript.skipped=false",
				"agent-result.json present",
				"behaviour-results.json present",
			},
		},
		{
			name: "skip recorded but agent output present",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				telemetry:        runTelemetry("run", skippedAttrTrue),
				behaviourResults: `{}`,
			}},
			wantErr: []string{"behaviour-results.json present"},
		},
		{
			// The agent can write under iteration-N/output but not the
			// host's run-telemetry.jsonl; a nested copy must not count.
			name: "agent-written telemetry copy is ignored",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				telemetry: runTelemetry("run", skippedAttrFalse),
				filepath.Join(skipTestRunDir, "iteration-1", "output", "run-telemetry.jsonl"): runTelemetry("run", skippedAttrTrue),
			}},
			wantErr: []string{"=false"},
		},
		{
			name: "harness without pre-script",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				telemetry: runTelemetry("run", ""),
			}},
			wantErr: []string{"no fullsend.prescript.skipped attribute"},
		},
		{
			name: "no run span",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				telemetry: runTelemetry("agent", skippedAttrTrue),
			}},
			wantErr: []string{"no run span"},
		},
		{
			name: "artifact without telemetry",
			fake: &fakeSkipArtifactCI{files: map[string]string{
				filepath.Join(skipTestRunDir, "agent.log"): "log",
			}},
			wantErr: []string{"no host run-telemetry.jsonl in artifact"},
		},
		{
			name:    "missing artifact",
			fake:    &fakeSkipArtifactCI{err: errors.New(`artifact "fullsend-pre" not found on workflow run 42`)},
			wantErr: []string{`harness "pre" run 42: downloading artifact fullsend-pre`, "not found"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
			err := thenHarnessRunSkippedByPreScript(skipWorld(tc.fake), " pre ")
			assert.Equal(t, 42, tc.fake.gotRunID)
			assert.Equal(t, "fullsend-pre", tc.fake.gotName)
			if len(tc.wantErr) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantErr {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestThenHarnessRunSkippedByPreScript_NoWorkflowRun(t *testing.T) {
	t.Parallel()
	fake := &fakeSkipArtifactCI{}
	err := thenHarnessRunSkippedByPreScript(&world.World{CI: fake}, "pre")
	require.ErrorContains(t, err, "no workflow run recorded")
	assert.Empty(t, fake.gotName, "must not download without a tracked run")
}

// zipArtifact builds an artifact archive from relative path → content.
func zipArtifact(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// The real CI drivers extract into dest/<driver artifact name>/, and the
// two forges keep different prefixes: upload-artifact strips output/,
// GitLab artifacts:paths keeps it. A skipped run must pass on both.
func TestThenHarnessRunSkippedByPreScript_RealDriverLayouts(t *testing.T) {
	telemetry := filepath.ToSlash(filepath.Join(skipTestRunDir, "run-telemetry.jsonl"))
	tests := []struct {
		name     string
		driver   func(forge.Client) ci.Driver
		artifact string
		prefix   string
	}{
		{name: "github", driver: func(c forge.Client) ci.Driver { return githubactions.New(c, "") }, artifact: "fullsend-pre"},
		{name: "gitlab", driver: func(c forge.Client) ci.Driver { return gitlabci.New(c, "") }, artifact: "fullsend pre agent", prefix: "output/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEHAVIOUR_ARTIFACT_DIR", t.TempDir())
			client := forge.NewFakeClient()
			client.WorkflowRunArtifacts = map[int][]forge.WorkflowArtifact{42: {{ID: 7, Name: tc.artifact}}}
			client.WorkflowArtifactContents = map[int][]byte{7: zipArtifact(t, map[string]string{
				tc.prefix + telemetry: runTelemetry("run", skippedAttrTrue),
			})}
			w := &world.World{Org: "org", RepoName: "repo", WorkflowRun: &forge.WorkflowRun{ID: 42}, CI: tc.driver(client)}
			require.NoError(t, thenHarnessRunSkippedByPreScript(w, "pre"))

			// The same layout with the agent's output present still fails.
			client.WorkflowArtifactContents[7] = zipArtifact(t, map[string]string{
				tc.prefix + telemetry: runTelemetry("run", skippedAttrFalse),
				tc.prefix + skipTestRunDir + "/iteration-1/output/agent-result.json": `{}`,
			})
			err := thenHarnessRunSkippedByPreScript(w, "pre")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "=false")
			assert.Contains(t, err.Error(), "agent-result.json present")
		})
	}
}
