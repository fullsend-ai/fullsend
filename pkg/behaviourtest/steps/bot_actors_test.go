package steps

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeBotActorCI serves one workflow run named after the pushed workflow.
type fakeBotActorCI struct {
	ci.Driver

	scm *fakeMintProbeSCM
	run *forge.WorkflowRun
}

func (f *fakeBotActorCI) ListRecentRuns(context.Context, string, string, int) ([]forge.WorkflowRun, error) {
	if f.run == nil {
		return nil, nil
	}
	run := *f.run
	for key := range f.scm.branchFiles {
		if strings.Contains(key, ".github/workflows/bot-actor-") {
			run.Name = strings.TrimSuffix(filepath.Base(key), ".yml")
		}
	}
	return []forge.WorkflowRun{run}, nil
}

// botActorSCM adds the issue calls the e2e bot makes to the branch fake.
type botActorSCM struct {
	*fakeMintProbeSCM

	comments []string
	labels   []string
}

func (f *botActorSCM) AddComment(_ context.Context, _, _ string, _ int, body string) (*forge.IssueComment, error) {
	f.comments = append(f.comments, body)
	return &forge.IssueComment{Body: body}, nil
}

func (f *botActorSCM) AddIssueLabels(_ context.Context, _, _ string, _ int, labels ...string) error {
	f.labels = append(f.labels, labels...)
	return nil
}

func botActorTestWorld(run *forge.WorkflowRun) (*world.World, *botActorSCM) {
	scmDriver := &botActorSCM{fakeMintProbeSCM: newFakeMintProbeSCM()}
	w := mintProbeTestWorld(scmDriver, &fakeBotActorCI{scm: scmDriver.fakeMintProbeSCM, run: run})
	w.IssueNumber = 9
	return w, scmDriver
}

func validBotActorSpec() botActorWorkflowSpec {
	return botActorWorkflowSpec{
		Name:     "bot-actor-0a1b2c3d",
		Branch:   "behaviour/bot-actor-0a1b2c3d",
		Role:     "review",
		Repo:     "halfsend-03/test-repo-07",
		RepoName: "test-repo-07",
		Issue:    9,
		Action:   botActionComment,
		Value:    "/fs-do",
	}
}

