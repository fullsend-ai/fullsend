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

// ValidateOrgAllowed checks that org is in the allowed list (case-insensitive).
func ValidateOrgAllowed(org string, allowedOrgs []string) error {
	for _, entry := range allowedOrgs {
		if strings.EqualFold(entry, org) {
			return nil
		}
	}
	return fmt.Errorf("repository_owner %q not in allowed orgs", org)
}

// ValidateWorkflowRef checks that a job_workflow_ref claim references an
// allowed workflow. It validates that the ref belongs to the token owner's
// .fullsend config repo, the upstream fullsend-ai/fullsend repo, or a
// registered per-repo repo, and that the workflow file is in the allowed
// list. The repository parameter is the token's repository claim and is
// used to cross-check per-repo matches.
func ValidateWorkflowRef(ref, repository string, perRepoWIFRepos map[string]bool, allowedWorkflowFiles []string) error {
	if ref == "" {
		return fmt.Errorf("missing job_workflow_ref claim")
	}

	lowerRef := strings.ToLower(ref)
	var relPath string
	matched := false

	// Extract the repository owner from the repository claim and only
	// check that specific org's .fullsend/ prefix, rather than iterating
	// all allowedOrgs. This ensures the workflow ref matches the token's
	// own org, not any allowed org.
	if idx := strings.Index(repository, "/"); idx > 0 {
		repoOwner := strings.ToLower(repository[:idx])
		configPrefix := repoOwner + "/.fullsend/"
		if strings.HasPrefix(lowerRef, configPrefix) {
			relPath = strings.TrimPrefix(lowerRef, configPrefix)
			matched = true
		}
	}

	if !matched {
		upstreamPrefix := "fullsend-ai/fullsend/"
		if strings.HasPrefix(lowerRef, upstreamPrefix) {
			relPath = strings.TrimPrefix(lowerRef, upstreamPrefix)
			matched = true
		}
	}

	if !matched {
		repoKey := strings.ToLower(repository)
		if perRepoWIFRepos[repoKey] {
			repoPrefix := repoKey + "/"
			if strings.HasPrefix(lowerRef, repoPrefix) {
				relPath = strings.TrimPrefix(lowerRef, repoPrefix)
				matched = true
			}
		}
	}

	if !matched {
		return fmt.Errorf("job_workflow_ref does not reference .fullsend, upstream repo, or registered per-repo repo")
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
