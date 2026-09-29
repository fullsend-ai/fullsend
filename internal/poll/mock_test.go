package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// pipelineCall records a CreatePipeline invocation.
type pipelineCall struct {
	Owner     string
	Repo      string
	Ref       string
	Variables map[string]string
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

	// files is per-branch file content: branch → path → bytes.
	// ForceCommitFileToBranch replaces the branch tree with a single file.
	files map[string]map[string][]byte
	// fileContentRefs records every ref GetFileContentAtRef was queried
	// with, in order, so tests can assert persistWithCAS pins its content
	// read to the exact SHA a prior GetBranchRef call returned.
	fileContentRefs []string
	fileContentErr  error
	branchRefErr    error
	forceCommitErr  error
	// forceCommitErrSeq is an error queue for CommitFileToBranch / ForceCommitFileToBranch.
	// Each call shifts the first element; when empty, falls through to forceCommitErr.
	forceCommitErrSeq []error
	deleteRefErr      error
	deletedRefs       []string
	forceCommits      int
	// branchGen is the CAS generation for each branch. GetBranchRef returns
	// a SHA derived from it; CommitFileToBranch 409s when expectedSHA is
	// stale relative to the current generation.
	branchGen map[string]int
	// conflictOnce, when set, simulates a concurrent writer: the document
	// is installed as the new branch tip and the commit returns
	// ErrNonFastForward once, then the field is cleared.
	conflictOnce *persistedPollState

	// pendingSign holds branch → unsigned poll state seeded via
	// setPollState/setSlashState. It is signed lazily by
	// GetFileContentAtRef using the owner/repo the caller actually
	// queries with, since the real HMAC domain (see (*Poller).hmacDomain)
	// is bound to the project path, and different tests exercise
	// pollers with different project paths.
	pendingSign map[string]persistedPollState

	issue    map[int]*Issue // keyed by IID
	issueErr map[int]error
	mr       map[int]*MergeRequest // keyed by IID
	mrErr    map[int]error

	memberLevel map[int]int   // keyed by userID
	memberErr   map[int]error // keyed by userID

	projectPaths map[int]string // keyed by project ID

	emojis []emojiCall

	authenticatedUser string
	authErr           error

	pipelineCounter  int
	pipelineErr      error
	pipelineCalls    []pipelineCall
	pipelineErrAfter int // fail after N successful calls (0 = always fail if pipelineErr set)
}

func newMockClient() *mockClient {
	return &mockClient{
		notes:          make(map[int][]Note),
		noteErr:        make(map[int]error),
		mrNotes:        make(map[int][]Note),
		mrNoteErr:      make(map[int]error),
		labelEvents:    make(map[int][]ResourceLabelEvent),
		labelEventsErr: make(map[int]error),
		files:          make(map[string]map[string][]byte),
		pendingSign:    make(map[string]persistedPollState),
		issue:          make(map[int]*Issue),
		issueErr:       make(map[int]error),
		mr:             make(map[int]*MergeRequest),
		mrErr:          make(map[int]error),
		memberLevel:    make(map[int]int),
		memberErr:      make(map[int]error),
		projectPaths:   make(map[int]string),
		branchGen:      make(map[string]int),
	}
}

func mockBranchSHA(branch string, gen int) string {
	return fmt.Sprintf("sha-%s-%d", branch, gen)
}

// mockResolveRef maps a mockBranchSHA-formatted ref back to the branch it
// names, so GetFileContentAtRef can serve a persistWithCAS-style read
// pinned to the SHA a prior GetBranchRef call returned. The mock does not
// keep per-commit historical snapshots; it resolves a SHA ref to that
// branch's current content, which is sufficient for these tests since
// nothing else writes to a branch between a GetBranchRef/
// GetFileContentAtRef pair in a single persistWithCAS attempt. A ref that
// isn't a recognized mockBranchSHA (e.g. a literal branch name) is
// returned unchanged.
func mockResolveRef(ref string) string {
	for _, b := range []string{PollStateBranchSlash, PollStateBranchEvents} {
		if strings.HasPrefix(ref, "sha-"+b+"-") {
			return b
		}
	}
	return ref
}

var _ GitLabClient = (*mockClient)(nil)

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

// testDispatchSecret is the shared HMAC secret used by test pollers
// (see newTestPoller) and by setPollState when seeding state, so seeded
// documents verify under the default test poller now that the poller
// fails closed on unsigned state.
const testDispatchSecret = "test-secret"

func (m *mockClient) putBranchFile(branch, path string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putBranchFileLocked(branch, path, data)
}