func TestRenderBotActorWorkflow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		action, value, apiPath string
	}{
		{botActionComment, "/fs-do", "comments"},
		{botActionLabel, "do-it", "labels"},
	} {
		spec := validBotActorSpec()
		spec.Action, spec.Value = tc.action, tc.value
		rendered, err := renderBotActorWorkflow(spec)
		require.NoError(t, err, tc.action)

		var doc struct {
			Name string `yaml:"name"`
			On   struct {
				Push struct {
					Branches []string `yaml:"branches"`
				} `yaml:"push"`
			} `yaml:"on"`
			Jobs map[string]struct {
				Permissions map[string]string `yaml:"permissions"`
				Steps       []struct {
					Env map[string]string `yaml:"env"`
					Run string            `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(rendered, &doc), tc.action)
		assert.Equal(t, spec.Name, doc.Name)
		assert.Equal(t, []string{spec.Branch}, doc.On.Push.Branches)
		job := doc.Jobs["act"]
		assert.Equal(t, map[string]string{"contents": "read", "id-token": "write"}, job.Permissions)
		require.Len(t, job.Steps, 1)
		env := job.Steps[0].Env
		assert.Equal(t, "review", env["ROLE"])
		assert.Equal(t, "halfsend-03/test-repo-07", env["REPO"])
		assert.Equal(t, "test-repo-07", env["REPO_NAME"])
		assert.Equal(t, "9", env["ISSUE"])
		assert.Equal(t, tc.action, env["ACTION"])
		assert.Equal(t, tc.value, env["VALUE"])
		assert.Equal(t, "${{ vars.FULLSEND_MINT_URL }}", env["MINT_URL"])
		assert.Contains(t, job.Steps[0].Run, `${MINT_URL%/}/v1/token`)
		assert.Contains(t, job.Steps[0].Run, "http_code}", "the revoke command keeps its curl format")
		assert.Contains(t, job.Steps[0].Run, tc.apiPath)
	}
}

func TestRenderBotActorWorkflow_ScriptSyntax(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	rendered, err := renderBotActorWorkflow(validBotActorSpec())
	require.NoError(t, err)
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(rendered, &doc))
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(doc.Jobs["act"].Steps[0].Run)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestRenderBotActorWorkflow_RejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*botActorWorkflowSpec){
		"role":           func(s *botActorWorkflowSpec) { s.Role = "review'; id" },
		"action":         func(s *botActorWorkflowSpec) { s.Action = "delete" },
		"issue":          func(s *botActorWorkflowSpec) { s.Issue = 0 },
		"quote in value": func(s *botActorWorkflowSpec) { s.Value = "x'y" },
		"expression":     func(s *botActorWorkflowSpec) { s.Value = "${{ secrets.X }}" },
		"newline":        func(s *botActorWorkflowSpec) { s.Value = "a\nb" },
		"empty value":    func(s *botActorWorkflowSpec) { s.Value = "" },
		"name":           func(s *botActorWorkflowSpec) { s.Name = "a b" },
		"repo name":      func(s *botActorWorkflowSpec) { s.RepoName = "a/b" },
		"branch":         func(s *botActorWorkflowSpec) { s.Branch = "x'y" },
		"repo mismatch":  func(s *botActorWorkflowSpec) { s.Repo = "halfsend-03/other" },
	} {
		spec := validBotActorSpec()
		mutate(&spec)
		_, err := renderBotActorWorkflow(spec)
		assert.Error(t, err, name)
	}
}

func TestWhenBotActs_E2EBot(t *testing.T) {
	t.Parallel()

	w, scmDriver := botActorTestWorld(nil)
	before := time.Now().Truncate(time.Second)
	require.NoError(t, whenBotActs(context.Background(), w, actorE2EBot, botActionComment, "/fs-do"))
	require.NoError(t, whenBotActs(context.Background(), w, actorE2EBot, botActionLabel, "do-it"))

	assert.Equal(t, []string{"/fs-do"}, scmDriver.comments)
	assert.Equal(t, []string{"do-it"}, scmDriver.labels)
	assert.Empty(t, scmDriver.branches, "the e2e bot acts directly, without a workflow")
	assert.False(t, w.ScenarioStart.Before(before))

	start := w.ScenarioStart
	require.NoError(t, whenBotActs(context.Background(), w, actorE2EBot, botActionLabel, "again"))
	assert.Equal(t, start, w.ScenarioStart, "a label keeps the start the comment set")

	assert.ErrorContains(t, e2eBotActs(context.Background(), w, "delete", "x"), "unknown bot action")
}

func TestWhenBotActs_RoleBot(t *testing.T) {
	fastMintProbePolls(t)
	w, scmDriver := botActorTestWorld(&forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "success"})

	require.NoError(t, whenBotActs(context.Background(), w, actorCoderBot, botActionComment, "/fs-do"))

	require.Len(t, scmDriver.branches, 1)
	assert.Equal(t, scmDriver.branches, w.CreatedBranches, "the workflow branch is cleaned up with the other branches")
	assert.False(t, w.ScenarioStart.IsZero())
	assert.Empty(t, scmDriver.comments, "a role bot comments from its workflow, not through the suite's driver")
	var workflow string
	for key, content := range scmDriver.branchFiles {
		if strings.Contains(key, ".github/workflows/bot-actor-") {
			workflow = string(content)
		}
	}
	require.NotEmpty(t, workflow)
	assert.Contains(t, workflow, "ROLE: 'coder'")
	assert.Contains(t, workflow, "VALUE: '/fs-do'")
}

func TestWhenBotActs_Failures(t *testing.T) {
	fastMintProbePolls(t)

	t.Run("no issue", func(t *testing.T) {
		w, _ := botActorTestWorld(nil)
		w.IssueNumber = 0
		assert.ErrorContains(t, whenBotActs(context.Background(), w, actorE2EBot, botActionComment, "/fs-do"), "no issue created")
	})
	t.Run("unknown bot", func(t *testing.T) {
		w, _ := botActorTestWorld(nil)
		assert.ErrorContains(t, whenBotActs(context.Background(), w, "retro bot", botActionComment, "/fs-do"), "unknown bot actor")
	})
	t.Run("run never starts", func(t *testing.T) {
		w, _ := botActorTestWorld(nil)
		assert.ErrorContains(t, whenBotActs(context.Background(), w, actorReviewBot, botActionLabel, "do-it"), "workflow failure")
	})
	t.Run("run fails", func(t *testing.T) {
		w, _ := botActorTestWorld(&forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "failure"})
		err := whenBotActs(context.Background(), w, actorReviewBot, botActionLabel, "do-it")
		assert.ErrorContains(t, err, "workflow failure")
		assert.ErrorContains(t, err, `"failure"`)
	})
	t.Run("unsafe value", func(t *testing.T) {
		w, scmDriver := botActorTestWorld(&forge.WorkflowRun{ID: 7, Status: "completed", Conclusion: "success"})
		assert.ErrorContains(t, whenBotActs(context.Background(), w, actorReviewBot, botActionComment, "it's"), "invalid comment value")
		assert.Empty(t, scmDriver.branches, "nothing is pushed for an unsafe value")
	})
	t.Run("CI driver cannot list runs", func(t *testing.T) {
		scmDriver := &botActorSCM{fakeMintProbeSCM: newFakeMintProbeSCM()}
		w := mintProbeTestWorld(scmDriver, &fakeBranchCI{})
		w.IssueNumber = 9
		assert.ErrorContains(t, whenBotActs(context.Background(), w, actorReviewBot, botActionLabel, "do-it"), "ci.RunLister")
	})
	t.Run("no repository", func(t *testing.T) {
		w, _ := botActorTestWorld(nil)
		w.RepoOwner, w.RepoName = "", ""
		assert.ErrorContains(t, whenBotActs(context.Background(), w, actorReviewBot, botActionLabel, "do-it"), "no repo configured")
	})
}

// TestBotActorAuthorizationFeature_RowsMatchSteps expands every Examples
// row of the bot actor feature and checks that each of its steps matches a
// step pattern the suite registers, so a typo in the table fails here
// rather than after a lease.
func TestBotActorAuthorizationFeature_RowsMatchSteps(t *testing.T) {
	t.Parallel()

	f, err := os.Open(filepath.Join("..", "..", "..", "e2e", "behaviour", "features", "platform", "bot-actor-authorization.feature"))
	require.NoError(t, err)
	defer f.Close()

	var steps, header []string
	var rows [][]string
	inOutline := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Scenario Outline:"):
			inOutline = true
		case inOutline && (strings.HasPrefix(line, "Given ") || strings.HasPrefix(line, "When ") ||
			strings.HasPrefix(line, "Then ") || strings.HasPrefix(line, "And ")):
			steps = append(steps, line[strings.Index(line, " ")+1:])
		case inOutline && strings.HasPrefix(line, "|"):
			var cells []string
			for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
				cells = append(cells, strings.TrimSpace(c))
			}
			if header == nil {
				header = cells
			} else {
				rows = append(rows, cells)
			}
		}
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, []string{"actor", "needs-review-bot", "needs-coder-bot", "needs-triage-bot", "on-label"}, header)
	require.NotEmpty(t, rows)

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^the (` + actorAlternation + `) posts "([^"]+)" on the issue$`),
		regexp.MustCompile(`^the (` + actorAlternation + `) labels the issue "([^"]+)"$`),
		regexp.MustCompile(`^the (` + botActorAlternation + `) posts "([^"]+)" on the issue$`),
		regexp.MustCompile(`^the (` + botActorAlternation + `) labels the issue "([^"]+)"$`),
		regexp.MustCompile(`^an issue$`),
		regexp.MustCompile(`^the label "([^"]+)" exists$`),
		regexp.MustCompile(`^the harness "([^"]+)" workflow completes successfully$`),
		regexp.MustCompile(`^the harness "([^"]+)" was dispatched exactly (\d+) times?$`),
	}
	actors := map[string]bool{}
	for _, row := range rows {
		require.Len(t, row, len(header))
		values := map[string]string{}
		for i, h := range header {
			values[h] = row[i]
		}
		actors[values["actor"]] = true
		for _, step := range steps {
			expanded := step
			for h, v := range values {
				expanded = strings.ReplaceAll(expanded, "<"+h+">", v)
			}
			assert.NotContains(t, expanded, "<", "unexpanded placeholder in %q", expanded)
			matched := false
			for _, p := range patterns {
				matched = matched || p.MatchString(expanded)
			}
			assert.True(t, matched, "no step matches %q", expanded)
		}
	}
	for actor := range botActorRoles {
		assert.True(t, actors[actor], "no row for %s", actor)
	}
	assert.True(t, actors[actorE2EBot], "no row for the unregistered e2e bot")
}
