package github

import (
	"context"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
)

// Driver implements scm.Driver using forge.Client.
type Driver struct {
	Client forge.Client
}

func New(client forge.Client) scm.Driver {
	return &Driver{Client: client}
}

func (d *Driver) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	return d.Client.CreateIssue(ctx, owner, repo, title, body, labels...)
}

func (d *Driver) AddIssueLabels(ctx context.Context, owner, repo string, number int, labels ...string) error {
	return d.Client.AddIssueLabels(ctx, owner, repo, number, labels...)
}

func (d *Driver) AddComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error) {
	return d.Client.CreateIssueComment(ctx, owner, repo, number, body)
}

func (d *Driver) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	return d.Client.CloseIssue(ctx, owner, repo, number)
}

func (d *Driver) CommitFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	_, err := d.Client.CommitFiles(ctx, owner, repo, message, []forge.TreeFile{{
		Path:    path,
		Content: content,
		Mode:    "100644",
	}})
	return err
}

func (d *Driver) GetIssue(ctx context.Context, owner, repo string, number int) (*forge.Issue, error) {
	return d.Client.GetIssue(ctx, owner, repo, number)
}

func (d *Driver) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	return d.Client.GetFileContent(ctx, owner, repo, path)
}

func (d *Driver) CreateBranch(ctx context.Context, owner, repo, branch string) error {
	return d.Client.CreateBranch(ctx, owner, repo, branch)
}

func (d *Driver) CommitFileToBranch(ctx context.Context, owner, repo, branch, path, message string, content []byte) error {
	return d.Client.CreateOrUpdateFileOnBranch(ctx, owner, repo, branch, path, message, content)
}

func (d *Driver) CreateChangeProposal(ctx context.Context, owner, repo, title, body, head, base string) (*forge.ChangeProposal, error) {
	return d.Client.CreateChangeProposal(ctx, owner, repo, title, body, head, base)
}

func (d *Driver) SubmitPullRequestReview(ctx context.Context, owner, repo string, number int, event string) error {
	sha, err := d.Client.GetPullRequestHeadSHA(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	return d.Client.CreatePullRequestReview(ctx, owner, repo, number, event, "behaviour test review", sha, nil)
}

func (d *Driver) CreateFork(ctx context.Context, owner, repo string) (string, string, error) {
	// Idempotent: reuse existing fork if one already exists.
	forkOwner, forkRepo, err := d.Client.FindExistingFork(ctx, owner, repo)
	if err != nil {
		return "", "", err
	}
	if forkOwner != "" {
		return forkOwner, forkRepo, nil
	}
	return d.Client.CreateFork(ctx, owner, repo)
}

func (d *Driver) CommitFileToFork(ctx context.Context, forkOwner, forkRepo, branch, path, message string, content []byte) error {
	return d.Client.CreateOrUpdateFileOnBranch(ctx, forkOwner, forkRepo, branch, path, message, content)
}

func (d *Driver) CreateForkChangeProposal(ctx context.Context, baseOwner, baseRepo, title, body, forkOwner, headBranch, baseBranch string) (*forge.ChangeProposal, error) {
	head := forkOwner + ":" + headBranch
	return d.Client.CreateChangeProposal(ctx, baseOwner, baseRepo, title, body, head, baseBranch)
}

// ParseRepo splits "owner/repo" into owner and repo name.
func ParseRepo(fullName string) (owner, repo string, err error) {
	return scm.ParseRepo(fullName)
}
