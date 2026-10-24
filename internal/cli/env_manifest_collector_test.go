package cli

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/agentnew"
	"github.com/fullsend-ai/fullsend/internal/envmanifest"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// Fixtures for the sandbox env manifest collector: the probe agent and
// sandbox names, a shell-safe trace ID, a valid W3C traceparent, and the
// provenance recorded for harness-delivered variables.
const (
	envManifestAgentName   = "env-manifest-probe"
	envManifestSandboxName = "fs-env-manifest"
	envManifestTraceID     = "01234567-89ab-4cde-8f01-23456789abcd"
	envManifestTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	envManifestProvenance  = "fullsend agent new Vertex scaffold (internal/agentnew); the fleet delivers the same variables through env/gcp-vertex.env in fullsend-ai/agents"
)

// productionSandboxRuntimes lists every runtime the sandbox env contract must
// represent: the registry entries a real agent can dispatch under.
// excludedSandboxRuntimes holds the registry's local test stubs, per the
// design's D5 stub exclusion — exactly dummy and dummy-playback. opencode is
// not a stub for this manifest even though the registry's config validation
// groups it with the stubs and keeps it out of config.ValidRuntimes: it
// resolves in the registry, so it stays in the production set with its empty
// export set. TestEnvManifestRuntimeEnumeration pins the enumeration to the
// registry.
var (
	productionSandboxRuntimes = []string{"claude", "codex", "opencode", "pi"}
	excludedSandboxRuntimes   = []string{"dummy", "dummy-playback"}
)

// sandboxEnvGates transcribes, for each conditionally emitted sandbox env
// var, the emitter's guard that decides whether it is set. The collector
// fails when an emitter derives a conditional key missing here — a new gated
// export cannot enter the manifest undescribed — and when a gate's key no
// longer derives from any emitter, so the descriptions cannot go stale.
var sandboxEnvGates = map[string]string{
	"FULLSEND_SLUG":               "the harness sets a slug",
	"FULLSEND_OUTPUT_SCHEMA":      "an output schema is configured (validation_loop.schema, or the legacy FULLSEND_OUTPUT_SCHEMA runner env) and its file uploads into the sandbox",
	"FULLSEND_OUTPUT_FILE":        "the legacy runner_env FULLSEND_OUTPUT_FILE is set",
	"FULLSEND_FETCH_URL":          "the runtime fetch service is enabled (ADR 0038)",
	"FULLSEND_FETCH_TOKEN":        "the runtime fetch service is enabled (ADR 0038)",
	"FULLSEND_TRACE_ID":           "trace-ID injection succeeds; the run warns and continues when it fails",
	"TIRITH_FAIL_ON":              "security.sandbox_hooks.tirith.fail_on is set",
	"TIRITH_REQUIRED":             "Tirith scanning is required (security.sandbox_hooks.tirith.enabled, default true)",
	"FULLSEND_EGRESS_ALLOWLIST":   "an SSRF egress allowlist is configured, including the auto-merged forge egress entry",
	"FULLSEND_ITERATION_DEADLINE": "writeIterationEnv succeeds; the run warns and continues when it fails",
	"FULLSEND_TIMEOUT_MINUTES":    "writeIterationEnv succeeds; the run warns and continues when it fails",
	"TRACEPARENT":                 "writeIterationEnv succeeds; the run warns and continues when it fails",
}

// envManifestKeyRe matches a KEY= assignment token while excluding $KEY
// references: an emitter's output assigns the names it exports and reads the
// ones it prefixes with $.
var envManifestKeyRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])([A-Za-z_][A-Za-z0-9_]*)=`)

// envManifestRegistryCaseRe matches a case clause of the Resolve switch at
// switch depth: gofmt keeps a clause's runtime labels on the line that starts
// with "case".
var envManifestRegistryCaseRe = regexp.MustCompile(`(?m)^\tcase\b[^\n]*:`)

// envManifestRegistryLabelRe extracts the double-quoted labels from one case
// clause of the resolution registry's switch.
var envManifestRegistryLabelRe = regexp.MustCompile(`"([^"\n]*)"`)

