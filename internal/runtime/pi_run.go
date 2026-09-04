package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Model selection for pi. The fleet's harnesses name Claude-style aliases
// (opus, sonnet, ...), which are mapped onto pi's `provider/id` form here; a
// harness or agents: entry may also give `provider/id` directly. The
// ids are pi 0.84.2's Anthropic catalog
// (packages/ai/src/providers/data/anthropic.json), which the vendored
// anthropic-vertex extension registers verbatim; whether Vertex accepts each
// id is a lifecycle-test item (docs/runtimes.md). Both the provider and the
// final model string can be overridden from the runner environment.
const (
	piDefaultProvider = "anthropic-vertex"
	piDefaultModel    = "opus"
	// piXaiVertexProvider is the provider prefix for the xai-vertex extension:
	// used by translatePiModel to normalize short-form xai/ specs and by
	// buildPiRunCommand to gate extension loading and env hygiene.
	piXaiVertexProvider = "xai-vertex"
	// piOpenAIProvider is the lowercase provider name used as a gate in
	// buildPiRunCommand. Unlike Vertex providers, OpenAI models use pi's
	// built-in openai provider, which reads OPENAI_API_KEY from the env —
	// the run-scoped OpenShell provider injects a short-lived placeholder.
	piOpenAIProvider = "openai"
	// piProviderEnv replaces the provider prefix applied to bare model ids.
	// The model itself is resolved once by the CLI (--model, FULLSEND_MODEL,
	// or the FULLSEND_PI_MODEL alias on pi; #6526) and arrives in
	// RunParams.Model — the runtime does not read a model env var.
	piProviderEnv = "FULLSEND_PI_PROVIDER"
	// piRuntimeEnv tells skills running inside the sandbox which runtime
	// they are on, so a skill can take a runtime-specific path deliberately.
	piRuntimeEnv = "FULLSEND_RUNTIME"
)

var piModelAliases = map[string]string{
	"opus":   "claude-opus-4-6",
	"sonnet": "claude-sonnet-4-6",
	"haiku":  "claude-haiku-4-5",
	"fable":  "claude-fable-5-1",
}

// piDocumentedAliases lists the aliases pi resolves through docs/runtimes/pi.md
// (and docs/runtimes.md). Any alias in this set must have an entry in
// piModelAliases; one that does not is a missing-mapping bug — the bare
// alias is not a pi catalog id and will silently substitute a fallback
// model with the wrong wire id.
var piDocumentedAliases = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}

// validatePiModel returns an error if model is a documented alias that has
// no entry in the merged alias table (piModelAliases + configAliases).
// Bare ids and provider/id specs pass through without validation — only
// known aliases are checked, because those are the names that cannot
// resolve as bare ids on pi.
func validatePiModel(model string, configAliases map[string]string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		model = piDefaultModel
	}
	if strings.Contains(model, "/") {
		return nil
	}
	if piDocumentedAliases[model] {
		aliases := mergedPiModelAliases(configAliases)
		if _, ok := aliases[model]; !ok {
			return fmt.Errorf("model alias %q is documented but has no pi mapping; add it to piModelAliases with a catalog id enabled in the fleet's Vertex project, or map it for this repo under models.aliases in .fullsend/config.yaml", model)
		}
	}
	return nil
}

// mergedPiModelAliases returns a copy of piModelAliases with per-key
// overrides from configAliases applied. Always a fresh map, so a caller
// can never mutate the package-level table through it.
func mergedPiModelAliases(configAliases map[string]string) map[string]string {
	merged := maps.Clone(piModelAliases)
	maps.Copy(merged, configAliases)
	return merged
}

