package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// LiveClient is an HTTP client for the Jira Cloud REST API v3.
type LiveClient struct {
	httpClient *http.Client
	baseURL    string
	email      string // for Basic auth (Cloud)
	token      string
}

// Option configures the Jira client.
type Option func(*LiveClient)

// WithBaseURL sets a custom base URL for the Jira instance.
func WithBaseURL(rawURL string) Option {
	return func(c *LiveClient) {
		c.baseURL = strings.TrimRight(rawURL, "/")
	}
}

// WithEmail sets the email address for Basic auth (Jira Cloud).
// When set, the client uses Authorization: Basic base64(email:token).
// When empty, the client uses Authorization: Bearer token (Cloud PAT).
// Note: despite the Bearer-auth option, this client's REST endpoints are
// Cloud-only (see apiURL) — Data Center is not currently supported.
func WithEmail(email string) Option {
	return func(c *LiveClient) {
		c.email = email
	}
}

// WithHTTPClient sets a custom HTTP client for API calls.
func WithHTTPClient(client *http.Client) Option {
	return func(c *LiveClient) {
		c.httpClient = client
	}
}

// ValidateBaseURL checks that the base URL uses https, unless it points to a
// loopback address (for httptest servers), and rejects embedded credentials.
// Exported so other Jira-backed clients (e.g. tracker.JiraClient) can apply
// the same policy to base URLs they accept.
func ValidateBaseURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}
	// Credentials in the URL (https://user:token@host) would be stored
	// verbatim, propagated into the issue browse URL that lands in every
	// dispatched NormalizedEvent (and the Actions UI via the guide's
	// script), and echoed into the error below. Require them via
	// JIRA_USER_EMAIL/JIRA_TOKEN instead. u.Redacted() masks any password
	// in error text.
	if u.User != nil {
		return fmt.Errorf("base URL %q must not embed credentials; use JIRA_USER_EMAIL and JIRA_TOKEN", u.Redacted())
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return fmt.Errorf("base URL %q uses insecure scheme %q; only https is allowed for non-loopback hosts", u.Redacted(), u.Scheme)
}

// checkRedirect refuses any redirect that leaves the original request's
// origin (scheme+host). This client only talks to a single Jira Cloud
// instance, which never legitimately redirects its REST API off-origin, so
// refusing outright is safe — and it is strictly stronger than stripping
// the Authorization header per hop. Go's client re-copies the *initial*
// request's headers onto every redirect hop and only strips them when the
// hop leaves the initial domain-or-subdomain (host only, ignoring scheme),
// so a per-hop Del relative to the previous hop leaves a gap: a same-host
// https→http downgrade chain, or a subdomain chain, re-attaches the
// Authorization header (Basic email:token) on a later hop. Comparing
// against via[0] and refusing also removes the SSRF surface of following
// redirects to arbitrary internal hosts.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	orig := via[0].URL
	if req.URL.Scheme != orig.Scheme || req.URL.Host != orig.Host {
		return fmt.Errorf("refusing redirect off the Jira origin %q to %q://%q", orig.Host, req.URL.Scheme, req.URL.Host)
	}
	return nil
}

// New creates a new Jira client with the given API token.
func New(token string, opts ...Option) (*LiveClient, error) {
	if token == "" {
		return nil, fmt.Errorf("jira: token must not be empty")
	}
	c := &LiveClient{
		httpClient: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: checkRedirect,
		},
		token: token,
	}
	for _, o := range opts {
		o(c)
	}
	if c.baseURL == "" {
		return nil, fmt.Errorf("jira: base URL must be set via WithBaseURL")
	}
	if err := ValidateBaseURL(c.baseURL); err != nil {
		return nil, err
	}
	return c, nil
}

// APIError represents an error response from the Jira API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("jira api: %d %s", e.StatusCode, e.Message)
}

// IsTransient reports whether the API error represents a transient
// failure that may succeed on retry: server errors (500–504) and
// rate limits (429). This method satisfies the transientReporter
// interface used by forge.IsTransient.
func (e *APIError) IsTransient() bool {
	return e.StatusCode == http.StatusTooManyRequests ||
		(e.StatusCode >= 500 && e.StatusCode <= 504)
}