// TestEnvManifestCollector asserts the manifest the collector derives by
// executing the production sandbox-env emitters: the unconditional bootstrap
// and iteration-env exports, the gated exports with their gates, each
// production runtime's export set, the scaffold-delivered Vertex variables,
// and the reserved set the runner, OIDC and provider maps merge into.
func TestEnvManifestCollector(t *testing.T) {
	m := collectSandboxEnvManifest(t)

	t.Run("always", func(t *testing.T) {
		assert.Equal(t, []string{
			"FULLSEND_OUTPUT_DIR",
			"FULLSEND_ROLE",
			"FULLSEND_TARGET_REPO_DIR",
			"PATH",
		}, m.Exports.Always)
	})

	t.Run("conditional", func(t *testing.T) {
		want := []string{
			"FULLSEND_EGRESS_ALLOWLIST",
			"FULLSEND_FETCH_TOKEN",
			"FULLSEND_FETCH_URL",
			"FULLSEND_ITERATION_DEADLINE",
			"FULLSEND_OUTPUT_FILE",
			"FULLSEND_OUTPUT_SCHEMA",
			"FULLSEND_SLUG",
			"FULLSEND_TIMEOUT_MINUTES",
			"FULLSEND_TRACE_ID",
			"TIRITH_FAIL_ON",
			"TIRITH_REQUIRED",
			"TRACEPARENT",
		}
		got := slices.Collect(maps.Keys(m.Exports.Conditional))
		slices.Sort(got)
		assert.Equal(t, want, got)
		for _, tc := range []struct {
			key          string
			gateContains string
		}{
			{"FULLSEND_SLUG", "slug"},
			{"FULLSEND_OUTPUT_SCHEMA", "schema"},
			{"FULLSEND_OUTPUT_FILE", "FULLSEND_OUTPUT_FILE"},
			{"FULLSEND_FETCH_URL", "fetch service"},
			{"FULLSEND_FETCH_TOKEN", "fetch service"},
			{"FULLSEND_TRACE_ID", "warns"},
			{"TIRITH_FAIL_ON", "tirith"},
			{"TIRITH_REQUIRED", "tirith"},
			{"FULLSEND_EGRESS_ALLOWLIST", "egress"},
			{"FULLSEND_ITERATION_DEADLINE", "writeIterationEnv"},
			{"FULLSEND_TIMEOUT_MINUTES", "writeIterationEnv"},
			{"TRACEPARENT", "writeIterationEnv"},
		} {
			assert.Contains(t, m.Exports.Conditional[tc.key], tc.gateContains, "gate for %s", tc.key)
		}
	})

	t.Run("runtime specific", func(t *testing.T) {
		for _, tc := range []struct {
			runtime string
			want    []string
		}{
			{"claude", []string{"CLAUDE_CONFIG_DIR"}},
			{"codex", []string{"CODEX_HOME"}},
			{"opencode", []string{
				"OPENCODE_CONFIG_DIR",
				"OPENCODE_DISABLE_EXTERNAL_SKILLS",
				"OPENCODE_DISABLE_PROJECT_CONFIG",
			}},
			{"pi", []string{
				"JITI_FS_CACHE",
				"PI_CODING_AGENT_DIR",
				"PI_CODING_AGENT_SESSION_DIR",
				"PI_OFFLINE",
				"PI_SKIP_VERSION_CHECK",
				"PI_TELEMETRY",
			}},
		} {
			t.Run(tc.runtime, func(t *testing.T) {
				assert.Equal(t, tc.want, m.Exports.RuntimeSpecific[tc.runtime])
			})
		}
		assert.Len(t, m.Exports.RuntimeSpecific, len(productionSandboxRuntimes))
	})

	t.Run("harness delivered", func(t *testing.T) {
		want := []string{
			"ANTHROPIC_VERTEX_PROJECT_ID",
			"CLAUDE_CODE_USE_VERTEX",
			"CLOUD_ML_REGION",
			"GOOGLE_APPLICATION_CREDENTIALS",
		}
		got := slices.Collect(maps.Keys(m.HarnessDelivered))
		slices.Sort(got)
		assert.Equal(t, want, got)
		for key, delivered := range m.HarnessDelivered {
			assert.False(t, delivered.RunnerGuaranteed, "%q is harness-delivered, not runner-guaranteed", key)
			assert.Equal(t, envManifestProvenance, delivered.Provenance, "provenance for %q", key)
		}
	})

	t.Run("reserved", func(t *testing.T) {
		assert.Equal(t, []string{
			"ACTIONS_ID_TOKEN_REQUEST_TOKEN",
			"ACTIONS_ID_TOKEN_REQUEST_URL",
			"ALL_PROXY",
			"BASH_ENV",
			"CDPATH",
			"CURL_CA_BUNDLE",
			"DYLD_INSERT_LIBRARIES",
			"ENV",
			"FULLSEND_FETCH_TOKEN",
			"FULLSEND_FETCH_URL",
			"FULLSEND_GCP_OIDC_AUTH_FILE",
			"FULLSEND_GCP_OIDC_URL",
			"FULLSEND_GCP_PROJECT_ID",
			"FULLSEND_GCP_WIF_PROVIDER",
			"FULLSEND_GITHUB_RUNNER_SECRETS",
			"FULLSEND_GITHUB_RUNNER_SECRETS_FILE",
			"FULLSEND_GITLAB_TRIGGER_ACCOUNT_TOKEN",
			"FULLSEND_INFERENCE_GATEWAY_API_KEY",
			"FULLSEND_ITERATION_DEADLINE",
			"FULLSEND_OPENAI_API_KEY",
			"FULLSEND_OPENAI_AUDIENCE",
			"FULLSEND_OPENAI_IDENTITY_PROVIDER_ID",
			"FULLSEND_OPENAI_SERVICE_ACCOUNT_ID",
			"FULLSEND_OUTPUT_DIR",
			"FULLSEND_OUTPUT_FILE",
			"FULLSEND_OUTPUT_SCHEMA",
			"FULLSEND_ROLE",
			"FULLSEND_RUNNER_PATH",
			"FULLSEND_SLUG",
			"FULLSEND_TARGET_REPO_DIR",
			"FULLSEND_TIMEOUT_MINUTES",
			"FULLSEND_TRIGGER_TOKEN",
			"FULLSEND_WEBHOOK_SECRET",
			"GH_WORKFLOW_TOKEN",
			"GIT_ASKPASS",
			"GIT_CONFIG_GLOBAL",
			"GIT_EXEC_PATH",
			"GIT_SSH_COMMAND",
			"GIT_TEMPLATE_DIR",
			"HOME",
			"HOSTALIASES",
			"HTTPS_PROXY",
			"HTTP_PROXY",
			"IFS",
			"JAVA_TOOL_OPTIONS",
			"LD_AUDIT",
			"LD_LIBRARY_PATH",
			"LD_PRELOAD",
			"NODE_EXTRA_CA_CERTS",
			"NODE_OPTIONS",
			"NO_PROXY",
			"OPENAI_API_KEY",
			"PATH",
			"PERL5OPT",
			"PROMPT_COMMAND",
			"PYTHONPATH",
			"PYTHONSTARTUP",
			"RUBYOPT",
			"SHELL",
			"SSL_CERT_DIR",
			"SSL_CERT_FILE",
			"TRACEPARENT",
		}, m.Reserved)
	})

	t.Run("renders deterministically", func(t *testing.T) {
		assert.Equal(t, envmanifest.SchemaVersion, m.SchemaVersion)
		first, err := m.Render()
		require.NoError(t, err)
		second, err := m.Render()
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})
}

