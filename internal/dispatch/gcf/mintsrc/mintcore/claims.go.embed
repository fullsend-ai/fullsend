package mintcore

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Claims holds the subset of GitHub Actions OIDC JWT claims validated by the mint.
type Claims struct {
	Issuer          string   `json:"iss"`
	Audience        Audience `json:"aud"`
	IssuedAt        int64    `json:"iat"`
	Expiry          int64    `json:"exp"`
	Repository      string   `json:"repository"`
	RepositoryOwner string   `json:"repository_owner"`
	JobWorkflowRef  string   `json:"job_workflow_ref"`
}

// Audience handles the OIDC aud claim which can be a string or array of strings.
type Audience []string

// UnmarshalJSON handles both string and array-of-strings forms.
func (a *Audience) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("aud must not be empty")
		}
		*a = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("aud must be a string or array of strings")
	}
	if len(arr) == 0 {
		return fmt.Errorf("aud must not be empty")
	}
	for _, v := range arr {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("aud must not contain empty values")
		}
	}
	*a = arr
	return nil
}

// Contains reports whether aud is in the audience list.
func (a Audience) Contains(aud string) bool {
	for _, v := range a {
		if v == aud {
			return true
		}
	}
	return false
}

const upstreamRepoPrefix = "fullsend-ai/fullsend/"

// ParseAllowedOrgs splits a comma-separated ALLOWED_ORGS value into trimmed entries.
func ParseAllowedOrgs(allowedOrgs string) []string {
	if allowedOrgs == "" {
		return nil
	}
	var orgs []string
	for _, o := range strings.Split(allowedOrgs, ",") {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			orgs = append(orgs, trimmed)
		}
	}
	return orgs
}

// IsPublicMint reports whether the given list contains the wildcard entry "*".
// It is used by provisioner and CLI code that checks ALLOWED_ORGS for legacy
// public-mode detection. New code should use IsPublicMintRepos instead, which
// checks PER_REPO_WIF_REPOS — the canonical source for public mint mode.
func IsPublicMint(allowedOrgs []string) bool {
	for _, entry := range allowedOrgs {
		if entry == "*" {
			return true
		}
	}
	return false
}

// IsPublicMintRepos reports whether perRepoWIFRepos contains the wildcard
// entry "*", meaning every repository gets per-repo treatment (public mint
// mode).
func IsPublicMintRepos(perRepoWIFRepos map[string]bool) bool {
	return perRepoWIFRepos["*"]
}

// IsPerRepoMode reports whether repository gets per-repo treatment.
// A repo is per-repo if it appears in PER_REPO_WIF_REPOS, or if
// PER_REPO_WIF_REPOS contains "*" (public mint mode).
func IsPerRepoMode(repository string, perRepoWIFRepos map[string]bool) bool {
	if perRepoWIFRepos["*"] {
		return true
	}
	return perRepoWIFRepos[strings.ToLower(repository)]
}

// AuthorizeToken performs the common authorization policy called by the
// handler after a verifier backend authenticates the token. It determines
// whether a caller gets per-repo or per-org treatment and validates
// accordingly:
//
//   - If the caller's repository is in PER_REPO_WIF_REPOS (or PER_REPO_WIF_REPOS
//     contains "*"), the caller gets per-repo treatment — authorized without
//     requiring repository_owner in ALLOWED_ORGS.
//   - Otherwise the caller's repository_owner must be in ALLOWED_ORGS (per-org).
//
// In both cases, repository_owner must be non-empty (defense-in-depth).
func AuthorizeToken(claims *Claims, allowedOrgs []string, perRepoWIFRepos map[string]bool) error {
	if claims.RepositoryOwner == "" {
		return fmt.Errorf("missing repository_owner claim")
	}
	if IsPerRepoMode(claims.Repository, perRepoWIFRepos) {
		// Per-repo callers don't need ALLOWED_ORGS membership.
		return nil
	}
	// Per-org path: org must be in ALLOWED_ORGS.
	return ValidateOrgAllowed(claims.RepositoryOwner, allowedOrgs)
}