func (e *APIError) Unwrap() error {
	if e.StatusCode == http.StatusNotFound {
		return forge.ErrNotFound
	}
	if e.StatusCode == http.StatusForbidden {
		return forge.ErrForbidden
	}
	return nil
}

const maxRetries = 5

// apiURL builds a Jira Cloud REST API v3 URL. This client targets Cloud
// only: v3/ADF and the cursor-based search/group endpoints below don't
// exist on Data Center (which speaks /rest/api/2 with startAt/total
// pagination and takes a groupname, not a groupId). Data Center support
// is tracked as future work, not implemented here.
func (c *LiveClient) apiURL(path string) string {
	return c.baseURL + "/rest/api/3" + path
}

func (c *LiveClient) setAuth(req *http.Request) {
	if c.email != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(c.email + ":" + c.token))
		req.Header.Set("Authorization", "Basic "+cred)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// do executes an HTTP request with auth, error handling, and retry with backoff.
func (c *LiveClient) do(ctx context.Context, method, path string, body io.Reader, result any) error {
	reqURL := c.apiURL(path)

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("read request body: %w", err)
		}
	}

	for attempt := range maxRetries {
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		c.setAuth(req)
		req.Header.Set("Accept", "application/json")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if isTransientError(err) && isIdempotent(method, path) && attempt < maxRetries-1 {
				delay := retryDelay(nil, attempt)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			return fmt.Errorf("http %s %s: %w", method, path, err)
		}

		if isRetryable(resp, method, path) {
			_ = resp.Body.Close()
			if attempt == maxRetries-1 {
				return &APIError{
					StatusCode: resp.StatusCode,
					Message:    fmt.Sprintf("retryable error after %d attempts on %s %s", maxRetries, method, path),
				}
			}
			delay := retryDelay(resp, attempt)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		// For responses with no expected body (204, 200/201 with no result target).
		if result == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			return parseErrorResponse(resp)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			return json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(result)
		}

		return parseErrorResponse(resp)
	}

	return fmt.Errorf("exhausted retries for %s %s", method, path)
}

// parseErrorResponse reads a Jira error response and returns an APIError.
func parseErrorResponse(resp *http.Response) error {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var errResp struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(data, &errResp) == nil {
		var parts []string
		parts = append(parts, errResp.ErrorMessages...)
		for k, v := range errResp.Errors {
			parts = append(parts, fmt.Sprintf("%s: %s", k, v))
		}
		if len(parts) > 0 {
			return &APIError{StatusCode: resp.StatusCode, Message: strings.Join(parts, "; ")}
		}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
}

func isRetryable(resp *http.Response, method, path string) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode >= 500 && resp.StatusCode <= 504 && isIdempotent(method, path) {
		return true
	}
	return false
}

func isTransientError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

func isIdempotent(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead ||
		method == http.MethodPut || method == http.MethodDelete {
		return true
	}
	// /search/jql is a read-only JQL search issued as a POST because the
	// query is passed in the request body; it's safe to retry like a GET.
	return method == http.MethodPost && path == "/search/jql"
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	const maxRetryAfterSecs = 300
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				if secs > maxRetryAfterSecs {
					secs = maxRetryAfterSecs
				}
				return time.Duration(secs) * time.Second
			}
		}
	}
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	half := base / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// ---------------------------------------------------------------------------
// API methods
// ---------------------------------------------------------------------------

