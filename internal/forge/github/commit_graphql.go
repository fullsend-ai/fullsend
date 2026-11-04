package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// GitHub signs Git Data REST commits only for App installation tokens.
// createCommitOnBranch commits are signed for any credential (author = the
// authenticated account, committer = GitHub, like the web UI), which is
// what rulesets with required_signatures accept. The mutation cannot set
// author/committer or file modes and sends all contents inline, so
// usesGitDataPath keeps commits that need any of those on the REST path.
// See ADR 0140.

const createCommitOnBranchMutation = `mutation($input: CreateCommitOnBranchInput!) {
  createCommitOnBranch(input: $input) {
    commit { oid }
  }
}`

// graphQLError is one entry of a GraphQL errors array (returned with HTTP
// 200, so never an *APIError).
type graphQLError struct {
	Type    string
	Message string
}

func (e *graphQLError) Error() string {
	return fmt.Sprintf("github graphql: %s: %s", e.Type, strings.TrimSpace(e.Message))
}

// usesGitDataPath reports whether the changed files need the Git Data REST
// API: a file streamed from LocalPath or a mode other than 100644.
// Deletions never force it.
func usesGitDataPath(files []forge.TreeFile) bool {
	for _, f := range files {
		if f.Delete {
			continue
		}
		if f.LocalPath != "" || (f.Mode != "" && f.Mode != "100644") {
			return true
		}
	}
	return false
}

// graphqlEndpoint is <host>/api/graphql on GitHub Enterprise Server (REST
// base ends in /api/v3) and <base>/graphql otherwise.
func (c *LiveClient) graphqlEndpoint() string {
	base := strings.TrimRight(c.baseURL, "/")
	if strings.HasSuffix(base, "/api/v3") {
		return strings.TrimSuffix(base, "/api/v3") + "/api/graphql"
	}
	return base + "/graphql"
}

// splitCommitMessage splits a message into the headline and body of a
// CommitMessage; GitHub rejoins them with a blank line, so trailers
// round-trip.
func splitCommitMessage(message string) (headline, body string) {
	message = strings.TrimRight(message, "\n")
	headline, body, _ = strings.Cut(message, "\n")
	return strings.TrimSpace(headline), strings.TrimLeft(body, "\n")
}

func (c *LiveClient) commitFilesViaGraphQL(ctx context.Context, owner, repo, branch, headSHA, message string, changed []forge.TreeFile) error {
	var additions []forge.TreeFile
	var deletions []string
	for _, f := range changed {
		if f.Delete {
			deletions = append(deletions, f.Path)
		} else {
			additions = append(additions, f)
		}
	}
	_, err := c.createCommitOnBranch(ctx, owner, repo, branch, headSHA, message, additions, deletions)
	return err
}