// translatePiModel resolves the harness/agent model (already overridden by
// the CLI when --model/FULLSEND_MODEL/FULLSEND_PI_MODEL apply) into pi's
// --model value: aliases map to catalog ids, bare ids get the provider
// prefix, provider/id passes through.
//
// configAliases, when non-nil, overrides piModelAliases per key — a repo
// can remap "sonnet" to a different generation without restating "opus"
// (#6882, models.aliases in .fullsend/config.yaml).
//
// Special case: the xai-vertex extension's model ids carry a publisher
// segment ("xai/grok-4.6") because pi sends Model.id on the wire verbatim
// and Vertex wants the publisher-qualified name. Both the short "xai/..."
// spec and a bare id under FULLSEND_PI_PROVIDER=xai-vertex are normalized
// to the three-segment "xai-vertex/xai/..." form. Without that, strings.Cut
// yields provider "xai" (or a two-segment spec the extension does not
// register), the gate in buildPiRunCommand never fires, and the run falls
// through to pi's built-in xai provider which requires XAI_API_KEY.
//
// Matching is case-insensitive throughout, because the gate uses
// strings.EqualFold for the same reason: pi resolves provider prefixes
// case-insensitively, so "XAI/grok-4.6" must not slip past normalization
// and reach the built-in provider with XAI_API_KEY still set.
func translatePiModel(model string, configAliases map[string]string) string {
	provider := strings.TrimSpace(os.Getenv(piProviderEnv))
	if provider == "" {
		provider = piDefaultProvider
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = piDefaultModel
	}
	// Resolve the alias first, then normalise the *resolved* value: an
	// alias may map to a provider/id spec (config.ValidateModelAliases
	// accepts it), and running the xai normalisation and the "/" passthrough on
	// the alias name instead would re-prefix the spec
	// ("anthropic-vertex/anthropic-vertex/…") and skip the xai-vertex
	// gate in buildPiRunCommand. The alias table is consulted once: a
	// value that is itself an alias key is rejected at config validation.
	if id, ok := mergedPiModelAliases(configAliases)[model]; ok {
		model = id
	}
	if spec, ok := normalizeXaiVertexModel(provider, model); ok {
		return spec
	}
	if strings.Contains(model, "/") {
		return model
	}
	return provider + "/" + model
}

// piModelProvider is the lowercase provider prefix of the pi model spec that
// model resolves to — the value the gates in buildPiRunCommand and
// NeedsOpenAIProvider branch on. pi matches provider prefixes
// case-insensitively, so the prefix is folded here once: otherwise
// "Anthropic-Vertex/..." would run on Vertex with the ANTHROPIC_* unset
// skipped, and "OpenAI/..." would not be recognised as needing the OpenAI
// run-scoped provider.
func piModelProvider(model string, configAliases map[string]string) string {
	provider, _, _ := strings.Cut(translatePiModel(model, configAliases), "/")
	return strings.ToLower(provider)
}

// normalizeXaiVertexModel renders the canonical three-segment spec for the
// xai-vertex provider, or reports false when the input is not for it.
//
// Three inputs reach this provider, and all must land on the same spec:
//
//	"xai/grok-4.6"             (any case)  -> "xai-vertex/xai/grok-4.6"
//	"xai-vertex/xai/grok-4.6"  (any case)  -> "xai-vertex/xai/grok-4.6"
//	"grok-4.6" with FULLSEND_PI_PROVIDER=xai-vertex -> "xai-vertex/xai/grok-4.6"
//
// The third matters because a harness may still select this provider with
// a bare id plus the provider env var (the only way before harness `model:`
// accepted "/", #6570). Left alone it would render the two-segment
// "xai-vertex/grok-4.6", which the extension does not register — pi then
// substitutes a fallback model with the wrong wire id and only warns.
func normalizeXaiVertexModel(provider, model string) (string, bool) {
	const wirePrefix = "xai/"
	head, rest, hasSlash := strings.Cut(model, "/")
	switch {
	case hasSlash && strings.EqualFold(head, piXaiVertexProvider):
		// Already three-segment; re-render so the provider segment is canonical.
		if inner, id, ok := strings.Cut(rest, "/"); ok && strings.EqualFold(inner, "xai") {
			return piXaiVertexProvider + "/" + wirePrefix + id, true
		}
		return piXaiVertexProvider + "/" + wirePrefix + rest, true
	case hasSlash && strings.EqualFold(head, "xai"):
		return piXaiVertexProvider + "/" + wirePrefix + rest, true
	case !hasSlash && strings.EqualFold(provider, piXaiVertexProvider):
		return piXaiVertexProvider + "/" + wirePrefix + model, true
	}
	return "", false
}