// searchRequest is the POST body for /rest/api/3/search/jql.
type searchRequest struct {
	JQL           string   `json:"jql"`
	MaxResults    int      `json:"maxResults"`
	Fields        []string `json:"fields"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

// searchFields lists the issue fields the poller actually consumes.
// Requesting them explicitly (rather than "*all") bounds search payloads:
// with "*all", 50 issues per page each carrying full ADF descriptions,
// embedded first comment pages, attachments, and every custom field can
// plausibly exceed the client's 10MB decode cap — and since a failed
// search aborts the whole cycle with the same data returning every time,
// that failure mode would wedge the poller for the entire project.
var searchFields = []string{"summary", "status", "labels", "reporter", "created", "updated"}

// maxSearchPages limits pagination to prevent unbounded memory growth
// from overly broad JQL queries.
const maxSearchPages = 200

// SearchIssues executes a JQL search and returns up to limit matching
// issues, using the POST /rest/api/3/search/jql endpoint with cursor-based
// pagination (nextPageToken + isLast). Pagination stops as soon as limit
// issues have been collected, so a limit smaller than the full match set
// bounds API cost rather than fetching everything and truncating
// afterward. A limit <= 0 fetches all matching issues, capped at
// maxSearchPages pages (10,000 issues at 50 per page) to prevent unbounded
// memory growth.
//
// This endpoint is eventually consistent: Atlassian documents that recent
// updates might not be immediately visible in results. For the poller that
// only means an issue can appear (or re-sort under ORDER BY updated) a
// cycle late — per-issue checkpoints come from the strongly-consistent
// direct comment/changelog GETs, never from search results, so no events
// are lost. Do not "fix" ordering assumptions by trusting search freshness.
func (c *LiveClient) SearchIssues(ctx context.Context, jql string, limit int) ([]Issue, error) {
	var all []Issue
	var nextPageToken string
	for page := 0; page < maxSearchPages; page++ {
		// Changelog is deliberately not expanded here: the response types
		// have nowhere to decode it, and the poller fetches changelog
		// per-selected-issue via ListChangelog, so expanding it for every
		// candidate would only inflate search payloads.
		body := searchRequest{
			JQL:           jql,
			MaxResults:    50,
			Fields:        searchFields,
			NextPageToken: nextPageToken,
		}
		bodyJSON, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal search request: %w", err)
		}
		var result SearchResult
		if err := c.do(ctx, http.MethodPost, "/search/jql", bytes.NewReader(bodyJSON), &result); err != nil {
			return nil, fmt.Errorf("search issues: %w", err)
		}
		all = append(all, result.Issues...)
		if limit > 0 && len(all) >= limit {
			all = all[:limit]
			break
		}
		if result.IsLast || len(result.Issues) == 0 || result.NextPageToken == "" {
			break
		}
		nextPageToken = result.NextPageToken
	}
	return all, nil
}

// GetIssue fetches a single issue by ID or key.
func (c *LiveClient) GetIssue(ctx context.Context, issueIDOrKey string) (*Issue, error) {
	var issue Issue
	path := "/issue/" + url.PathEscape(issueIDOrKey)
	if err := c.do(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, fmt.Errorf("get issue %s: %w", issueIDOrKey, err)
	}
	return &issue, nil
}

// GetStatus fetches a single status by ID or name, including its
// statusCategory. Used to classify changelog status transitions by category
// rather than by matching locale/workflow-specific status name substrings.
func (c *LiveClient) GetStatus(ctx context.Context, idOrName string) (*Status, error) {
	var status Status
	path := "/status/" + url.PathEscape(idOrName)
	if err := c.do(ctx, http.MethodGet, path, nil, &status); err != nil {
		return nil, fmt.Errorf("get status %s: %w", idOrName, err)
	}
	return &status, nil
}

// maxListPages limits per-issue and per-group pagination (comments,
// changelog, group members) to prevent unbounded memory growth from
// issues or groups with very large histories, mirroring maxSearchPages.
// Comments and changelog fetch 100 items per page (10,000-entry cap);
// group members fetch 50 per page — that endpoint's documented maximum —
// for a 5,000-member cap.
//
// These endpoints paginate oldest-first, so hitting the cap hides the
// NEWEST items — the ones the poller cares about — which is why the cap
// is logged loudly when reached rather than silently truncating.
const maxListPages = 100

// ListComments fetches all comments on an issue, exhausting pagination up
// to maxListPages pages. Comments include entity properties via
// ?expand=properties so callers can match sticky markers stored as
// comment properties instead of scanning body text.
func (c *LiveClient) ListComments(ctx context.Context, issueIDOrKey string) ([]Comment, error) {
	var all []Comment
	startAt := 0
	truncated := true
	for page := 0; page < maxListPages; page++ {
		path := fmt.Sprintf("/issue/%s/comment?orderBy=created&maxResults=100&startAt=%d&expand=properties",
			url.PathEscape(issueIDOrKey), startAt)
		var result CommentPage
		if err := c.do(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, fmt.Errorf("list comments for %s (startAt=%d): %w", issueIDOrKey, startAt, err)
		}
		all = append(all, result.Comments...)
		if startAt+len(result.Comments) >= result.Total || len(result.Comments) == 0 {
			truncated = false
			break
		}
		startAt += len(result.Comments)
	}
	if truncated {
		log.Printf("WARNING: comment listing for %s truncated at %d pages; newer comments beyond the cap are invisible to the poller", issueIDOrKey, maxListPages)
	}
	return all, nil
}

// commentRequest is the request body shape for creating or updating a
// comment: Jira Cloud only accepts a comment body in ADF, not markdown.
type commentRequest struct {
	Body       map[string]any    `json:"body"`
	Properties []CommentProperty `json:"properties,omitempty"`
}

// CreateComment adds a new comment to an issue. body is markdown; it's
// converted to ADF before being sent, since Jira Cloud doesn't accept
// markdown directly.
func (c *LiveClient) CreateComment(ctx context.Context, issueIDOrKey, body string) (*Comment, error) {
	return c.CreateCommentWithProperties(ctx, issueIDOrKey, body, nil)
}

// CreateCommentWithProperties adds a new comment to an issue with
// optional entity properties. body is markdown; it's converted to ADF
// before being sent, since Jira Cloud doesn't accept markdown directly.
// Properties are stored alongside the comment and are invisible in the
// Jira UI; they are used for sticky-comment marker storage.
func (c *LiveClient) CreateCommentWithProperties(ctx context.Context, issueIDOrKey, body string, properties []CommentProperty) (*Comment, error) {
	adf, err := MarkdownToADF(body)
	if err != nil {
		return nil, fmt.Errorf("convert comment body to ADF: %w", err)
	}
	req := commentRequest{Body: adf, Properties: properties}
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal create comment request: %w", err)
	}
	var comment Comment
	path := "/issue/" + url.PathEscape(issueIDOrKey) + "/comment"
	if err := c.do(ctx, http.MethodPost, path, bytes.NewReader(reqBody), &comment); err != nil {
		return nil, fmt.Errorf("create comment on %s: %w", issueIDOrKey, err)
	}
	return &comment, nil
}

// UpdateComment replaces the body of an existing comment. body is
// markdown; it's converted to ADF before being sent, for the same reason
// as CreateComment.
func (c *LiveClient) UpdateComment(ctx context.Context, issueIDOrKey, commentID, body string) error {
	adf, err := MarkdownToADF(body)
	if err != nil {
		return fmt.Errorf("convert comment body to ADF: %w", err)
	}
	reqBody, err := json.Marshal(commentRequest{Body: adf})
	if err != nil {
		return fmt.Errorf("marshal update comment request: %w", err)
	}
	path := "/issue/" + url.PathEscape(issueIDOrKey) + "/comment/" + url.PathEscape(commentID)
	if err := c.do(ctx, http.MethodPut, path, bytes.NewReader(reqBody), nil); err != nil {
		return fmt.Errorf("update comment %s on %s: %w", commentID, issueIDOrKey, err)
	}
	return nil
}

// GetCommentProperty retrieves the value of an entity property on a
// comment. Returns forge.ErrNotFound (wrapped) if the property does not
// exist.
func (c *LiveClient) GetCommentProperty(ctx context.Context, issueIDOrKey, commentID, propertyKey string) (json.RawMessage, error) {
	path := fmt.Sprintf("/comment/%s/properties/%s",
		url.PathEscape(commentID), url.PathEscape(propertyKey))
	var prop EntityPropertyValue
	if err := c.do(ctx, http.MethodGet, path, nil, &prop); err != nil {
		return nil, fmt.Errorf("get comment property %s on comment %s of %s: %w", propertyKey, commentID, issueIDOrKey, err)
	}
	return prop.Value, nil
}

// SetCommentProperty sets (creates or updates) an entity property on a
// comment. Used to store sticky-comment markers in a location invisible
// to Jira users.
func (c *LiveClient) SetCommentProperty(ctx context.Context, issueIDOrKey, commentID, propertyKey string, value any) error {
	path := fmt.Sprintf("/comment/%s/properties/%s",
		url.PathEscape(commentID), url.PathEscape(propertyKey))
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal comment property value: %w", err)
	}
	if err := c.do(ctx, http.MethodPut, path, bytes.NewReader(body), nil); err != nil {
		return fmt.Errorf("set comment property %s on comment %s of %s: %w", propertyKey, commentID, issueIDOrKey, err)
	}
	return nil
}

// DeleteComment removes a comment by ID from the given issue.
func (c *LiveClient) DeleteComment(ctx context.Context, issueIDOrKey, commentID string) error {
	path := "/issue/" + url.PathEscape(issueIDOrKey) + "/comment/" + url.PathEscape(commentID)
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("delete comment %s on %s: %w", commentID, issueIDOrKey, err)
	}
	return nil
}

// ListChangelog fetches all changelog entries for an issue, exhausting
// pagination up to maxListPages pages.
func (c *LiveClient) ListChangelog(ctx context.Context, issueIDOrKey string) ([]ChangelogEntry, error) {
	var all []ChangelogEntry
	startAt := 0
	truncated := true
	for page := 0; page < maxListPages; page++ {
		path := fmt.Sprintf("/issue/%s/changelog?maxResults=100&startAt=%d",
			url.PathEscape(issueIDOrKey), startAt)
		var result changelogPage
		if err := c.do(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, fmt.Errorf("list changelog for %s (startAt=%d): %w", issueIDOrKey, startAt, err)
		}
		all = append(all, result.Values...)
		if result.IsLast || len(result.Values) == 0 {
			truncated = false
			break
		}
		startAt += len(result.Values)
	}
	if truncated {
		log.Printf("WARNING: changelog listing for %s truncated at %d pages; newer entries beyond the cap are invisible to the poller", issueIDOrKey, maxListPages)
	}
	return all, nil
}

// GetEntityProperty retrieves the value of an entity property on an issue.
// Returns forge.ErrNotFound (wrapped) if the property does not exist.
func (c *LiveClient) GetEntityProperty(ctx context.Context, issueIDOrKey, propertyKey string) (json.RawMessage, error) {
	path := fmt.Sprintf("/issue/%s/properties/%s",
		url.PathEscape(issueIDOrKey), url.PathEscape(propertyKey))
	var prop EntityPropertyValue
	if err := c.do(ctx, http.MethodGet, path, nil, &prop); err != nil {
		return nil, fmt.Errorf("get entity property %s on %s: %w", propertyKey, issueIDOrKey, err)
	}
	return prop.Value, nil
}

// SetEntityProperty sets (creates or updates) an entity property on an issue.
func (c *LiveClient) SetEntityProperty(ctx context.Context, issueIDOrKey, propertyKey string, value any) error {
	path := fmt.Sprintf("/issue/%s/properties/%s",
		url.PathEscape(issueIDOrKey), url.PathEscape(propertyKey))
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal entity property value: %w", err)
	}
	if err := c.do(ctx, http.MethodPut, path, bytes.NewReader(body), nil); err != nil {
		return fmt.Errorf("set entity property %s on %s: %w", propertyKey, issueIDOrKey, err)
	}
	return nil
}

// DeleteEntityProperty removes an entity property from an issue.
func (c *LiveClient) DeleteEntityProperty(ctx context.Context, issueIDOrKey, propertyKey string) error {
	path := fmt.Sprintf("/issue/%s/properties/%s",
		url.PathEscape(issueIDOrKey), url.PathEscape(propertyKey))
	if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("delete entity property %s on %s: %w", propertyKey, issueIDOrKey, err)
	}
	return nil
}

// RolePriority returns the priority of a Jira project role name. Higher
// values take precedence when a user appears in multiple roles.
//
// KNOWN LIMITATION (intentional for the MVP): matches by role name, not by
// the project's permission scheme. See mapJiraRole in internal/jirapoll and
// docs/guides/user/jira-integration.md#actor-role-resolution.
func RolePriority(roleName string) int {
	switch strings.ToLower(roleName) {
	case "administrators":
		return 2
	case "developers":
		return 1
	default:
		return 0
	}
}

// GetProjectRoleActors returns the direct users and group assignments
// for each project role without enumerating group members. This avoids
// the 100-page pagination cap on the group/member endpoint that used to
// truncate large groups. Callers combine the returned structure with
// GetUserGroups for per-actor resolution.
func (c *LiveClient) GetProjectRoleActors(ctx context.Context, projectKey string) (map[string]ProjectRoleActors, error) {
	path := fmt.Sprintf("/project/%s/role", url.PathEscape(projectKey))
	var roleList ProjectRoleList
	if err := c.do(ctx, http.MethodGet, path, nil, &roleList); err != nil {
		return nil, fmt.Errorf("list project roles for %s: %w", projectKey, err)
	}

	result := make(map[string]ProjectRoleActors)
	for roleName, roleURL := range roleList {
		idx := strings.LastIndex(roleURL, "/")
		if idx < 0 || idx == len(roleURL)-1 {
			log.Printf("WARNING: role %s for project %s has an unparseable URL %q; skipping (its actors will resolve to external)", roleName, projectKey, roleURL)
			continue
		}
		roleID := roleURL[idx+1:]

		detailPath := fmt.Sprintf("/project/%s/role/%s",
			url.PathEscape(projectKey), url.PathEscape(roleID))
		var detail ProjectRoleDetail
		if err := c.do(ctx, http.MethodGet, detailPath, nil, &detail); err != nil {
			return nil, fmt.Errorf("get project role %s (id %s): %w", roleName, roleID, err)
		}

		actors := ProjectRoleActors{DirectUsers: make(map[string]bool)}
		for _, actor := range detail.Actors {
			if actor.ActorUser != nil && actor.ActorUser.AccountID != "" {
				actors.DirectUsers[actor.ActorUser.AccountID] = true
			}
			if actor.Type == "atlassian-group-role-actor" && actor.ActorGroup != nil && actor.ActorGroup.GroupID != "" {
				actors.GroupIDs = append(actors.GroupIDs, actor.ActorGroup.GroupID)
			}
		}
		result[roleName] = actors
	}

	return result, nil
}

// maxExpectedUserGroups is a sanity bound on GetUserGroups's response, not
// a real Jira limit: per Atlassian's REST v3 reference
// (https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-users/#api-rest-api-3-user-groups-get),
// GET /user/groups returns the user's full group list unpaginated. If
// that assumption is ever wrong for some account, silently truncating
// here would resolve that actor's role incorrectly with no signal, so a
// suspiciously large response logs a WARNING instead of failing quietly.
const maxExpectedUserGroups = 200

// GetUserGroups returns the groups that the specified user belongs to,
// for per-actor role resolution that is not subject to the group/member
// pagination cap (see GetProjectRoleActors).
func (c *LiveClient) GetUserGroups(ctx context.Context, accountID string) ([]UserGroupInfo, error) {
	path := fmt.Sprintf("/user/groups?accountId=%s", url.QueryEscape(accountID))
	var groups []UserGroupInfo
	if err := c.do(ctx, http.MethodGet, path, nil, &groups); err != nil {
		return nil, fmt.Errorf("get user groups for %s: %w", accountID, err)
	}
	if len(groups) > maxExpectedUserGroups {
		log.Printf("WARNING: user %s belongs to %d groups (>%d expected); if this endpoint paginates after all, some groups may be missing from role resolution", accountID, len(groups), maxExpectedUserGroups)
	}
	return groups, nil
}

// GetMyself returns the currently authenticated user.
func (c *LiveClient) GetMyself(ctx context.Context) (*User, error) {
	var user User
	if err := c.do(ctx, http.MethodGet, "/myself", nil, &user); err != nil {
		return nil, fmt.Errorf("get myself: %w", err)
	}
	return &user, nil
}
