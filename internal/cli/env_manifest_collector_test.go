package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	envManifestProvenance  = "fullsend agent new Vertex scaffold (internal/agentnew); the fleet delivers these plus GOOGLE_CLOUD_PROJECT and ANTHROPIC_DEFAULT_SONNET_MODEL through env/gcp-vertex.env in fullsend-ai/agents"
)

// productionSandboxRuntimes lists every runtime the sandbox env contract must
// represent: the registry entries a real agent can dispatch under.
// excludedSandboxRuntimes holds the registry's local test stubs, per the
// design's D5 stub exclusion — exactly dummy and dummy-playback. opencode is
// not a stub for this manifest even though the registry's config validation
// groups it with the stubs and keeps it out of config.ValidRuntimes: it
// resolves in the registry, so it stays in the production set.
// TestEnvManifestRuntimeEnumeration pins the enumeration to the registry.
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
	"TIRITH_FAIL_ON":              "security.sandbox_hooks.tirith.fail_on is set; only the claude, codex, and pi bootstraps append hook env, so it is unreachable under opencode",
	"TIRITH_REQUIRED":             "Tirith scanning is required (security.sandbox_hooks.tirith.enabled, default true); only the claude, codex, and pi bootstraps append hook env, so it is unreachable under opencode",
	"FULLSEND_EGRESS_ALLOWLIST":   "an SSRF egress allowlist is configured, including the auto-merged forge egress entry; only the claude, codex, and pi bootstraps append hook env, so it is unreachable under opencode",
	"FULLSEND_ITERATION_DEADLINE": "writeIterationEnv succeeds; the run warns and continues when it fails",
	"FULLSEND_TIMEOUT_MINUTES":    "writeIterationEnv succeeds; the run warns and continues when it fails",
	"TRACEPARENT":                 "writeIterationEnv succeeds; the run warns and continues when it fails",
}

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
			"FULLSEND_PERSONAS_FILE",
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
			{"claude", []string{
				"ANTHROPIC_API_KEY",
				"ANTHROPIC_AUTH_TOKEN",
				"ANTHROPIC_BASE_URL",
				"CLAUDE_CODE_API_KEY_HELPER_TTL_MS",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
				"CLAUDE_CONFIG_DIR",
			}},
			{"codex", []string{
				"CODEX_HOME",
				"FULLSEND_CODEX_HOOK_DIGESTS",
				"FULLSEND_CODEX_SPAWN_DIGESTS",
				"FULLSEND_RUNTIME",
				"FULLSEND_TRUSTED_PATH",
				"RUST_LOG",
			}},
			{"opencode", []string{
				"BUN_RUNTIME_TRANSPILER_CACHE_PATH",
				"FULLSEND_OPENCODE_HOOK_CONFIG",
				"FULLSEND_RUNTIME",
				"GOOGLE_APPLICATION_CREDENTIALS",
				"GOOGLE_CLOUD_LOCATION",
				"GOOGLE_CLOUD_PROJECT",
				"HOME",
				"OPENCODE_CONFIG_CONTENT",
				"OPENCODE_CONFIG_DIR",
				"OPENCODE_DISABLE_DEFAULT_PLUGINS",
				"OPENCODE_DISABLE_EXTERNAL_SKILLS",
				"OPENCODE_DISABLE_MODELS_FETCH",
				"OPENCODE_DISABLE_PROJECT_CONFIG",
			}},
			{"pi", []string{
				"FULLSEND_PI_CHILD_BIN",
				"FULLSEND_PI_CHILD_NODE",
				"FULLSEND_PI_HOOK_PATH",
				"FULLSEND_PI_HOOK_PYTHON",
				"FULLSEND_PI_MANIFEST",
				"FULLSEND_PI_MANIFEST_SHA256",
				"FULLSEND_RUNTIME",
				"GOOGLE_CLOUD_LOCATION",
				"GOOGLE_CLOUD_PROJECT",
				"INFERENCE_GATEWAY_BASE_URL",
				"INFERENCE_GATEWAY_CONFIG_FILE",
				"INFERENCE_GATEWAY_TOKEN_FILE",
				"JITI_FS_CACHE",
				"PI_CODING_AGENT_DIR",
				"PI_CODING_AGENT_SESSION_DIR",
				"PI_OFFLINE",
				"PI_SKIP_VERSION_CHECK",
				"PI_TELEMETRY",
				"XAI_VERTEX_PROJECT_ID",
			}},
		} {
			t.Run(tc.runtime, func(t *testing.T) {
				assert.Equal(t, tc.want, m.Exports.RuntimeSpecific[tc.runtime])
			})
		}
		assert.Len(t, m.Exports.RuntimeSpecific, len(productionSandboxRuntimes))
	})

	// Every name a runtime's EnvExports or launch command exports must land
	// in some export category, so a bare re-export or a launch-only name
	// cannot fall out of the contract unnoticed.
	t.Run("runtime exports accounted for", func(t *testing.T) {
		for _, name := range productionSandboxRuntimes {
			backend, err := agentruntime.Resolve(name)
			require.NoError(t, err)
			emitted := append(envmanifest.ExportedNames(backend.EnvExports()...), agentruntime.LaunchEnvExportsFor(backend.Runtime)...)
			for _, key := range emitted {
				_, conditional := m.Exports.Conditional[key]
				covered := conditional || slices.Contains(m.Exports.Always, key) || slices.Contains(m.Exports.RuntimeSpecific[name], key)
				assert.True(t, covered, "%s exports %q, which no manifest category lists", name, key)
			}
		}
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
			"FULLSEND_PERSONAS_FILE",
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
		first, err := m.RenderSchemas()
		require.NoError(t, err)
		second, err := m.RenderSchemas()
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
	alwaysKeys := map[string]bool{}
	conditional := map[string]bool{}
	for key := range gatedOn {
		if gatedOff[key] {
			b.AddAlways(key)
			alwaysKeys[key] = true
		} else {
			conditional[key] = true
		}
	}

	iterationCmd := iterationEnvCommand(30, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), envManifestTraceparent)
	for _, key := range sandboxEnvFileKeys(t, iterationCmd) {
		if !alwaysKeys[key] {
			conditional[key] = true
		}
	}

	for _, key := range traceIDExportKeys(t) {
		if !alwaysKeys[key] {
			conditional[key] = true
		}
	}
	// Hook env is appended by the runtime bootstrap, not by bootstrapEnv,
	// so only the runtimes whose Bootstrap reaches appendHookEnv emit it.
	// A hook key no shared emitter also produces is restricted to those
	// runtimes.
	sharedConditional := maps.Clone(conditional)
	var hookOnly []string
	for _, key := range hookEnvExportKeys(t) {
		if !alwaysKeys[key] {
			conditional[key] = true
			if !sharedConditional[key] {
				hookOnly = append(hookOnly, key)
			}
		}
	}
	hookRuntimes := hookEnvRuntimes(t)
	for key := range conditional {
		gate, ok := sandboxEnvGates[key]
		require.True(t, ok, "sandbox env var %q is emitted under a gate the collector does not describe; add it to sandboxEnvGates", key)
		b.AddConditional(key, gate)
	}
	for _, key := range hookOnly {
		b.RestrictConditional(key, hookRuntimes...)
		for _, rt := range productionSandboxRuntimes {
			if !slices.Contains(hookRuntimes, rt) {
				assert.Contains(t, sandboxEnvGates[key], "unreachable under "+rt,
					"%s is hook env that %s's Bootstrap never appends; its gate description must say so", key, rt)
			}
		}
	}
	for key := range sandboxEnvGates {
		require.True(t, conditional[key], "sandboxEnvGates describes %q, which no emitter emits under a gate anymore", key)
	}

	for _, name := range productionSandboxRuntimes {
		backend, err := agentruntime.Resolve(name)
		require.NoError(t, err, "resolving runtime %q", name)
		// runtime_specific unions the runtime's EnvExports statements with
		// the bare names its launch command sets (LaunchEnvExporter, which
		// internal/runtime pins to the launch commands themselves). Names
		// the shared categories already cover — PATH, or the hook env a
		// launch command re-exports — stay in their shared category, so
		// runtime_specific lists only what the runtime adds.
		keys := append(envmanifest.ExportedNames(backend.EnvExports()...), agentruntime.LaunchEnvExportsFor(backend.Runtime)...)
		keys = slices.DeleteFunc(keys, func(key string) bool { return alwaysKeys[key] || conditional[key] })
		b.AddRuntimeSpecific(name, keys...)
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
	captureDir := envManifestCaptureDir(envManifestOpenshell(t))
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
	for _, key := range envmanifest.ExportedNames(envManifestCapturedEnv(t, captureDir)) {
		keys[key] = true
	}
	return keys
}

// traceIDExportKeys runs injectTraceID under the recording stub, replays
// the command it sent to the sandbox, and returns the key its .env append
// exports. The emitter's call site only warns on failure, so the derived key
// is conditional, not always.
func traceIDExportKeys(t *testing.T) []string {
	t.Helper()
	execLog := envManifestOpenshell(t)
	require.NoError(t, injectTraceID(envManifestSandboxName, envManifestTraceID))
	return sandboxEnvFileKeys(t, envManifestEnvWrites(t, execLog)...)
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
	execLog := envManifestOpenshell(t)
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
	return sandboxEnvFileKeys(t, envManifestEnvWrites(t, execLog)...)
}

// hookEnvRuntimes returns the production runtimes whose Bootstrap reaches
// appendHookEnv, sorted. appendHookEnv is package-private to
// internal/runtime and some runtimes' bootstraps need a live sandbox to
// drive, so the set comes from the package's static call graph: every
// function and method body in internal/runtime, walked from
// <RuntimeType>.Bootstrap through plain calls and calls on the receiver.
func hookEnvRuntimes(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "runtime")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	calls := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err, "parsing internal/runtime/%s", name)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key, recvName, recvType := fn.Name.Name, "", ""
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				typ := fn.Recv.List[0].Type
				if star, ok := typ.(*ast.StarExpr); ok {
					typ = star.X
				}
				if ident, ok := typ.(*ast.Ident); ok {
					recvType = ident.Name
					key = recvType + "." + fn.Name.Name
				}
				if len(fn.Recv.List[0].Names) == 1 {
					recvName = fn.Recv.List[0].Names[0].Name
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident:
					calls[key] = append(calls[key], f.Name)
				case *ast.SelectorExpr:
					if x, ok := f.X.(*ast.Ident); ok && recvName != "" && x.Name == recvName {
						calls[key] = append(calls[key], recvType+"."+f.Sel.Name)
					}
				}
				return true
			})
		}
	}
	require.Contains(t, calls, "appendHookEnv", "appendHookEnv not found in internal/runtime")

	reaches := func(start string) bool {
		seen := map[string]bool{}
		stack := []string{start}
		for len(stack) > 0 {
			fn := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if fn == "appendHookEnv" {
				return true
			}
			if seen[fn] {
				continue
			}
			seen[fn] = true
			stack = append(stack, calls[fn]...)
		}
		return false
	}

	var runtimes []string
	for _, name := range productionSandboxRuntimes {
		backend, err := agentruntime.Resolve(name)
		require.NoError(t, err, "resolving runtime %q", name)
		typ := reflect.TypeOf(backend.Runtime)
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		bootstrap := typ.Name() + ".Bootstrap"
		require.Contains(t, calls, bootstrap, "runtime %q: %s not found in internal/runtime", name, bootstrap)
		if reaches(bootstrap) {
			runtimes = append(runtimes, name)
		}
	}
	// ClaudeRuntime.Bootstrap is the path hookEnvExportKeys drives, so the
	// static walk must find it; otherwise the walk has stopped seeing calls.
	require.Contains(t, runtimes, "claude", "the call-graph walk no longer finds ClaudeRuntime.Bootstrap reaching appendHookEnv")
	slices.Sort(runtimes)
	return runtimes
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

