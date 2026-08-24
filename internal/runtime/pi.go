package runtime

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// PiRuntime drives the pi agent runtime (earendil-works/pi, CLI `pi`,
// pinned in the sandbox image). Bootstrap (pi_bootstrap.go) translates the
// Claude-style agent definition and installs the sandbox hook scripts behind
// a pi extension; Run (pi_run.go) executes `pi --print --mode json` and
// normalizes the stream via parsePiStream (pi_progress.go); transcripts are
// pi's session JSONL files (pi_transcript.go). Selected per org/repo with
// `runtime: pi` (#6464).
type PiRuntime struct{}

// piVertexExtensionPath is the interim Claude-on-Vertex provider for pi
// (twoGiants/pi-anthropic-vertex, pinned in the sandbox image by
// PI_ANTHROPIC_VERTEX_VERSION). pi's built-in google-vertex provider is
// Gemini-only and the upstream anthropic-vertex provider
// (earendil-works/pi#5262) is still open. Run loads it with `-e` alongside
// `--no-extensions`; it registers provider "anthropic-vertex". Project comes
// from GOOGLE_CLOUD_PROJECT, GCLOUD_PROJECT, ANTHROPIC_VERTEX_PROJECT_ID or
// GOOGLE_CLOUD_PROJECT_ID (first set wins; the fleet env exports both
// GOOGLE_CLOUD_PROJECT and ANTHROPIC_VERTEX_PROJECT_ID, and Run pins the
// former to the latter so pi cannot diverge from Claude Code), region from
// CLOUD_ML_REGION or GOOGLE_CLOUD_LOCATION. Credentials come from
// google-auth-library reading GOOGLE_APPLICATION_CREDENTIALS — the WIF
// external_account config plus the runner-refreshed OIDC token file the
// harness delivers via host_files, the same path Claude Code on Vertex uses.
// Its bundled Anthropic SDK would send a stray ANTHROPIC_API_KEY to Google
// and honour ANTHROPIC_VERTEX_BASE_URL as the endpoint, so Run unsets the
// ANTHROPIC_* variables for this provider. Swap for the upstream provider
// once #5262 ships in a pinned pi release.
const piVertexExtensionPath = sandbox.SandboxPiExtensionsDir + "/anthropic-vertex"

func (PiRuntime) Name() string { return "pi" }

// System returns the OTEL GenAI gen_ai.system value. Pi is multi-provider
// (anthropic, google-vertex, community extensions), so the system is the
// runtime itself rather than a single model vendor (same precedent as
// OpenCodeRuntime).
func (PiRuntime) System() string { return "pi" }

// ConfigDir returns the pi config directory inside the sandbox. It is
// exported to the agent process as PI_CODING_AGENT_DIR (see EnvExports) and
// lives outside the cloned repo tree so the target repo cannot pre-seed it
// and workspace resets do not clear it. It is not a permission boundary: the
// agent process runs as the same user and pi loads extensions from
// <dir>/extensions/, which is why Run passes --no-extensions and names the
// runner-supplied extensions explicitly. Session JSONL storage is
// <dir>/sessions (PI_CODING_AGENT_SESSION_DIR, also passed as --session-dir).
func (PiRuntime) ConfigDir() string { return sandbox.SandboxPiConfig }

func (PiRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// EnvExports pins pi's config and session locations to runner-owned paths
// and disables all startup network traffic (update checks, package update
// checks, telemetry). PI_OFFLINE does not affect the inference call itself;
// PI_TELEMETRY=0 additionally drops pi's provider attribution headers.
// Var names/semantics per earendil-works/pi docs/environment-variables.md
// (PI_CODING_AGENT_DIR, PI_CODING_AGENT_SESSION_DIR, PI_OFFLINE,
// PI_SKIP_VERSION_CHECK, PI_TELEMETRY) — re-verify against that doc when
// PI_VERSION moves. The sandbox image bakes the same values as ENV defaults
// for ad-hoc invocations (images/sandbox/Containerfile).
func (r PiRuntime) EnvExports() []string {
	return []string{
		fmt.Sprintf("export PI_CODING_AGENT_DIR=%s", r.ConfigDir()),
		fmt.Sprintf("export PI_CODING_AGENT_SESSION_DIR=%s", r.piSessionsDir()),
		"export PI_OFFLINE=1",
		"export PI_SKIP_VERSION_CHECK=1",
		"export PI_TELEMETRY=0",
	}
}

// Compile-time interface assertions.
var (
	_ Runtime           = PiRuntime{}
	_ TranscriptHandler = PiRuntime{}
	_ DebugLogNamer     = PiRuntime{}
)
