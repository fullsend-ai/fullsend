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
// The opt-in covers delivering runnable jobs, typed dispatch activation,
// and the managed poll schedules. It does not cover the webhook fast path
// or the trigger account: those publish one more credential whose safety
// rests on callers being unable to inject variables, so they stay deferred
// and agents start from the poll schedules instead.

type gitlabPipelineVarOptInKey struct{}

// WithGitLabPipelineVarOptIns returns a context that records which GitLab
// repositories in m opted in to a relaxed pipeline-variable setting. The
// restriction checks read it by owner/repo, so callers set it once after
// loading the manifest. A nil manifest records no opt-ins.
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
// value does when the repository opted in.
func gitlabPipelineVarRoleAccepted(ctx context.Context, owner, repo, role string) bool {
	return role == forge.PipelineVarOverrideNoOneAllowed || gitlabPipelineVarOptedIn(ctx, owner, repo)
}

// GitLabPipelineVarOptInWarning is the plain-language warning printed every
// time fullsend relaxes the check for a repository (ADR 0175).
func GitLabPipelineVarOptInWarning(repoFullName string) string {
	return fmt.Sprintf("%s sets allow_insecure_pipeline_variables (ADR 0175), so fullsend accepts a \"Minimum role to use pipeline variables\" weaker than \"No one allowed\". "+
		"Anyone at that role who can run a pipeline on the default branch can point CI_API_V4_URL at a host they control, or set a BASH_ENV file variable that runs their code inside fullsend jobs, "+
		"and read the job token, the Poller token, and the role tokens. The webhook fast path stays off; agents start from the poll schedules.", repoFullName)
}

// gitlabPipelineVarOptInFastPathNote explains why the webhook fast path and
// trigger account stay deferred for an opted-in repository.
const gitlabPipelineVarOptInFastPathNote = "allow_insecure_pipeline_variables does not enable the webhook fast path or the trigger account; they stay deferred until the setting is no_one_allowed"
