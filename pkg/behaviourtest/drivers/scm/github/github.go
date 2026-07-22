package github

import (
	"context"
	"fmt"

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

func (d *Driver) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	return d.Client.DeleteRef(ctx, owner, repo, "heads/"+branch)
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

func (d *Driver) CreateRepo(ctx context.Context, org, name, description string) error {
	_, err := d.Client.CreateRepo(ctx, org, name, description, false)
	if err != nil && forge.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func (d *Driver) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, err := d.Client.GetRepo(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("getting default branch: %w", err)
	}
	return r.DefaultBranch, nil
}

func (d *Driver) EnsureRepoPublic(ctx context.Context, owner, repo string) error {
	r, err := d.Client.GetRepo(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("checking repo visibility: %w", err)
	}
	if !r.Private {
		return nil
	}
	// Org may force repos private despite CreateRepo(private=false).
	// Attempt to update visibility.
	if err := d.Client.UpdateRepoVisibility(ctx, owner, repo, false); err != nil {
		return fmt.Errorf("repo %s/%s is private despite requesting public; "+
			"failed to update visibility (org policy may prevent public repos): %w",
			owner, repo, err)
	}
	// Re-verify after update.
	r, err = d.Client.GetRepo(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("re-checking repo visibility after update: %w", err)
	}
	if r.Private {
		return fmt.Errorf("repo %s/%s is still private after visibility update; "+
			"the org may enforce private-only repos", owner, repo)
	}
	return nil
}

func (d *Driver) DeleteRepo(ctx context.Context, owner, repo string) error {
	return d.Client.DeleteRepo(ctx, owner, repo)
}

func (d *Driver) CreateFork(ctx context.Context, owner, repo, forkName string) (string, error) {
	return d.Client.CreateForkInOrg(ctx, owner, repo, owner, forkName)
}

func (d *Driver) CommitFileToFork(ctx context.Context, forkOwner, forkRepo, branch, path, message string, content []byte) error {
	return d.Client.CreateOrUpdateFileOnBranch(ctx, forkOwner, forkRepo, branch, path, message, content)
}

func (d *Driver) CreateForkChangeProposal(ctx context.Context, baseOwner, baseRepo, title, body, forkOwner, forkRepo, head, base string) (*forge.ChangeProposal, error) {
	if forkOwner == baseOwner {
		// Same-owner fork: the REST API's "owner:branch" head format is
		// ambiguous because the owner is the same for both repos. Use the
		// cross-repo method which explicitly identifies the head repository.
		return d.Client.CreateCrossRepoChangeProposal(ctx, baseOwner, baseRepo, forkOwner, forkRepo, title, body, head, base)
	}
	// Cross-owner fork: the standard "forkOwner:branch" format works.
	headRef := forkOwner + ":" + head
	return d.Client.CreateChangeProposal(ctx, baseOwner, baseRepo, title, body, headRef, base)
}

// ParseRepo splits "owner/repo" into owner and repo name.
func ParseRepo(fullName string) (owner, repo string, err error) {
	return scm.ParseRepo(fullName)
}
