package failclass

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

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
		// getFileWithRetry marks a 404 that outlasts every retry; the
		// mark must survive the install driver's wraps (#9469).
		"exhausted post-install 404 through install wraps": {
			err: fmt.Errorf("allocating repo acme/widget: %w",
				fmt.Errorf("post-install validation for acme/widget: %w",
					fmt.Errorf("post-install: missing vendored marker .defaults/action.yml: %w",
						behaviourfail.Mark(fmt.Errorf("not found on 5 attempts over 30s: %w", forge.ErrNotFound),
							"github: file not readable after commit")))),
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
// negative criteria (plain assertion error, unrecognized 500 APIError,
// an error merely containing "429") plus nil and a 403 that is a
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
		"unrecognized api error 500": {
			err: &gh.APIError{StatusCode: http.StatusInternalServerError, Message: "boom"},
		},
		"message merely containing 429": {
			err: errors.New("workflow run 429 missing expected annotation"),
		},
		"forbidden 403 without rate limit message": {
			err: &gh.APIError{StatusCode: http.StatusForbidden, Message: "resource not accessible by integration"},
		},
		// A post-install validation failure other than an exhausted 404
		// (here, a wrong runtime) is a product assertion (#9469).
		"non-404 post-install validation error through install wraps": {
			err: fmt.Errorf("allocating repo acme/widget: %w",
				fmt.Errorf("post-install validation for acme/widget: %w",
					errors.New(`post-install: .fullsend/config.yaml runtime is "claude", want dummy`))),
		},
		// The missing-workflow-run timeout keeps its scenario class until
		// diagnostics show whether the run is late or lost (#9469).
		"workflow not dispatched timeout through step wrap": {
			err: fmt.Errorf("waiting for issue-open workflow: %w",
				fmt.Errorf("workflow fullsend.yaml (issues) %w", ci.ErrWorkflowNotDispatched)),
		},
		// The typed error the GitHub Actions driver returns, with a
		// poll-error detail, behind the same step wrap.
		"not dispatched error type through step wrap": {
			err: fmt.Errorf("waiting for issue-open workflow: %w",
				&ci.NotDispatchedError{WorkflowFile: "fullsend.yaml", Event: "issues", Detail: " (failed on 3 of 3 polls)"}),
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
