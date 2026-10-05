package repos

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// Remote scaffold template paths within the fullsend-ai/fullsend repo.
const scaffoldGitHubShimPath = "internal/scaffold/fullsend-repo/templates/shim-per-repo.yaml"

// scaffoldGitLabPaths lists the GitLab scaffold files fetched from a pinned
// ref. The pipeline wrapper must come first: the webhook dispatcher files
// (#7771) are fetched only when it references them, so refs that predate
// the dispatcher keep their original scaffold (see fetchRemoteGitLabScaffold).
var scaffoldGitLabPaths = []struct {
	repoPath string
	outPath  string
}{
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-pipeline.yml", ".gitlab/ci/fullsend-pipeline.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-agent.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-poll.yml", ".gitlab/ci/fullsend-poll.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-dispatcher.yml", ".gitlab/ci/fullsend-dispatcher.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/trust-ci-server-ca.sh", ".gitlab/ci/scripts/trust-ci-server-ca.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/pin-ci-job-identity.sh", ".gitlab/ci/scripts/pin-ci-job-identity.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/select-gitlab-role-token.sh", ".gitlab/ci/scripts/select-gitlab-role-token.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/install-fullsend-cli.sh", ".gitlab/ci/scripts/install-fullsend-cli.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/run-poll-job.sh", ".gitlab/ci/scripts/run-poll-job.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/run-dispatcher-job.sh", ".gitlab/ci/scripts/run-dispatcher-job.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/run-agent-job.sh", ".gitlab/ci/scripts/run-agent-job.sh"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/checkout-mr-source.sh", ".gitlab/ci/scripts/checkout-mr-source.sh"},
}

// FetchRemoteScaffold fetches scaffold templates from fullsend-ai/fullsend
// at the given ref and renders them for the specified forge. This is used
// when fullsend_ref pins to a version that differs from the running
// binary, so embedded templates would be incorrect.
//
// Callers should skip this function when vendored is true: the running
// binary's embedded templates already match the binary being committed to
// the repo, so there is no version-skew concern and the remote fetch
// would add unnecessary API latency.
//
// Template paths (scaffoldGitHubShimPath, scaffoldGitLabPaths) are pinned
// to the current binary's layout. If the remote ref reorganises these
// paths, the fetch returns an error. GitLab mutation paths must not fall back
// to embedded templates: their pipeline-input contract may be incompatible
// with the pinned version. Other callers may use best-effort fallback.
func FetchRemoteScaffold(ctx context.Context, ghClient forge.Client,
	manifestRef, resolvedSHA, forgeName string,
	agentRunnerTags, controlRunnerTags []string,
	vendored bool,
) (scaffold.InstallFiles, error) {
	switch forgeName {
	case ForgeGitHub:
		return fetchRemoteGitHubScaffold(ctx, ghClient, manifestRef, resolvedSHA, vendored)
	case ForgeGitLab:
		return fetchRemoteGitLabScaffold(ctx, ghClient, manifestRef, resolvedSHA, agentRunnerTags, controlRunnerTags)
	default:
		return nil, fmt.Errorf("unsupported forge %q for remote scaffold fetch", forgeName)
	}
}

func fetchRemoteGitHubScaffold(ctx context.Context, client forge.Client,
	manifestRef, resolvedSHA string, vendored bool,
) (scaffold.InstallFiles, error) {
	content, err := client.GetFileContentAtRef(ctx, shimOwner, shimRepo, scaffoldGitHubShimPath, manifestRef)
	if err != nil {
		return nil, fmt.Errorf("fetching GitHub shim template at %s: %w", manifestRef, err)
	}

	opts := scaffold.RenderOptionsForInstall(vendored, true, resolvedSHA, manifestRef)
	rendered, err := scaffold.RenderTemplate("templates/shim-per-repo.yaml", content, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering remote GitHub shim: %w", err)
	}

	files := scaffold.InstallFiles{{
		Path:    ".github/workflows/fullsend.yaml",
		Content: scaffold.PrependManagedHeader(".github/workflows/fullsend.yaml", rendered),
		Mode:    "100644",
	}}

	for _, path := range scaffold.PerRepoThinCallerPaths() {
		remotePath := "internal/scaffold/fullsend-repo/" + path
		raw, fetchErr := client.GetFileContentAtRef(ctx, shimOwner, shimRepo, remotePath, manifestRef)
		if fetchErr != nil {
			if forge.IsNotFound(fetchErr) {
				continue
			}
			return nil, fmt.Errorf("fetching remote thin caller %s at %s: %w", path, manifestRef, fetchErr)
		}
		tcRendered, renderErr := scaffold.RenderTemplate(path, raw, opts)
		if renderErr != nil {
			return nil, fmt.Errorf("rendering remote thin caller %s: %w", path, renderErr)
		}
		files = append(files, scaffold.InstallFile{
			Path:    path,
			Content: scaffold.PrependManagedHeader(path, tcRendered),
			Mode:    "100644",
		})
	}

	return files, nil
}

func fetchRemoteGitLabScaffold(ctx context.Context, client forge.Client,
	manifestRef, resolvedSHA string, agentRunnerTags, controlRunnerTags []string,
) (scaffold.InstallFiles, error) {
	agentTagYAML := scaffold.FormatRunnerTags(agentRunnerTags)
	controlTagYAML := scaffold.FormatRunnerTags(controlRunnerTags)
	versionMarker := scaffold.FormatVersionMarker(resolvedSHA, manifestRef)
	fullsendVersion := scaffold.ResolveFullsendVersion(resolvedSHA, manifestRef)

	var files scaffold.InstallFiles
	// dispatcherRequired is set from the fetched pipeline wrapper: a ref
	// whose wrapper does not include the webhook dispatcher template
	// predates it, so its dispatcher files are skipped rather than
	// synthesized from newer embedded content. A wrapper that does
	// reference them must have both, or the fetch fails.
	dispatcherRequired := false
	for _, sp := range scaffoldGitLabPaths {
		isDispatcherFile := slices.Contains(gitlabDispatcherPaths(), sp.outPath)
		if isDispatcherFile && !dispatcherRequired {
			continue
		}
		content, err := client.GetFileContentAtRef(ctx, shimOwner, shimRepo, sp.repoPath, manifestRef)
		if err != nil {
			return nil, fmt.Errorf("fetching GitLab template %s at %s: %w", sp.repoPath, manifestRef, err)
		}

		if sp.outPath == fullsendPipelineInclude {
			dispatcherRequired = gitlabWrapperReferencesDispatcher(content)
		}

		rendered := strings.ReplaceAll(string(content), "__AGENT_RUNNER_TAGS__", agentTagYAML)
		rendered = strings.ReplaceAll(rendered, "__CONTROL_RUNNER_TAGS__", controlTagYAML)
		// Older pinned refs still ship the pre-split placeholder; stamp
		// remaining occurrences with agent tags so fetch does not leave
		// a literal __RUNNER_TAGS__ in generated CI.
		rendered = strings.ReplaceAll(rendered, "__RUNNER_TAGS__", agentTagYAML)
		rendered = strings.ReplaceAll(rendered, "__FULLSEND_VERSION__", fullsendVersion)
		if sp.outPath == fullsendPipelineInclude && versionMarker != "" {
			rendered = scaffold.InsertAfterDocStart(rendered, versionMarker)
		}
		files = append(files, scaffold.InstallFile{
			Path:    sp.outPath,
			Content: []byte(rendered),
			Mode:    scaffold.FileMode(sp.outPath),
		})
	}
	return files, nil
}
