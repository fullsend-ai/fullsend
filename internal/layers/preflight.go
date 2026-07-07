package layers

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// SkipReason describes why a preflight check was skipped.
type SkipReason string

const (
	// SkipNone means the preflight was not skipped.
	SkipNone SkipReason = ""
	// SkipInstallationToken means the token is a GitHub App installation token.
	SkipInstallationToken SkipReason = "installation"
	// SkipFineGrained means the token is a fine-grained PAT whose
	// permissions cannot be introspected.
	SkipFineGrained SkipReason = "fine-grained"
)

// PreflightResult describes what a preflight check found.
type PreflightResult struct {
	// Required is the set of scopes the operation needs.
	Required []string
	// Granted is the set of scopes the token actually has.
	Granted []string
	// Missing is the set of scopes needed but not granted.
	Missing []string
	// Skipped is true when scope introspection was unavailable
	// (e.g., fine-grained tokens that don't report scopes).
	Skipped bool
	// SkippedReason indicates why the preflight was skipped.
	SkippedReason SkipReason
}

// OK returns true if no scopes are missing.
func (r *PreflightResult) OK() bool {
	return len(r.Missing) == 0
}

// scopeDescriptions maps GitHub OAuth scopes to human-readable
// explanations of why fullsend needs them. Scopes that don't appear
// here are printed without a description.
var scopeDescriptions = map[string]string{
	"repo":        "read/write repository contents, secrets, and workflows",
	"workflow":    "create and update GitHub Actions workflow files",
	"admin:org":   "manage organization-level Actions secrets (dispatch token)",
	"delete_repo": "delete the .fullsend config repository (uninstall only)",
}

// fineGrainedEquivalents maps OAuth scopes to equivalent fine-grained
// PAT permissions. Used in skip guidance when scopes cannot be verified.
var fineGrainedEquivalents = map[string]string{
	"repo":        "Contents (read/write), Secrets (read/write), Variables (read/write), Pull requests (read/write)",
	"workflow":    "Workflows (read/write)",
	"admin:org":   "Organization administration (read/write)",
	"delete_repo": "Administration (read/write)",
}

// SkipGuidance returns a human-readable message listing the scopes
// that could not be verified and their fine-grained equivalents.
func (r *PreflightResult) SkipGuidance() string {
	if len(r.Required) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Could not verify scopes: %s\n", strings.Join(r.Required, ", "))
	b.WriteString("Ensure your token has these fine-grained permissions:\n")
	for _, scope := range r.Required {
		if equiv, ok := fineGrainedEquivalents[scope]; ok {
			fmt.Fprintf(&b, "  • %s → %s\n", scope, equiv)
		} else {
			fmt.Fprintf(&b, "  • %s\n", scope)
		}
	}
	b.WriteString("  • Metadata (read-only) — required by GitHub for all tokens")
	return b.String()
}

// Error returns a human-readable error describing missing scopes and
// how to fix the problem.
func (r *PreflightResult) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "token is missing required scopes: %s\n", strings.Join(r.Missing, ", "))

	b.WriteString("\nWhy each scope is needed:\n")
	for _, scope := range r.Missing {
		if desc, ok := scopeDescriptions[scope]; ok {
			fmt.Fprintf(&b, "  • %s — %s\n", scope, desc)
		} else {
			fmt.Fprintf(&b, "  • %s\n", scope)
		}
	}

	b.WriteString("\nTo add the missing scopes, run:\n")
	fmt.Fprintf(&b, "  gh auth refresh -s %s\n", strings.Join(r.Missing, ","))
	b.WriteString("\nNote: gh auth scopes apply to every organization your\n")
	b.WriteString("account belongs to. If that is a concern, create a\n")
	b.WriteString("fine-grained personal access token scoped to a single org\n")
	b.WriteString("and export it as GH_TOKEN instead.")
	return b.String()
}

// Preflight checks that the forge client's token has all the scopes
// required by the stack's layers for the given operation. It returns a
// PreflightResult describing what was found.
//
// If the token is a GitHub App installation token, or scope introspection
// is unavailable (e.g., fine-grained PATs), Preflight returns a result with
// OK() == true and Skipped set. OAuth scope preflight does not apply to
// installation tokens; for tokens we cannot introspect we let the operation
// proceed and fail at the point of use if permissions are actually missing.
func (s *Stack) Preflight(ctx context.Context, op Operation, client forge.Client) (*PreflightResult, error) {
	required := s.CollectRequiredScopes(op)
	if len(required) == 0 {
		return &PreflightResult{}, nil
	}

	isInstallation, err := client.IsInstallationToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("detecting installation token: %w", err)
	}
	if isInstallation {
		return &PreflightResult{Required: required, Skipped: true, SkippedReason: SkipInstallationToken}, nil
	}

	granted, err := client.GetTokenScopes(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking token scopes: %w", err)
	}

	// If the forge can't report scopes (fine-grained tokens return nil),
	// we can't validate. Let the operation proceed but warn the caller.
	if granted == nil {
		return &PreflightResult{Required: required, Skipped: true, SkippedReason: SkipFineGrained}, nil
	}

	grantedSet := make(map[string]bool, len(granted))
	for _, s := range granted {
		grantedSet[s] = true
	}

	var missing []string
	for _, scope := range required {
		if !grantedSet[scope] {
			missing = append(missing, scope)
		}
	}

	return &PreflightResult{
		Required: required,
		Granted:  granted,
		Missing:  missing,
	}, nil
}
