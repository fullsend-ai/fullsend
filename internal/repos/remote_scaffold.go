package repos

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// Remote scaffold template paths within the fullsend-ai/fullsend repo.
const scaffoldGitHubShimPath = "internal/scaffold/fullsend-repo/templates/shim-per-repo.yaml"

var scaffoldGitLabPaths = []struct {
	repoPath string
	outPath  string
}{
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-dispatch.yml", ".gitlab/ci/fullsend-dispatch.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-agent.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-poll.yml", ".gitlab/ci/fullsend-poll.yml"},
	{"internal/scaffold/fullsend-repo-gitlab/.gitlab-ci.yml", ".gitlab-ci.yml"},
}

// FetchRemoteScaffold fetches scaffold templates from fullsend-ai/fullsend
// at the given ref and renders them for the specified forge. This is used
// when fullsend_ref pins to a version that differs from the running
// binary, so embedded templates would be incorrect.
//
// Template paths (scaffoldGitHubShimPath, scaffoldGitLabPaths) are pinned
// to the current binary's layout. If the remote ref reorganises these
// paths, the fetch fails gracefully and the caller falls back to embedded
// templates — cross-version compatibility is best-effort.
func FetchRemoteScaffold(ctx context.Context, ghClient forge.Client,
	manifestRef, resolvedSHA, forgeName string,
	runnerTags []string,
	credentialMode string,
) (scaffold.InstallFiles, error) {
	switch forgeName {
	case ForgeGitHub:
		return fetchRemoteGitHubScaffold(ctx, ghClient, manifestRef, resolvedSHA, credentialMode)
	case ForgeGitLab:
		return fetchRemoteGitLabScaffold(ctx, ghClient, manifestRef, resolvedSHA, runnerTags)
	default:
		return nil, fmt.Errorf("unsupported forge %q for remote scaffold fetch", forgeName)
	}
}

func fetchRemoteGitHubScaffold(ctx context.Context, client forge.Client,
	manifestRef, resolvedSHA, credentialMode string,
) (scaffold.InstallFiles, error) {
	content, err := client.GetFileContentAtRef(ctx, shimOwner, shimRepo, scaffoldGitHubShimPath, manifestRef)
	if err != nil {
		return nil, fmt.Errorf("fetching GitHub shim template at %s: %w", manifestRef, err)
	}

	opts := scaffold.RenderOptionsForInstall(false, true, resolvedSHA, manifestRef)
	opts.CredentialMode = credentialMode
	rendered, err := scaffold.RenderTemplate("templates/shim-per-repo.yaml", content, opts)
	if err != nil {
		return nil, fmt.Errorf("rendering remote GitHub shim: %w", err)
	}

	return scaffold.InstallFiles{{
		Path:    ".github/workflows/fullsend.yaml",
		Content: scaffold.PrependManagedHeader(".github/workflows/fullsend.yaml", rendered),
		Mode:    "100644",
	}}, nil
}

func fetchRemoteGitLabScaffold(ctx context.Context, client forge.Client,
	manifestRef, resolvedSHA string, runnerTags []string,
) (scaffold.InstallFiles, error) {
	tagYAML := scaffold.FormatRunnerTags(runnerTags)
	versionMarker := scaffold.FormatVersionMarker(resolvedSHA, manifestRef)

	var files scaffold.InstallFiles
	for _, sp := range scaffoldGitLabPaths {
		content, err := client.GetFileContentAtRef(ctx, shimOwner, shimRepo, sp.repoPath, manifestRef)
		if err != nil {
			return nil, fmt.Errorf("fetching GitLab template %s at %s: %w", sp.repoPath, manifestRef, err)
		}

		rendered := strings.ReplaceAll(string(content), "__RUNNER_TAGS__", tagYAML)
		if sp.outPath == ".gitlab/ci/fullsend-dispatch.yml" && versionMarker != "" {
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
