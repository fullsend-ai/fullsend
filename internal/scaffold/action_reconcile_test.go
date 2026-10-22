package scaffold

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type compositeActionStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type compositeActionFile struct {
	Outputs map[string]struct {
		Description string `yaml:"description"`
		Value       string `yaml:"value"`
	} `yaml:"outputs"`
	Runs struct {
		Steps []compositeActionStep `yaml:"steps"`
	} `yaml:"runs"`
}

func loadCompositeAction(t *testing.T) compositeActionFile {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	require.NoError(t, err)

	var action compositeActionFile
	require.NoError(t, yaml.Unmarshal(content, &action))
	return action
}

func extractActionStep(t *testing.T, stepName string) compositeActionStep {
	t.Helper()
	action := loadCompositeAction(t)
	for _, step := range action.Runs.Steps {
		if step.Name == stepName {
			require.NotEmpty(t, step.Run, "step %q has no run script", stepName)
			return step
		}
	}
	t.Fatalf("step %q not found in action.yml", stepName)
	return compositeActionStep{}
}

// TestActionOutputs_DeclaresDisabled verifies that action.yml exports the
// disabled output.
func TestActionOutputs_DeclaresDisabled(t *testing.T) {
	action := loadCompositeAction(t)
	out, ok := action.Outputs["disabled"]
	require.True(t, ok, "action.yml must declare 'disabled' output")
	assert.Equal(t, "${{ steps.run.outputs.disabled }}", out.Value)
	assert.NotEmpty(t, out.Description)
}

// TestReconcileStatusStep_DisabledAgent_ExitsEarlyWithoutCallingReconcileStatus
// verifies that a disabled agent skips reconcile-status entirely.
func TestReconcileStatusStep_DisabledAgent_ExitsEarlyWithoutCallingReconcileStatus(t *testing.T) {
	step := extractActionStep(t, "Finalize orphaned status comment")

	// Set up a fake fullsend executable that fails loudly if called.
	binDir := t.TempDir()
	fullsendStub := filepath.Join(binDir, "fullsend")
	require.NoError(t, os.WriteFile(fullsendStub, []byte("#!/bin/sh\necho 'ERROR: fullsend must not be invoked for disabled agent' >&2\nexit 1\n"), 0o755))

	cmd := exec.Command("bash", "-c", step.Run)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"PATH=" + binDir + ":" + os.Getenv("PATH"),
		"WAS_DISABLED=true",
		"AGENT=code",
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	require.NoError(t, err, "script stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), "Fullsend reported that 'code' is disabled in config, skipping reconcile-status")
}

// TestReconcileStatusStep_WasSkipped_InvokesReconcileStatus verifies that a
// dynamic skip forwards --was-skipped to reconcile-status.
func TestReconcileStatusStep_WasSkipped_InvokesReconcileStatus(t *testing.T) {
	step := extractActionStep(t, "Finalize orphaned status comment")

	// Set up a stub fullsend executable that records its arguments.
	binDir := t.TempDir()
	logFile := filepath.Join(binDir, "reconcile.log")
	fullsendStub := filepath.Join(binDir, "fullsend")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" > %q\nexit 0\n", logFile)
	require.NoError(t, os.WriteFile(fullsendStub, []byte(script), 0o755))

	cmd := exec.Command("bash", "-c", step.Run)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{
		"PATH=" + binDir + ":" + os.Getenv("PATH"),
		"WAS_DISABLED=false",
		"WAS_SKIPPED=true",
		"AGENT=code",
		"STATUS_REPO=org/repo",
		"STATUS_NUMBER=42",
		"RUN_ID=run-123",
		"RUN_URL=https://ci/run/123",
		"MINT_URL=https://mint.example.com",
		"JOB_STATUS=success",
		"HARNESS_ROLE=",
		"PR_HEAD_SHA_INPUT=deadbeef0123456789",
		"FULLSEND_DIR=" + t.TempDir(),
		"GITHUB_WORKSPACE=" + t.TempDir(),
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	require.NoError(t, err, "script stderr: %s", stderr.String())

	recorded, err := os.ReadFile(logFile)
	require.NoError(t, err)
	recArgs := string(recorded)
	assert.Contains(t, recArgs, "reconcile-status")
	assert.Contains(t, recArgs, "--repo org/repo")
	assert.Contains(t, recArgs, "--number 42")
	assert.Contains(t, recArgs, "--run-id run-123")
	assert.Contains(t, recArgs, "--was-skipped")
	assert.Contains(t, recArgs, "--job-status success")
}

// TestReconcileStatusStep_GatedOnDisabledOutput verifies that action.yml
// gates the reconcile-status step with steps.run.outputs.disabled != 'true'.
func TestReconcileStatusStep_GatedOnDisabledOutput(t *testing.T) {
	step := extractActionStep(t, "Finalize orphaned status comment")
	assert.Contains(t, step.If, "steps.run.outputs.disabled != 'true'")
}
