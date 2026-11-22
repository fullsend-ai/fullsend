package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/jiramock"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// defaultJiraAgents lists the agent names the router recognizes.
// Matches defaultAgentsRepoKnownAgents in internal/cli/run.go.
var defaultJiraAgents = []string{"triage", "code", "fix", "review", "retro", "prioritize"}

// routerMatcher adapts a dispatch.HarnessRouter to the jirapoll.EventMatcher
// interface for behaviour tests. It converts the normevent.Event to a
// dispatch.NormalizedEvent for routing, then wraps matched stages as
// DispatchRecords.
type routerMatcher struct {
	router *dispatch.HarnessRouter
}

func (m *routerMatcher) Match(_ context.Context, event *normevent.Event) ([]jirapoll.DispatchRecord, error) {
	if !harnessdispatch.IsAuthorized(event) {
		return nil, nil
	}
	// Convert normevent.Event → dispatch.NormalizedEvent for the legacy router.
	de := dispatch.NormalizedEvent{
		Repo: event.Repo,
		Entity: dispatch.Entity{
			Kind: string(event.Entity.Kind),
			ID:   event.Entity.ID,
			Key:  event.Entity.Key,
			URL:  event.Entity.URL,
		},
		Transition: dispatch.Transition{
			Kind: string(event.Transition.Kind),
		},
		Actor: dispatch.Actor{
			ID:               event.Actor.ID,
			Kind:             string(event.Actor.Kind),
			Role:             string(event.Actor.Role),
			IsEntityAuthor:   event.Actor.IsEntityAuthor,
			IsEntityAssignee: event.Actor.IsEntityAssignee,
		},
		State: dispatch.State{
			Labels: event.State.Labels,
		},
		Source: dispatch.Source{
			System:    string(event.Source.System),
			RawType:   event.Source.RawType,
			RawAction: event.Source.RawAction,
		},
	}
	if event.Transition.Comment != nil {
		de.Transition.Comment = &dispatch.TransitionComment{
			Command:     event.Transition.Comment.Command,
			Body:        event.Transition.Comment.Body,
			Instruction: event.Transition.Comment.Instruction,
		}
	}
	if event.Transition.Label != nil {
		de.Transition.Label = &dispatch.TransitionLabel{
			Name:   event.Transition.Label.Name,
			Action: event.Transition.Label.Action,
		}
	}

	stages, err := m.router.Route(&de)
	if err != nil {
		return nil, err
	}

	// Marshal the NormalizedEvent as JSON for the event payload.
	neJSON, _ := json.Marshal(de)

	var records []jirapoll.DispatchRecord
	for _, stage := range stages {
		records = append(records, jirapoll.DispatchRecord{
			Agent:        stage,
			Role:         stage,
			SourceRepo:   event.Repo,
			EventType:    event.Source.RawType,
			EventPayload: string(neJSON),
			StatusRepo:   event.Repo,
			StatusNumber: fmt.Sprintf("%d", event.Entity.ID),
		})
	}
	return records, nil
}

func registerJiraPollSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a mock Jira server$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenMockJiraServer(world.FromContext(ctx))
	})
	sc.Step(`^a Jira issue "([^"]+)" with labels "([^"]*)"$`, func(ctx context.Context, key, labels string) (context.Context, error) {
		return ctx, givenJiraIssue(world.FromContext(ctx), key, labels)
	})
	sc.Step(`^Jira issue "([^"]+)" has id "([^"]*)"$`, func(ctx context.Context, key, id string) (context.Context, error) {
		return ctx, givenJiraIssueID(world.FromContext(ctx), key, id)
	})
	sc.Step(`^a comment "([^"]*)" is added to Jira issue "([^"]+)"$`, func(ctx context.Context, body, key string) (context.Context, error) {
		return ctx, whenJiraComment(world.FromContext(ctx), key, body)
	})
	sc.Step(`^the label "([^"]+)" is added to Jira issue "([^"]+)"$`, func(ctx context.Context, label, key string) (context.Context, error) {
		return ctx, whenJiraLabelAdded(world.FromContext(ctx), key, label)
	})
	sc.Step(`^the Jira poller runs$`, func(ctx context.Context) (context.Context, error) {
		return ctx, whenJiraPollerRuns(ctx, world.FromContext(ctx))
	})
	sc.Step(`^fullsend poll runs the Jira poller with target repo "([^"]*)"$`, func(ctx context.Context, repo string) (context.Context, error) {
		return ctx, whenJiraPollCLIRuns(ctx, world.FromContext(ctx), repo)
	})
	sc.Step(`^fullsend poll fails with "([^"]*)" and writes no dispatch output$`, func(ctx context.Context, text string) (context.Context, error) {
		return ctx, thenJiraPollCLIFails(world.FromContext(ctx), text)
	})
	sc.Step(`^the dispatch output contains a "([^"]+)" stage for issue "([^"]+)"$`, func(ctx context.Context, stage, key string) (context.Context, error) {
		return ctx, thenDispatchContains(world.FromContext(ctx), stage, key)
	})
	sc.Step(`^the dispatch output does not contain a stage for issue "([^"]+)"$`, func(ctx context.Context, key string) (context.Context, error) {
		return ctx, thenDispatchNotContains(world.FromContext(ctx), key)
	})
	sc.Step(`^the poll log says the event for issue "([^"]+)" was skipped$`, func(ctx context.Context, key string) (context.Context, error) {
		return ctx, thenPollLogSaysEventSkipped(world.FromContext(ctx), key)
	})
	sc.Step(`^the "([^"]+)" Jira project role is backed by group "([^"]+)"$`, func(ctx context.Context, role, group string) (context.Context, error) {
		return ctx, givenRoleBackedByGroup(world.FromContext(ctx), role, group)
	})
	sc.Step(`^Jira user "([^"]+)" belongs to group "([^"]+)"$`, func(ctx context.Context, accountID, group string) (context.Context, error) {
		return ctx, givenUserInGroup(world.FromContext(ctx), accountID, group)
	})
	sc.Step(`^a comment "([^"]*)" from Jira user "([^"]+)" is added to Jira issue "([^"]+)"$`, func(ctx context.Context, body, accountID, key string) (context.Context, error) {
		return ctx, whenJiraCommentFromActor(world.FromContext(ctx), key, body, accountID)
	})
	sc.Step(`^the dispatch output attributes issue "([^"]+)" to actor "([^"]+)" with role "([^"]+)"$`, func(ctx context.Context, key, actorID, role string) (context.Context, error) {
		return ctx, thenDispatchActorRole(world.FromContext(ctx), key, actorID, role)
	})
}

func givenMockJiraServer(w *world.World) error {
	srv, state := jiramock.NewServer()
	w.JiraMockServer = srv
	w.JiraMockState = state

	// Create a temp dir with a minimal .fullsend config layout.
	tmpDir, err := os.MkdirTemp("", "jira-poll-test-*")
	if err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	w.JiraConfigDir = tmpDir

	fsDir := filepath.Join(tmpDir, ".fullsend")
	if err := os.MkdirAll(fsDir, 0o755); err != nil {
		return fmt.Errorf("create .fullsend dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(fsDir, "config.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func givenJiraIssue(w *world.World, key, labels string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	var labelList []string
	for _, l := range strings.Split(labels, ",") {
		l = strings.TrimSpace(l)
		if l != "" {
			labelList = append(labelList, l)
		}
	}
	w.JiraMockState.AddIssue(key, labelList)
	return nil
}

func givenJiraIssueID(w *world.World, key, id string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if !w.JiraMockState.SetIssueID(key, id) {
		return fmt.Errorf("issue %q not found in the mock Jira server", key)
	}
	return nil
}

func whenJiraComment(w *world.World, key, body string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	w.JiraMockState.AddComment(key, body)
	return nil
}

func whenJiraLabelAdded(w *world.World, key, label string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	w.JiraMockState.AddLabelChange(key, label)
	return nil
}

func whenJiraPollerRuns(ctx context.Context, w *world.World) error {
	if w.JiraMockServer == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}

	jiraClient, err := jira.New("test-token", jira.WithBaseURL(w.JiraMockServer.URL))
	if err != nil {
		return fmt.Errorf("create jira client: %w", err)
	}

	matcher := &routerMatcher{router: dispatch.NewHarnessRouter(defaultJiraAgents)}

	outputPath := filepath.Join(w.JiraConfigDir, "dispatches.json")
	opts := jirapoll.Options{
		TargetRepo:  "test-org/test-repo",
		JiraBaseURL: w.JiraMockServer.URL,
		JiraProject: "PROJ",
		OutputPath:  outputPath,
		N:           50, // process all candidates
	}

	poller := jirapoll.New(jiraClient, matcher, opts)
	// The poller reports validation skips through the standard logger, not
	// through the dispatch output or its error (a partial skip succeeds),
	// so capture the run's log for the Then steps: without it a scenario
	// can pin "no dispatch" but not "skipped because validation failed".
	// The logger is process-global and behaviour tests run in parallel, so
	// the capture window is serialized: two concurrent swaps would each
	// lose (or steal) the other's warnings.
	jiraPollLogMu.Lock()
	defer jiraPollLogMu.Unlock()
	var logBuf bytes.Buffer
	prevWriter := log.Writer()
	log.SetOutput(&logBuf)
	runErr := poller.Run(ctx)
	// Restore the writer before reading the buffer: bytes.Buffer is not
	// safe for concurrent use, so a foreign log.Printf from a parallel
	// scenario would race with the read while the logger still points
	// here. log.SetOutput and log.Printf share the logger's mutex, so no
	// write can target the buffer after the restore returns.
	log.SetOutput(prevWriter)
	w.JiraPollerLog = logBuf.String()
	if runErr != nil {
		return fmt.Errorf("poller run: %w", runErr)
	}
	return nil
}

// jiraPollLogMu serializes the standard-logger capture around a poller run
// in whenJiraPollerRuns (see there for why).
var jiraPollLogMu sync.Mutex

// jiraPollCLITimeout bounds one `fullsend poll` run launched by the CLI step.
const jiraPollCLITimeout = 2 * time.Minute

// whenJiraPollCLIRuns runs the suite's fullsend CLI as
// `fullsend poll --input-driver jira-poll` with the given target repo. The
// Jira site, project, and poll settings come from tracker.jira in the
// scenario's config.yaml (#9077 removed --jira-url and --jira-project), so
// the step writes that mapping with a syntactically valid HTTPS base_url
// (https://jira.invalid) and placeholder scoped credentials: the CLI
// validates the connection — which requires an https URL — before the
// argument checks, and the scenarios using this step refuse during argument
// validation, before any Jira contact, so they must not fail on the URL
// instead of the argument under test. The mock Jira server is still
// required: it provisions the config dir the run and the scenario's earlier
// steps use. A run that exits non-zero is recorded, not returned.
func whenJiraPollCLIRuns(ctx context.Context, w *world.World, repo string) error {
	if w.JiraMockServer == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	if w.CLIBinary == "" {
		return fmt.Errorf("no fullsend CLI binary configured for the suite")
	}
	if err := writeJiraPollTrackerConfig(w); err != nil {
		return err
	}
	w.JiraPollCLIOutputPath = filepath.Join(w.JiraConfigDir, "cli-dispatches.json")
	runCtx, cancel := context.WithTimeout(ctx, jiraPollCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, w.CLIBinary, "poll", "--input-driver", "jira-poll",
		"--target-repo", repo, "--output", w.JiraPollCLIOutputPath,
		"--fullsend-dir", filepath.Join(w.JiraConfigDir, ".fullsend"))
	cmd.Dir = w.JiraConfigDir
	cmd.Env = jiraPollCLIEnv(os.Environ(), w.JiraConfigDir)
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	w.JiraPollCLIOutput = string(out)
	w.JiraPollCLIExitCode = 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("running fullsend poll: %w", err)
		}
		w.JiraPollCLIExitCode = exitErr.ExitCode()
	}
	return nil
}