// TestEnvManifestRuntimeEnumeration pins the collector's runtime enumeration
// to the production resolution registry: productionSandboxRuntimes must be
// exactly the registry's resolvable runtimes minus excludedSandboxRuntimes.
// The registry is internal/runtime.Resolve's switch; it exports no name list,
// and probing Resolve with candidate names cannot discover an entry nobody
// asked about (config.ValidRuntimes omits opencode, so config is no proxy),
// so the enumeration is driven from the registry's own case labels. A runtime
// registered without a manifest decision, a retired runtime the collector
// still lists, or an exclusion naming nothing the registry resolves all fail
// here.
func TestEnvManifestRuntimeEnumeration(t *testing.T) {
	registry := envManifestRegistryRuntimes(t)

	for _, stub := range excludedSandboxRuntimes {
		require.Contains(t, registry, stub, "excludedSandboxRuntimes excludes %q, which the registry does not resolve", stub)
	}
	want := slices.DeleteFunc(slices.Clone(registry), func(name string) bool {
		return slices.Contains(excludedSandboxRuntimes, name)
	})
	got := slices.Clone(productionSandboxRuntimes)
	slices.Sort(got)
	assert.Equal(t, want, got, "productionSandboxRuntimes must be exactly the registry's runtimes minus the stub exclusion")

	for _, name := range productionSandboxRuntimes {
		_, err := agentruntime.Resolve(name)
		require.NoError(t, err, "resolving runtime %q", name)
	}
}

