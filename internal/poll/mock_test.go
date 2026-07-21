package poll

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// emojiCall records a CreateNoteAwardEmoji invocation.
type emojiCall struct {
	NoteableIID int
	NoteID      int
	Emoji       string
}

// mockClient implements GitLabClient with configurable return values
// and error injection for deterministic testing.
type mockClient struct {
	mu sync.Mutex

	issues    []Issue
	issuesErr error

	mrs    []MergeRequest
	mrsErr error

	notes   map[int][]Note // keyed by issue IID
	noteErr map[int]error

	mrNotes   map[int][]Note // keyed by MR IID
	mrNoteErr map[int]error

	events    []ProjectEvent
	eventsErr error

	labelEvents    map[int][]ResourceLabelEvent // keyed by issue IID
	labelEventsErr map[int]error

	variables   map[string]string
	variableErr map[string]error
	updatedVars map[string]string // records UpdateCIVariable calls

	issue    map[int]*Issue // keyed by IID
	issueErr map[int]error

	memberLevel map[int]int   // keyed by userID
	memberErr   map[int]error // keyed by userID

	projectPaths map[int]string // keyed by project ID

	emojis []emojiCall

	authenticatedUser string
	authErr           error
}

func newMockClient() *mockClient {
	return &mockClient{
		notes:          make(map[int][]Note),
		noteErr:        make(map[int]error),
		mrNotes:        make(map[int][]Note),
		mrNoteErr:      make(map[int]error),
		labelEvents:    make(map[int][]ResourceLabelEvent),
		labelEventsErr: make(map[int]error),
		variables:      make(map[string]string),
		variableErr:    make(map[string]error),
		updatedVars:    make(map[string]string),
		issue:          make(map[int]*Issue),
		issueErr:       make(map[int]error),
		memberLevel:    make(map[int]int),
		memberErr:      make(map[int]error),
		projectPaths:   make(map[int]string),
	}
}

func (m *mockClient) ListIssuesUpdatedSince(_ context.Context, _, _ string, _ time.Time) ([]Issue, error) {
	return m.issues, m.issuesErr
}

func (m *mockClient) ListMergeRequestsUpdatedSince(_ context.Context, _, _ string, _ time.Time) ([]MergeRequest, error) {
	return m.mrs, m.mrsErr
}

func (m *mockClient) ListProjectEvents(_ context.Context, _, _ string, _ string, _ time.Time) ([]ProjectEvent, error) {
	return m.events, m.eventsErr
}

func (m *mockClient) ListIssueNotes(_ context.Context, _, _ string, issueIID int) ([]Note, error) {
	if err, ok := m.noteErr[issueIID]; ok && err != nil {
		return nil, err
	}
	return m.notes[issueIID], nil
}

func (m *mockClient) ListMergeRequestNotes(_ context.Context, _, _ string, mrIID int) ([]Note, error) {
	if err, ok := m.mrNoteErr[mrIID]; ok && err != nil {
		return nil, err
	}
	return m.mrNotes[mrIID], nil
}

func (m *mockClient) ListResourceLabelEvents(_ context.Context, _, _ string, issueIID int) ([]ResourceLabelEvent, error) {
	if err, ok := m.labelEventsErr[issueIID]; ok && err != nil {
		return nil, err
	}
	return m.labelEvents[issueIID], nil
}

func (m *mockClient) GetCIVariable(_ context.Context, _, _, name string) (string, error) {
	if err, ok := m.variableErr[name]; ok && err != nil {
		return "", err
	}
	val, ok := m.variables[name]
	if !ok {
		return "", forge.ErrNotFound
	}
	return val, nil
}

func (m *mockClient) UpdateCIVariable(_ context.Context, _, _, name, value string, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedVars[name] = value
	return nil
}

func (m *mockClient) GetAuthenticatedUser(_ context.Context) (string, error) {
	return m.authenticatedUser, m.authErr
}

func (m *mockClient) CreateNoteAwardEmoji(_ context.Context, _, _, _ string, noteableIID, noteID int, emoji string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emojis = append(m.emojis, emojiCall{
		NoteableIID: noteableIID,
		NoteID:      noteID,
		Emoji:       emoji,
	})
	return nil
}

func (m *mockClient) GetIssue(_ context.Context, _, _ string, issueIID int) (*Issue, error) {
	if err, ok := m.issueErr[issueIID]; ok && err != nil {
		return nil, err
	}
	iss, ok := m.issue[issueIID]
	if !ok {
		return nil, forge.ErrNotFound
	}
	return iss, nil
}

func (m *mockClient) GetMemberAccessLevel(_ context.Context, _, _ string, userID int) (int, error) {
	if err, ok := m.memberErr[userID]; ok && err != nil {
		return 0, err
	}
	level, ok := m.memberLevel[userID]
	if !ok {
		return 0, fmt.Errorf("member not found")
	}
	return level, nil
}

func (m *mockClient) GetProjectPath(_ context.Context, projectID int) (string, error) {
	path, ok := m.projectPaths[projectID]
	if !ok {
		return "", forge.ErrNotFound
	}
	return path, nil
}