// piBareModelID strips the provider prefix from a pi model spec.
// It removes only the first segment (the provider) so that three-segment
// specs like "xai-vertex/xai/grok-4.6" return "xai/grok-4.6" (the wire
// model id) rather than just "grok-4.6".
func piBareModelID(spec string) string {
	if _, after, ok := strings.Cut(spec, "/"); ok {
		return after
	}
	return spec
}

// piThinkingLevels are pi's --thinking values; the harness effort values are
// a subset, so the mapping is identity (docs/runtimes.md config-key table).
var piThinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// piDefaultThinking is passed when the harness sets no effort. pi's own
// default is "medium" (core/defaults.js DEFAULT_THINKING_LEVEL); Claude Code
// runs at "high" on Vertex/API-key, so without this the same agent would
// reason at a lower level on pi. pi maps the level onto Anthropic's adaptive
// effort and clamps it for models without reasoning.
const piDefaultThinking = "high"

// piThinkingFor returns the --thinking level for a harness effort value and
// whether effort was a recognised level; an empty effort yields the default.
func piThinkingFor(effort string) (string, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return piDefaultThinking, true
	}
	if piThinkingLevels[effort] {
		return effort, true
	}
	return piDefaultThinking, false
}

// piHooksMissingExit is the exit code the run command uses when the hook
// adapter or manifest is not where Bootstrap put it. pi itself silently
// skips a missing -e path (package-manager.ts resolveLocalExtensionSource
// returns on !existsSync), so without this guard a deleted or renamed
// extension would give a hookless iteration that looks healthy.
const piHooksMissingExit = 97

// piConfigTamperedExit is the exit code of the config-dir integrity guard
// for the openai provider (auth.json or models.json present). Distinct from
// piHooksMissingExit so Run can name the actual cause instead of reporting a
// hook-adapter problem.
const piConfigTamperedExit = 98

