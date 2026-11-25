package cli

import (
	"bytes"
	"encoding/json"
	"github.com/fullsend-ai/fullsend/internal/executionplan"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExecutionPlanCommandRejectsBeforeDriver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	for _, input := range []string{`{}`, `{"version":1,"unknown":true}`, `{} {}`, `null`} {
		require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
		cmd := newExecutionPlanCmd()
		cmd.SetArgs([]string{"--plan", path, "--state-dir", filepath.Join(dir, "state"), "--driver", "/nonexistent"})
		cmd.SetOut(&bytes.Buffer{})
		require.Error(t, cmd.Execute())
		_, err := os.Stat(filepath.Join(dir, "state"))
		require.True(t, os.IsNotExist(err))
	}
	cmd := newExecutionPlanCmd()
	cmd.SetArgs([]string{"--plan", filepath.Join(dir, "missing"), "--state-dir", dir, "--driver", "/nonexistent"})
	require.Error(t, cmd.Execute())
	cmd = newExecutionPlanCmd()
	cmd.SetArgs([]string{"--driver", "/nonexistent"})
	require.Error(t, cmd.Execute())
}

func TestExecutionPlanRegistered(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"execution-plan"})
	require.NoError(t, err)
	require.Equal(t, "execution-plan", cmd.Name())
}

func TestExecutionPlanCommandReportsBlockedWork(t *testing.T) {
	driver, err := exec.LookPath("false")
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	plan := executionplan.Plan{Version: 1, ID: "case-123", Request: "advisory-123", Authorization: "reviewed-grant", Harness: "assessment", ControlSHA: strings.Repeat("a", 40), Deadline: time.Now().Add(time.Hour), MaxParallel: 1, MaxAttempts: 1, Targets: []executionplan.Target{{ID: "release-1", Forge: "github", Host: "github.com", Repository: "example/project", Ref: "refs/heads/release-1", SHA: strings.Repeat("b", 40)}}}
	input, err := json.Marshal(plan)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, input, 0o600))
	// A valid JSON prefix must not hide trailing data beyond the read limit.
	oversized := append(append(append([]byte(nil), input...), bytes.Repeat([]byte(" "), (4<<20)+1-len(input))...), []byte("{}")...)
	require.NoError(t, os.WriteFile(path, oversized, 0o600))
	cmd := newExecutionPlanCmd()
	cmd.SetArgs([]string{"--plan", path, "--state-dir", filepath.Join(dir, "state"), "--driver", driver})
	require.Error(t, cmd.Execute())
	_, err = os.Stat(filepath.Join(dir, "state"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, os.WriteFile(path, input, 0o600))
	cmd = newExecutionPlanCmd()
	cmd.SetArgs([]string{"--plan", path, "--state-dir", filepath.Join(dir, "state"), "--driver", "/nonexistent"})
	require.Error(t, cmd.Execute())
	cmd = newExecutionPlanCmd()
	cmd.SetArgs([]string{"--plan", path, "--state-dir", filepath.Join(dir, "state"), "--driver", driver})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	var report struct {
		Complete bool
		State    executionplan.State
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.False(t, report.Complete)
	require.Equal(t, "authorization", report.State.Targets[0].Blocker)
	require.Empty(t, report.State.Targets[0].Attempts)
}
