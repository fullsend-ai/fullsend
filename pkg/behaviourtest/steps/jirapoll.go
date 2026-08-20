package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			ID:             event.Actor.ID,
			Kind:           string(event.Actor.Kind),
			Role:           string(event.Actor.Role),
			IsEntityAuthor: event.Actor.IsEntityAuthor,
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
	sc.Step(`^a comment "([^"]*)" is added to Jira issue "([^"]+)"$`, func(ctx context.Context, body, key string) (context.Context, error) {
		return ctx, whenJiraComment(world.FromContext(ctx), key, body)
	})
	sc.Step(`^the label "([^"]+)" is added to Jira issue "([^"]+)"$`, func(ctx context.Context, label, key string) (context.Context, error) {
		return ctx, whenJiraLabelAdded(world.FromContext(ctx), key, label)
	})
	sc.Step(`^the Jira poller runs$`, func(ctx context.Context) (context.Context, error) {
		return ctx, whenJiraPollerRuns(ctx, world.FromContext(ctx))
	})
	sc.Step(`^the dispatch output contains a "([^"]+)" stage for issue "([^"]+)"$`, func(ctx context.Context, stage, key string) (context.Context, error) {
		return ctx, thenDispatchContains(world.FromContext(ctx), stage, key)
	})
	sc.Step(`^the dispatch output does not contain a stage for issue "([^"]+)"$`, func(ctx context.Context, key string) (context.Context, error) {
		return ctx, thenDispatchNotContains(world.FromContext(ctx), key)
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
	if err := poller.Run(ctx); err != nil {
		return fmt.Errorf("poller run: %w", err)
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
