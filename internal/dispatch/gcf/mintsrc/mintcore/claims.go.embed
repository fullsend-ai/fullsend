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
// handler after a verifier backend authenticates the token. Only callers
// with per-repo treatment are authorized: the caller's repository must be
// in PER_REPO_WIF_REPOS, or PER_REPO_WIF_REPOS must contain "*" (public
// mint mode). Organization membership alone (the legacy per-org
// ALLOWED_ORGS model) does not authorize a caller.
//
// repository_owner must be non-empty (defense-in-depth).
func AuthorizeToken(claims *Claims, perRepoWIFRepos map[string]bool) error {
	if claims.RepositoryOwner == "" {
		return fmt.Errorf("missing repository_owner claim")
	}
	if !IsPerRepoMode(claims.Repository, perRepoWIFRepos) {
		return fmt.Errorf("repository %q is not enrolled for per-repo mint access", claims.Repository)
	}
	return nil
}

// ValidateWorkflowRef checks that a job_workflow_ref claim references an
// allowed workflow host and basename.
//
// The workflow must be hosted by a repo in workflowHostRepos. The upstream
// repo (fullsend-ai/fullsend) is always accepted regardless of the
// workflowHostRepos contents. The workflow basename must be in
// allowedWorkflowFiles.
//
// Public mode (PER_REPO_WIF_REPOS=*) is not special-cased — it uses the
// same path. The only difference between public and tight per-repo mode
// is caller enrollment (PER_REPO_WIF_REPOS=* accepts all requesting repos).
// See ADR 0082 §2 (revised 2026-08-05).
func ValidateWorkflowRef(ref string, workflowHostRepos map[string]bool, allowedWorkflowFiles []string) error {
	if ref == "" {
		return fmt.Errorf("missing job_workflow_ref claim")
	}

	lowerRef := strings.ToLower(ref)

	var relPath string
	matched := false

	// Upstream is always accepted.
	if strings.HasPrefix(lowerRef, upstreamRepoPrefix) {
		relPath = strings.TrimPrefix(lowerRef, upstreamRepoPrefix)
		matched = true
	}

	if !matched {
		for host := range workflowHostRepos {
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
