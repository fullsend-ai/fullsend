package repos

import (
	"context"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitlabInheritedVariableOverrideProblem checks the instance-level and
// ancestor-group CI/CD variables a project inherits for a definition of one
// of the reserved values (the keys of env). An inherited value takes
// precedence over predefined and job-level values, so it can invalidate
// dispatch routing or the trigger-pipeline evaluator's job exclusions.
//
// Every scope the credential can read is checked, and a definition found in
// any of them is a problem. Listing instance-level variables needs
// administrator access and listing group variables needs group Owner access,
// which a project Maintainer usually lacks. A scope that answers forbidden is
// unverified, not unsafe: it is skipped and the remaining scopes are still
// checked. This deliberately trusts instance administrators and group Owners
// not to override those reserved values; an override in a scope the
// installing credential cannot read, whether present at install time or
// added later, is not detected. Errors other than forbidden mean the
// overrides could not be checked at all.
func gitlabInheritedVariableOverrideProblem(ctx context.Context, client forge.Client, owner, repo string, env map[string]*string) (string, error) {
	problem, _, err := gitlabInheritedVariableScan(ctx, client, owner, repo, env)
	if problem != "" {
		problem = "webhook fast-path deferred: " + problem
	}
	return problem, err
}

// gitlabInheritedVariableScan is gitlabInheritedVariableOverrideProblem plus
// the inherited scopes it could not read ("instance" and group paths), so a
// caller that acts on the result can report what stayed unverified. The
// returned problem carries no consumer-specific prefix.
func gitlabInheritedVariableScan(ctx context.Context, client forge.Client, owner, repo string, env map[string]*string) (string, []string, error) {
	var unverified []string
	// Instance-level variables (self-managed GitLab) are inherited by every
	// project.
	instanceVars, instanceErr := client.ListInstanceVariables(ctx)
	if instanceErr != nil && !forge.IsForbidden(instanceErr) {
		return "", nil, safeAPIError("checking instance variable overrides", instanceErr)
	}
	if forge.IsForbidden(instanceErr) {
		unverified = append(unverified, "instance")
	}
	for _, variable := range instanceVars {
		if _, reserved := env[variable.Name]; reserved {
			return "instance variable override: " + variable.Name, nil, nil
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
			unverified = append(unverified, group)
			continue
		}
		if groupErr != nil {
			return "", nil, safeAPIError("checking inherited group variable overrides", groupErr)
		}
		for _, variable := range inherited {
			if _, reserved := env[variable.Name]; reserved {
				return "group variable override: " + variable.Name, nil, nil
			}
		}
	}
	return "", unverified, nil
}