// buildPiRunCommand renders the in-sandbox command line. Security-relevant
// flags: --no-approve and defaultProjectTrust "never" keep repo-owned .pi/
// out; --no-extensions with explicit -e means only the runner-vetted
// extensions load; --tools is pi's strict allowlist across built-in and
// extension tools. Whether the hook adapter is loaded is decided from the
// runner's own signal (params.HooksSettingsPath, set when the harness
// enables security — the same signal ClaudeRuntime uses for --settings),
// never from the agent-writable manifest, and the command fails closed if
// the adapter or manifest file is missing.
func buildPiRunCommand(params RunParams, m *piManifest) string {
	r := PiRuntime{}
	envFile := sandbox.SandboxWorkspace + "/.env"
	hooksEnabled := params.HooksSettingsPath != ""
	hooksExt := r.ConfigDir() + "/" + piHooksExtensionFile

	// The agent definition's model is the fallback when the runner resolved
	// none; EffectiveModel is shared with NeedsOpenAIProvider so the launch
	// and the provider decision cannot disagree (#6920).
	model := EffectiveModel(params.Model, m.Model)
	modelSpec := translatePiModel(model, params.ModelAliases)
	// piModelProvider folds the provider prefix case-insensitively (pi
	// matches it the same way), so it is the single source of truth for
	// this gate and for NeedsOpenAIProvider.
	provider := piModelProvider(model, params.ModelAliases)
	vertex := provider == piDefaultProvider
	xaiVertex := provider == piXaiVertexProvider
	openai := provider == piOpenAIProvider

	parts := []string{"cd " + shellQuote(params.RepoDir)}
	// Resolve the pi binary before the agent-writable .env is sourced and
	// make the name read-only: .env could otherwise define a pi() function
	// or put its own pi first on PATH and run agent code after the guards,
	// and a readonly assignment attempt aborts the sourcing shell instead.
	// The launch below uses the path, which no function or alias can shadow.
	parts = append(parts, "&& "+piBinaryPin())
	if hooksEnabled {
		// Before .env: that file is agent-writable and could otherwise
		// shadow the guard's tools with functions or a PATH entry.
		parts = append(parts, "&& "+piHooksGuard(hooksExt, r.piManifestPath()))
	}
	if openai {
		// Same reason: check the config dir before .env can shadow `test`,
		// then seed pi's auth.json with the placeholder the environment
		// carries before .env can replace OPENAI_API_KEY with another
		// provider's placeholder.
		parts = append(parts, "&& "+piOpenAIConfigGuard(r.ConfigDir()), "&& "+PiOpenAIAuthSeed(r.ConfigDir()))
	}
	parts = append(parts,
		"&& . "+shellQuote(envFile),
		// .env is agent-writable; re-pin the runner-owned locations and the
		// offline switches after it so a rewritten .env cannot move pi's
		// config dir out from under the guards below.
		"&& "+strings.Join(r.EnvExports(), " && "),
		"&& export "+piManifestEnv+"="+shellQuote(r.piManifestPath()),
		"&& export "+piRuntimeEnv+"=pi",
		// pi's built-in google-vertex (Gemini) provider resolves credentials
		// from GOOGLE_APPLICATION_CREDENTIALS + GOOGLE_CLOUD_PROJECT +
		// GOOGLE_CLOUD_LOCATION, all required; the fleet exports the region
		// as CLOUD_ML_REGION (what the Anthropic-on-Vertex extension reads),
		// so mirror it and Gemini on Vertex is just a model name.
		`&& export GOOGLE_CLOUD_LOCATION="${GOOGLE_CLOUD_LOCATION:-$CLOUD_ML_REGION}"`,
	)
	if vertex {
		// Claude-on-Vertex: the bundled Anthropic SDK would send a stray
		// ANTHROPIC_API_KEY to Google as X-Api-Key and honour
		// ANTHROPIC_VERTEX_BASE_URL as the endpoint; AUTH_TOKEN and BASE_URL
		// are cleared for hygiene. The project is pinned to the variable
		// Claude Code on Vertex is driven by, so both runtimes hit the same
		// GCP project regardless of an ambient GOOGLE_CLOUD_PROJECT (the
		// extension reads that one first).
		parts = append(parts,
			"&& unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_VERTEX_BASE_URL",
			`&& export GOOGLE_CLOUD_PROJECT="${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}"`,
		)
	}
	if xaiVertex {
		// Grok-on-Vertex: unset XAI_API_KEY so pi's built-in xai provider
		// (which requires the key for xAI's native API) cannot shadow this
		// extension.
		//
		// Default XAI_VERTEX_PROJECT_ID to the fleet's Vertex project so the
		// extension does not fall back to an ambient GOOGLE_CLOUD_PROJECT that
		// may point somewhere else -- but only when the runner has not set it.
		// Each Vertex provider resolves its own project variable
		// (XAI_VERTEX_PROJECT_ID, ANTHROPIC_VERTEX_PROJECT_ID,
		// GOOGLE_CLOUD_PROJECT), so pi happily serves Grok, Claude and Gemini
		// from different projects in one process; overriding an explicit value
		// here would collapse that and leave no way to point Grok at its own
		// project. That matters when Grok is enabled in Model Garden for a
		// different project than Claude -- the call then fails 403
		// PERMISSION_DENIED with nothing to tune.
		parts = append(parts,
			"&& unset XAI_API_KEY",
			`&& export XAI_VERTEX_PROJECT_ID="${XAI_VERTEX_PROJECT_ID:-${ANTHROPIC_VERTEX_PROJECT_ID:-$GOOGLE_CLOUD_PROJECT}}"`,
		)
	}
	if openai {
		// OpenAI via runner-exchanged WIF or static OPENAI_API_KEY: the
		// run-scoped OpenShell provider injects OPENAI_API_KEY as a
		// placeholder, which the seed above put in auth.json for pi to
		// re-read per request. Unset OPENAI_BASE_URL and AZURE_OPENAI_API_KEY
		// so a stray .env cannot redirect traffic or inject a different
		// credential, and clear OPENAI_API_KEY itself so pi's resolution
		// cannot fall through to a value .env planted in the environment.
		// NODE_OPTIONS/NODE_PATH would let .env load code into pi before
		// it starts; with the credential endpoint-bound at the gateway that
		// code could only sabotage this run, but there is no reason to
		// allow it.
		parts = append(parts, "&& unset OPENAI_BASE_URL AZURE_OPENAI_API_KEY OPENAI_API_KEY NODE_OPTIONS NODE_PATH")
		// Config-dir integrity guard, second pass: .env itself could have
		// written auth.json or models.json just now. `unset -f` is a special
		// builtin, which a sourced function cannot shadow, so it restores the
		// real `test` before the check; the first pass (before .env) already
		// caught anything written between iterations. This runs for the
		// openai provider even when hooks are disabled, because the threat is
		// credential leak, not tool misuse.
		parts = append(parts, "&& unset -f test command grep tr sed printf pi", "&& "+piOpenAIConfigGuard(r.ConfigDir()))
	}
	parts = append(parts,
		`&& "$`+piBinaryVar+`"`,
		"--print",
		"--mode json",
		"--no-approve",
		"--no-extensions",
		"--no-prompt-templates",
		"--no-themes",
		"--session-dir "+shellQuote(r.piSessionsDir()),
	)
	if vertex {
		// The interim Claude-on-Vertex provider is only needed for the
		// anthropic-vertex model spec; other providers get pi's built-ins.
		parts = append(parts, "-e "+shellQuote(piVertexExtensionPath))
	}
	if xaiVertex {
		parts = append(parts, "-e "+shellQuote(piXaiVertexExtensionPath))
	}
	// The openai provider needs no -e extension and deliberately no
	// --api-key: that flag outranks auth.json in pi's resolution order and
	// would pin the iteration to the placeholder it launched with.
	if hooksEnabled {
		parts = append(parts, "-e "+shellQuote(hooksExt))
	}
	if m.Tools != nil {
		tools := m.Tools
		if len(tools) == 0 {
			// An agent that lists only tools pi cannot provide (or only
			// Skill) gets no built-in tools rather than the defaultTools set.
			parts = append(parts, "--no-builtin-tools")
		} else {
			parts = append(parts, "--tools "+shellQuote(strings.Join(tools, ",")))
		}
	}

	parts = append(parts, "--model "+shellQuote(modelSpec))

	level, _ := piThinkingFor(params.Effort)
	parts = append(parts, "--thinking "+shellQuote(level))

	// In --print mode pi reads a non-TTY stdin to EOF as extra input and
	// blocks while the pipe stays open (verified on 0.84.2: an idle pipe
	// hangs, /dev/null proceeds). Close it here so the run never depends on
	// how the sandbox exec wires stdin.
	// The validation loop replaces the prompt on a retry iteration to inject
	// the previous failure (#1050/#6494); every runtime must honour it, or
	// feedback_mode silently degrades to a blind retry.
	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	parts = append(parts, shellQuote(prompt), "</dev/null")

	if params.Debug != "" {
		// pi has no debug-file flag; in debug mode its stderr goes to the
		// artifact ExtractDebugLog downloads instead of the console.
		parts = append(parts, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+piDebugLogFile))
	}
	return strings.Join(parts, " ")
}

