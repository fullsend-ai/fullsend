// Package failclass classifies behaviour-suite failure errors as
// infrastructure (environment) or scenario (assertion) failures.
//
// It is the composite half of a two-package layout: the stdlib-only
// marker leaf internal/behaviourfail lets producers with
// context-dependent modes (install drivers, e2etest bootstrap) tag
// their errors with behaviourfail.Mark, while this package recognizes
// the structural error types — the GitHub forge client's APIError and
// the install driver's CapacityError — which requires importing them.
// Both halves exist because the install driver must import the marker
// to tag its errors while the classifier must import the driver to
// recognize them; a single package would be an import cycle
// (failclass → install → failclass).
package failclass

import (
	"errors"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

// Classify returns err's failure class for the behaviour suite:
// behaviourfail.Infrastructure when the error matches a known
// infrastructure mode, behaviourfail.Scenario otherwise — including
// for nil.
//
// The allowlist is deliberately narrow; each rule names a failure
// mode the behaviour suite has observed in CI (#8036):
//
//   - behaviourfail.Is: an error a producer explicitly marked as
//     infrastructure (setup-poll exhaustion, GCP WIF quota, token
//     mint/download).
//   - a rate-limit gh.APIError: primary 429, or a 403 whose message
//     says "rate limit" (secondary limit), per gh.IsRateLimitError.
//   - an install.CapacityError: the slot pool could not provide
//     capacity — every cause (rate-limited, denied, or failed orgs)
//     is an environment condition, not a product assertion.
//   - the gh.ErrInvitationPending sentinel: collaborator access
//     awaits an accepted invitation.
//
// Everything else classifies as scenario: nil, plain assertion
// failures, unrecognized API errors (a 500 is a product or forge
// defect, not an environment condition), and errors whose message
// merely mentions rate limits. This conservative default is what
// makes the single automatic retry safe — an infrastructure-classified
// run is retried exactly once, so a broader allowlist would risk
// masking product regressions behind that retry. Unknown
// infrastructure modes surface as scenario failures in the
// per-failure report instead of silently retrying.
//
// Extending the allowlist for a new mode is one rule here (or, for
// context-dependent modes, one behaviourfail.Mark at the producing
// call site); anything unmatched stays scenario, which is the safe
// direction.
func Classify(err error) behaviourfail.Class {
	if err == nil {
		return behaviourfail.Scenario
	}
	if behaviourfail.Is(err) {
		return behaviourfail.Infrastructure
	}
	// A rate-limit API error: primary 429 or secondary-rate-limit 403.
	// errors.As pins the rule to the structural *gh.APIError type, so
	// an error whose message merely mentions "429" or "rate limit"
	// without an APIError in its chain stays scenario.
	var apiErr *gh.APIError
	if errors.As(err, &apiErr) && gh.IsRateLimitError(err) {
		return behaviourfail.Infrastructure
	}
	var capacity *install.CapacityError
	if errors.As(err, &capacity) {
		return behaviourfail.Infrastructure
	}
	if errors.Is(err, gh.ErrInvitationPending) {
		return behaviourfail.Infrastructure
	}
	return behaviourfail.Scenario
}