// createCommitOnBranch runs the mutation and returns the new commit OID.
// Callers filter deletions against the tree first (a missing path aborts
// the commit) and skip unchanged additions (identical content still
// produces a commit).
func (c *LiveClient) createCommitOnBranch(ctx context.Context, owner, repo, branch, expectedHeadOID, message string, additions []forge.TreeFile, deletions []string) (string, error) {
	headline, body := splitCommitMessage(message)
	commitMessage := map[string]any{"headline": headline}
	if body != "" {
		commitMessage["body"] = body
	}

	adds := make([]map[string]string, 0, len(additions))
	for _, f := range additions {
		content, err := f.Bytes()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f.Path, err)
		}
		adds = append(adds, map[string]string{
			"path":     f.Path,
			"contents": base64.StdEncoding.EncodeToString(content),
		})
	}
	dels := make([]map[string]string, 0, len(deletions))
	for _, path := range deletions {
		dels = append(dels, map[string]string{"path": path})
	}

	payload := map[string]any{
		"query": createCommitOnBranchMutation,
		"variables": map[string]any{
			"input": map[string]any{
				"branch": map[string]string{
					"repositoryNameWithOwner": owner + "/" + repo,
					"branchName":              branch,
				},
				"expectedHeadOid": expectedHeadOID,
				"message":         commitMessage,
				"fileChanges": map[string]any{
					"additions": adds,
					"deletions": dels,
				},
			},
		},
	}

	// GitHub reports a GraphQL rate limit with HTTP 200, so doRequest's
	// retry never sees it.
	for attempt := 0; ; attempt++ {
		resp, err := c.post(ctx, c.graphqlEndpoint(), payload)
		if err != nil {
			return "", fmt.Errorf("create commit on branch: %w", wrapForbidden(err))
		}

		var result struct {
			Data struct {
				CreateCommitOnBranch struct {
					Commit struct {
						OID string `json:"oid"`
					} `json:"commit"`
				} `json:"createCommitOnBranch"`
			} `json:"data"`
			Errors []graphQLError `json:"errors"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			return "", fmt.Errorf("decode create commit on branch response: %w", err)
		}
		if len(result.Errors) == 0 {
			oid := result.Data.CreateCommitOnBranch.Commit.OID
			if oid == "" {
				return "", errors.New("create commit on branch: response has no commit oid")
			}
			return oid, nil
		}
		if staleHeadError(result.Errors) != nil {
			// doRequest resends the mutation after a 5xx or timeout, and a
			// resend of a commit GitHub had already applied is stale against
			// that commit: the branch content tells it from a concurrent
			// change.
			oid, err := c.branchHoldsChanges(ctx, owner, repo, branch, additions, deletions)
			if err != nil {
				return "", fmt.Errorf("create commit on branch: %w", err)
			}
			if oid != "" {
				return oid, nil
			}
		}
		if firstGraphQLError(result.Errors, "RATE_LIMITED") == nil {
			return "", classifyCreateCommitErrors(result.Errors)
		}
		delay := rateLimitedDelay(resp, attempt)
		if delay > maxRateLimitWait || attempt == maxRetries-1 {
			err := classifyCreateCommitErrors(result.Errors)
			if rl, ok := parseRateLimit(resp.Header); ok {
				err = fmt.Errorf("%w [%s]", err, rl)
			}
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-c.afterFunc(delay):
		}
		// The target may have changed during the wait; a refused guard
		// means the mutation is not resent.
		if err := checkRetryGuard(ctx, http.MethodPost, c.graphqlEndpoint()); err != nil {
			return "", fmt.Errorf("create commit on branch: %w", err)
		}
	}
}

// maxRateLimitWait is the longest rate-limit wait createCommitOnBranch sits
// out; a longer one is reported instead. retryDelay bounds Retry-After the
// same way.
const maxRateLimitWait = 5 * time.Minute

// rateLimitedDelay is the wait GitHub's GraphQL docs ask for after a
// RATE_LIMITED answer.
func rateLimitedDelay(resp *http.Response, attempt int) time.Duration {
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if rl, ok := parseRateLimit(resp.Header); ok && rl.Remaining == 0 && !rl.Reset.IsZero() {
		return max(time.Until(rl.Reset), 0) + time.Second
	}
	return secondaryRateLimitBackoff + time.Second<<attempt
}

// branchHoldsChanges returns the head of branch when its tree already has
// every addition at its blob SHA and none of the deletions, else "".
func (c *LiveClient) branchHoldsChanges(ctx context.Context, owner, repo, branch string, additions []forge.TreeFile, deletions []string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.getRepoObjectWithRetry(ctx, "get branch ref", "decode ref",
		fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, branch), &ref); err != nil {
		return "", err
	}
	treeSHA, err := c.getCommitTreeSHA(ctx, owner, repo, ref.Object.SHA)
	if err != nil {
		return "", err
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			SHA  string `json:"sha"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := c.getRepoObjectWithRetry(ctx, "get tree", "decode tree",
		fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, treeSHA), &tree); err != nil {
		return "", err
	}
	if tree.Truncated {
		return "", nil
	}
	blobs := make(map[string]string, len(tree.Tree))
	for _, entry := range tree.Tree {
		blobs[entry.Path] = entry.SHA
	}
	for _, f := range additions {
		want, err := treeFileBlobSHA(f)
		if err != nil {
			return "", err
		}
		if blobs[f.Path] != want {
			return "", nil
		}
	}
	for _, path := range deletions {
		if _, ok := blobs[path]; ok {
			return "", nil
		}
	}
	return ref.Object.SHA, nil
}

