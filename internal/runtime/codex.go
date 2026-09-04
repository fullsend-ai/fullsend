package runtime

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// codexDebugLogFile is the per-iteration debug artifact for codex runs
// (codex writes its tracing output to stderr, which Run will tee here).
const codexDebugLogFile = "codex-debug.log"

// CodexRuntime drives the Codex agent runtime (openai/codex, CLI `codex`,
// pinned in the sandbox image by CODEX_VERSION). Bootstrap
// (codex_bootstrap.go) translates the Claude-style agent definition into a
// runner-owned config.toml and installs the sandbox hook scripts behind an
// adapter codex invokes from hooks.json (codex_config.go); Run (codex_run.go)
// executes `codex exec --json` against a run-scoped OpenAI provider whose
// bearer token comes from a runner-seeded file, and normalizes the stream via
// parseCodexStream (codex_progress.go); transcripts are codex's rollout
// session JSONL files (codex_transcript.go). Selectable with `runtime: codex`
// in per-repo config or on an `agents:` entry (#6920, ADR 0099).
type CodexRuntime struct{}

func (CodexRuntime) Name() string { return "codex" }

// System returns the OTEL GenAI gen_ai.system value. Unlike pi and opencode,
// codex serves a single model vendor — it speaks the OpenAI Responses API and
// has no Vertex, Anthropic or Gemini path — so the system is the vendor
// ("openai"), not the runtime name.
func (CodexRuntime) System() string { return "openai" }

// ConfigDir returns the codex config directory inside the sandbox. It is
// exported to the agent process as CODEX_HOME (see EnvExports) and lives
// outside the cloned repo tree so the target repo cannot pre-seed it and
// workspace resets do not clear it. It is not a permission boundary: the
// agent process runs as the same user, so the runner-written files under it
// must be checksum-guarded before each launch rather than trusted.
func (CodexRuntime) ConfigDir() string { return sandbox.SandboxCodexConfig }

func (CodexRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// EnvExports pins codex's config location to the runner-owned path. codex
// refuses to start when CODEX_HOME does not exist, so Bootstrap must create
// the directory as well; the sandbox image bakes the same value as an ENV
// default for ad-hoc invocations (images/sandbox/Containerfile). codex keeps
// its other hygiene settings (update check, analytics, telemetry) in
// config.toml rather than the environment, so there is nothing else here.
func (r CodexRuntime) EnvExports() []string {
	return []string{fmt.Sprintf("export CODEX_HOME=%s", r.ConfigDir())}
}

// DebugLogName implements DebugLogNamer: the local artifact for codex's
// stderr trace output.
func (CodexRuntime) DebugLogName() string { return codexDebugLogFile }

// OpenAIAuthFile implements runtime.OpenAICredentialSeeder: the runner-owned
// file codex's auth command prints. codex caches the token for
// refresh_interval_ms and then re-runs the command, so re-seeding this file is
// what lets a running iteration follow a credential refresh — the same role
// pi's auth.json plays, and for the same reason (OpenShell 0.0.115 pins a
// revision-scoped placeholder to the generation it was issued for and refuses
// the unrevisioned alias, so the process environment cannot carry a
// placeholder that survives a refresh; a file the process re-reads can).
func (r CodexRuntime) OpenAIAuthFile() string { return r.ConfigDir() + "/" + codexTokenFile }

// OpenAIAuthSeed implements runtime.OpenAICredentialSeeder: the POSIX sh
// fragment that writes the placeholder the sandbox environment carries for
// OPENAI_API_KEY into the token file, atomically via rename so the auth
// command never reads a half-written file.
//
// It runs at iteration start, before the agent-writable .env is sourced, and
// the runner re-runs it through `sandbox exec` after every credential refresh
// once the sandbox has observed the new generation: an exec'd shell's
// environment holds the current placeholder, so the runner never needs to know
// the opaque revision. A value that is not a gateway placeholder fails the
// run — a real key in the sandbox environment would mean the provider path was
// bypassed, and forwarding it would defeat the design (ADR 0092).
func (r CodexRuntime) OpenAIAuthSeed() string {
	dir := shellQuote(r.ConfigDir())
	final := shellQuote(r.OpenAIAuthFile())
	tmp := shellQuote(r.OpenAIAuthFile() + ".fullsend")
	// piPlaceholderPrefix is the OpenShell gateway namespace, not a
	// pi-specific value; it is assembled from two parts there on purpose and
	// is referenced rather than copied so there is exactly one spelling of it
	// in the tree (fullsend#6716).
	return `case "${OPENAI_API_KEY:-}" in ` + piPlaceholderPrefix +
		`*OPENAI_API_KEY) ;; *) echo 'fullsend: OPENAI_API_KEY in the sandbox is not a gateway placeholder (openai provider not attached, or a real key reached the sandbox); refusing to run codex' >&2; exit 1 ;; esac` +
		` && case "$OPENAI_API_KEY" in *[!A-Za-z0-9_:]*) echo 'fullsend: OPENAI_API_KEY placeholder has unexpected characters; refusing to run codex' >&2; exit 1 ;; esac` +
		` && command -p mkdir -p ` + dir +
		` && printf '%s' "$OPENAI_API_KEY" > ` + tmp +
		` && command -p mv -f ` + tmp + ` ` + final
}

// Compile-time interface assertions.
var (
	_ Runtime           = CodexRuntime{}
	_ TranscriptHandler = CodexRuntime{}
	_ DebugLogNamer     = CodexRuntime{}

	_ OpenAICredentialSeeder = CodexRuntime{}
)