// envManifestRegistryRuntimes returns the sorted runtime names the production
// resolution registry dispatches on, read from internal/runtime's Resolve
// switch — the registry's own case labels. Resolve offers no exported
// enumeration and cannot reveal names it is never asked about, so the labels
// are the only honest source; any registry edit that adds, removes, or
// reshapes a case changes what this function returns, and a registry.go that
// can no longer be parsed fails the caller. The empty label is Resolve's
// default alias for claude, not a distinct runtime, and is dropped.
func envManifestRegistryRuntimes(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "runtime", "registry.go"))
	require.NoError(t, err, "reading the resolution registry")
	start := strings.Index(string(src), "func Resolve(")
	require.NotEqual(t, -1, start, "func Resolve not found in internal/runtime/registry.go")
	body := string(src)[start:]
	if end := strings.Index(body, "\nfunc "); end != -1 {
		body = body[:end]
	}
	seen := map[string]bool{}
	var names []string
	for _, clause := range envManifestRegistryCaseRe.FindAllString(body, -1) {
		for _, match := range envManifestRegistryLabelRe.FindAllStringSubmatch(clause, -1) {
			name := match[1]
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	require.NotEmpty(t, names, "no runtime case labels found in func Resolve")
	slices.Sort(names)
	return names
}

// collectSandboxEnvManifest derives the sandbox environment-variable contract
// by executing the production emitters that build the sandbox environment:
// bootstrapEnv with every optional gate on and off, the iteration-env
// command, injectTraceID, the hook-env append through ClaudeRuntime.Bootstrap,
// every production runtime's EnvExports, and the agent-new Vertex scaffold,
// plus the live reserved map after init() merges the OIDC and provider-only
// sets into it. Keys are parsed from the emitters' real output, so the
// manifest cannot disagree with the emitters without this test failing.
func collectSandboxEnvManifest(t *testing.T) *envmanifest.Manifest {
	t.Helper()
	b := envmanifest.NewBuilder()

	gatedOn := bootstrapEnvExportKeys(t, true)
	gatedOff := bootstrapEnvExportKeys(t, false)
	for key := range gatedOff {
		require.True(t, gatedOn[key], "bootstrapEnv emits %q only with its gates off", key)
	}
	// These variables are assigned readonly WITHOUT export in the
	// bootstrap/launch script (env_data.go, runnerPathCaptureLine): the
	// sandbox never sees them as env vars, only as shell variables.
	// Exclude them from every export category.
	readonlyNotExported := map[string]bool{
		"FULLSEND_RUNNER_PATH":  true, // runnerPathCaptureLine
		"FULLSEND_ENV_PYTHON":   true, // envDataPythonVar
		"FULLSEND_ENV_EXPORTS":  true, // envDataExportsVar
		"FULLSEND_TRUSTED_PATH": true, // envDataTrustedPathVar
		"FULLSEND_READLINK":     true, // envDataReadlinkVar
	}
	alwaysKeys := map[string]bool{}
	conditional := map[string]bool{}
	for key := range gatedOn {
		if readonlyNotExported[key] {
			continue
		}
		if gatedOff[key] {
			b.AddAlways(key)
			alwaysKeys[key] = true
		} else {
			conditional[key] = true
		}
	}

	iterationCmd := iterationEnvCommand(30, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), envManifestTraceparent)
	for _, key := range parseExportedEnvKeys(iterationCmd) {
		if !readonlyNotExported[key] && !alwaysKeys[key] {
			conditional[key] = true
		}
	}

	for _, key := range traceIDExportKeys(t) {
		if !readonlyNotExported[key] && !alwaysKeys[key] {
			conditional[key] = true
		}
	}
	for _, key := range hookEnvExportKeys(t) {
		if !readonlyNotExported[key] && !alwaysKeys[key] {
			conditional[key] = true
		}
	}
	for key := range conditional {
		gate, ok := sandboxEnvGates[key]
		require.True(t, ok, "sandbox env var %q is emitted under a gate the collector does not describe; add it to sandboxEnvGates", key)
		b.AddConditional(key, gate)
	}
	for key := range sandboxEnvGates {
		require.True(t, conditional[key], "sandboxEnvGates describes %q, which no emitter emits under a gate anymore", key)
	}

	for _, name := range productionSandboxRuntimes {
		backend, err := agentruntime.Resolve(name)
		require.NoError(t, err, "resolving runtime %q", name)
		b.AddRuntimeSpecific(name, parseExportedEnvKeys(backend.EnvExports()...)...)
	}

	for _, key := range harnessDeliveredKeys(t) {
		b.AddHarnessDelivered(key, envManifestProvenance)
	}

	for key := range reservedSandboxKeys {
		b.AddReserved(key)
	}
	for _, key := range sandbox.ReservedCredentialKeys() {
		b.AddReserved(key)
	}

	manifest := b.Build()
	return &manifest
}

