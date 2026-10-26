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
// Joined errors (errors.Join, or fmt.Errorf with multiple %w verbs)
// classify as infrastructure only when every branch independently
// matches an infrastructure rule. A scenario branch joined beside an
// infra branch — a lease race joined with a rate-limited release, for
// example — is an independent cause that keeps the whole error
// scenario-classified, so the single automatic retry never masks it
// (spec R4: any scenario-classified failure fails immediately). A
// behaviourfail mark placed on the joined error itself still wins:
// the mark is the producer's explicit statement that the whole
// failure is environmental.
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
	return classifyTree(err)
}

// classifyTree walks err's chain node by node. A joined error
// (errors.Join, or fmt.Errorf with multiple %w verbs) classifies as
// infrastructure only when every branch independently does; any
// scenario branch is an independent cause that keeps the whole tree
// scenario. A single-chain error classifies as infrastructure when
// any node in its chain matches an allowlist rule.
func classifyTree(err error) behaviourfail.Class {
	for err != nil {
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			branches := joined.Unwrap()
			if len(branches) == 0 {
				return behaviourfail.Scenario
			}
			for _, branch := range branches {
				if classifyTree(branch) == behaviourfail.Scenario {
					return behaviourfail.Scenario
				}
			}
			return behaviourfail.Infrastructure
		}
		if infraRuleAt(err) {
			return behaviourfail.Infrastructure
		}
		err = errors.Unwrap(err)
	}
	return behaviourfail.Scenario
}

// infraRuleAt reports whether err itself — not its wrapped chain —
// matches one allowlist rule. Checking per node is what keeps an
// errors.Join branch independent: errors.As on the joined error would
// otherwise reach into every branch and let one infra cause mask the
// scenario cause joined beside it.
func infraRuleAt(err error) bool {
	if _, ok := err.(*behaviourfail.InfraError); ok {
		return true
	}
	if apiErr, ok := err.(*gh.APIError); ok && gh.IsRateLimitError(apiErr) {
		return true
	}
	if _, ok := err.(*install.CapacityError); ok {
		return true
	}
	return err == gh.ErrInvitationPending
}