// ValidateOrgAllowed checks that org is in the allowed list (case-insensitive).
// When allowedOrgs contains *, any non-empty org is accepted (public mint mode).
func ValidateOrgAllowed(org string, allowedOrgs []string) error {
	if org == "" {
		return fmt.Errorf("missing repository_owner claim")
	}
	if IsPublicMint(allowedOrgs) {
		return nil
	}
	for _, entry := range allowedOrgs {
		if strings.EqualFold(entry, org) {
			return nil
		}
	}
	return fmt.Errorf("repository_owner %q not in allowed orgs", org)
}

// ValidateWorkflowRef checks that a job_workflow_ref claim references an
// allowed workflow host and basename.
//
// In per-repo mode (isPerRepo=true), including public mint mode
// (PER_REPO_WIF_REPOS=*), the workflow must be hosted by a repo in
// workflowHostRepos. The upstream repo (fullsend-ai/fullsend) is always
// accepted regardless of the workflowHostRepos contents. The workflow
// basename must be in allowedWorkflowFiles.
//
// Public mode is not special-cased — it uses the same per-repo path.
// The only difference between public and tight per-repo mode is caller
// enrollment (PER_REPO_WIF_REPOS=* accepts all requesting repos).
// See ADR 0082 §2 (revised 2026-08-05).
//
// In per-org mode (isPerRepo=false), the workflow must come from the
// caller's own org .fullsend config repo or the upstream repo. These are
// hard-wired — no separate allow-list is consulted. The workflow basename
// must be in allowedWorkflowFiles.
//
// For dual-enrolled callers (both per-repo and per-org), the handler
// calls this function twice — once in per-org mode, then falling back to
// per-repo mode if per-org fails. This accepts workflows from either
// source: {org}/.fullsend / upstream OR workflowHostRepos / upstream.
func ValidateWorkflowRef(ref, repository string, isPerRepo bool, workflowHostRepos map[string]bool, allowedWorkflowFiles []string) error {
	if ref == "" {
		return fmt.Errorf("missing job_workflow_ref claim")
	}

	lowerRef := strings.ToLower(ref)

	var relPath string
	matched := false

	if isPerRepo {
		// Per-repo mode: check workflowHostRepos allow-list.
		// Upstream is always accepted.
		if strings.HasPrefix(lowerRef, upstreamRepoPrefix) {
			relPath = strings.TrimPrefix(lowerRef, upstreamRepoPrefix)
			matched = true
		}

		if !matched && workflowHostRepos["*"] {
			if idx := strings.Index(lowerRef, "/.github/workflows/"); idx > 0 {
				relPath = lowerRef[idx+1:]
				matched = true
			}
		}

		if !matched {
			for host := range workflowHostRepos {
				if host == "*" {
					continue
				}
				hostPrefix := strings.ToLower(host) + "/"
				if strings.HasPrefix(lowerRef, hostPrefix) {
					relPath = strings.TrimPrefix(lowerRef, hostPrefix)
					matched = true
					break
				}
			}
		}

		if !matched {
			return fmt.Errorf("job_workflow_ref does not reference an allowed workflow host repo")
		}
	} else {
		// Per-org mode: hard-wire to {org}/.fullsend and upstream.
		if idx := strings.Index(repository, "/"); idx > 0 {
			repoOwner := strings.ToLower(repository[:idx])
			configPrefix := repoOwner + "/.fullsend/"
			if strings.HasPrefix(lowerRef, configPrefix) {
				relPath = strings.TrimPrefix(lowerRef, configPrefix)
				matched = true
			}
		}

		if !matched {
			if strings.HasPrefix(lowerRef, upstreamRepoPrefix) {
				relPath = strings.TrimPrefix(lowerRef, upstreamRepoPrefix)
				matched = true
			}
		}

		if !matched {
			return fmt.Errorf("job_workflow_ref does not reference .fullsend or upstream repo")
		}
	}

	if atIdx := strings.Index(relPath, "@"); atIdx > 0 {
		relPath = relPath[:atIdx]
	}

	if !strings.HasPrefix(relPath, ".github/workflows/") {
		return fmt.Errorf("job_workflow_ref does not reference a workflow file")
	}

	workflowFile := strings.TrimPrefix(relPath, ".github/workflows/")
	for _, wf := range allowedWorkflowFiles {
		if wf == "*" || strings.EqualFold(wf, workflowFile) {
			return nil
		}
	}
	return fmt.Errorf("workflow file %q not in allowed list", workflowFile)
}
