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

// runRouteStep executes a route-job step script under bash with env, in a
// directory holding files (paths relative to the repo root).
func runRouteStep(t *testing.T, stepName string, env, files map[string]string) routeStepRun {
	t.Helper()
	script := extractReusableDispatchStepScript(t, "route", stepName)
	dir := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	outPath := filepath.Join(t.TempDir(), "output")
	summaryPath := filepath.Join(t.TempDir(), "summary")
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

func requireBash4(t *testing.T) {
	t.Helper()
	// "Determine stage" uses the bash 4 case terminator ;;& (GitHub runners
	// ship bash 5; stock macOS bash is 3.2).
	out, err := exec.Command("bash", "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	require.NoError(t, err)
	if major, err := strconv.Atoi(strings.TrimSpace(string(out))); err != nil || major < 4 {
		t.Skip("Determine stage needs bash 4 or later")
	}
}

func requireYQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq not found in PATH")
	}
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
	celRoutePRComment = map[string]string{
		"EVENT_NAME": "issue_comment", "EVENT_ACTION": "created",
		"COMMENT_BODY": "looks good", "ISSUE_IS_PR": "true",
	}
	celRouteIssueComment = map[string]string{
		"EVENT_NAME": "issue_comment", "EVENT_ACTION": "created",
		"COMMENT_BODY": "looks good", "ISSUE_IS_PR": "false",
	}
)

// finalizeEnv is the "Finalize stage" env for a bash stage and the CEL
// route result for agent. configOutcome "" means the cel-* steps did not run.
func finalizeEnv(bashStage, configOutcome, routeOutcome, decision string) map[string]string {
	return map[string]string{
		"BASH_STAGE":          bashStage,
		"BASH_TRIGGER_SOURCE": "",
		"ROUTE_AGENT":         "review",
		"CEL_OPT_IN_OUTCOME":  "",
		"CEL_CONFIG_OUTCOME":  configOutcome,
		"CEL_ROUTE_OUTCOME":   routeOutcome,
		"CEL_ROUTE_DECISION":  decision,
		"CEL_ROUTE_REASON":    "test reason",
		"CEL_ROUTE_HARNESS":   "agents@abc",
	}
}

// optInEnv is the opt-in step env for an event and bash stage.
func optInEnv(eventName, issueIsPR, bashStage string) map[string]string {
	return map[string]string{"EVENT_NAME": eventName, "ISSUE_IS_PR": issueIsPR, "BASH_STAGE": bashStage}
}

// TestRouteJob_NoReviewEntryIsByteIdentical runs the real bash "Determine
// stage" step, the opt-in step and "Finalize stage" for a repository without
// a review agent entry: the stage and trigger_source outputs must equal the
// bash step's, and the CEL steps must print nothing and write no summary.
func TestRouteJob_NoReviewEntryIsByteIdentical(t *testing.T) {
	requireBash4(t)
	requireYQ(t)
	configs := map[string]string{
		"no config":            "",
		"no agents":            "version: \"1\"\n",
		"other agents only":    "agents:\n  - name: triage\n    model: opus\n",
		"review settings only": "agents:\n  - name: review\n    model: sonnet\n",
	}
	for name, event := range map[string]map[string]string{
		"review label":  celRouteReviewLabel,
		"other label":   celRouteOtherLabel,
		"closed":        celRouteClosed,
		"PR comment":    celRoutePRComment,
		"issue comment": celRouteIssueComment,
	} {
		for cfgName, cfg := range configs {
			t.Run(name+"/"+cfgName, func(t *testing.T) {
				files := map[string]string{}
				if cfg != "" {
					files[".fullsend/config.yaml"] = cfg
				}
				bash := runRouteStep(t, "Determine stage", event, files)

				isPR := event["ISSUE_IS_PR"]
				if isPR == "" {
					isPR = "false"
				}
				optIn := runRouteStep(t, "Detect CEL route opt-in", optInEnv(event["EVENT_NAME"], isPR, bash.outputs["stage"]), files)
				assert.Equal(t, "false", optIn.outputs["opt_in"])
				assert.Empty(t, optIn.stdout)

				env := finalizeEnv(bash.outputs["stage"], "", "", "")
				env["BASH_TRIGGER_SOURCE"] = bash.outputs["trigger_source"]
				got := runRouteStep(t, "Finalize stage", env, nil)
				assert.Equal(t, bash.outputs["stage"], got.outputs["stage"])
				assert.Equal(t, bash.outputs["trigger_source"], got.outputs["trigger_source"])
				assert.Empty(t, got.stdout)
				assert.Empty(t, got.summary)
			})
		}
	}
}

