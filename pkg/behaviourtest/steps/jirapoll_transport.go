package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jiradispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// This file holds the transport-bounds scenarios for Jira-sourced GitLab
// dispatch (R-005 behaviour coverage for the bounded transported event
// fields and the permanent refusal of oversized /fs-* commands). The
// scenarios run the real jirapoll.Poller against the mock Jira server with
// the production CEL trigger matching and record dispatch (the shared
// internal/jiradispatch matcher and GitLab dispatcher newCELMatcher and
// runJiraPollGitLab in internal/cli compose), and dispatch through the real
// GitLab dispatch door — poll.Poller.DispatchJiraEvent, including
// encodeJiraEventPayload's transport budget — onto a fake GitLab API that
// records the pipelines it is asked to create, mirroring the boundary the
// jira_lifecycle_test.go scenarios stop at.

// fakeGitLabAPI implements poll.GitLabClient for the transport scenarios.
// Only the dispatch door's two calls do anything: GetFileContentAtRef
// serves the scaffold's committed pipeline wrapper (so the typed-contract
// check passes exactly as against a real installed project), and
// CreatePipelineWithInputs records the agent pipeline. Everything the
// poll cycle itself would use is inert; the Jira poller drives these
// scenarios, not the GitLab event poller.
type fakeGitLabAPI struct {
	w *world.World
}

func (f *fakeGitLabAPI) ListIssuesUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]poll.Issue, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) ListMergeRequestsUpdatedSince(ctx context.Context, owner, repo string, since time.Time) ([]poll.MergeRequest, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) ListProjectEvents(ctx context.Context, owner, repo string, targetType string, after time.Time) ([]poll.ProjectEvent, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) ListIssueNotes(ctx context.Context, owner, repo string, issueIID int) ([]poll.Note, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) ListMergeRequestNotes(ctx context.Context, owner, repo string, mrIID int) ([]poll.Note, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) ListMergeRequestDiffVersions(ctx context.Context, owner, repo string, mrIID int) ([]poll.MergeRequestDiffVersion, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) ListResourceLabelEvents(ctx context.Context, owner, repo string, issueIID int) ([]poll.ResourceLabelEvent, error) {
	return nil, nil
}

func (f *fakeGitLabAPI) GetFileContentAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	if path == ".gitlab/ci/fullsend-pipeline.yml" {
		return scaffold.GitLabPerRepoFile(".gitlab/ci/fullsend-pipeline.yml")
	}
	return nil, forge.ErrNotFound
}

func (f *fakeGitLabAPI) GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error) {
	return "", forge.ErrNotFound
}

func (f *fakeGitLabAPI) CommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte, expectedSHA string) error {
	return forge.ErrNotFound
}

func (f *fakeGitLabAPI) ForceCommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	return forge.ErrNotFound
}

func (f *fakeGitLabAPI) DeleteRef(ctx context.Context, owner, repo, refPath string) error {
	return forge.ErrNotFound
}

func (f *fakeGitLabAPI) GetAuthenticatedUser(ctx context.Context) (string, error) {
	return "poller-001", nil
}

func (f *fakeGitLabAPI) GetAuthenticatedUserID(ctx context.Context) (int, error) {
	return 1, nil
}

func (f *fakeGitLabAPI) GetUserBot(ctx context.Context, userID int) (bool, error) {
	return false, nil
}

func (f *fakeGitLabAPI) CreateNoteAwardEmoji(ctx context.Context, owner, repo string, noteableType string, noteableIID, noteID int, emoji string) error {
	return forge.ErrNotFound
}

func (f *fakeGitLabAPI) GetIssue(ctx context.Context, owner, repo string, issueIID int) (*poll.Issue, error) {
	return nil, forge.ErrNotFound
}

func (f *fakeGitLabAPI) GetMergeRequest(ctx context.Context, owner, repo string, mrIID int) (*poll.MergeRequest, error) {
	return nil, forge.ErrNotFound
}

func (f *fakeGitLabAPI) GetMemberAccessLevel(ctx context.Context, owner, repo string, userID int) (int, error) {
	return 0, forge.ErrNotFound
}

func (f *fakeGitLabAPI) GetProjectPath(ctx context.Context, projectID int) (string, error) {
	return "", forge.ErrNotFound
}

func (f *fakeGitLabAPI) CreatePipelineWithInputs(ctx context.Context, owner, repo, ref string, inputs map[string]forge.PipelineInputValue) (int64, string, error) {
	stage := pipelineInputString(inputs, "stage")
	rk := pipelineInputString(inputs, "resource_key")
	f.w.JiraTransport.Pipelines = append(f.w.JiraTransport.Pipelines, world.JiraTransportPipeline{
		Stage:       stage,
		ResourceKey: rk,
	})
	return int64(len(f.w.JiraTransport.Pipelines)), fmt.Sprintf("https://gitlab.example.com/%s/%s/-/pipelines/%d", owner, repo, len(f.w.JiraTransport.Pipelines)), nil
}

