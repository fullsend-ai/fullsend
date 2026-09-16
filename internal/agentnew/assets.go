package agentnew

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// sharedAssets returns the scaffold files a generated agent depends on but
// does not own: the sandbox policy, the role's providers and profiles, and
// the schema validator when the validation loop is enabled.
//
// These are written only when absent and are never overwritten, because they
// are shared by every agent in the directory. They are needed at all because
// a per-repo install vendors none of them: CollectPerRepoInstallFiles returns
// only the shim workflow and one thin caller, and CI's workspace layering
// covers providers/ only — policies/ and profiles/ are not in LAYERED_DIRS
// (see TestLayeredDirsMatchWorkspacePreparation), so the copies this command
// writes are the ones every run uses. The policy has no scaffold copy at
// all: fleet agents resolve theirs from fullsend-ai/agents by URL, and the
// template here is the seed for repo-local agents (#6834, #7268).
//
// Providers and profiles come from the existing scaffold embed, so a
// generated tree is byte-identical to what CI layers in for providers/.
func sharedAssets(role Role, validationLoop bool) ([]File, error) {
	files := []File{}

	policy, err := templates.ReadFile("templates/policies/base.yaml")
	if err != nil {
		return nil, fmt.Errorf("reading base policy: %w", err)
	}
	files = append(files, File{Path: "policies/base.yaml", Data: policy, Mode: 0o644, Shared: true})

	// Providers and profiles are referenced by path rather than by bare
	// name. A bare name with no definition on disk does not fail loudly: the
	// embedded provider fallback fills in only the OpenAI provider, so every
	// other name degrades to a warning and then a sandbox that cannot reach
	// Vertex — the "agent crashes at 0s" symptom in the BYOA guide.
	for _, path := range append(append([]string{}, role.Providers...), role.Profiles...) {
		data, err := scaffold.FullsendRepoFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s from the embedded scaffold: %w", path, err)
		}
		files = append(files, File{Path: path, Data: data, Mode: 0o644, Shared: true})
	}

	if validationLoop {
		script, err := templates.ReadFile("templates/scripts/validate-output-schema.sh")
		if err != nil {
			return nil, fmt.Errorf("reading validation script: %w", err)
		}
		files = append(files, File{
			Path: "scripts/validate-output-schema.sh", Data: script, Mode: 0o755, Shared: true,
		})
	}
	return files, nil
}
