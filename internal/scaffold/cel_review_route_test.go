package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type routeStepRun struct {
	outputs map[string]string // GITHUB_OUTPUT key=value lines
	stdout  string
	summary string // GITHUB_STEP_SUMMARY contents
}

// runRouteStep executes a route-job step script under bash with env.
func runRouteStep(t *testing.T, stepName string, env map[string]string) routeStepRun {
	t.Helper()
	script := extractReusableDispatchStepScript(t, "route", stepName)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "output")
	summaryPath := filepath.Join(dir, "summary")
	require.NoError(t, os.WriteFile(outPath, nil, 0o644))
	require.NoError(t, os.WriteFile(summaryPath, nil, 0o644))

	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GITHUB_OUTPUT=" + outPath,
		"GITHUB_STEP_SUMMARY=" + summaryPath,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s failed:\n%s", stepName, out)

	output, err := os.ReadFile(outPath)
	require.NoError(t, err)
	summary, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	outputs := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			outputs[k] = v
		}
	}
	return routeStepRun{outputs: outputs, stdout: string(out), summary: string(summary)}
}

// Events the bash router decides without calling the GitHub API.
var (
	celRouteReviewLabel = map[string]string{
		"EVENT_NAME": "pull_request_target", "EVENT_ACTION": "labeled",
		"TRIGGERING_LABEL": "ready-for-review", "PR_USER_LOGIN": "someone",
	}
	celRouteOtherLabel = map[string]string{
		"EVENT_NAME": "pull_request_target", "EVENT_ACTION": "labeled",
		"TRIGGERING_LABEL": "documentation", "PR_USER_LOGIN": "someone",
	}
	celRouteClosed = map[string]string{
		"EVENT_NAME": "pull_request_target", "EVENT_ACTION": "closed",
		"PR_USER_LOGIN": "someone",
	}
)

// finalizeEnv is the "Finalize stage" env for a bash stage and CEL result.
// configOutcome "" means the cel-* steps did not run.
func finalizeEnv(bashStage, mode, configOutcome, reviewOutcome, decision string) map[string]string {
	return map[string]string{
		"BASH_STAGE":          bashStage,
		"BASH_TRIGGER_SOURCE": "",
		"EVENT_NAME":          "pull_request_target",
		"ISSUE_IS_PR":         "false",
		"CEL_REVIEW_MODE":     mode,
		"CEL_CONFIG_OUTCOME":  configOutcome,
		"CEL_REVIEW_OUTCOME":  reviewOutcome,
		"CEL_REVIEW_DECISION": decision,
		"CEL_REVIEW_REASON":   "test reason",
		"CEL_REVIEW_HARNESS":  "agents@abc",
	}
}

