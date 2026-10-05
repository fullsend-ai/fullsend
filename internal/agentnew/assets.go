package agentnew

import (
	"bytes"
	_ "embed"
	"fmt"
)

// BasePolicy returns the base sandbox policy a generated agent's harness
// names: filesystem, landlock and process rules with no network section, so
// egress comes only from the harness's providers (ADR 0065). The behaviour
// suite commits it for scenario harnesses, because OpenShell 0.1 refuses a
// sandbox with no policy and a policy file that declares no fields.
func BasePolicy() []byte {
	return bytes.Clone(basePolicy)
}

//go:embed templates/policies/base.yaml
var basePolicy []byte

// sharedAssets returns the files a generated agent depends on but does not
// own: the sandbox policy, and the schema validator when the validation loop
// is enabled. They are written only when absent and never overwritten,
// because every agent in the directory shares them. A per-repo install
// vendors no policy and the binary ships none, so this template is the only
// in-repo copy (#7268). Providers and their profiles are not written: the
// harness names them by bare name and the runner resolves them from the
// binary.
func sharedAssets(validationLoop bool) ([]File, error) {
	files := []File{}

	files = append(files, File{Path: "policies/base.yaml", Data: BasePolicy(), Mode: 0o644, Shared: true})

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