// setBranchState seeds a validly-signed document for branch, deferring
// the actual signing to GetFileContentAtRef (see pendingSign) since the
// signature is now bound to the project path the poller under test
// queries with, not just the branch.
func (m *mockClient) setBranchState(branch string, s persistedPollState) {
	if branch == PollStateBranchSlash {
		s = persistedPollState{
			LastPollAtFast:     s.LastPollAtFast,
			DispatchedKeysFast: s.DispatchedKeysFast,
			FailedKeysFast:     s.FailedKeysFast,
		}
	} else {
		s = persistedPollState{
			LastPollAtFull:     s.LastPollAtFull,
			LabelState:         s.LabelState,
			DispatchedKeysFull: s.DispatchedKeysFull,
			FailedKeysFull:     s.FailedKeysFull,
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingSign == nil {
		m.pendingSign = make(map[string]persistedPollState)
	}
	m.pendingSign[branch] = s
}

// setPollState seeds a validly-signed events-branch document.
func (m *mockClient) setPollState(s persistedPollState) {
	m.setBranchState(PollStateBranchEvents, s)
}

// setSlashState seeds a validly-signed slash-branch document.
func (m *mockClient) setSlashState(s persistedPollState) {
	m.setBranchState(PollStateBranchSlash, s)
}

// setPollStateUnsigned seeds an events-branch document without
// re-signing (or with whatever HMAC field s already carries).
func (m *mockClient) setPollStateUnsigned(s persistedPollState) {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	m.putBranchFile(PollStateBranchEvents, PollStateFileName, data)
}

func (m *mockClient) setPollStateRaw(raw string) {
	m.putBranchFile(PollStateBranchEvents, PollStateFileName, []byte(raw))
}

func (m *mockClient) getBranchState(branch string) (persistedPollState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, ok := m.files[branch]
	if ok {
		data, ok := files[PollStateFileName]
		if ok {
			var s persistedPollState
			if err := json.Unmarshal(data, &s); err != nil {
				return persistedPollState{}, true
			}
			return s, true
		}
	}
	// Fall back to a seeded-but-not-yet-force-committed document (see
	// pendingSign): still visible to test assertions even though it
	// hasn't been materialized into files by an actual write.
	if s, ok := m.pendingSign[branch]; ok {
		return s, true
	}
	return persistedPollState{}, false
}

func (m *mockClient) getPollState() (persistedPollState, bool) {
	return m.getBranchState(PollStateBranchEvents)
}

func (m *mockClient) getSlashState() (persistedPollState, bool) {
	return m.getBranchState(PollStateBranchSlash)
}

func (m *mockClient) GetFileContentAtRef(_ context.Context, owner, repo, path, ref string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fileContentRefs = append(m.fileContentRefs, ref)
	if m.fileContentErr != nil {
		return nil, m.fileContentErr
	}
	// persistWithCAS pins its read to the SHA a prior GetBranchRef call
	// returned rather than a branch name; resolve it back to the branch
	// so the lookups below (keyed by branch name) still find it.
	branch := mockResolveRef(ref)
	// Seeded-valid documents (setPollState/setSlashState) are signed
	// here, using the owner/repo the caller queries with, rather than
	// at seed time: the real HMAC domain is bound to the project path,
	// which varies across tests. A real prior write (files[branch]) always
	// takes precedence over a stale seed.
	if path == PollStateFileName {
		if files, ok := m.files[branch]; !ok || files[path] == nil {
			if s, ok := m.pendingSign[branch]; ok {
				domain := hmacDomainFor(branch, owner+"/"+repo)
				sig, err := computeStateHMAC(testDispatchSecret, domain, s)
				if err != nil {
					return nil, err
				}
				s.HMAC = sig
				return json.Marshal(s)
			}
		}
	}
	files, ok := m.files[branch]
	if !ok {
		return nil, forge.ErrNotFound
	}
	data, ok := files[path]
	if !ok {
		return nil, forge.ErrNotFound
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *mockClient) GetBranchRef(_ context.Context, _, _, branch string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.branchRefErr != nil {
		return "", m.branchRefErr
	}
	_, hasFile := m.files[branch]
	_, hasPending := m.pendingSign[branch]
	if !hasFile && !hasPending {
		return "", forge.ErrNotFound
	}
	if m.branchGen == nil {
		m.branchGen = make(map[string]int)
	}
	return mockBranchSHA(branch, m.branchGen[branch]), nil
}

func (m *mockClient) consumeForceCommitErr() error {
	if len(m.forceCommitErrSeq) > 0 {
		e := m.forceCommitErrSeq[0]
		m.forceCommitErrSeq = m.forceCommitErrSeq[1:]
		return e
	}
	return m.forceCommitErr
}

func (m *mockClient) CommitFileToBranch(_ context.Context, owner, repo, branch, path, _ string, content []byte, expectedSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.consumeForceCommitErr(); err != nil {
		return err
	}
	if m.branchGen == nil {
		m.branchGen = make(map[string]int)
	}
	if m.conflictOnce != nil {
		s := *m.conflictOnce
		m.conflictOnce = nil
		domain := hmacDomainFor(branch, owner+"/"+repo)
		sig, err := computeStateHMAC(testDispatchSecret, domain, s)
		if err != nil {
			return err
		}
		s.HMAC = sig
		data, err := json.Marshal(s)
		if err != nil {
			return err
		}
		m.putBranchFileLocked(branch, path, data)
		m.branchGen[branch]++
		return fmt.Errorf("%w: concurrent update of %s", forge.ErrNonFastForward, branch)
	}
	_, hasFile := m.files[branch]
	_, hasPending := m.pendingSign[branch]
	if expectedSHA == "" {
		if hasFile || hasPending {
			return fmt.Errorf("%w: branch %s already exists", forge.ErrNonFastForward, branch)
		}
	} else {
		current := mockBranchSHA(branch, m.branchGen[branch])
		if !hasFile && !hasPending {
			return forge.ErrNotFound
		}
		if current != expectedSHA {
			return fmt.Errorf("%w: concurrent update of %s", forge.ErrNonFastForward, branch)
		}
	}
	m.putBranchFileLocked(branch, path, content)
	m.branchGen[branch]++
	m.forceCommits++
	return nil
}

func (m *mockClient) putBranchFileLocked(branch, path string, data []byte) {
	if m.files == nil {
		m.files = make(map[string]map[string][]byte)
	}
	copied := make([]byte, len(data))
	copy(copied, data)
	m.files[branch] = map[string][]byte{path: copied}
}

func (m *mockClient) ForceCommitFileToBranch(_ context.Context, _, _, branch, path, _ string, content []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.consumeForceCommitErr(); err != nil {
		return err
	}
	m.putBranchFileLocked(branch, path, content)
	if m.branchGen == nil {
		m.branchGen = make(map[string]int)
	}
	m.branchGen[branch]++
	m.forceCommits++
	return nil
}

func (m *mockClient) DeleteRef(_ context.Context, _, _, refPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteRefErr != nil {
		return m.deleteRefErr
	}
	m.deletedRefs = append(m.deletedRefs, refPath)
	branch, ok := strings.CutPrefix(refPath, "heads/")
	if !ok {
		return fmt.Errorf("unsupported ref path %q", refPath)
	}
	_, existsFile := m.files[branch]
	_, existsPending := m.pendingSign[branch]
	if !existsFile && !existsPending {
		return forge.ErrNotFound
	}
	delete(m.files, branch)
	delete(m.pendingSign, branch)
	delete(m.branchGen, branch)
	return nil
}

func (m *mockClient) GetAuthenticatedUser(_ context.Context) (string, error) {
	return m.authenticatedUser, m.authErr
}

func (m *mockClient) GetAuthenticatedUserID(_ context.Context) (int, error) {
	return 0, m.authErr
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

func (m *mockClient) GetMergeRequest(_ context.Context, _, _ string, mrIID int) (*MergeRequest, error) {
	if err, ok := m.mrErr[mrIID]; ok && err != nil {
		return nil, err
	}
	mrObj, ok := m.mr[mrIID]
	if !ok {
		return nil, forge.ErrNotFound
	}
	return mrObj, nil
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

func (m *mockClient) CreatePipeline(_ context.Context, owner, repo, ref string, variables map[string]string) (int64, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vars := make(map[string]string, len(variables))
	for k, v := range variables {
		vars[k] = v
	}
	m.pipelineCalls = append(m.pipelineCalls, pipelineCall{
		Owner:     owner,
		Repo:      repo,
		Ref:       ref,
		Variables: vars,
	})
	if m.pipelineErr != nil && (m.pipelineErrAfter == 0 || len(m.pipelineCalls) > m.pipelineErrAfter) {
		return 0, "", m.pipelineErr
	}
	m.pipelineCounter++
	return int64(m.pipelineCounter), fmt.Sprintf("https://gitlab.example.com/-/pipelines/%d", m.pipelineCounter), nil
}