// piManifestEnv tells the hook extension where the manifest is.
const piManifestEnv = "FULLSEND_PI_MANIFEST"

// piBinaryVar holds the absolute path of the pi binary, resolved before
// .env is sourced and marked read-only.
const piBinaryVar = "FULLSEND_PI_BIN"

// piBinaryPin is the POSIX sh fragment that records where pi is. `command
// -v` is a builtin; `readonly` is a special builtin, so a later assignment
// in a sourced file is an error: under a POSIX sh such as dash (what
// `sh -c` is in the sandbox image) it aborts the sourcing shell, and under
// any shell the assignment fails and the pinned value stands.
func piBinaryPin() string {
	return `readonly ` + piBinaryVar + `="$(command -v pi)" && test -n "$` + piBinaryVar + `" || { echo 'fullsend: pi not found on PATH' >&2; exit 127; }`
}

// piPlaceholderPrefix is the namespace of OpenShell gateway placeholders,
// assembled from two parts on purpose: OpenShell 0.0.110+ resets any
// model request whose body contains the contiguous prefix (it is treated
// as credential-bearing traffic), so a source file that spelled it out
// could not be read by an agent running inside a sandbox.
const piPlaceholderPrefix = "openshell:resolve:env" + ":"

// piOpenAIAuthFile is the pi credential file the runner owns for the
// openai provider: pi's AuthStorage re-reads it whenever its revision
// changes and resolves the key per request (packages/coding-agent/src/core/
// auth-storage.ts, model-registry.ts at 0.84.3), which is what lets a
// running iteration follow a credential refresh. OpenShell 0.0.115 pins a
// revision-scoped placeholder (`v<opaque>_KEY` under piPlaceholderPrefix) to the
// value of the generation it was issued for, and refuses the unrevisioned
// alias for an endpoint-bound credential (crates/openshell-core/src/
// secrets.rs resolve_placeholder; both verified against a live gateway on
// 2026-08-27), so the process environment cannot carry a placeholder that
// follows a refresh — a file pi re-reads can.
const piOpenAIAuthFile = "auth.json"

