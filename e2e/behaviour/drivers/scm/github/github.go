package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Driver implements scm.Driver using forge.Client and GitHub REST where needed.
type Driver struct {
	Client forge.Client
	Token  string
}

func New(client forge.Client, token string) scm.Driver {
	return &Driver{Client: client, Token: token}
}

func (d *Driver) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	return d.Client.CreateIssue(ctx, owner, repo, title, body, labels...)
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
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, forge.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get issue returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"html_url"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	labels := make([]string, len(payload.Labels))
	for i, l := range payload.Labels {
		labels[i] = l.Name
	}
	return &forge.Issue{
		Number: payload.Number,
		Title:  payload.Title,
		Body:   payload.Body,
		URL:    payload.URL,
		Labels: labels,
	}, nil
}

// ParseRepo splits "owner/repo" into owner and repo name.
func ParseRepo(fullName string) (owner, repo string, err error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository %q: expected owner/repo", fullName)
	}
	return parts[0], parts[1], nil
}
