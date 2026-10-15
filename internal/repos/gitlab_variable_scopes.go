package repos

import (
	"context"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitlabInheritedVariableOverrideProblem checks the instance-level and
// ancestor-group CI/CD variables a project inherits for a definition of one
// of the predefined variables the trigger-pipeline evaluator treats as fixed
// (the keys of env). Such a variable takes precedence over the predefined
// value, so the evaluator could conclude that a job is excluded from trigger
// pipelines when GitLab would in fact run it.
//
// Every scope the credential can read is checked, and a definition found in
// any of them is a problem. Listing instance-level variables needs
// administrator access and listing group variables needs group Owner access,
// which a project Maintainer usually lacks. A scope that answers forbidden is
// unverified, not unsafe: it is skipped and the remaining scopes are still
// checked. This deliberately trusts instance administrators and group Owners
// not to override those predefined variables; an override in a scope the
// installing credential cannot read, whether present at install time or
// added later, is not detected. Errors other than forbidden mean the
// overrides could not be checked at all.
func gitlabInheritedVariableOverrideProblem(ctx context.Context, client forge.Client, owner, repo string, env map[string]*string) (string, error) {
	// Instance-level variables (self-managed GitLab) are inherited by every
	// project.
	instanceVars, instanceErr := client.ListInstanceVariables(ctx)
	if instanceErr != nil && !forge.IsForbidden(instanceErr) {
		return "", safeAPIError("checking instance variable overrides", instanceErr)
	}
	for _, variable := range instanceVars {
		if _, reserved := env[variable.Name]; reserved {
			return "webhook fast-path deferred: predefined instance variable override: " + variable.Name, nil
		}
	}
	// owner may contain only the first path component for nested projects.
	namespace := owner
	if slash := strings.LastIndex(repo, "/"); slash >= 0 {
		namespace += "/" + repo[:slash]
	}
	parts := strings.Split(namespace, "/")
	for i := range parts {
		group := strings.Join(parts[:i+1], "/")
		inherited, groupErr := client.ListOrgVariables(ctx, group)
		if forge.IsNotFound(groupErr) && i == 0 && len(parts) == 1 {
			continue
		} // personal namespace
		if forge.IsForbidden(groupErr) {
			// Unverified: the remaining groups are still checked, so a
			// readable scope's override is reported even when another scope
			// is unreadable.
			continue
		}
		if groupErr != nil {
			return "", safeAPIError("checking inherited group variable overrides", groupErr)
		}
		for _, variable := range inherited {
			if _, reserved := env[variable.Name]; reserved {
				return "webhook fast-path deferred: predefined group variable override: " + variable.Name, nil
			}
		}
	}
	return "", nil
}