// pipelineInputString reads a string-typed pipeline input the dispatch
// door delivered; an absent or non-string input reads as the empty string.
func pipelineInputString(inputs map[string]forge.PipelineInputValue, name string) string {
	b, err := json.Marshal(inputs[name])
	if err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s
}

func (f *fakeGitLabAPI) CreateChildPipelineWithInputs(ctx context.Context, owner, repo, ref, jobToken string, inputs map[string]forge.PipelineInputValue) (int64, string, error) {
	return f.CreatePipelineWithInputs(ctx, owner, repo, ref, inputs)
}

func registerJiraTransportSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a local harness "([^"]+)" with role "([^"]+)" and trigger:$`, func(ctx context.Context, name, role, trigger string) (context.Context, error) {
		return ctx, givenLocalHarness(world.FromContext(ctx), name, role, trigger)
	})
	sc.Step(`^the Jira poller dispatches to the GitLab project "([^"]+)"$`, func(ctx context.Context, project string) (context.Context, error) {
		return ctx, givenJiraGitLabTarget(world.FromContext(ctx), project)
	})
	sc.Step(`^(\d+) pad labels of (\d+) characters on Jira issue "([^"]+)"$`, func(ctx context.Context, count, width int, key string) (context.Context, error) {
		return ctx, givenPadLabels(world.FromContext(ctx), count, width, key)
	})
	sc.Step(`^an oversized "([^"]+)" command comment is added to Jira issue "([^"]+)"$`, func(ctx context.Context, command, key string) (context.Context, error) {
		return ctx, whenOversizedCommand(world.FromContext(ctx), command, key)
	})
	sc.Step(`^the Jira GitLab poller runs$`, func(ctx context.Context) (context.Context, error) {
		return ctx, whenJiraGitLabPollerRuns(ctx, world.FromContext(ctx))
	})
	sc.Step(`^an agent pipeline was created for issue "([^"]+)"$`, func(ctx context.Context, key string) (context.Context, error) {
		return ctx, thenPipelineCreated(world.FromContext(ctx), key)
	})
	sc.Step(`^no agent pipeline was created for issue "([^"]+)"$`, func(ctx context.Context, key string) (context.Context, error) {
		return ctx, thenNoPipelineCreated(world.FromContext(ctx), key)
	})
}