// bootstrapEnvExportKeys runs the production bootstrapEnv under the recording
// openshell stub and returns the keys its uploaded .env assigns. gated
// selects the all-gates-on harness — slug, validation schema, legacy output
// file, fetch service — against the same emitter with every optional
// emission off. Role is set in both runs: it is a required harness field, so
// its emission is unconditional in practice.
func bootstrapEnvExportKeys(t *testing.T, gated bool) map[string]bool {
	t.Helper()
	_, captureDir := envManifestOpenshell(t)
	h := &harness.Harness{Role: envManifestAgentName}
	var fetch []fetchServiceEnv
	if gated {
		schemaFile := filepath.Join(t.TempDir(), "output-schema.json")
		require.NoError(t, os.WriteFile(schemaFile, []byte(`{"type":"object"}`), 0o644))
		h.Slug = envManifestAgentName + "-slug"
		h.ValidationLoop = &harness.ValidationLoop{Script: "scripts/validate.sh", Schema: schemaFile}
		h.RunnerEnv = map[string]string{"FULLSEND_OUTPUT_FILE": "result.json"}
		fetch = append(fetch, fetchServiceEnv{addr: "127.0.0.1:1", token: "env-manifest-probe-token"})
	}
	require.NoError(t, bootstrapEnv(envManifestSandboxName, "/workspace/repo", h, nil, fetch...))
	keys := map[string]bool{}
	for _, key := range parseExportedEnvKeys(envManifestCapturedEnv(t, captureDir)) {
		keys[key] = true
	}
	return keys
}

// traceIDExportKeys runs injectTraceID under the recording stub and returns
// the key its .env append assigns. The emitter's call site only warns on
// failure, so the derived key is conditional, not always.
func traceIDExportKeys(t *testing.T) []string {
	t.Helper()
	argsLog, _ := envManifestOpenshell(t)
	require.NoError(t, injectTraceID(envManifestSandboxName, envManifestTraceID))
	return parseExportedEnvKeys(envManifestStubArgs(t, argsLog)...)
}

