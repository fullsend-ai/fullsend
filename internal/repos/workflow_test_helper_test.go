package repos

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func makeWorkflow(ref string) []byte {
	return []byte(fmt.Sprintf(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@%s
    with:
      install_mode: per-repo
`, ref))
}

func newFakeClientForBatch(repos ...string) *forge.FakeClient {
	fc := forge.NewFakeClient()
	for _, r := range repos {
		fc.PipelineVarOverrideRoles[r] = forge.PipelineVarOverrideNoOneAllowed
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
	}
	// Valid ref-upgrade fixtures must supply the target GitLab scaffold;
	// production must not silently substitute embedded files for a pinned ref.
	for _, ref := range []string{"v2.5.0", "v3.0.0"} {
		for _, path := range scaffoldGitLabPaths {
			content, err := scaffold.GitLabPerRepoFile(path.outPath)
			if err != nil {
				panic(err)
			}
			fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+path.repoPath+"@"+ref] = content
		}
	}
	// A GitHub OpenAI install that cannot fetch its explicit pin's scaffold
	// fails closed, so the default test pin serves the embedded per-repo
	// shim and thin callers, which forward the key and match what the
	// embedded fallback would have produced.
	shim, err := scaffold.PerRepoShimTemplate()
	if err != nil {
		panic(err)
	}
	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@v1.0.0"] = shim
	for _, path := range scaffold.PerRepoThinCallerPaths() {
		raw, err := scaffold.FullsendRepoFile(path)
		if err != nil {
			panic(err)
		}
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/internal/scaffold/fullsend-repo/"+path+"@v1.0.0"] = raw
	}
	return fc
}

func makeWorkflowSHAPinned(sha, tag string) []byte {
	return []byte(fmt.Sprintf(`name: fullsend
on:
  workflow_dispatch:
jobs:
  dispatch:
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@%s # %s
    with:
      install_mode: per-repo
`, sha, tag))
}