func firstGraphQLError(errs []graphQLError, typ string) *graphQLError {
	for i := range errs {
		if errs[i].Type == typ {
			return &errs[i]
		}
	}
	return nil
}

// staleHeadMessage matches GitHub's stale expectedHeadOid text ("is at
// <sha> but expected <sha>"), which it has also sent typed FORBIDDEN
// (#8580). Both SHAs are required so SAML, missing-scope and rule-violation
// FORBIDDEN errors do not match.
var staleHeadMessage = regexp.MustCompile(`is at [0-9a-f]{40} but expected [0-9a-f]{40}`)

// staleHeadError returns the first STALE_DATA entry, else the first entry
// whose message reports a stale expectedHeadOid whatever its type.
func staleHeadError(errs []graphQLError) *graphQLError {
	if e := firstGraphQLError(errs, "STALE_DATA"); e != nil {
		return e
	}
	for i := range errs {
		if staleHeadMessage.MatchString(errs[i].Message) {
			return &errs[i]
		}
	}
	return nil
}

// classifyCreateCommitErrors maps the errors array onto forge sentinels.
// GitHub may return several entries, so the most actionable wins: a
// protection rejection (classic protection answers
// BRANCH_PROTECTION_RULE_VIOLATION, rulesets FORBIDDEN with rule-violation
// text), then a stale head (STALE_DATA, or any type with stale-head text),
// NOT_FOUND, RATE_LIMITED, FORBIDDEN, then the first error as is. An
// exhausted RATE_LIMITED is reported as a 429 *APIError so IsRateLimitError
// and forge.IsTransient still apply.
func classifyCreateCommitErrors(errs []graphQLError) error {
	if len(errs) == 0 {
		return errors.New("create commit on branch: empty errors array")
	}
	for i := range errs {
		if errs[i].Type != "BRANCH_PROTECTION_RULE_VIOLATION" && !isBranchProtectionMessage(errs[i].Message) {
			continue
		}
		if isSignedCommitsRequiredMessage(errs[i].Message) {
			return fmt.Errorf("create commit on branch: %w: %w: %w", forge.ErrBranchProtected, forge.ErrSignedCommitsRequired, &errs[i])
		}
		return fmt.Errorf("create commit on branch: %w: %w", forge.ErrBranchProtected, &errs[i])
	}
	if e := staleHeadError(errs); e != nil {
		return fmt.Errorf("create commit on branch: %w: %w", forge.ErrNonFastForward, e)
	}
	if e := firstGraphQLError(errs, "NOT_FOUND"); e != nil {
		return fmt.Errorf("create commit on branch: %w: %w", forge.ErrNotFound, e)
	}
	if e := firstGraphQLError(errs, "RATE_LIMITED"); e != nil {
		return fmt.Errorf("create commit on branch: %w", &APIError{StatusCode: http.StatusTooManyRequests, Message: e.Message})
	}
	if e := firstGraphQLError(errs, "FORBIDDEN"); e != nil {
		return fmt.Errorf("create commit on branch: %w: %w", forge.ErrForbidden, e)
	}
	return fmt.Errorf("create commit on branch: %w", &errs[0])
}

// isRetriableCommitError reports whether a commit attempt should re-read
// the branch and run again: any non-fast-forward, or a GraphQL NOT_FOUND,
// which right after reading the ref is replica lag (#7861). REST 404s are
// already retried by retryOnRepoRace and are not retried here.
func isRetriableCommitError(err error) bool {
	if forge.IsNonFastForward(err) {
		return true
	}
	var gqlErr *graphQLError
	return errors.As(err, &gqlErr) && gqlErr.Type == "NOT_FOUND"
}
