package repos

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
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
		parts := strings.SplitN(r, "/", 2)
		fc.Repos = append(fc.Repos, forge.Repository{
			FullName:      r,
			Name:          parts[1],
			DefaultBranch: "main",
		})
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