// TestFinalizeStage_OffPassesBashStageThrough runs the real bash "Determine
// stage" step, then "Finalize stage" with the variable off, unset or OFF and
// arbitrary CEL step results: the stage and trigger_source outputs must equal
// the bash step's, with no log lines or step summary.
func TestFinalizeStage_OffPassesBashStageThrough(t *testing.T) {
	// "Determine stage" uses the bash 4 case terminator ;;& (GitHub runners
	// ship bash 5; stock macOS bash is 3.2).
	out, err := exec.Command("bash", "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	require.NoError(t, err)
	if major, err := strconv.Atoi(strings.TrimSpace(string(out))); err != nil || major < 4 {
		t.Skip("Determine stage needs bash 4 or later")
	}
	for name, event := range map[string]map[string]string{
		"review label": celRouteReviewLabel,
		"other label":  celRouteOtherLabel,
		"closed":       celRouteClosed,
	} {
		t.Run(name, func(t *testing.T) {
			bash := runRouteStep(t, "Determine stage", event)
			for _, mode := range []string{"", "off", "OFF"} {
				for _, decision := range []string{"run", "skip", "bash"} {
					env := finalizeEnv(bash.outputs["stage"], mode, "success", "success", decision)
					env["BASH_TRIGGER_SOURCE"] = bash.outputs["trigger_source"]
					got := runRouteStep(t, "Finalize stage", env)
					assert.Equal(t, bash.outputs["stage"], got.outputs["stage"], "mode=%q decision=%s", mode, decision)
					assert.Equal(t, bash.outputs["trigger_source"], got.outputs["trigger_source"], "mode=%q", mode)
					assert.Empty(t, got.stdout, "mode=%q decision=%s", mode, decision)
					assert.Empty(t, got.summary, "mode=%q decision=%s", mode, decision)
				}
			}
		})
	}
}

func TestFinalizeStage_KeepsTriggerSource(t *testing.T) {
	env := finalizeEnv("fix", "on", "", "", "")
	env["BASH_TRIGGER_SOURCE"] = "alice"
	got := runRouteStep(t, "Finalize stage", env)
	assert.Equal(t, "fix", got.outputs["stage"])
	assert.Equal(t, "alice", got.outputs["trigger_source"])
}

func TestFinalizeStage_CELReviewRoute(t *testing.T) {
	onComment := func(isPR string) map[string]string {
		env := finalizeEnv("", "on", "success", "success", "run")
		env["EVENT_NAME"] = "issue_comment"
		env["ISSUE_IS_PR"] = isPR
		return env
	}
	cases := []struct {
		name        string
		env         map[string]string
		wantStage   string
		wantLog     string
		wantSummary string
		wantNoLog   string
	}{
		{
			name: "on: run sets review where bash chose nothing",
			env:  finalizeEnv("", "on", "success", "success", "run"), wantStage: "review", wantSummary: "review_route=cel",
		},
		{
			name: "on: mode is case-insensitive",
			env:  finalizeEnv("", "On", "success", "success", "run"), wantStage: "review", wantSummary: "review_route=cel",
		},
		{
			name: "on: skip clears a bash review",
			env:  finalizeEnv("review", "on", "success", "success", "skip"), wantStage: "", wantSummary: "review_route=cel",
		},
		{
			name: "on: bash decision keeps the bash review",
			env:  finalizeEnv("review", "on", "success", "success", "bash"), wantStage: "review", wantSummary: "review_route=bash",
		},
		{
			name: "on: bash decision keeps no stage",
			env:  finalizeEnv("", "on", "success", "success", "bash"), wantStage: "", wantSummary: "review_route=bash",
		},
		{
			name: "on: run on a PR comment sets review",
			env:  onComment("true"), wantStage: "review",
		},
		{
			name: "on: run on an issue comment is ignored",
			env:  onComment("false"), wantStage: "", wantLog: "not on a pull request", wantSummary: "review_route=bash",
		},
		{
			name: "on: CEL steps not run keeps another bash stage",
			env:  finalizeEnv("retro", "on", "", "", ""), wantStage: "retro", wantNoLog: "::",
		},
		{
			name: "on: CEL steps skipped keeps the bash stage",
			env:  finalizeEnv("review", "on", "skipped", "skipped", ""), wantStage: "review", wantNoLog: "::",
		},
		{
			name: "on: a failed evaluation keeps the bash stage and reports an error",
			env:  finalizeEnv("review", "on", "success", "failure", ""), wantStage: "review", wantLog: "::error::CEL review route failed",
		},
		{
			name: "on: a failed config checkout keeps the bash stage and reports an error",
			env:  finalizeEnv("review", "on", "failure", "", ""), wantStage: "review", wantLog: "::error::CEL review route failed",
		},
		{
			name: "shadow: mismatch is logged and the bash stage kept",
			env:  finalizeEnv("review", "shadow", "success", "success", "skip"), wantStage: "review",
			wantLog:     "::warning::CEL review route mismatch: bash=review cel=skip",
			wantSummary: "CEL review route (shadow): bash=review cel=skip",
		},
		{
			name: "shadow: agreement logs no mismatch",
			env:  finalizeEnv("review", "shadow", "success", "success", "run"), wantStage: "review",
			wantLog: "::notice::CEL review route (shadow): bash=review cel=run", wantNoLog: "mismatch",
		},
		{
			name: "shadow: a failed evaluation only warns",
			env:  finalizeEnv("", "shadow", "success", "failure", ""), wantStage: "", wantLog: "::warning::CEL review route (shadow) failed",
		},
		{
			name: "unknown mode warns and keeps bash",
			env:  finalizeEnv("", "yes", "success", "success", "run"), wantStage: "", wantLog: "is not off, shadow or on",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRouteStep(t, "Finalize stage", tc.env)
			assert.Equal(t, tc.wantStage, got.outputs["stage"])
			if tc.wantLog != "" {
				assert.Contains(t, got.stdout, tc.wantLog)
			}
			if tc.wantNoLog != "" {
				assert.NotContains(t, got.stdout, tc.wantNoLog)
			}
			if tc.wantSummary != "" {
				assert.Contains(t, got.summary, tc.wantSummary)
			}
		})
	}
}

