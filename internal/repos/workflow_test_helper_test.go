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
	// The default test pin's reusable workflows forward the OpenAI WIF
	// identifiers, so openai-wif routes pass the compatibility check.
	serveOpenAIWIFWorkflows(fc, "v1.0.0", true)
	return fc
}

// serveOpenAIWIFWorkflows serves the reusable workflows at ref. When
// forward is true they have the structure of current releases: GCP secrets
// optional and the agent step receiving the FULLSEND_OPENAI_* variables;
// otherwise they predate that forwarding.
func serveOpenAIWIFWorkflows(fc *forge.FakeClient, ref string, forward bool) {
	for _, path := range openAIWIFReusableWorkflows {
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+path+"@"+ref] = reusableWorkflowFixture(forward, false)
	}
}

// reusableWorkflowFixture renders a reusable workflow with one agent step.
// forward adds the OpenAI WIF identifier env to that step; gcpRequired
// declares the GCP secrets required, as releases that predate optional GCP
// credentials do.
func reusableWorkflowFixture(forward, gcpRequired bool) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "name: reusable\non:\n  workflow_call:\n    secrets:\n")
	for _, name := range openAIWIFGCPSecrets {
		fmt.Fprintf(&b, "      %s:\n        required: %t\n", name, gcpRequired)
	}
	b.WriteString("jobs:\n  agent:\n    runs-on: ubuntu-latest\n    permissions:\n      id-token: write\n    steps:\n      - name: Run agent\n        uses: ./.defaults/\n        env:\n          REPO_FULL_NAME: ${{ github.repository }}\n")
	if forward {
		for _, name := range openAIWIFVariables {
			fmt.Fprintf(&b, "          %s: ${{ vars.%s }}\n", name, name)
		}
	}
	b.WriteString("        with:\n          agent: triage\n")
	return []byte(b.String())
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
