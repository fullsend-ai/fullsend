package agentnew

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/harness"
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
// skips policies/ (the embedded scaffold has no policies/ directory, so the
// [[ -d ]] guard fails) and never had profiles/ in LAYERED_DIRS at all.
//
// The bytes come from the existing scaffold embed wherever possible, so a
// generated tree is byte-identical to what CI layers in and to what the
// fleet runs.
func sharedAssets(role Role, validationLoop bool) ([]File, error) {
	files := []File{}

	policy, err := templates.ReadFile("templates/policies/base.yaml")
	if err != nil {
		return nil, fmt.Errorf("reading base policy: %w", err)
	}
	files = append(files, File{Path: "policies/base.yaml", Data: policy, Mode: 0o644, Shared: true})

	// Path-referenced providers and profiles are copied from the embedded
	// scaffold. A bare name is skipped: the OpenAI provider is binary-only
	// (appendEmbeddedProviderDefs fills it in at run time) and passing it to
	// FullsendRepoFile would look for a file the scaffold does not ship.
	for _, path := range append(append([]string{}, role.Providers...), role.Profiles...) {
		if !harness.IsProviderPath(path) {
			continue
		}
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