// TestCELReviewRouteStepsGatedAndIsolated checks the route job wiring: the
// bash step runs first under id bash-route, every CEL step continues on error
// and is gated on the first one, which checks the variable, the event and the
// bash stage, and "Finalize stage" (id route) comes last before validation.
func TestCELReviewRouteStepsGatedAndIsolated(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name            string `yaml:"name"`
				ID              string `yaml:"id"`
				If              string `yaml:"if"`
				ContinueOnError bool   `yaml:"continue-on-error"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &wf))

	order := map[string]int{}
	seen := 0
	for i, step := range wf.Jobs["route"].Steps {
		if step.ID != "" {
			order[step.ID] = i
		}
		if step.Name == "Validate routed stage" {
			order["validate"] = i
		}
		if !strings.Contains(step.Name, "CEL review route") {
			continue
		}
		seen++
		assert.True(t, step.ContinueOnError, "step %q must continue on error", step.Name)
		if step.ID == "cel-config" {
			assert.Contains(t, step.If, "vars.FULLSEND_CEL_ROUTING_REVIEW == 'shadow'")
			assert.Contains(t, step.If, "vars.FULLSEND_CEL_ROUTING_REVIEW == 'on'")
			assert.Contains(t, step.If, "steps.bash-route.outputs.stage == ''")
			assert.Contains(t, step.If, "steps.bash-route.outputs.stage == 'review'")
			assert.Contains(t, step.If, "github.event_name == 'pull_request_target'")
			assert.Contains(t, step.If, "github.event_name == 'issue_comment' && github.event.issue.pull_request")
			continue
		}
		assert.True(t, strings.HasPrefix(step.If, "steps.cel-config.outcome == 'success'"),
			"step %q must be gated on the cel-config step, got if: %q", step.Name, step.If)
	}
	assert.GreaterOrEqual(t, seen, 3, "expected the CEL review route steps in the route job")
	for _, step := range wf.Jobs["route"].Steps {
		if step.ID == "route" {
			assert.Empty(t, step.If, "Finalize stage must always run")
			assert.False(t, step.ContinueOnError, "Finalize stage writes the stage outputs; a failure must fail the job")
		}
	}
	assert.Less(t, order["bash-route"], order["cel-config"], "bash routing runs before the CEL steps")
	assert.Less(t, order["cel-review"], order["route"], "Finalize stage runs after the CEL evaluation")
	assert.Less(t, order["route"], order["validate"], "Finalize stage runs before the stage is validated")
}

// TestEvaluateCELReviewRouteStep runs the "Evaluate CEL review route" script
// against a stub fullsend binary and checks the step outputs it writes.
func TestEvaluateCELReviewRouteStep(t *testing.T) {
	script := extractReusableDispatchStepScript(t, "route", "Evaluate CEL review route")
	cases := []struct {
		name       string
		cliOutput  string
		cliExit    int
		wantErr    bool
		wantOutput string
	}{
		{
			name:       "decision is written to the step outputs",
			cliOutput:  `{"agent":"review","decision":"run","reason":"trigger matched","harness":"agents@abc"}`,
			wantOutput: "decision=run\nreason=trigger matched\nharness=agents@abc\n",
		},
		{
			name:       "missing harness is written empty",
			cliOutput:  `{"agent":"review","decision":"bash","reason":"resolved harness has no trigger"}`,
			wantOutput: "decision=bash\nreason=resolved harness has no trigger\nharness=\n",
		},
		{name: "unknown decision fails the step", cliOutput: `{"decision":"maybe"}`, wantErr: true},
		{name: "CLI failure fails the step", cliExit: 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			require.NoError(t, os.MkdirAll(bin, 0o755))
			stub := "#!/bin/sh\nprintf '%s\\n' '" + tc.cliOutput + "'\nexit " + map[bool]string{true: "1", false: "0"}[tc.cliExit != 0] + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(stub), 0o755))
			outPath := filepath.Join(dir, "output")
			require.NoError(t, os.WriteFile(outPath, nil, 0o644))

			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = []string{
				"PATH=" + bin + ":" + os.Getenv("PATH"),
				"GITHUB_OUTPUT=" + outPath,
				"EVENT_ACTION=opened",
			}
			out, err := cmd.CombinedOutput()
			if tc.wantErr {
				require.Error(t, err, "step should fail:\n%s", out)
				return
			}
			require.NoError(t, err, "step failed:\n%s", out)
			got, err := os.ReadFile(outPath)
			require.NoError(t, err)
			assert.Equal(t, tc.wantOutput, string(got))
		})
	}
}