// piOpenAIAuthShape is the whole-file shape (whitespace removed) the config
// guard accepts for auth.json besides pi's own empty `{}`: exactly one
// openai api_key entry whose key is a gateway placeholder for OPENAI_API_KEY.
const piOpenAIAuthShape = `[{]"openai":[{]"type":"api_key","key":"` + piPlaceholderPrefix + `[A-Za-z0-9_]*OPENAI_API_KEY"[}][}]`

// PiOpenAIAuthSeed is the POSIX sh fragment that writes the placeholder the
// sandbox environment carries for OPENAI_API_KEY into pi's auth.json under
// configDir (atomically, via rename — pi locks and re-reads the file). It
// runs at iteration start, before the agent-writable .env is sourced, and
// the runner re-runs it through `sandbox exec` after every credential
// refresh, once the sandbox has observed the new generation: an exec'd
// shell's environment holds the current placeholder, so the runner never
// needs to know the opaque revision. A value that is not a gateway
// placeholder fails the run: a real key in the sandbox environment would
// mean the provider path was bypassed, and forwarding it would defeat the
// design.
func PiOpenAIAuthSeed(configDir string) string {
	dir := shellQuote(configDir)
	final := shellQuote(configDir + "/" + piOpenAIAuthFile)
	tmp := shellQuote(configDir + "/" + piOpenAIAuthFile + ".fullsend")
	return `case "${OPENAI_API_KEY:-}" in ` + piPlaceholderPrefix + `*OPENAI_API_KEY) ;; *) echo 'fullsend: OPENAI_API_KEY in the sandbox is not a gateway placeholder (openai provider not attached, or a real key reached the sandbox); refusing to run the openai provider' >&2; exit 1 ;; esac` +
		` && case "$OPENAI_API_KEY" in *[!A-Za-z0-9_:]*) echo 'fullsend: OPENAI_API_KEY placeholder has unexpected characters; refusing to run the openai provider' >&2; exit 1 ;; esac` +
		` && command -p mkdir -p ` + dir +
		` && printf '{"openai":{"type":"api_key","key":"%s"}}\n' "$OPENAI_API_KEY" > ` + tmp +
		` && command -p mv -f ` + tmp + ` ` + final
}

// OpenAIAuthSeed implements OpenAICredentialSeeder: the fragment that seeds
// pi's auth.json with the sandbox's current OPENAI_API_KEY placeholder. The
// runner runs it through `sandbox exec` after every credential refresh; Run
// emits the same fragment at iteration start.
func (r PiRuntime) OpenAIAuthSeed() string { return PiOpenAIAuthSeed(r.ConfigDir()) }

// OpenAIAuthFile implements OpenAICredentialSeeder: pi's auth.json inside
// the sandbox, which the runner greps for the new placeholder to confirm a
// re-seed landed.
func (r PiRuntime) OpenAIAuthFile() string { return r.ConfigDir() + "/" + piOpenAIAuthFile }

