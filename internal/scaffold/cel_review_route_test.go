package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type routeStepRun struct {
	output  string // GITHUB_OUTPUT contents
	stdout  string
	summary string // GITHUB_STEP_SUMMARY contents
}

// runDetermineStage executes the route job's "Determine stage" script under
// bash with event env that needs no GitHub API call, plus extra env.
func runDetermineStage(t *testing.T, event map[string]string, extra map[string]string) routeStepRun {
	t.Helper()
	script := extractReusableDispatchStepScript(t, "route", "Determine stage")
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
	for k, v := range event {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range extra {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "Determine stage failed:\n%s", out)

	output, err := os.ReadFile(outPath)
	require.NoError(t, err)
	summary, err := os.ReadFile(summaryPath)
	require.NoError(t, err)
	return routeStepRun{output: string(output), stdout: string(out), summary: string(summary)}
}

func stageOf(t *testing.T, r routeStepRun) string {
	t.Helper()
	for _, line := range strings.Split(r.output, "\n") {
		if v, ok := strings.CutPrefix(line, "stage="); ok {
			return v
		}
	}
	t.Fatalf("no stage= line in GITHUB_OUTPUT:\n%s", r.output)
	return ""
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

func celEnv(mode, outcome, decision string) map[string]string {
	return map[string]string{
		"CEL_REVIEW_MODE":     mode,
		"CEL_REVIEW_OUTCOME":  outcome,
		"CEL_REVIEW_DECISION": decision,
		"CEL_REVIEW_REASON":   "test reason",
		"CEL_REVIEW_HARNESS":  "agents@abc",
	}
}

// TestDetermineStage_CELReviewRouteOffIsNoOp checks that with the variable
// off or unset the step's output, log and summary are byte-for-byte the same
// as with no CEL env at all, whatever the CEL step outputs would say.
func TestDetermineStage_CELReviewRouteOffIsNoOp(t *testing.T) {
	for name, event := range map[string]map[string]string{
		"review label": celRouteReviewLabel,
		"other label":  celRouteOtherLabel,
		"closed":       celRouteClosed,
	} {
		t.Run(name, func(t *testing.T) {
			baseline := runDetermineStage(t, event, nil)
			for _, mode := range []string{"", "off"} {
				for _, decision := range []string{"run", "skip", "bash"} {
					got := runDetermineStage(t, event, celEnv(mode, "success", decision))
					assert.Equal(t, baseline, got, "mode=%q decision=%s", mode, decision)
				}
			}
		})
	}
}

func TestDetermineStage_CELReviewRoute(t *testing.T) {
	cases := []struct {
		name        string
		event       map[string]string
		cel         map[string]string
		wantStage   string
		wantLog     string
		wantSummary string
	}{
		{
			name:  "on: run sets review where bash chose nothing",
			event: celRouteOtherLabel, cel: celEnv("on", "success", "run"),
			wantStage: "review", wantSummary: "review_route=cel",
		},
		{
			name:  "on: skip clears a bash review",
			event: celRouteReviewLabel, cel: celEnv("on", "success", "skip"),
			wantStage: "", wantSummary: "review_route=cel",
		},
		{
			name:  "on: bash decision keeps the bash review",
			event: celRouteReviewLabel, cel: celEnv("on", "success", "bash"),
			wantStage: "review", wantSummary: "review_route=bash",
		},
		{
			name:  "on: bash decision keeps no stage",
			event: celRouteOtherLabel, cel: celEnv("on", "success", "bash"),
			wantStage: "", wantSummary: "review_route=bash",
		},
		{
			name:  "on: another bash stage is never replaced",
			event: celRouteClosed, cel: celEnv("on", "success", "run"),
			wantStage: "retro", wantLog: "keeping bash stage 'retro'",
		},
		{
			name:  "on: a failed CEL step keeps the bash stage and reports an error",
			event: celRouteReviewLabel, cel: celEnv("on", "failure", ""),
			wantStage: "review", wantLog: "::error::CEL review route failed",
		},
		{
			name:  "on: a skipped CEL step keeps the bash stage",
			event: celRouteReviewLabel, cel: celEnv("on", "skipped", ""),
			wantStage: "review",
		},
		{
			name:  "shadow: mismatch is logged and the bash stage kept",
			event: celRouteReviewLabel, cel: celEnv("shadow", "success", "skip"),
			wantStage: "review", wantLog: "::warning::CEL review route mismatch: bash=review cel=skip",
			wantSummary: "CEL review route (shadow): bash=review cel=skip",
		},
		{
			name:  "shadow: agreement logs no mismatch",
			event: celRouteReviewLabel, cel: celEnv("shadow", "success", "run"),
			wantStage: "review", wantLog: "::notice::CEL review route (shadow): bash=review cel=run",
		},
		{
			name:  "shadow: a failed CEL step only warns",
			event: celRouteOtherLabel, cel: celEnv("shadow", "failure", ""),
			wantStage: "", wantLog: "::warning::CEL review route (shadow) failed",
		},
		{
			name:  "unknown mode warns and keeps bash",
			event: celRouteOtherLabel, cel: celEnv("yes", "success", "run"),
			wantStage: "", wantLog: "is not off, shadow or on",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runDetermineStage(t, tc.event, tc.cel)
			assert.Equal(t, tc.wantStage, stageOf(t, got))
			if tc.wantLog != "" {
				assert.Contains(t, got.stdout, tc.wantLog)
			}
			if tc.wantSummary != "" {
				assert.Contains(t, got.summary, tc.wantSummary)
			}
			if strings.Contains(tc.name, "agreement") {
				assert.NotContains(t, got.stdout, "mismatch")
			}
		})
	}
}

// TestCELReviewRouteStepsGatedOnVariable checks that every CEL review route
// step in the route job is skipped unless FULLSEND_CEL_ROUTING_REVIEW is
// shadow or on: the first step carries the variable check and the rest
// depend on its outcome.
func TestCELReviewRouteStepsGatedOnVariable(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				ID   string `yaml:"id"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &wf))

	seen := 0
	for _, step := range wf.Jobs["route"].Steps {
		if !strings.Contains(step.Name, "CEL review route") {
			continue
		}
		seen++
		if step.ID == "cel-config" {
			assert.Contains(t, step.If, "vars.FULLSEND_CEL_ROUTING_REVIEW == 'shadow'")
			assert.Contains(t, step.If, "vars.FULLSEND_CEL_ROUTING_REVIEW == 'on'")
			continue
		}
		assert.True(t, strings.HasPrefix(step.If, "steps.cel-config.outcome == 'success'"),
			"step %q must be gated on the cel-config step, got if: %q", step.Name, step.If)
	}
	assert.GreaterOrEqual(t, seen, 3, "expected the CEL review route steps in the route job")
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