// hookEnvExportKeys derives the security-hook env keys (TIRITH_FAIL_ON,
// TIRITH_REQUIRED, FULLSEND_EGRESS_ALLOWLIST) by driving the exported
// production path that emits them: ClaudeRuntime.Bootstrap ends in
// appendHookEnv, which is package-private to internal/runtime and cannot be
// called from here directly. Every hook gate is on, so all three keys
// derive; each remains conditional in the manifest because the emitters
// guard on the harness security config.
func hookEnvExportKeys(t *testing.T) []string {
	t.Helper()
	argsLog, _ := envManifestOpenshell(t)
	agentPath := filepath.Join(t.TempDir(), "agent.md")
	require.NoError(t, os.WriteFile(agentPath, []byte("# env manifest probe\n"), 0o644))
	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
		Tirith:              &harness.TirithConfig{FailOn: "high"},
		SSRFEgressAllowlist: "env-manifest-probe.internal:443",
	}}}
	in := envManifestBootstrapInput{
		sandboxName: envManifestSandboxName,
		agentPath:   agentPath,
		agentName:   envManifestAgentName,
		hooks:       security.SandboxHookConfigFromHarness(h),
	}
	require.NoError(t, agentruntime.ClaudeRuntime{}.Bootstrap(in))
	// Filter to only args-log lines that write to .env — the
	// appendHookEnv pattern "echo 'export KEY=...' >> .env".
	// The full Bootstrap also runs env_data launch scripts whose
	// shell-internal variables (p, d, s, r, IFS, NAME, etc.) would
	// be noise if parsed.
	var hookLines []string
	for _, line := range envManifestStubArgs(t, argsLog) {
		if strings.Contains(line, ".env") {
			hookLines = append(hookLines, line)
		}
	}
	return parseExportedEnvKeys(hookLines...)
}

// envManifestScaffoldOptions builds a valid agent-new Options whose
// renderings differ only in runtime and model, so the Vertex env block is
// the only env.sandbox difference between the pair.
func envManifestScaffoldOptions(t *testing.T, runtimeName, model string) agentnew.Options {
	t.Helper()
	role, err := agentnew.LookupRole("coder")
	require.NoError(t, err)
	trigger, err := agentnew.ExpandTrigger(agentnew.DefaultOn, envManifestAgentName)
	require.NoError(t, err)
	return agentnew.Options{
		Name:           envManifestAgentName,
		Role:           "coder",
		Description:    "Env manifest probe agent",
		Trigger:        trigger,
		Model:          model,
		Effort:         agentnew.DefaultEffort,
		Slug:           envManifestAgentName,
		Image:          role.Image,
		TimeoutMinutes: agentnew.DefaultTimeoutMinutes,
		Runtime:        runtimeName,
	}
}

// scaffoldSandboxEnvKeys renders a scaffold through the production
// generator, loads its harness through the production loader, and returns
// the env.sandbox key set the harness declares.
func scaffoldSandboxEnvKeys(t *testing.T, opts agentnew.Options) map[string]bool {
	t.Helper()
	require.NoError(t, opts.Validate())
	files, err := agentnew.Render(opts)
	require.NoError(t, err)
	dir := t.TempDir()
	harnessPath := ""
	for _, f := range files {
		path := filepath.Join(dir, f.Path)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, f.Data, os.FileMode(f.Mode)))
		if strings.HasPrefix(f.Path, "harness/") {
			harnessPath = path
		}
	}
	require.NotEmpty(t, harnessPath, "the scaffold must contain a harness file")
	h, err := harness.Load(harnessPath)
	require.NoError(t, err)
	keys := map[string]bool{}
	if h.Env != nil {
		for key := range h.Env.Sandbox {
			keys[key] = true
		}
	}
	return keys
}

// harnessDeliveredKeys derives the harness-delivered variables as the
// env.sandbox keys the Vertex scaffold adds over an otherwise identical
// scaffold without Vertex: the credentials and routing that stock harness
// configuration delivers rather than the runner.
func harnessDeliveredKeys(t *testing.T) []string {
	t.Helper()
	withVertex := scaffoldSandboxEnvKeys(t, envManifestScaffoldOptions(t, "", agentnew.DefaultModel))
	withoutVertex := scaffoldSandboxEnvKeys(t, envManifestScaffoldOptions(t, "codex", "openai/gpt-5.6-luna"))
	var keys []string
	for key := range withVertex {
		if !withoutVertex[key] {
			keys = append(keys, key)
		}
	}
	return keys
}