// envManifestOpenshell installs an openshell stub on PATH. On a sandbox
// upload whose local file is a fullsend-env-*.sh temp file it copies that
// file into a capture directory — bootstrapEnv composes the sandbox .env in
// exactly such a file — and on a sandbox exec it appends the command (the
// last argument) to a NUL-separated log, so the emitters' sandbox commands
// can be replayed. Only openshell is stubbed; tar and the other host tools
// the runtimes shell out to keep working. The capture directory is
// returned through envManifestCaptureDir.
func envManifestOpenshell(t *testing.T) (execLog string) {
	t.Helper()
	base := t.TempDir()
	execLog = filepath.Join(base, "exec.log")
	captureDir := filepath.Join(base, "captured")
	binDir := filepath.Join(base, "bin")
	require.NoError(t, os.MkdirAll(captureDir, 0o755))
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"sandbox\" ] && [ \"$2\" = \"upload\" ]; then\n" +
		"  case \"$5\" in\n" +
		"    *fullsend-env-*) cp \"$5\" '" + captureDir + "/' ;;\n" +
		"  esac\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"sandbox\" ] && [ \"$2\" = \"exec\" ]; then\n" +
		"  for last; do :; done\n" +
		"  printf '%s\\0' \"$last\" >> '" + execLog + "'\n" +
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
	return execLog
}