// writeJiraPollTrackerConfig writes the scenario config's tracker.jira with
// the settings the jira-poll driver reads from config.yaml (#9077) in place
// of the removed --jira-url/--jira-project flags: project PROJ, polling
// enabled, and a syntactically valid HTTPS base_url (https://jira.invalid).
// The base_url deliberately does not point at the mock Jira server: the mock
// serves plain HTTP, the CLI's connection validation requires https and runs
// before the argument checks, and the scenarios using this step refuse
// during argument validation — before any Jira contact — so an HTTP URL
// would fail the run on the URL instead of the argument under test. The
// suite's config starts as a bare `version: 1`, which this replaces for the
// run.
func writeJiraPollTrackerConfig(w *world.World) error {
	cfg := "version: \"1\"\n" +
		"tracker:\n" +
		"  jira:\n" +
		"    base_url: https://jira.invalid\n" +
		"    project: PROJ\n" +
		"    poll:\n" +
		"      enabled: true\n"
	path := filepath.Join(w.JiraConfigDir, ".fullsend", "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		return fmt.Errorf("write tracker.jira config: %w", err)
	}
	return nil
}

// thenJiraPollCLIFails checks the recorded `fullsend poll` run exited
// non-zero, printed text, and wrote no dispatch output.
func thenJiraPollCLIFails(w *world.World, text string) error {
	if w.JiraPollCLIOutputPath == "" {
		return fmt.Errorf("fullsend poll has not run; use 'When fullsend poll runs the Jira poller with target repo'")
	}
	if w.JiraPollCLIExitCode == 0 {
		return fmt.Errorf("fullsend poll succeeded, want it to fail with %q; output:\n%s", text, w.JiraPollCLIOutput)
	}
	if !strings.Contains(w.JiraPollCLIOutput, text) {
		return fmt.Errorf("fullsend poll failed (exit %d) without %q; output:\n%s", w.JiraPollCLIExitCode, text, w.JiraPollCLIOutput)
	}
	if _, err := os.Stat(w.JiraPollCLIOutputPath); err == nil {
		return fmt.Errorf("fullsend poll wrote dispatch output to %s", w.JiraPollCLIOutputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking dispatch output: %w", err)
	}
	return nil
}

func thenDispatchContains(w *world.World, stage, issueKey string) error {
	dispatches, err := readDispatches(w)
	if err != nil {
		return err
	}

	for _, d := range dispatches {
		if d.Agent == stage && dispatchMatchesIssue(d, issueKey) {
			return nil
		}
	}
	return fmt.Errorf("dispatch output missing agent=%q for %s; got %d dispatches: %v",
		stage, issueKey, len(dispatches), dispatches)
}

func givenRoleBackedByGroup(w *world.World, role, group string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	w.JiraMockState.SetRoleGroup(role, group)
	return nil
}

func givenUserInGroup(w *world.World, accountID, group string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	w.JiraMockState.SetUserGroups(accountID, group)
	return nil
}

func whenJiraCommentFromActor(w *world.World, key, body, accountID string) error {
	if w.JiraMockState == nil {
		return fmt.Errorf("mock Jira server not initialized")
	}
	w.JiraMockState.AddCommentFromActor(key, body, accountID, accountID)
	return nil
}

func thenDispatchActorRole(w *world.World, issueKey, actorID, wantRole string) error {
	dispatches, err := readDispatches(w)
	if err != nil {
		return err
	}

	for _, d := range dispatches {
		if d.EventPayload == "" {
			continue
		}
		var ne dispatch.NormalizedEvent
		if err := json.Unmarshal([]byte(d.EventPayload), &ne); err != nil {
			return fmt.Errorf("unmarshal payload: %w", err)
		}
		if ne.Actor.ID == actorID {
			if ne.Actor.Role != wantRole {
				return fmt.Errorf("actor %s role = %q, want %q", actorID, ne.Actor.Role, wantRole)
			}
			return nil
		}
	}
	return fmt.Errorf("no dispatch found for issue %s with actor %s", issueKey, actorID)
}

func thenDispatchNotContains(w *world.World, issueKey string) error {
	dispatches, err := readDispatches(w)
	if err != nil {
		return err
	}

	for _, d := range dispatches {
		if dispatchMatchesIssue(d, issueKey) {
			return fmt.Errorf("dispatch output unexpectedly contains agent=%q for %s",
				d.Agent, issueKey)
		}
	}
	return nil
}

// thenPollLogSaysEventSkipped asserts the captured poller log shows the
// validation skip for the issue. The dispatch output alone cannot tell a
// validation skip from any other reason an event produced no dispatch (a
// fetch error, a filter), so the scenario pins the skip warning itself.
func thenPollLogSaysEventSkipped(w *world.World, issueKey string) error {
	if w.JiraPollerLog == "" {
		return fmt.Errorf("no captured Jira poller log; did the Jira poller step run?")
	}
	for _, want := range []string{
		"WARNING: skipping event ",
		fmt.Sprintf(" for issue %s: normalized event: ", issueKey),
	} {
		if !strings.Contains(w.JiraPollerLog, want) {
			return fmt.Errorf("poll log lacks %q:\n%s", want, w.JiraPollerLog)
		}
	}
	return nil
}

// dispatchMatchesIssue checks whether a DispatchRecord belongs to the given
// Jira issue key by inspecting the entity.key field inside EventPayload.
func dispatchMatchesIssue(d jirapoll.DispatchRecord, issueKey string) bool {
	if d.EventPayload == "" {
		return false
	}
	var payload struct {
		Entity struct {
			Key string `json:"key"`
		} `json:"entity"`
	}
	if err := json.Unmarshal([]byte(d.EventPayload), &payload); err != nil {
		return false
	}
	return payload.Entity.Key == issueKey
}

func readDispatches(w *world.World) ([]jirapoll.DispatchRecord, error) {
	if w.JiraConfigDir == "" {
		return nil, fmt.Errorf("jira config dir not set")
	}
	outputPath := filepath.Join(w.JiraConfigDir, "dispatches.json")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read dispatches: %w", err)
	}
	var dispatches []jirapoll.DispatchRecord
	if err := json.Unmarshal(data, &dispatches); err != nil {
		return nil, fmt.Errorf("parse dispatches: %w", err)
	}
	return dispatches, nil
}

// jiraPollCLIEnv builds the environment of the fullsend poll child: the
// passthrough variables mintStatusEnv also keeps, a HOME under home, and
// the placeholder Jira credentials. The behaviour job's own secrets (API
// tokens, private keys, actor PATs) never reach the child.
func jiraPollCLIEnv(environ []string, home string) []string {
	env := make([]string, 0, len(mintStatusPassthroughEnv)+4)
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && slices.Contains(mintStatusPassthroughEnv, name) {
			env = append(env, kv)
		}
	}
	return append(env,
		"HOME="+home,
		"NO_COLOR=1",
		"FULLSEND_JIRA_TOKEN=test-token",
		"FULLSEND_JIRA_EMAIL=poller@example.com",
	)
}