// envManifestOpenshell installs an openshell stub on PATH that records every
// invocation's arguments to a log and, on a sandbox upload whose local file
// is a fullsend-env-*.sh temp file, copies that file into a capture
// directory. bootstrapEnv composes the sandbox .env in exactly such a temp
// file and uploads it through openshell, so the capture holds the real .env
// content, while the args log carries the export tokens the sandbox-exec
// emitters append. Only openshell is stubbed; tar and the other host tools
// the runtimes shell out to keep working.
func envManifestOpenshell(t *testing.T) (argsLog, captureDir string) {
	t.Helper()
	base := t.TempDir()
	argsLog = filepath.Join(base, "args.log")
	captureDir = filepath.Join(base, "captured")
	binDir := filepath.Join(base, "bin")
	require.NoError(t, os.MkdirAll(captureDir, 0o755))
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + argsLog + "'\n" +
		"if [ \"$1\" = \"sandbox\" ] && [ \"$2\" = \"upload\" ]; then\n" +
		"  case \"$5\" in\n" +
		"    *fullsend-env-*) cp \"$5\" '" + captureDir + "/' ;;\n" +
		"  esac\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"sandbox\" ] && [ \"$2\" = \"exec\" ]; then\n" +
		"  for last; do :; done\n" +
		"  case \"$last\" in\n" +
		"    \"mkdir -m 700 -- \"*|\"rm -f -- \"*) sh -c \"$last\"; exit $? ;;\n" +
		"    \"test -f \"*) exit 0 ;;\n" +
		"    *\"|fullsend-hook-sep|\"*) printf '%s|fullsend-hook-sep|%s\\n' /usr/bin/python3 /usr/local/bin:/usr/bin:/bin; exit 0 ;;\n" +
		"    *\"sys.version_info\"*) echo 3.12; exit 0 ;;\n" +
		"    *fullsend-env-sep*) printf '%s' \"|fullsend-env-sep|\"; exit 0 ;;\n" +
		"    cat\\ *) exit 0 ;;\n" +
		"  esac\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog, captureDir
}

// envManifestStubArgs returns the arguments the openshell stub recorded, one
// invocation per line.
func envManifestStubArgs(t *testing.T, argsLog string) []string {
	t.Helper()
	data, err := os.ReadFile(argsLog)
	require.NoError(t, err, "the openshell stub recorded no invocations")
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// envManifestCapturedEnv returns the .env content bootstrapEnv uploaded, and
// fails when more or fewer than one env file was captured.
func envManifestCapturedEnv(t *testing.T, captureDir string) string {
	t.Helper()
	entries, err := os.ReadDir(captureDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "bootstrapEnv must upload exactly one .env file")
	data, err := os.ReadFile(filepath.Join(captureDir, entries[0].Name()))
	require.NoError(t, err)
	return string(data)
}

// parseExportedEnvKeys returns the distinct KEY names the lines assign,
// sorted. It reads emitter output — .env file content, recorded exec command
// lines, or an EnvExports slice — so the derivation tracks what the emitters
// actually emit.
func parseExportedEnvKeys(lines ...string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, line := range lines {
		for _, match := range envManifestKeyRe.FindAllStringSubmatch(line, -1) {
			if !seen[match[1]] {
				seen[match[1]] = true
				keys = append(keys, match[1])
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// envManifestBootstrapInput implements runtime.BootstrapInput and
// runtime.SandboxHooksBootstrap for the hook-env derivation, mirroring the
// bootstrap inputs internal/runtime's own tests use.
type envManifestBootstrapInput struct {
	sandboxName string
	agentPath   string
	agentName   string
	hooks       security.SandboxHookConfig
}

func (i envManifestBootstrapInput) SandboxName() string { return i.sandboxName }

func (i envManifestBootstrapInput) AgentPath() string { return i.agentPath }

func (i envManifestBootstrapInput) AgentName() string { return i.agentName }

func (i envManifestBootstrapInput) SkillDirs() []string { return nil }

func (i envManifestBootstrapInput) Plugins() []agentruntime.PluginInput { return nil }

func (i envManifestBootstrapInput) ModelAliases() map[string]string { return nil }

func (i envManifestBootstrapInput) AgentSubagents() map[string]*string { return nil }

func (i envManifestBootstrapInput) ParentModel() string { return "" }

func (i envManifestBootstrapInput) OpenAIProviderAttached() bool { return false }

func (i envManifestBootstrapInput) SandboxHookConfig() security.SandboxHookConfig {
	return i.hooks
}

var (
	_ agentruntime.BootstrapInput        = envManifestBootstrapInput{}
	_ agentruntime.SandboxHooksBootstrap = envManifestBootstrapInput{}
)
