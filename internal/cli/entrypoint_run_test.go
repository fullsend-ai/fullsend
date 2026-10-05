package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunEntrypointChecked(t *testing.T) {
	tests := []struct {
		name       string
		preErr     error
		runCode    int
		runErr     error
		postErr    error
		wantCode   int
		wantErr    string
		wantRuns   int
		wantChecks int
	}{
		{name: "success", runCode: 0, wantCode: 0, wantRuns: 1, wantChecks: 2},
		{name: "nonzero does not replay", runCode: 23, wantCode: 23, wantErr: "entrypoint exited with status 23", wantRuns: 1, wantChecks: 2},
		{name: "launch error", runCode: -1, runErr: errors.New("launch failed"), wantCode: -1, wantErr: "launch failed", wantRuns: 1, wantChecks: 2},
		{name: "precheck blocks launch", preErr: errors.New("tampered before start"), wantCode: entrypointIntegrityFailureExitCode, wantErr: "pre-entrypoint integrity verification failed", wantRuns: 0, wantChecks: 1},
		{name: "postcheck rejects result", runCode: 0, postErr: errors.New("tampered during run"), wantCode: entrypointIntegrityFailureExitCode, wantErr: "post-entrypoint integrity verification failed", wantRuns: 1, wantChecks: 2},
		{name: "cancellation still postchecks", runCode: -1, runErr: context.Canceled, wantCode: -1, wantErr: "context canceled", wantRuns: 1, wantChecks: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs, checks := 0, 0
			check := func(context.Context) error {
				checks++
				if checks == 1 {
					return tt.preErr
				}
				return tt.postErr
			}
			code, err := runEntrypointChecked(context.Background(), check, func(context.Context) (int, error) {
				runs++
				return tt.runCode, tt.runErr
			})
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantRuns, runs)
			assert.Equal(t, tt.wantChecks, checks)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}

	assert.False(t, shouldPreserveEntrypointDiagnostics(false, nil, 0, nil))
	assert.True(t, shouldPreserveEntrypointDiagnostics(true, errors.New("failed"), -1, nil))
	assert.True(t, shouldPreserveEntrypointDiagnostics(true, nil, 124, nil), "timeout exit is diagnostic")
	assert.True(t, shouldPreserveEntrypointDiagnostics(true, context.Canceled, -1, context.Canceled), "cancellation is diagnostic")
	assert.False(t, shouldPreserveEntrypointDiagnostics(true, nil, 0, nil), "successful runs use the normal extraction path")
}

func TestPreserveEntrypointDiagnosticsExtractsOutputAndTranscripts(t *testing.T) {
	binDir := t.TempDir()
	openshell := filepath.Join(binDir, "openshell")
	script := `#!/bin/sh
case "$2" in
  exec)
    case "$*" in
      *"/sandbox/workspace/output"*) printf '%s\n' '/sandbox/workspace/output/result.txt' ;;
      *"/sandbox/claude-config"*) printf '%s\n' '/sandbox/claude-config/projects/session.jsonl' ;;
      *) exit 0 ;;
    esac
    ;;
  download)
    mkdir -p "$5"
    case "$4" in
      */result.txt) printf 'partial result\n' > "$5/result.txt" ;;
      *) printf '{"type":"assistant"}\n' > "$5/session.jsonl" ;;
    esac
    ;;
  *) echo "unexpected openshell args: $*" >&2; exit 1 ;;
esac
`
	require.NoError(t, os.WriteFile(openshell, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	outputDir := filepath.Join(t.TempDir(), "output")
	transcriptDir := filepath.Join(t.TempDir(), "transcripts")
	preserveEntrypointDiagnostics("diagnostic-sandbox", "code", agentruntime.ClaudeRuntime{}, outputDir, transcriptDir, ui.New(io.Discard))

	output, err := os.ReadFile(filepath.Join(outputDir, "result.txt"))
	require.NoError(t, err)
	assert.Equal(t, "partial result\n", string(output))
	transcript, err := os.ReadFile(filepath.Join(transcriptDir, "code-session.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(transcript), "assistant")
}
