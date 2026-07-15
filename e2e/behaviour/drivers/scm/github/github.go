package github

import (
	"context"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm"
	"github.com/fullsend-ai/fullsend/internal/forge"
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

// ParseRepo splits "owner/repo" into owner and repo name.
func ParseRepo(fullName string) (owner, repo string, err error) {
	return scm.ParseRepo(fullName)
}