func TestCELRouteOptIn(t *testing.T) {
	requireYQ(t)
	review := func(cfg string) map[string]string { return map[string]string{".fullsend/config.yaml": cfg} }
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"named entry with a trigger", review("agents:\n  - name: Review\n    trigger: \"true\"\n"), "true"},
		{"unquoted boolean trigger", review("agents:\n  - name: review\n    trigger: false\n"), "true"},
		{"unnamed source with a trigger", review("agents:\n  - source: https://example.com/harness/Review.yml#sha256=ab\n    trigger: x\n"), "true"},
		{"source with several # strips the last fragment", review("agents:\n  - source: a#b/review.yaml#sha256=ab\n    trigger: x\n"), "true"},
		{"empty name falls back to the source", review("agents:\n  - name: \"\"\n    source: harness/review.yaml\n    trigger: x\n"), "true"},
		{"entry in config.base.yaml", map[string]string{".fullsend/config.base.yaml": "agents:\n  - name: review\n    trigger: \"true\"\n"}, "true"},
		{"unreadable config lets the CLI decide", review("agents: [unterminated\n"), "true"},
		{"a source without a trigger does not opt in", review("agents:\n  - name: review\n    source: harness/r.yaml\n"), "false"},
		{"string shorthand does not opt in", review("agents:\n  - harness/review.yaml\n"), "false"},
		{"review settings without a trigger", review("agents:\n  - name: review\n    model: sonnet\n"), "false"},
		{"null trigger", review("agents:\n  - name: review\n    trigger: null\n"), "false"},
		{"another agent named like review", review("agents:\n  - source: harness/review.v2.yaml\n    trigger: x\n"), "false"},
		{"a name wins over the source file name", review("agents:\n  - name: lint\n    source: harness/review.yaml\n    trigger: x\n"), "false"},
		{"other agents", review("agents:\n  - name: triage\n    trigger: \"true\"\n"), "false"},
		{"no config", nil, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRouteStep(t, "Detect CEL route opt-in", optInEnv("pull_request_target", "false", ""), tc.files)
			assert.Equal(t, "review", got.outputs["agent"])
			assert.Equal(t, tc.want, got.outputs["opt_in"])
		})
	}
}

func TestCELRouteOptIn_EventGate(t *testing.T) {
	requireYQ(t)
	files := map[string]string{".fullsend/config.yaml": "agents:\n  - name: review\n    trigger: \"true\"\n"}
	cases := []struct {
		name, event, isPR, stage, wantAgent string
	}{
		{"PR event with no bash stage", "pull_request_target", "false", "", "review"},
		{"PR event routed to review", "pull_request_target", "false", "review", "review"},
		{"PR comment", "issue_comment", "true", "", "review"},
		{"issue comment", "issue_comment", "false", "", ""},
		{"PR event routed to another stage", "pull_request_target", "false", "retro", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRouteStep(t, "Detect CEL route opt-in", optInEnv(tc.event, tc.isPR, tc.stage), files)
			assert.Equal(t, tc.wantAgent, got.outputs["agent"])
			assert.Equal(t, map[bool]string{true: "true", false: "false"}[tc.wantAgent != ""], got.outputs["opt_in"])
		})
	}
}

func TestFinalizeStage_KeepsTriggerSource(t *testing.T) {
	env := finalizeEnv("fix", "", "", "")
	env["BASH_TRIGGER_SOURCE"] = "alice"
	got := runRouteStep(t, "Finalize stage", env, nil)
	assert.Equal(t, "fix", got.outputs["stage"])
	assert.Equal(t, "alice", got.outputs["trigger_source"])
}