// piOpenAIConfigGuard is the POSIX sh fragment that fails closed when the
// pi config directory carries models.json, or an auth.json that is anything
// but pi's own empty `{}` or the runner-seeded openai placeholder entry
// (piOpenAIAuthShape). models.json is the only way to change pi's openai
// base URL (no env override exists), and a redirect to another allowed
// REST host is the placeholder-leak vector described in ADR 0025; any
// other auth.json content would supply a different key or provider. pi
// 0.84.3 writes an empty auth.json (`{}`) on every start
// (AuthStorage.ensureFileExists), so the file's presence proves nothing —
// only its content does. The comparison strips whitespace and matches the
// whole file, and additionally rejects any `\u00` JSON escape (pi never
// writes escaped keys; a planted `"open\u0061i"` would otherwise slip a
// substring check). Run emits the guard twice: before the agent-writable
// .env is sourced (nothing can shadow the builtins yet) and after it, behind
// `unset -f test command grep tr` (`unset` is a special builtin, so a
// function .env defined cannot stand in); `command -p` uses the default
// PATH, so a PATH swap cannot either. It applies whether or not hooks are
// enabled.
func piOpenAIConfigGuard(configDir string) string {
	auth := shellQuote(configDir + "/" + piOpenAIAuthFile)
	models := shellQuote(configDir + "/models.json")
	return fmt.Sprintf(
		`{ ! test -f %s && { ! test -s %s || { ! command -p grep -q '\\u00' %s && command -p tr -d ' \n\t\r' < %s | command -p grep -qxE '([{][}]|%s)'; }; } || { echo 'fullsend: pi config dir has models.json or an auth.json that is not the runner-seeded openai placeholder; refusing to run the openai provider (placeholder-leak risk)' >&2; exit %d; }; }`,
		models, auth, auth, auth, piOpenAIAuthShape, piConfigTamperedExit,
	)
}

// piHooksGuard is the POSIX sh fragment run before pi when hooks are
// expected: the adapter must exist and be byte-identical to the embedded
// copy (the agent can write to the config dir between iterations, as it can
// to Claude's hooks.json), and the manifest must exist. Otherwise it exits
// piHooksMissingExit, which terminates the `sh -c` before pi starts; the
// message goes to the runner's stderr, not the debug log.
func piHooksGuard(hooksExt, manifestPath string) string {
	sum := sha256.Sum256(piHooksExtensionJS)
	// `command -p` bypasses shell functions and uses the system default
	// PATH, so nothing the agent left in the environment can stand in for
	// sha256sum or cut; test, [ and echo are builtins.
	return fmt.Sprintf(`{ test -f %s && test -f %s && [ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ] || { echo 'fullsend: pi hook adapter or manifest missing or modified; refusing to run unhooked' >&2; exit %d; }; }`,
		shellQuote(hooksExt), shellQuote(manifestPath), shellQuote(hooksExt), shellQuote(hex.EncodeToString(sum[:])), piHooksMissingExit)
}

