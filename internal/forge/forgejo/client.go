// Package forgejo is the Forgejo API adapter. It implements the
// repository-content operations that the repos lifecycle needs for
// repositories declared under forgejo.instances in repos.yaml, using the
// Forgejo v1 REST API (Forgejo 17 and later).
//
// File writes are conditional on the SHA of the revision the caller read,
// so a file replaced between the read and the write causes a conflict rather
// than an overwrite. It also reads Forgejo Actions runs, jobs, artifacts,
// and logs (actions.go) for the behaviour-test CI driver, reads and
// updates issues, issue comments, and labels (issues.go) for the triage
// and polling flows, implements the branch, pull-request, review,
// conversation-comment, and commit-status operations of the code, review,
// and fix flows (pulls.go), and reads git commits, trees, and blobs by
// object ID (git.go) for agent definitions pinned to a commit. Operations
// Forgejo support does not yet cover are not part of this adapter.
package forgejo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// maxResponseBytes bounds every API response body the client reads.
const maxResponseBytes = 10 << 20

// LiveClient is a Forgejo API client for one instance. It is safe for
// concurrent use.
type LiveClient struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// New returns a client for the Forgejo instance at baseURL. baseURL may
// include a path prefix (https://example.com/forgejo), which is preserved
// in every API request. token is sent as the API credential; it is never
// included in returned errors.
func New(baseURL, token string, httpClient *http.Client) (*LiveClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing forgejo base URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("forgejo base URL must use https or http, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("forgejo base URL has no host")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	// Every request carries the API token, so no redirect is followed:
	// Go would keep the Authorization header for the same host (including
	// an https-to-http downgrade) and a redirect could point an
	// authenticated request at a loopback or private service. The policy
	// is applied to a copy, so a supplied client keeps its transport and
	// timeout but cannot opt back in to following redirects.
	guarded := *httpClient
	guarded.CheckRedirect = refuseRedirect
	return &LiveClient{baseURL: u, token: token, httpClient: &guarded}, nil
}

// refuseRedirect rejects every redirect of an authenticated request.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("refusing to follow redirect to %s: authenticated Forgejo requests are never redirected", redactURL(req.URL))
}

// contentsResponse is the subset of Forgejo's ContentsResponse the client
// reads.
type contentsResponse struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	SHA      string `json:"sha"`
}

// contentsURL returns the API URL of path in owner/repo. Each segment is
// escaped so a repository path cannot address another API route.
func (c *LiveClient) contentsURL(owner, repo, filePath string) string {
	segs := strings.Split(strings.Trim(filePath, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.baseURL.String() + "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
		"/contents/" + strings.Join(segs, "/")
}

// do sends an API request and decodes a JSON response into out when out
// is non-nil. A 404 maps to forge.ErrNotFound.
func (c *LiveClient) do(ctx context.Context, method, rawURL string, body, out any) error {
	data, err := c.send(ctx, method, rawURL, body, "application/json", maxResponseBytes, false)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decoding response: %w", method, redactedString(rawURL), err)
		}
	}
	return nil
}

// raw sends a GET request and returns the response body, accepting
// content type accept. A body larger than limit is an error rather than
// a silently truncated result. A 404 maps to forge.ErrNotFound.
func (c *LiveClient) raw(ctx context.Context, rawURL, accept string, limit int64) ([]byte, error) {
	return c.send(ctx, http.MethodGet, rawURL, nil, accept, limit, true)
}

// redactedString redacts rawURL for an error message, or returns a
// placeholder when it does not parse.
func redactedString(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "(unparsable URL)"
	}
	return redactURL(u)
}

// rejectDotSegments returns an error when the path of u has a "." or ".."
// segment, escaped or not. url.PathEscape leaves such a segment unchanged,
// so an owner, repository, or branch name made of one would otherwise let a
// proxy that normalizes dot segments send the request to another API route.
// A segment is also checked after decoding, split on "/", so an encoded
// slash ("..%2Fother") cannot hide a dot segment from a proxy that decodes
// it before normalizing. No request is made for such a URL.
func rejectDotSegments(u *url.URL) error {
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		if dec, err := url.PathUnescape(seg); err == nil {
			seg = dec
		}
		for _, part := range strings.Split(seg, "/") {
			if part == "." || part == ".." {
				return fmt.Errorf("refusing request: URL path has a %q segment", part)
			}
		}
	}
	return nil
}

