package failclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

// githubGraphQLServerError is the generic message GitHub sent, with an
// empty type, for a createCommitOnBranch that failed on its side (#9472).
const githubGraphQLServerError = "Something went wrong while executing your query on 2026-11-13T17:32:20Z. Please include `04B1:1E1739:4F83F3:FCF283:6AF7499B` when reporting this issue."

// commitGraphQLError returns the error the GitHub forge client's
// CommitFiles produces when createCommitOnBranch answers HTTP 200 with
// one GraphQL error of the given type and message. The GraphQL error
// type is unexported, so the test drives the real client against a
// stub server instead of constructing it.
func commitGraphQLError(t *testing.T, typ, message string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo":
			json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": "abc123"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/git/commits/abc123":
			json.NewEncoder(w).Encode(map[string]any{"tree": map[string]string{"sha": "tree000"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/git/trees/tree000":
			json.NewEncoder(w).Encode(map[string]any{"tree": []any{}, "truncated": false})
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			json.NewEncoder(w).Encode(map[string]any{
				"data":   map[string]any{"createCommitOnBranch": nil},
				"errors": []map[string]any{{"type": typ, "message": message, "path": []string{"createCommitOnBranch"}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := gh.New("test-token").WithBaseURL(srv.URL).CommitFiles(context.Background(), "org", "repo", "msg",
		[]forge.TreeFile{{Path: "f.txt", Content: []byte("x"), Mode: "100644"}})
	require.Error(t, err)
	return err
}

// TestClassifyGitHubServerErrors covers the server-error rule (#9472):
// GitHub's generic GraphQL server error and a REST 5xx left after the
// client's retries are infrastructure, while typed GraphQL errors and
// non-rate-limit 4xx stay scenario.
func TestClassifyGitHubServerErrors(t *testing.T) {
	t.Parallel()

	agentPath := ".fullsend/agents/triage.md"
	serverErr := commitGraphQLError(t, "", githubGraphQLServerError)
	// Wrapped as steps/dispatch.go wraps a failed agent-resource commit.
	dispatchErr := fmt.Errorf("committing agent resource %s: %w", agentPath, serverErr)
	assert.Equal(t, behaviourfail.Infrastructure, Classify(dispatchErr))

	assert.Equal(t, behaviourfail.Infrastructure,
		Classify(fmt.Errorf("get repo: %w", &gh.APIError{StatusCode: http.StatusBadGateway, Message: "Bad Gateway"})))

	forbidden := commitGraphQLError(t, "FORBIDDEN", "Resource not accessible by integration")
	assert.Equal(t, behaviourfail.Scenario, Classify(fmt.Errorf("committing agent resource %s: %w", agentPath, forbidden)))

	staleHead := commitGraphQLError(t, "FORBIDDEN", "is at 98ad40bffbd1e5fb27d3636e7f7b6cbce85113c2 but expected 23ab78126def5042fe61d7e66dcbf5d9f35b589a")
	assert.Equal(t, behaviourfail.Scenario, Classify(staleHead))

	assert.Equal(t, behaviourfail.Scenario,
		Classify(&gh.APIError{StatusCode: http.StatusUnprocessableEntity, Message: "Validation Failed"}))

	assertion := errors.New("issue 42 missing label")
	assert.Equal(t, behaviourfail.Scenario, Classify(errors.Join(dispatchErr, assertion)))
	assert.Equal(t, behaviourfail.Scenario, Classify(errors.Join(assertion, dispatchErr)))
}

// TestClassifyInfrastructure covers every mode the allowlist
// recognizes as infrastructure: the #8036 acceptance criteria (429
// APIError, secondary-rate-limit 403, CapacityError populated only
// via Denied, invitation-pending through a wrap) plus the marked-error
// rule. Cases wrap their errors where noted so they also prove
// errors.As and errors.Is traverse the chain the way wrapped suite
// errors arrive.
func TestClassifyInfrastructure(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err error
	}{
		"marked error": {
			err: behaviourfail.Mark(errors.New("repo acme/widget not visible after 30 attempts"), "setup: repo never visible"),
		},
		"marked error through wrap chain": {
			err: fmt.Errorf("bootstrap: %w", behaviourfail.Mark(errors.New("iam 429"), "setup: gcp wif quota")),
		},
		"primary rate limit 429": {
			err: &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many requests"},
		},
		"primary rate limit 429 wrapped": {
			err: fmt.Errorf("add label: %w", &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many requests"}),
		},
		"secondary rate limit 403": {
			err: &gh.APIError{StatusCode: http.StatusForbidden, Message: "You have exceeded a secondary rate limit and need to wait"},
		},
		"capacity error populated only via denied": {
			err: &install.CapacityError{Denied: map[string]error{"fullsend-pool-1": errors.New("forbidden")}},
		},
		"capacity error wrapped": {
			err: fmt.Errorf("acquire slot: %w", &install.CapacityError{Denied: map[string]error{"fullsend-pool-1": errors.New("forbidden")}}),
		},
		"invitation pending through wrap": {
			err: fmt.Errorf("setup: %w", gh.ErrInvitationPending),
		},
		"invitation pending as add collaborator wraps it": {
			err: fmt.Errorf("add collaborator %s: %w, access not granted", "alice", gh.ErrInvitationPending),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, behaviourfail.Infrastructure, Classify(tc.err))
		})
	}
}

// TestClassifyScenario covers the conservative default: the #8036
// negative criteria (plain assertion error, an unrecognized APIError
// — a 404 here, since a 5xx is a server error (#9472) — an error
// merely containing "429") plus nil and a 403 that is a
// permission denial rather than a secondary rate limit. None of these
// may be retried or masked by an infrastructure label.
func TestClassifyScenario(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err error
	}{
		"nil": {err: nil},
		"plain assertion error": {
			err: errors.New("issue 42 missing label"),
		},
		"unrecognized api error 404": {
			err: &gh.APIError{StatusCode: http.StatusNotFound, Message: "Not Found"},
		},
		"message merely containing 429": {
			err: errors.New("workflow run 429 missing expected annotation"),
		},
		"forbidden 403 without rate limit message": {
			err: &gh.APIError{StatusCode: http.StatusForbidden, Message: "resource not accessible by integration"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, behaviourfail.Scenario, Classify(tc.err))
		})
	}
}

// TestClassifyJoinedErrors pins the join semantics (PR #8801 review):
// a joined error classifies as infrastructure only when every branch
// independently does. A scenario branch — a lease race joined with a
// rate-limited release — is an independent cause that keeps the whole
// error scenario, so the single automatic retry never masks it (spec
// R4). Both branch orders are covered because errors.Join order must
// not change the class.
func TestClassifyJoinedErrors(t *testing.T) {
	t.Parallel()

	marked := behaviourfail.Mark(errors.New("iam 429"), "setup: gcp wif quota")
	rateLimit := &gh.APIError{StatusCode: http.StatusTooManyRequests, Message: "too many requests"}
	capacity := &install.CapacityError{Denied: map[string]error{"fullsend-pool-1": errors.New("forbidden")}}
	assertion := errors.New("issue 42 missing label")
	leaseLost := errors.New("slot repo is no longer this lease's")

	scenarioJoined := map[string]error{
		"assertion joined with marked error":           errors.Join(assertion, marked),
		"marked error joined with assertion, reversed": errors.Join(marked, assertion),
		"rate limit joined with assertion":             errors.Join(rateLimit, assertion),
		"lease lost joined with rate-limited release":  errors.Join(leaseLost, rateLimit),
		"rate-limited release joined with lease lost":  errors.Join(rateLimit, leaseLost),
		"capacity joined with assertion":               errors.Join(capacity, assertion),
		"wrapped join keeps the scenario branch":       fmt.Errorf("triage: %w", errors.Join(assertion, marked)),
		"scenario branch nested through two wraps":     fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", errors.Join(marked, assertion))),
		"empty join classifies scenario":               errors.Join(),
	}
	for name, err := range scenarioJoined {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, behaviourfail.Scenario, Classify(err))
		})
	}

	infraJoined := map[string]error{
		"marked joined with rate limit": errors.Join(marked, rateLimit),
		"marked joined with capacity":   errors.Join(marked, capacity),
		"three infra branches":          errors.Join(marked, rateLimit, capacity),
		"infra join wrapped":            fmt.Errorf("bootstrap: %w", errors.Join(marked, rateLimit)),
		"mark on the join itself wins":  behaviourfail.Mark(errors.Join(assertion, marked), "setup: whole failure is environmental"),
	}
	for name, err := range infraJoined {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, behaviourfail.Infrastructure, Classify(err))
		})
	}
}
