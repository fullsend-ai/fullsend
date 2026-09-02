package runtime

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// OpenCodeRuntime drives the OpenCode agent runtime (anomalyco/opencode, CLI
// `opencode`). Bootstrap (opencode_bootstrap.go) translates the Claude-style
// agent definition into an OpenCode agent under the runner-owned config dir
// and injects the Vertex provider + permission-deny config; Run
// (opencode_run.go) executes `opencode run --format json` and normalizes the
// ndjson stream via parseOpenCodeStream (opencode_progress.go); transcripts
// are the interim tee'd output.jsonl (opencode_transcript.go). Selected per
// org/repo with `runtime: opencode` (#6035, unbound-force#510).
//
// Unlike pi, OpenCode reads AGENTS.md natively (no CLAUDE.md bridge — it does
// not implement ContextBridger). Its runner-owned config is pointed at by
// OPENCODE_CONFIG_DIR, and OPENCODE_DISABLE_PROJECT_CONFIG=true suppresses
// the workspace-directory config walk so a target repo's own
// .opencode/opencode.json cannot widen tool permissions. That flag also
// suppresses project-level AGENTS.md discovery (instruction.ts:81-133), so
// the run prelude writes a runner-owned opencode.json with
// config.instructions pointing at the workspace AGENTS.md as an absolute
// path, which bypasses the flag (instruction.ts:140-145). The resulting
// file merges with OPENCODE_CONFIG_CONTENT via mergeConfigConcatArrays.
type OpenCodeRuntime struct{}

func (OpenCodeRuntime) Name() string { return "opencode" }

// System returns the fallback OTEL GenAI provider identity. OpenCode is
// multi-provider (Anthropic, OpenAI, Google, etc.) and does not yet implement
// ProviderResolver, so the system is the runtime itself rather than a model
// vendor. The actual serving endpoint may be capturable from opencode's
// stream/export events in a future PR once the event schema is confirmed
// (see #1935).
func (OpenCodeRuntime) System() string { return "opencode" }

// ConfigDir returns the runner-owned OpenCode config directory inside the
// sandbox. It is pointed at OpenCode via OPENCODE_CONFIG_DIR (see EnvExports)
// and lives outside the cloned repo tree so the target repo cannot pre-seed
// it and a workspace reset does not clear it (path convention pinned by #515).
//
// OpenCode treats a directory as a config dir when it ends in ".opencode" or
// equals OPENCODE_CONFIG_DIR (config/config.ts:425), so the runner-owned dir
// needs no ".opencode" suffix. The hook plugin adapter #515 installs lives
// under this dir at plugins/ and is SHA-256 integrity-gated before .env is
// sourced (openCodeHooksExtensionPath / the fail-closed guard in Run).
func (OpenCodeRuntime) ConfigDir() string { return sandbox.SandboxOpenCodeConfig }

func (OpenCodeRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// EnvExports pins OpenCode's config discovery to the runner-owned dir and
// declares the two variables Bootstrap/Run rely on being present in the
// sandbox environment:
//
//   - OPENCODE_CONFIG_DIR points config discovery at the runner-owned dir so
//     the workspace .opencode/ is never on the search path.
//   - OPENCODE_CONFIG_CONTENT carries the Vertex provider registration plus the
//     tool-permission policy the harness delivers; it merges last
//     (config/config.ts:468), so it wins over any agent-authored repo config.
//     IMPORTANT: a non-interactive `opencode run` (Run, below) has no TTY, so
//     opencode auto-REJECTS every permission request that is not pre-resolved
//     by config (run.ts:810-819). The injected policy must therefore ALLOW the
//     tools a read-only agent needs (read, grep, glob, list, and read-only
//     bash) — a bare "deny" policy makes every tool call fail. Write-path
//     denial + the compensating hook adapter are unbound-force#515.
//   - GOOGLE_APPLICATION_CREDENTIALS is the WIF credential file, the same ADC
//     path Claude-on-Vertex and pi-on-Vertex use.
//
// OPENCODE_CONFIG_CONTENT and GOOGLE_APPLICATION_CREDENTIALS are delivered by
// the harness (env.sandbox / host_files); they are re-exported here so the
// prelude inherits them and so docs/runtimes.md's config-key table stays in
// sync.
//
// OPENCODE_DISABLE_PROJECT_CONFIG=true prevents OpenCode from walking the
// workspace directory for .opencode/opencode.json (which could widen tool
// permissions). The run prelude re-attaches workspace AGENTS.md via a
// runner-owned opencode.json with config.instructions (see
// openCodeInstructionsConfig).
func (r OpenCodeRuntime) EnvExports() []string {
	return []string{
		fmt.Sprintf("export OPENCODE_CONFIG_DIR=%s", r.ConfigDir()),
		// Suppress workspace config walk so a hostile repo's .opencode/
		// opencode.json cannot widen tool permissions. The run prelude
		// re-attaches workspace AGENTS.md via config.instructions.
		"export OPENCODE_DISABLE_PROJECT_CONFIG=true",
		"export OPENCODE_CONFIG_CONTENT",        // Vertex provider + permission denials (merges last)
		"export GOOGLE_APPLICATION_CREDENTIALS", // WIF credential file
	}
}

// Compile-time interface assertions.
var (
	_ Runtime           = OpenCodeRuntime{}
	_ TranscriptHandler = OpenCodeRuntime{}
	_ DebugLogNamer     = OpenCodeRuntime{}
)