// send sends an API request and returns the response body, read up to
// limit bytes. When strict is set, a body over limit is an error. A 404
// maps to forge.ErrNotFound and any other non-2xx status to *APIError.
func (c *LiveClient) send(ctx context.Context, method, rawURL string, body any, accept string, limit int64, strict bool) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if err := rejectDotSegments(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, redactURL(req.URL), err)
	}
	defer resp.Body.Close()
	readLimit := limit
	if strict {
		readLimit = limit + 1
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading response: %w", method, redactURL(req.URL), err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s %s: %w", method, redactURL(req.URL), forge.ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Method: method, URL: redactURL(req.URL), StatusCode: resp.StatusCode, Message: apiMessage(data)}
	}
	if strict && int64(len(data)) > limit {
		return nil, fmt.Errorf("%s %s: response exceeds %d bytes", method, redactURL(req.URL), limit)
	}
	return data, nil
}

// APIError is a non-2xx, non-404 Forgejo API response.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Message    string
}

// IsTransient reports whether the response is worth retrying: rate limiting
// or a temporary server failure. It satisfies the interface forge.IsTransient
// checks for.
func (e *APIError) IsTransient() bool {
	return e.StatusCode == http.StatusTooManyRequests || (e.StatusCode >= 500 && e.StatusCode <= 504)
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: Forgejo API returned HTTP %d", e.Method, e.URL, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: Forgejo API returned HTTP %d: %s", e.Method, e.URL, e.StatusCode, e.Message)
}

// apiMessage extracts Forgejo's error message. The text is server-controlled,
// so it is bounded and sanitized where the error is built: control characters
// (newlines, ANSI escapes) are replaced and the workflow-command delimiter is
// broken up, so a malicious or compromised server cannot inject terminal
// sequences or GitHub Actions workflow commands into CLI output.
func apiMessage(data []byte) string {
	var body struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(data, &body) == nil {
		msg = body.Message
	}
	return sanitizeMessage(msg)
}