// envManifestCaptureDir returns the directory envManifestOpenshell copies
// uploaded .env files into, given the exec log path it returned.
func envManifestCaptureDir(execLog string) string {
	return filepath.Join(filepath.Dir(execLog), "captured")
}

// envManifestEnvWrites returns the recorded sandbox commands that write an
// env file under the sandbox workspace: the .env appends and the
// iteration-env rewrite. Bootstrap's other commands (launch-script pins,
// probes) touch no env file and are not replayed.
func envManifestEnvWrites(t *testing.T, execLog string) []string {
	t.Helper()
	data, err := os.ReadFile(execLog)
	require.NoError(t, err, "the openshell stub recorded no sandbox exec")
	var cmds []string
	for _, cmd := range strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00") {
		if strings.Contains(cmd, "> "+sandbox.SandboxWorkspace+"/") {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// sandboxEnvFileKeys replays sandbox commands with /bin/sh against a scratch
// copy of the sandbox workspace and returns the names the env files they
// wrote export. Running the commands — rather than reading their text —
// means the derivation sees the file content the sandbox would source, so a
// quoted value or a dropped export keyword changes the result.
func sandboxEnvFileKeys(t *testing.T, cmds ...string) []string {
	t.Helper()
	require.NotEmpty(t, cmds, "no sandbox env-file writes to replay")
	workspace := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	for _, cmd := range cmds {
		local := strings.ReplaceAll(cmd, sandbox.SandboxWorkspace, workspace)
		out, err := exec.Command("/bin/sh", "-c", local).CombinedOutput()
		require.NoError(t, err, "replaying sandbox command %q: %s", cmd, out)
	}
	var files []string
	require.NoError(t, filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files = append(files, string(data))
		return err
	}))
	require.NotEmpty(t, files, "the replayed commands wrote no env file")
	return envmanifest.ExportedNames(files...)
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

func (i envManifestBootstrapInput) Effort() string { return "" }

func (i envManifestBootstrapInput) OpenAIProviderAttached() bool { return false }

func (i envManifestBootstrapInput) SandboxHookConfig() security.SandboxHookConfig {
	return i.hooks
}

var (
	_ agentruntime.BootstrapInput        = envManifestBootstrapInput{}
	_ agentruntime.SandboxHooksBootstrap = envManifestBootstrapInput{}
)
