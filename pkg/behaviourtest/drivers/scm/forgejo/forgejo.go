// Package forgejo is the Forgejo scm.Driver for behaviour tests.
//
// It is a minimal stub: every operation returns an error that satisfies
// errors.Is(err, drivers.ErrNotImplemented) and names the operation. It
// never delegates to the GitHub or GitLab drivers. Real Forgejo
// operations land with the Forgejo forge adapter (see #9642).
package forgejo

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
)

// driverName labels this driver in not-implemented errors.
const driverName = "forgejo SCM"

// Driver implements scm.Driver for Forgejo. It is immutable and holds no
// state, so it is safe for concurrent use.
type Driver struct {
	// Client is the forge client the driver was built with. The stub
	// does not use it.
	Client forge.Client
}

var _ scm.Driver = (*Driver)(nil)

// New returns the Forgejo SCM driver.
func New(client forge.Client) scm.Driver {
	return &Driver{Client: client}
}

func notImplemented(operation string) error {
	return drivers.NotImplemented(driverName, operation)
}

func (d *Driver) CreateIssue(context.Context, string, string, string, string, ...string) (*forge.Issue, error) {
	return nil, notImplemented("CreateIssue")
}

func (d *Driver) AddIssueLabels(context.Context, string, string, int, ...string) error {
	return notImplemented("AddIssueLabels")
}

func (d *Driver) EnsureLabel(context.Context, string, string, string) error {
	return notImplemented("EnsureLabel")
}

func (d *Driver) AddComment(context.Context, string, string, int, string) (*forge.IssueComment, error) {
	return nil, notImplemented("AddComment")
}

func (d *Driver) GetIssue(context.Context, string, string, int) (*forge.Issue, error) {
	return nil, notImplemented("GetIssue")
}

func (d *Driver) ListOpenIssues(context.Context, string, string, ...string) ([]forge.Issue, error) {
	return nil, notImplemented("ListOpenIssues")
}

func (d *Driver) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, notImplemented("GetFileContent")
}

func (d *Driver) GetFileContentAtRef(context.Context, string, string, string, string) ([]byte, error) {
	return nil, notImplemented("GetFileContentAtRef")
}

func (d *Driver) CommitFile(context.Context, string, string, string, string, []byte) error {
	return notImplemented("CommitFile")
}

func (d *Driver) CreateBranch(context.Context, string, string, string) error {
	return notImplemented("CreateBranch")
}

func (d *Driver) DeleteBranch(context.Context, string, string, string) error {
	return notImplemented("DeleteBranch")
}

func (d *Driver) CommitFileToBranch(context.Context, string, string, string, string, string, []byte) error {
	return notImplemented("CommitFileToBranch")
}

func (d *Driver) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, notImplemented("CreateChangeProposal")
}

func (d *Driver) SubmitPullRequestReview(context.Context, string, string, int, string) error {
	return notImplemented("SubmitPullRequestReview")
}

func (d *Driver) CloseIssue(context.Context, string, string, int) error {
	return notImplemented("CloseIssue")
}

func (d *Driver) ListOpenChangeProposals(context.Context, string, string) ([]forge.ChangeProposal, error) {
	return nil, notImplemented("ListOpenChangeProposals")
}

func (d *Driver) ListComments(context.Context, string, string, int) ([]forge.IssueComment, error) {
	return nil, notImplemented("ListComments")
}

func (d *Driver) ListIssueReactions(context.Context, string, string, int) ([]forge.Reaction, error) {
	return nil, notImplemented("ListIssueReactions")
}

func (d *Driver) CreateRepo(context.Context, string, string, string) error {
	return notImplemented("CreateRepo")
}

func (d *Driver) EnsureRepoPublic(context.Context, string, string) error {
	return notImplemented("EnsureRepoPublic")
}

func (d *Driver) GetDefaultBranch(context.Context, string, string) (string, error) {
	return "", notImplemented("GetDefaultBranch")
}

func (d *Driver) GetBranchRef(context.Context, string, string, string) (string, error) {
	return "", notImplemented("GetBranchRef")
}

func (d *Driver) DeleteRepo(context.Context, string, string) error {
	return notImplemented("DeleteRepo")
}

func (d *Driver) CreateFork(context.Context, string, string, string) (string, error) {
	return "", notImplemented("CreateFork")
}

func (d *Driver) CreateUserFork(context.Context, string, string) (string, string, error) {
	return "", "", notImplemented("CreateUserFork")
}

func (d *Driver) CommitFileToFork(context.Context, string, string, string, string, string, []byte) error {
	return notImplemented("CommitFileToFork")
}

func (d *Driver) CreateForkChangeProposal(context.Context, string, string, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return nil, notImplemented("CreateForkChangeProposal")
}

func (d *Driver) ListPullRequestReviews(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
	return nil, notImplemented("ListPullRequestReviews")
}

func (d *Driver) ListPullRequestCommits(context.Context, string, string, int) ([]string, error) {
	return nil, notImplemented("ListPullRequestCommits")
}
