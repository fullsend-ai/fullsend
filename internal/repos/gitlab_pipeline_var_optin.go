package repos

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ADR 0175 opt-in: a concrete GitLab manifest entry may set
// allow_insecure_pipeline_variables to accept a
// ci_pipeline_variables_minimum_override_role weaker than no_one_allowed.
// The manifest is where the opt-in is saved, so every later install,
// converge, and status run accepts it instead of refusing again.
//
// The opt-in covers every check that requires no_one_allowed: delivering
// runnable jobs, typed dispatch activation, the managed poll schedules, the
// webhook fast path, and the trigger account. The trigger credential adds no
// exposure the opt-in has not already accepted: the Poller token it leaves
// readable can already start pipelines that carry variables. An instance
// that does not report the setting is still refused, because the opt-in
// accepts a known weaker setting, not an unverifiable one.

type gitlabPipelineVarOptInKey struct{}

// WithGitLabPipelineVarOptIns returns a context that records which GitLab
// repositories in m opted in to a relaxed pipeline-variable setting. The
// restriction checks read it by owner/repo, so callers set it once after
// loading the manifest: Converge, Status, and the CLI's repos install each
// seed the context from the manifest they already resolved. A nil manifest
// records no opt-ins.
func WithGitLabPipelineVarOptIns(ctx context.Context, m *Manifest) context.Context {
	set := map[string]bool{}
	for _, name := range GitLabPipelineVarOptInRepos(m) {
		set[strings.ToLower(name)] = true
	}
	return context.WithValue(ctx, gitlabPipelineVarOptInKey{}, set)
}

// GitLabPipelineVarOptInRepos lists, sorted, the concrete GitLab entries in
// m that set allow_insecure_pipeline_variables.
func GitLabPipelineVarOptInRepos(m *Manifest) []string {
	if m == nil || m.GitLab == nil {
		return nil
	}
	var names []string
	for _, e := range m.GitLab.Repos {
		if e.AllowInsecurePipelineVariables {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	return names
}

// gitlabPipelineVarOptedIn reports whether ctx records the ADR 0175 opt-in
// for owner/repo.
func gitlabPipelineVarOptedIn(ctx context.Context, owner, repo string) bool {
	set, _ := ctx.Value(gitlabPipelineVarOptInKey{}).(map[string]bool)
	return set[strings.ToLower(owner+"/"+repo)]
}

// gitlabPipelineVarRoleAccepted reports whether role satisfies the
// restriction for owner/repo: no_one_allowed always does, and any other
// reported value does when the repository opted in. An empty role means
// the instance did not report the setting, which the opt-in never accepts.
func gitlabPipelineVarRoleAccepted(ctx context.Context, owner, repo, role string) bool {
	return role == forge.PipelineVarOverrideNoOneAllowed || (role != "" && gitlabPipelineVarOptedIn(ctx, owner, repo))
}

// GitLabPipelineVarOptInWarning is the plain-language warning printed every
// time fullsend relaxes the check for a repository.
func GitLabPipelineVarOptInWarning(repoFullName string) string {
	return fmt.Sprintf("%s sets allow_insecure_pipeline_variables, so fullsend accepts a \"Minimum role to use pipeline variables\" weaker than \"No one allowed\". "+
		"Anyone at that role who can run a pipeline on the default branch can point CI_API_V4_URL at a host they control, or set a BASH_ENV file variable that runs their code inside fullsend jobs, "+
		"and read the job token, the Poller token, the trigger token, and the role tokens.", repoFullName)
}