// sanitizeMessage bounds server-controlled text and neutralizes control
// characters and workflow-command delimiters before it reaches CLI output.
func sanitizeMessage(msg string) string {
	truncated := false
	if len(msg) > 300 {
		msg, truncated = strings.ToValidUTF8(msg[:300], ""), true
	}
	msg = strings.Map(func(r rune) rune {
		if r == ' ' || unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, msg)
	// A single pass leaves "::" behind in runs of three or more colons
	// (":::" becomes ": ::"), so repeat until none remain.
	for strings.Contains(msg, "::") {
		msg = strings.ReplaceAll(msg, "::", ": :")
	}
	if truncated {
		msg += "..."
	}
	return msg
}

// redactURL drops user info and query parameters from u for error messages.
func redactURL(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery = ""
	return c.String()
}

// getContents reads path on the default branch.
func (c *LiveClient) getContents(ctx context.Context, owner, repo, filePath string) (*contentsResponse, error) {
	var out contentsResponse
	if err := c.do(ctx, http.MethodGet, c.contentsURL(owner, repo, filePath), nil, &out); err != nil {
		return nil, err
	}
	if out.Type != "file" {
		return nil, fmt.Errorf("%q in %q/%q is a %q, not a file", filePath, owner, repo, out.Type)
	}
	return &out, nil
}

// File is a file's content and the SHA of the revision that holds it.
type File struct {
	Content []byte
	SHA     string
}

// GetFile returns the content of path on the default branch with the SHA of
// that revision, or an error wrapping forge.ErrNotFound when it does not
// exist. Pass the SHA to UpdateFile or DeleteFile to make the write
// conditional on the revision that was read.
func (c *LiveClient) GetFile(ctx context.Context, owner, repo, filePath string) (*File, error) {
	out, err := c.getContents(ctx, owner, repo, filePath)
	if err != nil {
		return nil, err
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("%q in %q/%q: unsupported content encoding %q", filePath, owner, repo, out.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("%q in %q/%q: decoding content: %w", filePath, owner, repo, err)
	}
	return &File{Content: data, SHA: out.SHA}, nil
}

// CreateFile creates path on the default branch. It fails when the file
// already exists.
func (c *LiveClient) CreateFile(ctx context.Context, owner, repo, filePath, message string, content []byte) error {
	body := map[string]string{
		"content": base64.StdEncoding.EncodeToString(content),
		"message": message,
	}
	return c.do(ctx, http.MethodPost, c.contentsURL(owner, repo, filePath), body, nil)
}

// UpdateFile replaces path on the default branch. sha is the revision the
// caller read; Forgejo rejects the write when the file has changed since.
func (c *LiveClient) UpdateFile(ctx context.Context, owner, repo, filePath, message string, content []byte, sha string) error {
	body := map[string]string{
		"content": base64.StdEncoding.EncodeToString(content),
		"message": message,
		"sha":     sha,
	}
	return c.do(ctx, http.MethodPut, c.contentsURL(owner, repo, filePath), body, nil)
}

// DeleteFile removes path from the default branch. sha is the revision the
// caller read; Forgejo rejects the delete when the file has changed since.
// It returns an error wrapping forge.ErrNotFound when the file does not
// exist.
func (c *LiveClient) DeleteFile(ctx context.Context, owner, repo, filePath, message, sha string) error {
	body := map[string]string{"message": message, "sha": sha}
	return c.do(ctx, http.MethodDelete, c.contentsURL(owner, repo, filePath), body, nil)
}

// CurrentUser returns the login of the user the client's token
// authenticates. A rejected token yields an *APIError with HTTP 401.
func (c *LiveClient) CurrentUser(ctx context.Context) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, c.baseURL.String()+"/api/v1/user", nil, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

// repoURL returns the API URL of owner/repo followed by suffix.
func (c *LiveClient) repoURL(owner, repo, suffix string) string {
	return c.baseURL.String() + "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + suffix
}

// GetFileAt returns the content of path at ref (a commit SHA, tag, or
// branch), or an error wrapping forge.ErrNotFound when it does not exist
// there.
func (c *LiveClient) GetFileAt(ctx context.Context, owner, repo, filePath, ref string) (*File, error) {
	var out contentsResponse
	if err := c.do(ctx, http.MethodGet, c.contentsURL(owner, repo, filePath)+"?ref="+url.QueryEscape(ref), nil, &out); err != nil {
		return nil, err
	}
	if out.Type != "file" {
		return nil, fmt.Errorf("%q in %q/%q at %q is a %q, not a file", filePath, owner, repo, ref, out.Type)
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("%q in %q/%q: unsupported content encoding %q", filePath, owner, repo, out.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("%q in %q/%q: decoding content: %w", filePath, owner, repo, err)
	}
	return &File{Content: data, SHA: out.SHA}, nil
}

// File change operations accepted by ChangeFiles.
const (
	FileCreate = "create"
	FileUpdate = "update"
	FileDelete = "delete"
)

// FileChange is one file operation of a ChangeFiles commit. SHA is the
// revision the caller read and is required for update and delete.
type FileChange struct {
	Operation string
	Path      string
	Content   []byte
	SHA       string
}

// ChangeFiles applies every change to the default branch in a single
// commit and returns the commit SHA. Forgejo rejects the whole commit when
// a created file already exists or an updated or deleted file no longer
// matches its SHA, so nothing is partially written.
func (c *LiveClient) ChangeFiles(ctx context.Context, owner, repo, message string, changes []FileChange) (string, error) {
	if len(changes) == 0 {
		return "", errors.New("no file changes to commit")
	}
	type fileOp struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Content   string `json:"content,omitempty"`
		SHA       string `json:"sha,omitempty"`
	}
	ops := make([]fileOp, 0, len(changes))
	for _, ch := range changes {
		switch ch.Operation {
		case FileCreate, FileUpdate, FileDelete:
		default:
			return "", fmt.Errorf("unsupported file operation %q for %q", ch.Operation, ch.Path)
		}
		op := fileOp{Operation: ch.Operation, Path: ch.Path, SHA: ch.SHA}
		if ch.Operation != FileDelete {
			op.Content = base64.StdEncoding.EncodeToString(ch.Content)
		}
		ops = append(ops, op)
	}
	body := map[string]any{"message": message, "files": ops}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/contents"), body, &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", fmt.Errorf("changing files in %q/%q: response has no commit SHA", owner, repo)
	}
	return out.Commit.SHA, nil
}

// tagResponse is the subset of Forgejo's Tag the client reads.
type tagResponse struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// GetTag returns the commit SHA tag points at, or an error wrapping
// forge.ErrNotFound when the tag does not exist.
func (c *LiveClient) GetTag(ctx context.Context, owner, repo, tag string) (string, error) {
	var out tagResponse
	if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/tags/"+url.PathEscape(tag)), nil, &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", fmt.Errorf("tag %q in %q/%q has no commit SHA", tag, owner, repo)
	}
	return out.Commit.SHA, nil
}

// CreateTag creates an annotated tag at target (a commit SHA or branch)
// and returns the commit SHA it points at. It fails when the tag exists.
func (c *LiveClient) CreateTag(ctx context.Context, owner, repo, tag, target, message string) (string, error) {
	body := map[string]string{"tag_name": tag, "target": target, "message": message}
	var out tagResponse
	if err := c.do(ctx, http.MethodPost, c.repoURL(owner, repo, "/tags"), body, &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", fmt.Errorf("created tag %q in %q/%q has no commit SHA", tag, owner, repo)
	}
	return out.Commit.SHA, nil
}