// givenLocalHarness writes a harness file with a CEL trigger into the
// scenario's .fullsend config and registers it as an agent, the layout
// writeLifecycleConfig in internal/cli uses for the same matcher.
func givenLocalHarness(w *world.World, name, role, trigger string) error {
	if w.JiraConfigDir == "" {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if w.JiraTransport == nil {
		w.JiraTransport = &world.JiraTransportState{}
	}
	for _, h := range w.JiraTransport.Harnesses {
		if h.Name == name {
			return fmt.Errorf("local harness %q already registered", name)
		}
	}
	w.JiraTransport.Harnesses = append(w.JiraTransport.Harnesses, world.JiraTransportHarness{
		Name:    name,
		Role:    role,
		Trigger: trigger,
	})

	fsDir := filepath.Join(w.JiraConfigDir, ".fullsend")
	harnessDir := filepath.Join(fsDir, "harness")
	if err := os.MkdirAll(harnessDir, 0o755); err != nil {
		return fmt.Errorf("create harness dir: %w", err)
	}
	agentDir := filepath.Join(fsDir, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "agent: agents/%s.md\nrole: %s\nslug: fullsend-ai-%s\n", name, role, name)
	b.WriteString("model: opus\nimage: ghcr.io/fullsend-ai/fullsend-sandbox:latest\ntrigger: |\n")
	for line := range strings.SplitSeq(strings.TrimRight(trigger, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if err := os.WriteFile(filepath.Join(harnessDir, name+".yaml"), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write harness: %w", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, name+".md"), []byte("# "+name+"\n"), 0o644); err != nil {
		return fmt.Errorf("write agent: %w", err)
	}

	// Rewrite config.yaml with every harness registered so far.
	var cfg strings.Builder
	cfg.WriteString("version: 1\nagents:\n")
	for _, h := range w.JiraTransport.Harnesses {
		fmt.Fprintf(&cfg, "  - name: %s\n    source: harness/%s.yaml\n", h.Name, h.Name)
	}
	return os.WriteFile(filepath.Join(fsDir, "config.yaml"), []byte(cfg.String()), 0o644)
}

// givenJiraGitLabTarget arms the GitLab-target poll cycle: the CEL
// matcher over the scenario's harnesses and the dispatch door onto the
// fake GitLab API.
func givenJiraGitLabTarget(w *world.World, project string) error {
	if w.JiraConfigDir == "" {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if w.JiraTransport == nil {
		w.JiraTransport = &world.JiraTransportState{}
	}
	if w.JiraTransport.Project != "" {
		return fmt.Errorf("GitLab target %q already set", w.JiraTransport.Project)
	}
	w.JiraTransport.Project = project
	return nil
}

// givenPadLabels places count labels of width characters on the issue
// without label-change events, so a later single label change carries a
// label state past the transport's 1024-byte JSON bound.
func givenPadLabels(w *world.World, count, width int, key string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if width < 1 {
		return fmt.Errorf("pad label width must be positive, got %d", width)
	}
	labels := make([]string, 0, count)
	for i := 0; i < count; i++ {
		labels = append(labels, fmt.Sprintf("%0*d", width, i))
	}
	w.JiraMockState.SetIssueLabels(key, labels)
	return nil
}

// whenOversizedCommand adds a one-line /fs-* command comment whose
// escape-heavy instruction cannot fit the dispatch transport budget: the
// adapter truncates comment text to 4096 runes, so the body and the
// instruction each carry ~4k of '<' (which JSON escapes sixfold), a
// payload far past the nine-chunk budget a command payload is never
// shortened to fit.
func whenOversizedCommand(w *world.World, command, key string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if !strings.HasPrefix(command, "/fs-") {
		return fmt.Errorf("command %q is not a /fs-* command", command)
	}
	body := command + " " + strings.Repeat("<", 4090)
	w.JiraMockState.AddComment(key, body)
	return nil
}

// whenJiraGitLabPollerRuns runs one poll cycle against the mock Jira
// server with the GitLab-target options the CLI's jira-poll driver
// builds, and requires the cycle to succeed: a transport-budget refusal
// drops the event and advances the checkpoint, it does not fail the
// cycle.
func whenJiraGitLabPollerRuns(ctx context.Context, w *world.World) error {
	if w.JiraMockServer == nil || w.JiraConfigDir == "" {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if w.JiraTransport == nil || w.JiraTransport.Project == "" {
		return fmt.Errorf("GitLab target not set")
	}

	jiraClient, err := jira.New("test-token", jira.WithBaseURL(w.JiraMockServer.URL))
	if err != nil {
		return fmt.Errorf("create jira client: %w", err)
	}

	fsDir := filepath.Join(w.JiraConfigDir, ".fullsend")
	cfg, err := config.LoadConfig(fsDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	candidates, err := harnessdispatch.ListTriggeredHarnesses(ctx, fsDir, cfg, nil)
	if err != nil {
		return fmt.Errorf("list triggered harnesses: %w", err)
	}
	matcher := &jiradispatch.Matcher{Candidates: candidates}

	glPoller := poll.New(&fakeGitLabAPI{w: w}, nil, w.JiraTransport.Project, poll.Options{
		GitLabURL:      "https://gitlab.example.com",
		PipelineRef:    "main",
		DispatchSecret: "test-dispatch-secret",
	})

	opts := jirapoll.Options{
		TargetRepo: w.JiraTransport.Project,
		// JiraBaseURL is the browse-URL prefix the normalized event's
		// entity URL is built from; the dispatch door requires https,
		// so it names the pretend Atlassian instance, not the mock
		// server the client below talks to (same split
		// jira_lifecycle_test.go uses).
		JiraBaseURL: "https://acme.atlassian.net",
		JiraProject: "PROJ",
		N:           50, // process all candidates
		TargetForge: jirapoll.TargetForgeGitLab,
		GitLabURL:   "https://gitlab.example.com",
		Dispatcher:  jiradispatch.GitLabDispatcher{Poller: glPoller},
	}
	poller := jirapoll.New(jiraClient, matcher, opts)
	if err := poller.Run(ctx); err != nil {
		return fmt.Errorf("poller run: %w", err)
	}
	return nil
}

func thenPipelineCreated(w *world.World, issueKey string) error {
	if w.JiraTransport == nil {
		return fmt.Errorf("GitLab target not set")
	}
	rk := "jira-" + issueKey
	for _, p := range w.JiraTransport.Pipelines {
		if p.ResourceKey == rk {
			return nil
		}
	}
	return fmt.Errorf("no agent pipeline was created for %s; pipelines: %+v", issueKey, w.JiraTransport.Pipelines)
}

func thenNoPipelineCreated(w *world.World, issueKey string) error {
	if w.JiraTransport == nil {
		return fmt.Errorf("GitLab target not set")
	}
	rk := "jira-" + issueKey
	for _, p := range w.JiraTransport.Pipelines {
		if p.ResourceKey == rk {
			return fmt.Errorf("an agent pipeline was created for %s (%s stage), want none", issueKey, p.Stage)
		}
	}
	return nil
}
