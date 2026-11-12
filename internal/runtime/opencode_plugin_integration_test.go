package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenCodeNativeHooks exercises an actual OpenCode process, not a mock of
// the plugin API. A scripted Anthropic endpoint makes tool selection and the
// model-visible continuation deterministic. Opt in with an absolute binary
// path; the normal test suite never downloads or launches a runtime.
func TestOpenCodeNativeHooks(t *testing.T) {
	binary := os.Getenv("OPENCODE_HOOK_PROBE_BINARY")
	if binary == "" {
		t.Skip("set OPENCODE_HOOK_PROBE_BINARY to run the native plugin probe")
	}
	require.True(t, filepath.IsAbs(binary), "use an absolute path to the binary under test")
	wantVersion := os.Getenv("OPENCODE_HOOK_PROBE_VERSION")
	if wantVersion == "" {
		containerfile, err := os.ReadFile("../../images/sandbox/Containerfile")
		require.NoError(t, err)
		pin := regexp.MustCompile(`(?m)^ARG OPENCODE_VERSION=(\S+)$`).FindSubmatch(containerfile)
		require.Len(t, pin, 2)
		wantVersion = string(pin[1])
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	require.NoError(t, err, "%s", version)
	gotVersion := strings.TrimPrefix(strings.TrimSpace(string(version)), "opencode v")
	require.Equal(t, wantVersion, gotVersion, "runtime differs from the selected version")
	t.Logf("native hook probe: OpenCode %s (%s)", wantVersion, binary)

	for _, scenario := range []string{"pre-block", "rewrite", "post-block", "nested", "ls-only",
		"bash-patterns", "native-bash-patterns", "trusted-bash-patterns", "native-trusted-bash-patterns",
		"error-rewrite", "error-canary", "error-scanner-failure", "nested-error", "repo-config-isolation",
		"bash-failed-verification", "trusted-file-patterns", "nested-trusted-bash", "relative-paths"} {
		t.Run(scenario, func(t *testing.T) {
			runOpenCodeHookProbe(t, binary, gotVersion, scenario, openCodeHookProbeDeadline(scenario))
		})
	}
}

func openCodeHookProbeDeadline(scenario string) time.Duration {
	if scenario == "repo-config-isolation" {
		return 10 * time.Second
	}
	return 6 * time.Second
}

type openCodeProbeEvent struct {
	Phase       string `json:"phase"`
	Tool        string `json:"tool"`
	SessionID   string `json:"sessionID"`
	CallID      string `json:"callID"`
	Description string `json:"description"`
}

type openCodeProbeCall struct {
	name string
	args map[string]any
}

func runOpenCodeHookProbe(t *testing.T, binary, version, scenario string, deadline time.Duration) {
	t.Helper()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	work := workspace
	relativePaths := scenario == "relative-paths"
	if relativePaths {
		work = filepath.Join(workspace, "repo")
	}
	configDir := filepath.Join(dir, "config")
	configIsolation := scenario == "repo-config-isolation"
	failedVerification := scenario == "bash-failed-verification"
	filePatterns := scenario == "trusted-file-patterns"
	nestedPolicy := scenario == "nested-trusted-bash"
	patternProbe := strings.Contains(scenario, "bash-patterns")
	trustedProbe := strings.Contains(scenario, "trusted-bash-patterns")
	nativeOnly := strings.HasPrefix(scenario, "native-")
	lsProbe := scenario == "ls-only"
	errorProbe := strings.HasPrefix(scenario, "error-") || scenario == "nested-error"
	stopBeforeSend := scenario == "error-canary" || scenario == "error-scanner-failure" || scenario == "post-block"
	for _, subdir := range []string{work, filepath.Join(configDir, "plugins"), filepath.Join(configDir, openCodeAgentDir)} {
		require.NoError(t, os.MkdirAll(subdir, 0o700))
	}
	// The plugin imports only Node builtins. Satisfy v1's opportunistic SDK
	// install bookkeeping so a registry request cannot stall this local probe.
	// This is test-only; the sandbox image installs the actual pinned SDK.
	sdkDir := filepath.Join(dir, "opencode-sdk")
	require.NoError(t, os.MkdirAll(filepath.Join(sdkDir, "node_modules"), 0o700))
	for _, name := range []string{"package.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join("../../images/sandbox/opencode-sdk", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(sdkDir, name), data, 0o444))
	}
	seed := exec.CommandContext(t.Context(), "/bin/sh", "-c", openCodeSDKCacheRestoreFrom(sdkDir, configDir, filepath.Join(dir, "xdg-config", "opencode")))
	seed.Env = append(os.Environ(), "HOME="+dir)
	seedOutput, err := seed.CombinedOutput()
	require.NoError(t, err, "seeding image-equivalent SDK bookkeeping: %s", seedOutput)
	fixture, err := os.ReadFile("testdata/opencode/hook-probe.ts")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "plugins", "00-probe-observer.ts"), fixture, 0o600))
	if !nativeOnly {
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "plugins", "fullsend-hooks.ts"), openCodeHookAdapter, 0o600))
	}
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	disabled := false
	hooks := security.SandboxHookConfigFromHarness(&harness.Harness{Security: &harness.SecurityConfig{
		SandboxHooks: &harness.SandboxHooks{Tirith: &harness.TirithConfig{Enabled: &disabled}},
	}})
	def := &piAgentDef{Model: "probe/model", BashAllowlist: []string{"printf", "cat", "curl", "touch"}}
	trustedPolicy := json.RawMessage(`{"*":"allow"}`)
	if patternProbe {
		def.Tools, def.BashAllowlist = []string{"Bash"}, []string{"printf", "cat"}
		trustedPolicy = json.RawMessage(`{"*":"deny","bash":"allow"}`)
	}
	if trustedProbe {
		trustedPolicy = json.RawMessage(`{"*":"deny","bash":{"*":"deny","printf":"allow","printf *":"allow","printf denied*":"deny","cat":"deny","cat *":"deny"}}`)
	}
	if nestedPolicy {
		def.Tools, def.BashAllowlist = []string{"Bash", "Task"}, []string{"printf", "touch"}
		trustedPolicy = json.RawMessage(`{"*":"deny","task":"allow","bash":{"*":"deny","printf *":"allow","printf denied*":"deny","touch *":"deny"}}`)
	}
	if lsProbe {
		def.Tools, def.BashAllowlist = []string{"LS"}, nil
		trustedPolicy = json.RawMessage(`{"*":"deny","read":"allow"}`)
		require.NoError(t, os.MkdirAll(filepath.Join(work, "directory-only-entry"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(work, "private.txt"), []byte("FULLSEND_LS_PRIVATE_CONTENT"), 0o600))
	}
	if relativePaths {
		def.Tools = []string{"Bash", "LS", "Write", "Edit"}
		require.NoError(t, os.MkdirAll(filepath.Join(work, "a", "b"), 0o700))
		require.NoError(t, os.Symlink("a/b", filepath.Join(work, "link")))
		require.NoError(t, os.WriteFile(filepath.Join(workspace, ".env"), []byte("FULLSEND_PROBE=original\n"), 0o600))
		require.NoError(t, os.Mkdir(filepath.Join(work, "private.txt"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "private.txt"), []byte("FULLSEND_LS_PRIVATE_CONTENT"), 0o600))
	}
	if failedVerification {
		def.BashAllowlist = append(def.BashAllowlist, "scan-secrets")
		require.NoError(t, os.MkdirAll(filepath.Join(work, "bin"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(work, "bin", "scan-secrets"), []byte("#!/bin/sh\nprintf 'No leaks found\\n'\nexit 1\n"), 0o700))
	}
	if filePatterns {
		git := exec.CommandContext(t.Context(), "git", "init", "--quiet", work)
		out, err := git.CombinedOutput()
		require.NoError(t, err, "%s", out)
		trustedPolicy = json.RawMessage(`{"*":"deny","bash":"allow","read":{"*":"deny","src/*":"allow"},"edit":{"*":"deny","src/*":"allow","src/private*":"deny"}}`)
	}
	rules := parseTrustedPermissionPolicy(`{"permission":` + string(trustedPolicy) + `}`)
	agent, err := openCodeAgentMarkdown("probe-parent", def, rules)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, openCodeAgentDir, "probe-parent.md"), agent, 0o600))
	policy := openCodeHookPolicy(filepath.Join(configDir, "hooks"), workspace, def, rules)
	policy.Python = python
	policy.ShellHome = dir
	policy.Env = map[string]string{"PATH": os.Getenv("PATH"), "FULLSEND_CANARY_TOKEN": "FULLSEND_PROBE_ORIGINAL_post-block"}
	if failedVerification {
		policy.Env["PATH"] = filepath.Join(work, "bin") + string(os.PathListSeparator) + policy.Env["PATH"]
	}
	policy.Groups = security.HookPlan(hooks)
	if !nativeOnly {
		policy.Activation = &openCodeHookActivation{Path: filepath.Join(dir, "plugin-activation"), Token: "native-hook-probe-" + scenario}
	}
	require.NoError(t, os.MkdirAll(policy.Dir, 0o700))
	for file, content := range security.HookFiles(hooks) {
		require.NoError(t, os.WriteFile(filepath.Join(policy.Dir, file), content, 0o600))
		policy.Digests[file] = codexAssetSHA256(content)
	}
	for _, kind := range []string{"rewrite", "post-block"} {
		value := "FULLSEND_PROBE_ORIGINAL_" + kind
		if kind == "rewrite" {
			value = "ghp_" + strings.Repeat("A", 36)
		}
		require.NoError(t, os.WriteFile(filepath.Join(work, "probe-"+kind+".txt"),
			[]byte(value), 0o600))
	}
	errorFile := filepath.Join(dir, "tool-error.txt")
	errorValue := "ghp_" + strings.Repeat("A", 36)
	if scenario == "error-canary" {
		errorValue = "FULLSEND_PROBE_ORIGINAL_post-block"
	}
	require.NoError(t, os.WriteFile(errorFile, []byte(errorValue), 0o600))
	if scenario == "error-scanner-failure" {
		source := []byte("print('{\"metadata\":{\"redact_error\":true}}')\n")
		require.NoError(t, os.WriteFile(filepath.Join(policy.Dir, "fault.py"), source, 0o600))
		policy.Digests["fault.py"] = codexAssetSHA256(source)
		policy.Groups = append(policy.Groups, security.HookGroup{Phase: security.HookPhasePostToolUse, Tools: []string{"*"}, Scripts: []string{"fault.py"}})
	}

	steps := map[string][]openCodeProbeCall{
		"root": {stepsForAllowedProbe()},
	}
	probeCall := func(kind string) openCodeProbeCall {
		command := "cat probe-" + kind + ".txt"
		if kind == "pre-block" {
			command = "touch forbidden; curl http://127.0.0.1"
		}
		return openCodeProbeCall{name: "bash", args: map[string]any{
			"command": command, "description": "probe-" + kind,
		}}
	}
	if scenario == "nested" {
		steps["root"] = []openCodeProbeCall{{name: "task", args: map[string]any{
			"description": "Native hook probe child", "subagent_type": "probe-child",
			"prompt": "FULLSEND_NATIVE_HOOK_CHILD: execute the scripted probe.",
		}}}
		steps["child"] = []openCodeProbeCall{stepsForAllowedProbe(), probeCall("pre-block"), probeCall("rewrite"), probeCall("post-block")}
	} else {
		steps["root"] = append(steps["root"], probeCall(scenario))
	}
	if patternProbe {
		bash := func(command, description string) openCodeProbeCall {
			return openCodeProbeCall{name: "bash", args: map[string]any{"command": command, "description": description}}
		}
		steps["root"] = []openCodeProbeCall{
			stepsForAllowedProbe(),
			bash("printf", "probe-bare"),
			bash("printf argument-check", "probe-arguments"),
			bash("touch forbidden", "probe-denied-command"),
			bash("printf escaped; touch forbidden-composed", "probe-denied-composition"),
		}
		if trustedProbe {
			steps["root"] = append(steps["root"],
				bash("printf denied-value", "probe-trusted-denial"),
				bash("cat probe-rewrite.txt", "probe-trusted-cat-denial"))
		} else {
			steps["root"] = append(steps["root"], bash("printf composed; printf again", "probe-allowed-composition"))
		}
	}
	if lsProbe {
		steps["root"] = []openCodeProbeCall{
			{name: "read", args: map[string]any{"filePath": work}},
			{name: "read", args: map[string]any{"filePath": filepath.Join(work, "private.txt")}},
			{name: "write", args: map[string]any{"filePath": filepath.Join(work, "forbidden"), "content": "must not be written"}},
		}
	}
	if relativePaths {
		steps["root"] = []openCodeProbeCall{
			stepsForAllowedProbe(),
			{name: "write", args: map[string]any{"filePath": "link/../../.env", "content": "overwritten", "description": "relative-write"}},
			{name: "edit", args: map[string]any{"filePath": "link/../../.env", "oldString": "original", "newString": "overwritten", "description": "relative-edit"}},
			{name: "read", args: map[string]any{"filePath": "link/../../private.txt", "description": "relative-read"}},
		}
	}
	if errorProbe {
		call := openCodeProbeCall{name: "probe_error", args: map[string]any{}}
		steps["root"] = []openCodeProbeCall{call}
		if scenario == "nested-error" {
			steps["root"] = []openCodeProbeCall{{name: "task", args: map[string]any{
				"description": "Native hook probe child", "subagent_type": "probe-child",
				"prompt": "FULLSEND_NATIVE_HOOK_CHILD: execute the scripted probe.",
			}}}
			steps["child"] = []openCodeProbeCall{call}
		}
	}
	if configIsolation {
		// A target-repo .env must not re-enable an alternate config/plugin
		// loader underneath the pinned environment of the compiled runtime.
		poison := filepath.Join(work, "poison.ts")
		require.NoError(t, os.WriteFile(poison, []byte("import {writeFileSync} from 'node:fs'; export default async()=>{writeFileSync("+strconv.Quote(filepath.Join(work, "poison-loaded"))+",'loaded'); return {};};"), 0o600))
		poisonConfig, _ := json.Marshal(map[string]any{"plugin": []string{"file://" + poison}})
		require.NoError(t, os.WriteFile(filepath.Join(work, "poison.json"), poisonConfig, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(work, ".env"), []byte("OPENCODE_CONFIG="+filepath.Join(work, "poison.json")+"\nOPENCODE_PURE=true\n"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(work, ".opencode"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(work, ".opencode", "opencode.json"), poisonConfig, 0o600))
		steps["root"] = []openCodeProbeCall{stepsForAllowedProbe(), probeCall("rewrite")}
	}
	if failedVerification {
		steps["root"] = []openCodeProbeCall{stepsForAllowedProbe(), {name: "bash", args: map[string]any{
			"command": "scan-secrets file.go", "description": "probe-failed-verification",
		}}}
	}
	if filePatterns {
		steps["root"] = []openCodeProbeCall{
			stepsForAllowedProbe(),
			{name: "write", args: map[string]any{"filePath": filepath.Join(work, "src", "allowed.txt"), "content": "path-scoped allowed"}},
			{name: "read", args: map[string]any{"filePath": filepath.Join(work, "src", "allowed.txt")}},
			{name: "write", args: map[string]any{"filePath": filepath.Join(work, "src", "private.txt"), "content": "must not be written"}},
			{name: "write", args: map[string]any{"filePath": filepath.Join(work, "denied.txt"), "content": "must not be written"}},
		}
	}
	if nestedPolicy {
		steps["root"] = []openCodeProbeCall{stepsForAllowedProbe(), {name: "task", args: map[string]any{
			"description": "Trusted policy child", "subagent_type": "probe-child",
			"prompt": "FULLSEND_NATIVE_HOOK_CHILD: execute the scripted probe.",
		}}}
		steps["child"] = []openCodeProbeCall{stepsForAllowedProbe()}
		for _, command := range []string{"touch forbidden-child-single", "printf safe; touch forbidden-child-sequence", "printf safe | touch forbidden-child-pipeline", "printf 'denied-value'"} {
			steps["child"] = append(steps["child"], openCodeProbeCall{name: "bash", args: map[string]any{
				"command": command, "description": "probe-child-denial-" + command,
			}})
		}
	}

	var mu sync.Mutex
	counts := map[string]int{}
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if !assert.NoError(t, err) {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		var request struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if !assert.NoError(t, json.Unmarshal(body, &request)) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		kind := "root"
		for _, message := range request.Messages {
			if message.Role != "user" {
				continue
			}
			for _, part := range message.Content {
				if part.Type == "text" && strings.Contains(part.Text, "FULLSEND_NATIVE_HOOK_CHILD:") {
					kind = "child"
				}
			}
		}
		mu.Lock()
		requests = append(requests, body)
		var call *openCodeProbeCall
		// Ignore title/summary generation, which does not offer tools.
		if len(request.Tools) > 0 {
			index := counts[kind]
			counts[kind]++
			if index < len(steps[kind]) {
				value := steps[kind][index]
				call = &value
			}
		}
		mu.Unlock()
		writeOpenCodeProbeResponse(w, call)
	}))
	defer server.Close()

	child := map[string]any{"mode": "subagent", "model": "probe/model", "description": "Native hook probe child"}
	if nestedPolicy {
		// A native agent-level allow can override the global trusted policy.
		// The adapter must still preserve the parent's ceiling in this child.
		child["permission"] = map[string]string{"bash": "allow"}
	}
	config := map[string]any{
		"autoupdate": false, "share": "disabled", "small_model": "probe/model",
		"enabled_providers": []string{"probe"},
		"provider": map[string]any{"probe": map[string]any{
			"npm":     "@ai-sdk/anthropic",
			"options": map[string]any{"baseURL": server.URL + "/v1", "apiKey": "probe-only-not-a-credential"},
			"models": map[string]any{"model": map[string]any{
				"name": "Native hook probe", "limit": map[string]int{"context": 32000, "output": 4096},
			}},
		}},
		"permission": trustedPolicy,
		"agent": map[string]any{
			"probe-child": child,
		},
	}
	configJSON, err := json.Marshal(config)
	require.NoError(t, err)
	eventsPath := filepath.Join(dir, "events.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()
	args := []string{"--print-logs", "--log-level", "DEBUG", "run", "--format", "json", "--model", "probe/model", "--agent", "probe-parent"}
	if strings.HasPrefix(version, "2.") {
		// V2 otherwise starts a persistent background service.
		args = append(args, "--standalone")
	}
	args = append(args, "FULLSEND_NATIVE_HOOK_PARENT: execute the scripted probe.")
	cmd := exec.CommandContext(ctx, binary, args...)
	if configIsolation {
		// Exercise the runner's .env drop/restore and SDK cache-restore path
		// before starting the real pinned runtime.
		trusted := openCodeTrustedEnv{ConfigContent: string(configJSON)}
		launch := []string{shellQuote(binary)}
		for _, arg := range args {
			launch = append(launch, shellQuote(arg))
		}
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", openCodeTrustedEnvPin(trusted)+" && "+envDataTrustedPin()+" && "+envDataLoad(filepath.Join(work, ".env"), openCodeEnvDataDrop...)+" && "+openCodeTrustedEnvRestore()+" && "+openCodeSDKCacheRestoreFrom(sdkDir, configDir)+" && exec "+strings.Join(launch, " "))
	}
	cmd.Dir = work
	// Do not inherit provider credentials, user plugins, or the harness config.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "SHELL=/bin/sh",
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "xdg-config"),
		"XDG_DATA_HOME=" + filepath.Join(dir, "xdg-data"),
		"XDG_CACHE_HOME=" + filepath.Join(dir, "xdg-cache"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "xdg-state"),
		"OPENCODE_CONFIG_DIR=" + configDir, "OPENCODE_CONFIG_CONTENT=" + string(configJSON),
		"OPENCODE_DISABLE_PROJECT_CONFIG=true", "OPENCODE_DISABLE_MODELS_FETCH=true",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=true", "OPENCODE_DISABLE_AUTOUPDATE=true",
		"FULLSEND_PROBE_EVENTS=" + eventsPath,
		"FULLSEND_PROBE_ERROR_FILE=" + errorFile,
		openCodeHookConfigEnv + "=" + openCodeHookConfigJSON(policy),
	}
	if configIsolation {
		cmd.Env = append(cmd.Env, "OPENCODE_PURE=true")
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("native hook scenario %q exceeded %s; output:\n%s", scenario, deadline, output)
	}
	if !stopBeforeSend {
		require.NoError(t, err, "native probe failed: %s", output)
	}

	mu.Lock()
	captured := bytes.Join(requests, []byte("\n"))
	actualCounts := make(map[string]int, len(counts))
	for kind, n := range counts {
		actualCounts[kind] = n
	}
	mu.Unlock()
	for kind, calls := range steps {
		want := len(calls) + 1
		if stopBeforeSend || (scenario == "nested" && kind == "child") {
			want = len(calls)
		}
		require.Equal(t, want, actualCounts[kind], "%s model-send count; output: %s", kind, output)
	}
	if !lsProbe && !errorProbe {
		assert.Contains(t, string(captured), "executed", "control command output must reach a model continuation; output: %s", output)
	}
	assert.NoFileExists(t, filepath.Join(work, "forbidden"))
	assert.NotContains(t, string(captured), "FULLSEND_PROBE_ORIGINAL_", "raw output reached a model request")
	assert.NotContains(t, string(captured), "ghp_"+strings.Repeat("A", 36), "unredacted secret reached a model request")

	eventBytes, err := os.ReadFile(eventsPath)
	require.NoError(t, err, "plugin was not loaded; output: %s", output)
	var events []openCodeProbeEvent
	for _, line := range bytes.Split(bytes.TrimSpace(eventBytes), []byte("\n")) {
		var event openCodeProbeEvent
		require.NoError(t, json.Unmarshal(line, &event))
		require.NotEmpty(t, event.SessionID)
		require.NotEmpty(t, event.CallID)
		events = append(events, event)
	}
	if !nativeOnly {
		activation, err := os.ReadFile(policy.Activation.Path)
		require.NoError(t, err, "production hook plugin was not activated; output: %s", output)
		assert.Equal(t, policy.Activation.Token, string(activation))
	}
	if errorProbe {
		var phases []string
		for _, event := range events {
			if event.Tool == "probe_error" {
				phases = append(phases, event.Phase)
			}
		}
		assert.Equal(t, []string{"before"}, phases, "throwing tool must genuinely skip the after hook")
		assert.NotContains(t, string(captured), errorValue, "failed-tool output must never reach a model")
		if stopBeforeSend {
			assert.Contains(t, string(output), "fullsend-hooks", "transform failure must be surfaced")
		} else {
			assert.Contains(t, string(captured), "ghp_...", "sanitized tool error must reach a continuation")
		}
		t.Logf("failed-tool transform verified (nested: %t, stopped before send: %t)", scenario == "nested-error", stopBeforeSend)
		return
	}
	if patternProbe {
		arguments := openCodeProbeResultFor(t, captured, "probe-arguments")
		assert.False(t, arguments.IsError, "allowed command with arguments must execute")
		assert.Contains(t, string(arguments.Content), "argument-check")
		assert.NoFileExists(t, filepath.Join(work, "forbidden-composed"))
		for _, description := range []string{"probe-denied-command", "probe-denied-composition"} {
			assert.True(t, openCodeProbeResultFor(t, captured, description).IsError, description)
		}
		if trustedProbe {
			for _, description := range []string{"probe-trusted-denial", "probe-trusted-cat-denial"} {
				assert.True(t, openCodeProbeResultFor(t, captured, description).IsError, description)
			}
		}
		if !trustedProbe {
			composed := openCodeProbeResultFor(t, captured, "probe-allowed-composition")
			assert.False(t, composed.IsError, "composition of allowed programs must execute")
			assert.Contains(t, string(composed.Content), "composedagain")
		}
		var barePhases []string
		for _, event := range events {
			if event.Tool == "bash" && event.Description == "probe-bare" {
				barePhases = append(barePhases, event.Phase)
			}
		}
		assert.Equal(t, []string{"before", "after"}, barePhases, "native permissions must allow the bare program name")
		t.Logf("validated generated native permissions (adapter enabled: %t, trusted pattern ceiling: %t)", !nativeOnly, trustedProbe)
		return
	}
	if relativePaths {
		contents, err := os.ReadFile(filepath.Join(workspace, ".env"))
		require.NoError(t, err)
		assert.Equal(t, "FULLSEND_PROBE=original\n", string(contents), "protected file must remain unchanged")
		for _, description := range []string{"relative-write", "relative-edit", "relative-read"} {
			result := openCodeProbeResultFor(t, captured, description)
			assert.True(t, result.IsError, description)
			assert.Contains(t, string(result.Content), "[fullsend-hooks]", "the adapter must deny before native permissions")
		}
		assert.NotContains(t, string(captured), "FULLSEND_LS_PRIVATE_CONTENT")
		return
	}
	if lsProbe {
		assert.Contains(t, string(captured), "directory-only-entry", "LS must produce a real directory listing")
		assert.NotContains(t, string(captured), "FULLSEND_LS_PRIVATE_CONTENT", "LS must not permit file reads")
		denied := map[string]bool{}
		for _, body := range bytes.Split(captured, []byte("\n")) {
			var request struct {
				Messages []struct {
					Content []struct {
						Type      string `json:"type"`
						ToolUseID string `json:"tool_use_id"`
						IsError   bool   `json:"is_error"`
					} `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.Unmarshal(body, &request))
			for _, message := range request.Messages {
				for _, part := range message.Content {
					if part.Type == "tool_result" && part.IsError {
						denied[part.ToolUseID] = true
					}
				}
			}
		}
		assert.Len(t, denied, 2, "file read and write must both return tool errors, regardless of which enforcement layer denies them")
		t.Log("LS-only generated agent listed a directory and was denied file reads and writes")
		return
	}
	if failedVerification {
		assert.Contains(t, string(captured), "No leaks found", "failed verification output must reach the model")
		assert.NotContains(t, string(captured), "scan-secrets: passed", "nonzero exit must prevent success suppression")
		var phases []string
		for _, event := range events {
			if event.Description == "probe-failed-verification" {
				phases = append(phases, event.Phase)
			}
		}
		assert.Equal(t, []string{"before", "after"}, phases, "nonzero Bash exit must exercise the native after hook")
		return
	}
	if filePatterns {
		allowed, err := os.ReadFile(filepath.Join(work, "src", "allowed.txt"))
		require.NoError(t, err, "path-scoped trusted edit rule must allow native writes")
		assert.Equal(t, "path-scoped allowed", string(allowed))
		assert.Contains(t, string(captured), "path-scoped allowed", "path-scoped trusted read rule must allow native reads")
		assert.NoFileExists(t, filepath.Join(work, "src", "private.txt"))
		assert.NoFileExists(t, filepath.Join(work, "denied.txt"))
		return
	}
	if nestedPolicy {
		for _, name := range []string{"forbidden-child-single", "forbidden-child-sequence", "forbidden-child-pipeline"} {
			assert.NoFileExists(t, filepath.Join(work, name))
		}
		for _, call := range steps["child"][1:] {
			result := openCodeProbeResultFor(t, captured, call.args["description"].(string))
			assert.True(t, result.IsError)
			assert.Contains(t, string(result.Content), "trusted permission policy")
		}
		var parentSession, childSession string
		for _, event := range events {
			if event.Tool == "task" {
				parentSession = event.SessionID
			}
		}
		for _, event := range events {
			if event.Tool == "bash" && event.Description == "probe-allowed" && event.SessionID != parentSession {
				childSession = event.SessionID
			}
		}
		require.NotEmpty(t, parentSession)
		require.NotEmpty(t, childSession)
		assert.NotEqual(t, parentSession, childSession)
		return
	}
	kinds := []string{scenario}
	if configIsolation {
		assert.NoFileExists(t, filepath.Join(work, "poison-loaded"), "repository dotenv must not inject a plugin")
		kinds = []string{"rewrite"}
	}
	if scenario == "nested" {
		kinds = []string{"pre-block", "rewrite", "post-block"}
	}
	for _, kind := range kinds {
		marker := map[string]string{
			"pre-block": "blocked by ssrf_pretool.py", "rewrite": "ghp_...", "post-block": "blocked by posttool_chain.py",
		}[kind]
		visible := string(captured)
		if stopBeforeSend {
			visible = string(output)
		}
		assert.Contains(t, visible, marker, "safe hook diagnostic must be surfaced")
		if kind == "pre-block" {
			assert.Contains(t, string(captured), "SSRF blocked:", "sanitized detector reason must reach the model")
		}
		if kind == "post-block" {
			assert.Contains(t, visible, "CANARY_LEAKED:", "sanitized detector reason must be surfaced")
		}
		var phases []string
		for _, event := range events {
			if event.Tool == "bash" && event.Description == "probe-"+kind {
				phases = append(phases, event.Phase)
			}
		}
		if kind == "pre-block" {
			assert.NotContains(t, phases, "after", "denied tool must not run its post hook")
		} else if kind == "rewrite" {
			assert.Equal(t, []string{"before", "after"}, phases)
		} else {
			assert.Contains(t, phases, "before")
		}
	}
	if scenario == "nested" {
		var parentSession, childSession string
		var taskPhases []string
		for _, event := range events {
			if event.Tool == "task" {
				parentSession = event.SessionID
				taskPhases = append(taskPhases, event.Phase)
			} else if event.Tool == "bash" {
				if childSession == "" {
					childSession = event.SessionID
				}
				assert.Equal(t, childSession, event.SessionID)
			}
		}
		assert.Equal(t, []string{"before"}, taskPhases, "child canary aborts Task before its after hook")
		require.NotEmpty(t, childSession)
		assert.NotEqual(t, parentSession, childSession, "Bash probes must run in a real child session")
	}
	t.Logf("observed %d native hook events; model continuations: %v", len(events), actualCounts)
}

type openCodeProbeResult struct {
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

// Inspect actual tool results, not matching text in the model's tool inputs.
func openCodeProbeResultFor(t *testing.T, captured []byte, description string) openCodeProbeResult {
	t.Helper()
	var callID string
	results := map[string]openCodeProbeResult{}
	for _, body := range bytes.Split(captured, []byte("\n")) {
		var request struct {
			Messages []struct {
				Content []struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					ToolUseID string `json:"tool_use_id"`
					Input     struct {
						Description string `json:"description"`
					} `json:"input"`
					openCodeProbeResult
				} `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(body, &request))
		for _, message := range request.Messages {
			for _, part := range message.Content {
				if part.Type == "tool_use" && part.Input.Description == description {
					callID = part.ID
				}
				if part.Type == "tool_result" {
					results[part.ToolUseID] = part.openCodeProbeResult
				}
			}
		}
	}
	require.NotEmpty(t, callID, "tool call %q was not recorded", description)
	result, ok := results[callID]
	require.True(t, ok, "tool result %q was not recorded", description)
	return result
}

func stepsForAllowedProbe() openCodeProbeCall {
	return openCodeProbeCall{name: "bash", args: map[string]any{
		"command": "printf executed", "description": "probe-allowed",
	}}
}

func writeOpenCodeProbeResponse(w http.ResponseWriter, call *openCodeProbeCall) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(event string, data map[string]any) {
		data["type"] = event
		encoded, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	}
	emit("message_start", map[string]any{"message": map[string]any{
		"id": "msg_probe", "type": "message", "role": "assistant", "model": "model",
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 1},
	}})
	stopReason := "end_turn"
	if call != nil {
		stopReason = "tool_use"
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
			"type": "tool_use", "id": "tool_" + call.name + "_" + fmt.Sprint(time.Now().UnixNano()), "name": call.name, "input": map[string]any{},
		}})
		args, _ := json.Marshal(call.args)
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
	} else {
		emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "Probe complete."}})
	}
	emit("content_block_stop", map[string]any{"index": 0})
	emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 10}})
	emit("message_stop", map[string]any{})
}
