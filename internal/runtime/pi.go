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

// piVertexExtensionPath is the Claude-on-Vertex provider for pi
// (fullsend-ai/pi-anthropic-vertex, pinned in the sandbox image by
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
// The extension bundles no Anthropic SDK, reads no ANTHROPIC_* variable
// except ANTHROPIC_VERTEX_PROJECT_ID, and derives its endpoint host from the
// region, so none of the ANTHROPIC_* family can steer it. Run's unset stays
// load-bearing for a different reason: pi's built-in anthropic provider is
// registered in the same process and discovers ANTHROPIC_AUTH_TOKEN and the
// API-key variables from the environment, so a stray value in the
// agent-writable .env would authenticate a direct-to-Anthropic path that
// never reaches Vertex. Keep the unset. Swap for the upstream provider once
// #5262 ships in a pinned pi release.
const piVertexExtensionPath = sandbox.SandboxPiExtensionsDir + "/anthropic-vertex"

// piXaiVertexExtensionPath is the Grok-on-Vertex provider for pi
// (fullsend-ai/pi-xai-vertex, pinned in the sandbox image by
// PI_XAI_VERTEX_VERSION). Grok on Vertex speaks the OpenAI-completions
// protocol, which neither pi's built-in xai provider (requires XAI_API_KEY
// for xAI's native API) nor google-vertex (Gemini-only) covers. Run loads
// it with `-e` alongside `--no-extensions`; it registers provider
// "xai-vertex". Project comes from XAI_VERTEX_PROJECT_ID,
// GOOGLE_CLOUD_PROJECT, or ANTHROPIC_VERTEX_PROJECT_ID (first set wins; Run
// pins XAI_VERTEX_PROJECT_ID to ANTHROPIC_VERTEX_PROJECT_ID so both Vertex
// providers hit the same GCP project). Credentials come from
// google-auth-library reading GOOGLE_APPLICATION_CREDENTIALS — the same ADC
// path the anthropic-vertex extension uses. Run unsets XAI_API_KEY so pi's
// built-in xai provider cannot shadow this one (#6571).
const piXaiVertexExtensionPath = sandbox.SandboxPiExtensionsDir + "/xai-vertex"

// OpenAI on pi: unlike the Vertex providers, OpenAI uses a runner-exchanged
// short-lived access token (WIF or a static OPENAI_API_KEY) delivered as a
// credential placeholder through the run-scoped OpenShell provider, not
// Vertex ADC. The model ids are two-segment ("openai/gpt-5.6-luna") and pass
// through to pi's built-in openai provider unchanged. The provider constant
// is piOpenAIProvider in pi_run.go.

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

// EnvExports pins pi's config and session locations to runner-owned paths,
// disables all startup network traffic (update checks, package update
// checks, telemetry) and disables the module loader's on-disk transpile
// cache. PI_OFFLINE does not affect the inference call itself;
// PI_TELEMETRY=0 additionally drops pi's provider attribution headers.
// Var names/semantics per earendil-works/pi docs/environment-variables.md
// (PI_CODING_AGENT_DIR, PI_CODING_AGENT_SESSION_DIR, PI_OFFLINE,
// PI_SKIP_VERSION_CHECK, PI_TELEMETRY) — re-verify against that doc when
// PI_VERSION moves. The sandbox image bakes the same values as ENV defaults
// for ad-hoc invocations (images/sandbox/Containerfile).
//
// JITI_FS_CACHE is not pi's own variable but jiti's, the loader pi imports
// every `-e` module through (createJiti in core/extensions/loader.ts passes
// no fsCache, so jiti resolves it from JITI_FS_CACHE, then JITI_CACHE, then
// true). jiti probes for a node_modules directory next to the module that
// created it — the bundled chunk under <pi>/dist/bundle/chunks/ in the
// published package, so <pi>/dist/bundle/chunks/node_modules/.cache/jiti —
// and falls back to $TMPDIR/jiti; in the sandbox image pi is
// root-installed and ships no such directory, so it is /tmp/jiti — writable
// by the agent and persistent across iterations. A cache entry is validated only against a
// ` /* v9-<hash of the source> */` trailer, so a body rewritten with the
// trailer left in place executes while the source file is untouched: that
// is a way around both the extension tree-hash preflight
// (piExtensionsGuard) and the hook adapter's SHA-256 check (piHooksGuard),
// neither of which can see it. Setting the variable to false makes jiti
// ignore any planted entry and create no cache directory at all (verified
// on pi 0.84.4, and jiti is still 2.7.0 at the pinned 0.85.0 —
// internal/runtime/testdata/pi/jiti-cache-check.sh reproduces both
// halves). Run re-exports these after the agent-writable
// .env is sourced, so the agent cannot switch the cache back on, and
// harness validation reserves the JITI_* family from extension env.
//
// The cache is one lever of several: the rest of jiti's environment
// (JITI_ALIAS above all, which remaps a module specifier to another file)
// is cleared outright right after .env, on every provider path — see
// piLoaderEnvNames in pi_run.go.
func (r PiRuntime) EnvExports() []string {
	return []string{
		fmt.Sprintf("export PI_CODING_AGENT_DIR=%s", r.ConfigDir()),
		fmt.Sprintf("export PI_CODING_AGENT_SESSION_DIR=%s", r.piSessionsDir()),
		"export PI_OFFLINE=1",
		"export PI_SKIP_VERSION_CHECK=1",
		"export PI_TELEMETRY=0",
		"export JITI_FS_CACHE=false",
	}
}

// Compile-time interface assertions.
var (
	_ Runtime           = PiRuntime{}
	_ TranscriptHandler = PiRuntime{}
	_ DebugLogNamer     = PiRuntime{}

	_ OpenAICredentialSeeder = PiRuntime{}
)
