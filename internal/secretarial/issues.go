package secretarial

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// IssueClient wraps the gh CLI for issue operations.
// It uses only the workflow GITHUB_TOKEN — no PATs, no app tokens.
type IssueClient struct {
	repo  string
	token string
}

// NewIssueClient returns a client scoped to repo (owner/name).
func NewIssueClient(repo string) *IssueClient {
	return &IssueClient{
		repo:  repo,
		token: os.Getenv("GH_TOKEN"),
	}
}

// Issue is a lightweight representation of a GitHub issue.
type Issue struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Labels []Label `json:"labels"`
}

// Label is a GitHub issue label.
type Label struct {
	Name string `json:"name"`
}

// IssueComment is a comment on a GitHub issue.
type IssueComment struct {
	Body string `json:"body"`
}

// ListOpen returns open issues (up to limit).
func (c *IssueClient) ListOpen(limit int) ([]Issue, error) {
	out, err := c.gh("issue", "list",
		"--repo", c.repo,
		"--state", "open",
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "number,title,labels,body",
	)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing issue list: %w", err)
	}
	return issues, nil
}

// ListComments returns comments on an issue (up to limit).
func (c *IssueClient) ListComments(issueNumber, limit int) ([]IssueComment, error) {
	out, err := c.gh("issue", "view",
		fmt.Sprintf("%d", issueNumber),
		"--repo", c.repo,
		"--json", "comments",
	)
	if err != nil {
		return nil, err
	}
	var result struct {
		Comments []IssueComment `json:"comments"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parsing issue comments: %w", err)
	}
	if limit > 0 && len(result.Comments) > limit {
		return result.Comments[len(result.Comments)-limit:], nil
	}
	return result.Comments, nil
}

// HasCommentContaining checks whether any existing comment on an issue
// contains the given substring. Used for idempotency — if we already posted
// a comment linking to a specific meeting doc, we skip it.
func (c *IssueClient) HasCommentContaining(issueNumber int, substring string) (bool, error) {
	comments, err := c.ListComments(issueNumber, 0)
	if err != nil {
		return false, err
	}
	for _, cm := range comments {
		if strings.Contains(cm.Body, substring) {
			return true, nil
		}
	}
	return false, nil
}

// Comment posts a comment on an existing issue.
// Uses --body-file to avoid newline mangling in shell argument passing.
func (c *IssueClient) Comment(issueNumber int, body string) error {
	return c.withBodyFile(body, func(path string) error {
		_, err := c.gh("issue", "comment",
			fmt.Sprintf("%d", issueNumber),
			"--repo", c.repo,
			"--body-file", path,
		)
		return err
	})
}

// Create files a new issue and returns its URL.
// Uses --body-file to avoid newline mangling in shell argument passing.
// If labels don't exist in the target repo, retries without them rather
// than failing the entire issue creation.
func (c *IssueClient) Create(title, body string, labels []string) (string, error) {
	var out []byte
	err := c.withBodyFile(body, func(path string) error {
		args := []string{"issue", "create",
			"--repo", c.repo,
			"--title", title,
			"--body-file", path,
		}
		if len(labels) > 0 {
			args = append(args, "--label", strings.Join(labels, ","))
		}
		var ghErr error
		out, ghErr = c.gh(args...)
		if ghErr != nil && len(labels) > 0 && strings.Contains(ghErr.Error(), "not found") {
			slog.Warn("labels not found in repo, retrying without labels", "labels", labels)
			argsNoLabels := []string{"issue", "create",
				"--repo", c.repo,
				"--title", title,
				"--body-file", path,
			}
			out, ghErr = c.gh(argsNoLabels...)
		}
		return ghErr
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// withBodyFile writes body to a temp file, calls fn with the file path, then
// cleans up. This sidesteps shell escaping issues with --body for multi-line
// markdown content.
func (c *IssueClient) withBodyFile(body string, fn func(path string) error) error {
	f, err := os.CreateTemp("", "gh-body-*.md")
	if err != nil {
		return fmt.Errorf("creating temp body file: %w", err)
	}
	defer os.Remove(f.Name())

	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return fmt.Errorf("writing body to temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing temp body file: %w", err)
	}

	return fn(f.Name())
}

// FormatIssueContext builds a plain-text summary of open issues for the LLM.
// Includes a truncated body snippet so the LLM can match on description
// content, not just titles.
func FormatIssueContext(issues []Issue) string {
	if len(issues) == 0 {
		return "There are no open issues in the repository."
	}
	var sb strings.Builder
	sb.WriteString("Open issues:\n")
	for _, iss := range issues {
		lbls := make([]string, 0, len(iss.Labels))
		for _, l := range iss.Labels {
			lbls = append(lbls, l.Name)
		}
		fmt.Fprintf(&sb, "#%d — %s", iss.Number, iss.Title)
		if len(lbls) > 0 {
			fmt.Fprintf(&sb, " [%s]", strings.Join(lbls, ", "))
		}
		sb.WriteByte('\n')
		if snippet := issueBodySnippet(iss.Body, 200); snippet != "" {
			fmt.Fprintf(&sb, "  %s\n", snippet)
		}
	}
	return sb.String()
}

// issueBodySnippet returns the first maxLen characters of a cleaned-up issue
// body, collapsing whitespace so it fits on ~one line. Returns "" for empty
// or trivially short bodies.
func issueBodySnippet(body string, maxLen int) string {
	s := strings.TrimSpace(body)
	if len(s) < 20 {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

func (c *IssueClient) gh(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Env = append(os.Environ(), "GH_TOKEN="+c.token)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		sub := strings.Join(args[:min(len(args), 2)], " ")
		stderrSnippet := truncateForLog(stderr.String(), 200)
		if stderrSnippet != "" {
			return nil, fmt.Errorf("gh %s failed: %w: %s", sub, err, stderrSnippet)
		}
		return nil, fmt.Errorf("gh %s failed: %w", sub, err)
	}
	return stdout.Bytes(), nil
}

// truncateForLog returns a truncated string safe for CI logs. It strips
// leading/trailing whitespace and caps at maxLen characters.
func truncateForLog(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