// Run executes one agent iteration and normalizes pi's --mode json stream
// into AgentEvents. pi exits 0 on model error in json mode, so the stream's
// verdict overrides the exit code (#2786/#5361).
func (r PiRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	m, err := readPiManifest(params.SandboxName, r.piManifestPath())
	if err != nil {
		return -1, err
	}
	if params.HooksSettingsPath != "" && (m.Hooks == nil || m.Hooks.Groups == nil) {
		// Same predicate as the adapter's `wired` check (a groups array,
		// possibly empty): without it the adapter would load and block every
		// tool call, so fail before spending an iteration on it.
		return -1, fmt.Errorf("security is enabled but the pi manifest at %s carries no hook plan (Bootstrap ran without the sandbox hook config, or the manifest was modified)", r.piManifestPath())
	}
	if _, ok := piThinkingFor(params.Effort); !ok {
		printer.StepWarn(fmt.Sprintf("effort %q is not a pi thinking level; running at --thinking %s", sanitizeOutput(params.Effort), piDefaultThinking))
	}
	if len(params.FallbackModels) > 0 {
		// pi has no built-in fallback chain; a fullsend extension for it is
		// tracked on #6527. Say so rather than silently dropping the list.
		printer.StepWarn(fmt.Sprintf("fallback models %s are not supported on pi yet and are ignored", sanitizeOutput(strings.Join(params.FallbackModels, ","))))
	}
	if err := validatePiModel(EffectiveModel(params.Model, m.Model), params.ModelAliases); err != nil {
		return -1, err
	}
	cmd := buildPiRunCommand(params, m)

	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()

	var reader io.Reader = stdout
	if params.OutputPath != "" {
		f, ferr := os.Create(params.OutputPath)
		if ferr != nil {
			printer.StepWarn(fmt.Sprintf("Failed to create %s: ", params.OutputPath) + ferr.Error())
		} else {
			defer f.Close()
			reader = io.TeeReader(stdout, f)
		}
	}

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer)
		handler = renderer.Handle
	}

	modelSpec := translatePiModel(EffectiveModel(params.Model, m.Model), params.ModelAliases)
	// Telemetry and the renderer get the bare model id, as they do for
	// Claude Code, so runs group by model across runtimes; the provider is
	// gen_ai.system's job and stays visible on the command line.
	metrics.Model = piBareModelID(modelSpec)
	// The wire carries no CLI version and the model only on the first
	// assistant message; Bootstrap's preflight and the resolved model are
	// known up front, so emit the InitEvent here and drop the parser's.
	handler(InitEvent{Model: metrics.Model, Version: m.PiVersion})

	var lastResult *ResultEvent
	innerHandler := handler
	handler = func(evt AgentEvent) {
		switch e := evt.(type) {
		case InitEvent:
			return
		case ResultEvent:
			lastResult = &e
			metrics.NumTurns = e.NumTurns
			metrics.TotalCostUSD = e.TotalCostUSD
			metrics.InputTokens = e.InputTokens
			metrics.OutputTokens = e.OutputTokens
			metrics.ReasoningTokens = e.ReasoningTokens
			metrics.CacheCreationInputTokens = e.CacheCreationInputTokens
			metrics.CacheReadInputTokens = e.CacheReadInputTokens
		case ToolUseEvent:
			metrics.ToolCalls.Add(1)
		}
		innerHandler(evt)
	}

	if _, parseErr := parsePiStream(reader, handler); parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		cancel()
		io.Copy(io.Discard, reader)
	}

	waitErr := execCmd.Wait()
	exitCode := -1
	if execCmd.ProcessState != nil {
		exitCode = execCmd.ProcessState.ExitCode()
	}
	if waitErr != nil && execCmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}
	if exitCode == piHooksMissingExit && params.HooksSettingsPath != "" {
		return exitCode, fmt.Errorf("pi hook adapter or manifest missing or modified in %s; refusing to run unhooked (was Bootstrap run, or did the agent change it?)", r.ConfigDir())
	}
	if exitCode == piConfigTamperedExit {
		return exitCode, fmt.Errorf("pi config dir %s has models.json or an openai entry in auth.json; refusing to run the openai provider because either can redirect or replace the runner's credential (pi's own empty auth.json is fine; did the agent write there between iterations?)", r.ConfigDir())
	}

	if exitCode == 0 && lastResult != nil && lastResult.IsError {
		msg := lastResult.ErrorMessage
		if msg == "" {
			msg = "stopReason " + lastResult.Subtype
		}
		printer.StepWarn("pi exited 0 but the stream reports an error: " + sanitizeOutput(msg))
		return 1, nil
	}
	return exitCode, nil
}

// ClearIterationArtifacts terminates processes the previous iteration left
// running (see killStrayProcesses), then removes its outputs and sessions
// so transcripts and output files are per-iteration.
func (r PiRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(sandbox.Exec, sandboxName, os.Stderr)
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/* %s",
		shellQuote(r.WorkspaceDir()), shellQuote(r.piSessionsDir()), shellQuote(r.WorkspaceDir()+"/"+piDebugLogFile))
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

// DebugLogName implements DebugLogNamer.
func (PiRuntime) DebugLogName() string { return piDebugLogFile }