func TestFinalizeStage_CELRoute(t *testing.T) {
	withOptIn := func(outcome string) map[string]string {
		env := finalizeEnv("review", "", "", "")
		env["CEL_OPT_IN_OUTCOME"] = outcome
		return env
	}
	badAgent := finalizeEnv("", "success", "success", "run")
	badAgent["ROUTE_AGENT"] = "review; rm -rf /"
	cases := []struct {
		name        string
		env         map[string]string
		wantStage   string
		wantLog     string
		wantSummary string
		wantNoLog   string
	}{
		{
			name: "run sets the agent's stage where bash chose nothing",
			env:  finalizeEnv("", "success", "success", "run"), wantStage: "review", wantSummary: "review_route=cel",
			wantLog: "::notice::CEL route (review): bash=none cel=run",
		},
		{
			name: "skip clears the agent's bash stage",
			env:  finalizeEnv("review", "success", "success", "skip"), wantStage: "", wantSummary: "review_route=cel",
		},
		{
			name: "skip with no bash stage keeps none",
			env:  finalizeEnv("", "success", "success", "skip"), wantStage: "",
		},
		{
			name: "bash decision keeps the bash stage",
			env:  finalizeEnv("review", "success", "success", "bash"), wantStage: "review", wantSummary: "review_route=bash",
		},
		{
			name: "CEL steps not run keeps another bash stage silently",
			env:  finalizeEnv("retro", "", "", ""), wantStage: "retro", wantNoLog: "::",
		},
		{
			name: "CEL steps skipped keeps the bash stage silently",
			env:  finalizeEnv("review", "skipped", "skipped", ""), wantStage: "review", wantNoLog: "::",
		},
		{
			name: "a failed evaluation keeps the bash stage and reports an error",
			env:  finalizeEnv("review", "success", "failure", ""), wantStage: "review", wantLog: "::error::CEL route failed",
		},
		{
			name: "a failed config checkout keeps the bash stage and reports an error",
			env:  finalizeEnv("review", "failure", "", ""), wantStage: "review", wantLog: "::error::CEL route failed",
		},
		{
			name: "a failed opt-in check keeps the bash stage and reports an error",
			env:  withOptIn("failure"), wantStage: "review", wantLog: "::error::CEL route opt-in check failed",
		},
		{
			name: "an unexpected agent name keeps the bash stage",
			env:  badAgent, wantStage: "", wantLog: "::error::CEL route failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runRouteStep(t, "Finalize stage", tc.env, nil)
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

// TestCELRouteStepsGatedAndIsolated checks the route job wiring: the bash
// step runs first under id bash-route; the opt-in step runs only for PR and
// comment events; every other CEL step continues on error behind it; and
// "Finalize stage" (id route) always runs, last before validation.
func TestCELRouteStepsGatedAndIsolated(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "FULLSEND_CEL_ROUTING_REVIEW", "routing is opted into by trigger, not a variable")
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
		if step.ID == "route" {
			assert.Empty(t, step.If, "Finalize stage must always run")
			assert.False(t, step.ContinueOnError, "Finalize stage writes the stage outputs; a failure must fail the job")
		}
		if !strings.Contains(step.Name, "CEL route") {
			continue
		}
		seen++
		assert.True(t, step.ContinueOnError, "step %q must continue on error", step.Name)
		switch step.ID {
		case "cel-opt-in":
			assert.Equal(t, "${{ github.event_name == 'pull_request_target' || github.event_name == 'issue_comment' }}", step.If)
		case "cel-config":
			assert.Equal(t, "steps.cel-opt-in.outputs.opt_in == 'true'", step.If)
		default:
			assert.True(t, strings.HasPrefix(step.If, "steps.cel-config.outcome == 'success'"),
				"step %q must be gated on the cel-config step, got if: %q", step.Name, step.If)
		}
	}
	assert.GreaterOrEqual(t, seen, 4, "expected the CEL route steps in the route job")
	assert.Less(t, order["bash-route"], order["cel-opt-in"], "bash routing runs before the CEL steps")
	assert.Less(t, order["cel-opt-in"], order["cel-config"])
	assert.Less(t, order["cel-route"], order["route"], "Finalize stage runs after the CEL evaluation")
	assert.Less(t, order["route"], order["validate"], "Finalize stage runs before the stage is validated")
}

// TestEvaluateCELReviewRouteStep runs the "Evaluate CEL route" script
// against a stub fullsend binary and checks the step outputs it writes.
func TestEvaluateCELRouteStep(t *testing.T) {
	script := extractReusableDispatchStepScript(t, "route", "Evaluate CEL route")
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
				"ROUTE_AGENT=review",
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

// TestValidateAgentsConfigStep_AcceptsOverrideOnlySettings runs the route
// job's yq "Validate agents config" step: an entry without a source is valid
// when it sets any setting config.AgentEntry.HasSettings counts, including
// trigger (the review routing opt-in) and subagents.
func TestValidateAgentsConfigStep_AcceptsOverrideOnlySettings(t *testing.T) {
	requireYQ(t)
	script := extractReusableDispatchStepScript(t, "route", "Validate agents config")
	cases := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"trigger only", "agents:\n  - name: review\n    trigger: >-\n      event.transition.kind == \"comment_added\"\n", false},
		{"subagents only", "agents:\n  - name: review\n    subagents:\n      default: sonnet\n", false},
		{"model only", "agents:\n  - name: triage\n    model: opus\n", false},
		{"name only", "agents:\n  - name: review\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, ".fullsend"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".fullsend", "config.yaml"), []byte(tc.config), 0o644))
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			out, err := cmd.CombinedOutput()
			if tc.wantErr {
				require.Error(t, err, "%s", out)
				assert.Contains(t, string(out), "without a source field")
				return
			}
			require.NoError(t, err, "%s", out)
		})
	}
}
