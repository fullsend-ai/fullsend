package failclass

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
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
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, behaviourfail.Scenario, Classify(tc.err))
		})
	}
}
