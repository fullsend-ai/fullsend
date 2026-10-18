package runtime

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// OpenCodeRuntime drives the OpenCode agent runtime (anomalyco/opencode),
// selected with `runtime: opencode` (#6035, unbound-force#510). Bootstrap
// translates the Claude-style agent into an OpenCode agent; Run executes
// `opencode run --format json` and normalizes its ndjson stream.
//
// OpenCode reads AGENTS.md natively, so it needs no ContextBridger. Project
// config discovery is disabled (see EnvExports), which also drops project
// AGENTS.md discovery; the run prelude re-attaches it via
// openCodeInstructionsConfig.
type OpenCodeRuntime struct{}

func (OpenCodeRuntime) Name() string { return "opencode" }

// System returns the fallback OTEL GenAI provider identity. OpenCode is
// multi-provider and does not implement ProviderResolver yet (#1935), so the
// system is the runtime itself rather than a model vendor.
func (OpenCodeRuntime) System() string { return "opencode" }

// ConfigDir returns the runner-owned OpenCode config directory
// (OPENCODE_CONFIG_DIR). It lives outside the cloned repo so the target repo
// cannot pre-seed it and a workspace reset does not clear it (#515). No
// ".opencode" suffix is needed: OPENCODE_CONFIG_DIR itself counts as a config
// dir (config/config.ts:425).
//
// The dir is root-owned 0775 (group sandbox) because the run prelude writes
// opencode.json here each iteration. Lockdown to 0750 with all writes via
// openshell is deferred to #7824.
func (OpenCodeRuntime) ConfigDir() string { return sandbox.SandboxOpenCodeConfig }

func (OpenCodeRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// EnvExports pins OpenCode's config discovery to the runner-owned dir and
// exports the variables Bootstrap and Run rely on.
//
// OPENCODE_CONFIG_CONTENT (Vertex provider + permission policy) and
// GOOGLE_APPLICATION_CREDENTIALS (WIF credential file) come from the harness;
// Run restores their pristine values after sourcing the agent-writable .env.
// The policy must ALLOW the tools a read-only agent needs: headless
// `opencode run` has no TTY and auto-rejects any permission request config
// does not pre-resolve (run.ts:795-808), so a bare "deny" fails every tool
// call. Write-path denial is unbound-force#515.
func (r OpenCodeRuntime) EnvExports() []string {
	return []string{
		fmt.Sprintf("export OPENCODE_CONFIG_DIR=%s", r.ConfigDir()),
		// Skip the workspace config walk so a hostile repo's
		// .opencode/opencode.json cannot widen tool permissions.
		"export OPENCODE_DISABLE_PROJECT_CONFIG=true",
		// Skip skill discovery from the workspace and $HOME so a hostile repo
		// cannot inject skills; runner-owned skills under ConfigDir still load.
		"export OPENCODE_DISABLE_EXTERNAL_SKILLS=true",
		"export OPENCODE_CONFIG_CONTENT",        // re-export: Vertex provider + permission policy
		"export GOOGLE_APPLICATION_CREDENTIALS", // re-export: WIF credential file
	}
}

// Compile-time interface assertions.
var (
	_ Runtime           = OpenCodeRuntime{}
	_ TranscriptHandler = OpenCodeRuntime{}
	_ DebugLogNamer     = OpenCodeRuntime{}
)
