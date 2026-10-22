package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeRuntimeMetadata(t *testing.T) {
	t.Parallel()

	rt := OpenCodeRuntime{}
	assert.Equal(t, "opencode", rt.Name())
	// Multi-provider convention, matching pi's System()=="pi" — NOT a single
	// model vendor like "anthropic".
	assert.Equal(t, "opencode", rt.System())
	// Runner-owned config dir, off the agent-writable workspace (#515 pin).
	assert.Equal(t, sandbox.SandboxOpenCodeConfig, rt.ConfigDir())
	assert.Equal(t, "/sandbox/opencode-config", rt.ConfigDir())
	assert.NotEqual(t, sandbox.SandboxWorkspace+"/.opencode", rt.ConfigDir(),
		"ConfigDir must be moved off the agent-writable workspace")
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	assert.Equal(t, openCodeDebugLogFile, rt.DebugLogName())
}

func TestOpenCodeRuntimeEnvExports(t *testing.T) {
	t.Parallel()

	rt := OpenCodeRuntime{}
	env := rt.EnvExports()
	assert.Contains(t, env, "export OPENCODE_CONFIG_DIR="+rt.ConfigDir())
	assert.Contains(t, env, "export OPENCODE_DISABLE_PROJECT_CONFIG=true")
	assert.Contains(t, env, "export OPENCODE_DISABLE_EXTERNAL_SKILLS=true")
	assert.Contains(t, env, "export OPENCODE_CONFIG_CONTENT")
	assert.Contains(t, env, "export GOOGLE_APPLICATION_CREDENTIALS")
}

func TestOpenCodeRuntimeResolvesFromRegistry(t *testing.T) {
	t.Parallel()

	b, err := Resolve("opencode")
	require.NoError(t, err)
	_, isRT := b.Runtime.(OpenCodeRuntime)
	assert.True(t, isRT, "Runtime should be OpenCodeRuntime")
	_, isTX := b.Transcripts.(OpenCodeRuntime)
	assert.True(t, isTX, "Transcripts should be OpenCodeRuntime")
}

// The opencode alias table must resolve to the same model generation as the
// other Claude-family runtimes; only the Vertex catalog suffix differs.
func TestOpenCodeAliasesMatchClaudeDefaults(t *testing.T) {
	for alias, want := range claudeAliasDefaults {
		got, _, _ := strings.Cut(openCodeModelAliases[alias], "@")
		assert.Equal(t, want, got, "alias %q", alias)
	}
}

func TestTranslateOpenCodeModel(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	assert.Equal(t, "google-vertex-anthropic/claude-opus-5-5@default", translateOpenCodeModel("opus", nil))
	assert.Equal(t, "google-vertex-anthropic/claude-sonnet-5-5@default", translateOpenCodeModel("sonnet", nil))
	// Empty falls back to the default alias.
	assert.Equal(t, "google-vertex-anthropic/claude-opus-5-5@default", translateOpenCodeModel("", nil))
	// A bare (non-alias) id gets the provider prefix and, on Vertex, @default.
	assert.Equal(t, "google-vertex-anthropic/claude-3-5@default", translateOpenCodeModel("claude-3-5", nil))
	// A bare id that already has an @version keeps it.
	assert.Equal(t, "google-vertex-anthropic/claude-3-5@20241022", translateOpenCodeModel("claude-3-5@20241022", nil))
	// A provider/model spec passes through unchanged.
	assert.Equal(t, "openai/gpt-5", translateOpenCodeModel("openai/gpt-5", nil))

	// Provider override from the environment.
	t.Setenv(openCodeProviderEnv, "myprov")
	assert.Equal(t, "myprov/claude-opus-5-5@default", translateOpenCodeModel("opus", nil))
	// Only google-vertex-anthropic gets @default appended.
	assert.Equal(t, "myprov/claude-3-5", translateOpenCodeModel("claude-3-5", nil))

	// The suffix also applies when the env names the Vertex provider explicitly.
	t.Setenv(openCodeProviderEnv, "google-vertex-anthropic")
	assert.Equal(t, "google-vertex-anthropic/claude-3-5@default", translateOpenCodeModel("claude-3-5", nil))
}

func TestTranslateOpenCodeModel_ConfigAliases(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	// Per-repo alias overrides the built-in alias; a bare id gets @default
	// on Vertex (#7823).
	overrides := map[string]string{"sonnet": "claude-sonnet-5"}
	assert.Equal(t, "google-vertex-anthropic/claude-sonnet-5@default", translateOpenCodeModel("sonnet", overrides))
	// An alias not overridden still uses the built-in, with a single suffix.
	assert.Equal(t, "google-vertex-anthropic/claude-opus-5-5@default", translateOpenCodeModel("opus", overrides))
	// An override that already carries an @version is left untouched.
	overrides = map[string]string{"opus": "claude-opus-4-6@20250901"}
	assert.Equal(t, "google-vertex-anthropic/claude-opus-4-6@20250901", translateOpenCodeModel("opus", overrides))
	// A per-repo alias that maps to a provider/id spec passes through.
	overrides = map[string]string{"sonnet": "xai/grok-4.6"}
	assert.Equal(t, "xai/grok-4.6", translateOpenCodeModel("sonnet", overrides))

	// A non-Vertex provider from the environment gets no suffix.
	t.Setenv(openCodeProviderEnv, "myprov")
	overrides = map[string]string{"sonnet": "claude-sonnet-5"}
	assert.Equal(t, "myprov/claude-sonnet-5", translateOpenCodeModel("sonnet", overrides))
}

func TestMergedOpenCodeModelAliases(t *testing.T) {
	t.Parallel()
	// nil configAliases returns a copy of the built-in aliases.
	merged := mergedOpenCodeModelAliases(nil)
	assert.Equal(t, openCodeModelAliases["opus"], merged["opus"])
	// Mutating the merged map must not affect the package-level table.
	merged["opus"] = "mutated"
	assert.NotEqual(t, "mutated", openCodeModelAliases["opus"], "merged map must be a copy")
	// Per-repo overrides win.
	merged = mergedOpenCodeModelAliases(map[string]string{"sonnet": "claude-sonnet-5"})
	assert.Equal(t, "claude-sonnet-5", merged["sonnet"])
	assert.Equal(t, openCodeModelAliases["opus"], merged["opus"], "fleet default preserved")
	assert.Equal(t, openCodeModelAliases["haiku"], merged["haiku"], "fleet default preserved")
}

func TestOpenCodeModelAliases_CoversDocumentedAliases(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")
	for alias := range openCodeDocumentedAliases {
		t.Run(alias, func(t *testing.T) {
			id, ok := openCodeModelAliases[alias]
			require.True(t, ok, "documented alias %q missing from openCodeModelAliases", alias)
			require.NotEmpty(t, id, "openCodeModelAliases[%q] must not be empty", alias)
			spec := translateOpenCodeModel(alias, nil)
			require.Contains(t, spec, "/", "translated spec must be provider/id, got %q", spec)
			require.NotEqual(t, openCodeDefaultProvider+"/"+alias, spec,
				"alias %q should not pass through as bare id", alias)
		})
	}
}

func TestOpenCodeBareModelID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "claude-opus-5-5@default", openCodeBareModelID("google-vertex-anthropic/claude-opus-5-5@default"))
	assert.Equal(t, "gpt-5", openCodeBareModelID("openai/gpt-5"))
	assert.Equal(t, "bare", openCodeBareModelID("bare"))
}

func TestOpenCodePermissionRecord(t *testing.T) {
	t.Parallel()

	// nil (no restriction) → empty map, so OpenCode's default set applies.
	rec := openCodePermissionRecord(nil)
	assert.NotNil(t, rec)
	assert.Empty(t, rec)

	rec = openCodePermissionRecord([]string{"Bash", "Read", "Edit", "Glob", "LS", "Task"})
	assert.Equal(t, []string{"bash", "edit", "glob", "read", "task"}, openCodeToolNamesSorted(rec))

	// Agent (current name) and Task (legacy alias) both map to "task".
	rec = openCodePermissionRecord([]string{"Agent", "Read"})
	assert.Equal(t, []string{"read", "task"}, openCodeToolNamesSorted(rec))
	// Both Agent and Task present → "task" appears once (map key dedup).
	rec = openCodePermissionRecord([]string{"Agent", "Task", "Read"})
	assert.Equal(t, []string{"read", "task"}, openCodeToolNamesSorted(rec))

	// Skill enables OpenCode's native skill tool; unsupported names are dropped.
	rec = openCodePermissionRecord([]string{"Skill", "NoSuchTool"})
	assert.NotNil(t, rec)
	assert.Equal(t, []string{"skill"}, openCodeToolNamesSorted(rec))
	// Every known tool is explicitly denied, plus a wildcard catch-all.
	assert.Len(t, rec, len(openCodeAllToolIDs)+1)
	assert.Equal(t, "deny", rec["*"], "wildcard deny must cover unknown tools")
	for _, id := range openCodeAllToolIDs {
		if id != "skill" {
			assert.Equal(t, "deny", rec[id], "tool %q should be denied", id)
		}
	}
	assert.Equal(t, "deny", openCodePermissionRecord([]string{"Read"})["skill"])
	// Unrestricted agent (nil) has no wildcard — it uses OpenCode's defaults.
	assert.Empty(t, openCodePermissionRecord(nil)["*"])

	// Write maps to the "edit" permission key (OpenCode's write tool checks
	// "edit", not "write"; see write.ts:55 and permission/index.ts).
	rec = openCodePermissionRecord([]string{"Write", "Read"})
	assert.Equal(t, "allow", rec["edit"], "Write should set edit to allow")
	assert.Equal(t, "deny", rec["write"], "the write key stays denied — no Claude tool maps to it")
	assert.Equal(t, []string{"edit", "read"}, openCodeToolNamesSorted(rec))
}

func TestIntersectPermissionRecord(t *testing.T) {
	t.Parallel()

	rules := func(perm string) []openCodePermissionRule {
		return parseTrustedPermissionPolicy(`{"permission":` + perm + `}`)
	}
	agent := openCodePermissionRecord([]string{"Bash", "Read", "Edit", "WebFetch"})

	// OpenCode applies the last matching rule, so order and glob keys count.
	capped := intersectPermissionRecord(agent, rules(`{"*":"deny","bash":"allow","read":"allow","web*":"deny"}`))
	assert.Equal(t, "allow", capped["bash"])
	assert.Equal(t, "allow", capped["read"])
	assert.Equal(t, "deny", capped["edit"], "only the wildcard matches edit")
	assert.Equal(t, "deny", capped["webfetch"], "glob key matches webfetch")
	assert.Equal(t, "deny", capped["write"], "never allowed by the agent")

	// A trailing "*" overrides the earlier specific allow.
	capped = intersectPermissionRecord(agent, rules(`{"read":"allow","*":"deny"}`))
	assert.Equal(t, "deny", capped["read"])

	// "ask" caps "allow" (headless runs auto-reject it).
	capped = intersectPermissionRecord(agent, rules(`{"*":"deny","bash":"ask"}`))
	assert.Equal(t, "ask", capped["bash"])

	// No matching trusted rule keeps the agent's action; an unrestricted
	// agent stays empty.
	assert.Equal(t, "allow", intersectPermissionRecord(agent, nil)["bash"])
	assert.Empty(t, intersectPermissionRecord(openCodePermissionRecord(nil), rules(`{"*":"deny"}`)))

	// A pattern map replaces an agent allow with a copy of the map, and an
	// agent without Bash keeps its deny.
	bashMap := `{"gh *":"allow","*":"deny"}`
	capped = intersectPermissionRecord(agent, rules(`{"*":"deny","bash":`+bashMap+`}`))
	assert.JSONEq(t, bashMap, string(capped["bash"].(json.RawMessage)))
	assert.Equal(t, "deny", capped["*"])
	// A partial pattern map keeps the earlier flat rule as its fallback.
	capped = intersectPermissionRecord(agent, rules(`{"*":"allow","bash":{"rm *":"deny"}}`))
	assert.JSONEq(t, `{"*":"allow","rm *":"deny"}`, string(capped["bash"].(json.RawMessage)))
	assert.Equal(t, `{"*":"allow","rm *":"deny"}`, string(capped["bash"].(json.RawMessage)), "rule order kept")
	noBash := intersectPermissionRecord(openCodePermissionRecord([]string{"Read"}), rules(`{"bash":`+bashMap+`}`))
	assert.Equal(t, "deny", noBash["bash"])
}

func TestValidateOpenCodePermissionPolicy_DuplicateKeys(t *testing.T) {
	t.Parallel()

	for _, policy := range []string{
		`{"*":"deny","bash":{"curl *":"allow"},"bash":{"ls *":"allow"}}`,
		`{"bash":{"*":"allow","rm *":"deny","*":"allow"}}`,
	} {
		err := validateOpenCodePermissionPolicy(json.RawMessage(policy))
		require.Error(t, err, policy)
		assert.Contains(t, err.Error(), "duplicate permission key")
	}
	require.NoError(t, validateOpenCodePermissionPolicy(json.RawMessage(`{"*":"deny","bash":{"*":"allow","rm *":"deny"}}`)))
}

func TestParseTrustedPermissionPolicy(t *testing.T) {
	t.Parallel()

	got := parseTrustedPermissionPolicy(`{"permission":{"*":"deny","bash":{"gh *":"allow"},"read":"allow"}}`)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"*", "bash", "read"}, []string{got[0].Key, got[1].Key, got[2].Key}, "config order kept")
	assert.JSONEq(t, `{"gh *":"allow"}`, string(got[1].Value))

	assert.Nil(t, parseTrustedPermissionPolicy("not json"))
	assert.Nil(t, parseTrustedPermissionPolicy(`{"provider":{}}`))
}

func TestOpenCodeAgentMarkdown(t *testing.T) {
	t.Parallel()

	def := &piAgentDef{
		Name:        "triage",
		Description: "Triage incoming issues",
		Model:       "opus",
		Tools:       []string{"Bash", "Read"},
		Body:        "You are the triage agent.",
	}
	// Trusted policy allows bash and read but denies everything else.
	md, err := openCodeAgentMarkdown("triage", def,
		parseTrustedPermissionPolicy(`{"permission":{"*":"deny","bash":"allow","read":"allow"}}`))
	require.NoError(t, err)
	s := string(md)
	assert.True(t, strings.HasPrefix(s, "---\n"), "starts with frontmatter fence")
	assert.Contains(t, s, `"mode": "primary"`)
	assert.Contains(t, s, `"description": "Triage incoming issues"`)
	assert.Contains(t, s, `"model": "opus"`)
	assert.Contains(t, s, `"bash": "allow"`)
	assert.Contains(t, s, `"read": "allow"`)
	// Tools not in the allowlist are explicitly denied.
	assert.Contains(t, s, `"write": "deny"`)
	assert.Contains(t, s, `"webfetch": "deny"`)
	assert.Contains(t, s, "You are the triage agent.")
	assert.True(t, strings.HasSuffix(s, "\n"))
	// The body follows a closing fence.
	assert.Contains(t, s, "\n---\n\n")
}

func TestOpenCodeValidatedArg(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "google-vertex-anthropic/claude-opus-5-5@default", openCodeValidatedArg("google-vertex-anthropic/claude-opus-5-5@default"))
	assert.Equal(t, "high", openCodeValidatedArg("high"))
	// Control characters and shell metacharacters are stripped.
	assert.Equal(t, "opusrm -rf", openCodeValidatedArg("opus;rm -rf"))
	assert.Equal(t, "abc", openCodeValidatedArg("a\x00b\nc"))
	assert.Equal(t, "safe.name-1_2@x", openCodeValidatedArg("safe.name-1_2@x"))
}

func TestOpenCodeInstructionsConfig(t *testing.T) {
	t.Parallel()

	for _, repoDir := range []string{"/sandbox/workspace/myrepo", "/sandbox/workspace/repo\\with\"quotes\nand-newline"} {
		cfg := openCodeInstructionsConfig(repoDir)
		var decoded map[string][]string
		require.NoError(t, json.Unmarshal([]byte(cfg), &decoded))
		assert.Equal(t, map[string][]string{
			"instructions": {repoDir + "/AGENTS.md"},
		}, decoded)
	}
}

func TestBuildOpenCodeRunCommand(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		SandboxName:   "sbx",
		AgentBaseName: "triage",
		Model:         "opus",
		Effort:        "high",
		RepoDir:       sandbox.SandboxWorkspace + "/myrepo",
	}
	trustedEnv := openCodeTrustedEnv{
		ConfigContent:   `{"permission":{"*":"deny"}}`,
		CredentialsPath: "/runner/adc.json",
	}
	cmd := buildOpenCodeRunCommand(params, "triage", trustedEnv)

	assert.Contains(t, cmd, "cd "+shellQuote(params.RepoDir))
	// .env is read as data, never sourced (#8574).
	assert.Contains(t, cmd, "&& "+openCodeEnvLoad)
	assert.NotContains(t, cmd, ". "+shellQuote(sandbox.SandboxWorkspace+"/.env"))
	assert.NotContains(t, cmd, ". "+sandbox.SandboxWorkspace+"/.env")
	assert.Contains(t, cmd, "&& export "+openCodeRuntimeEnv+"=opencode")
	// opencode runs inside the tee pipeline so its stream lands in a sandbox
	// transcript file for ExtractTranscripts while still streaming to the host.
	assert.Contains(t, cmd, `"$`+openCodeBinaryVar+`" run --format json --thinking`)
	assert.Contains(t, cmd, "--model "+shellQuote("google-vertex-anthropic/claude-opus-5-5@default"))
	assert.Contains(t, cmd, "--variant "+shellQuote("high"))
	assert.Contains(t, cmd, "--agent "+shellQuote("triage"))
	assert.Contains(t, cmd, shellQuote(DefaultAgentPrompt))
	assert.Contains(t, cmd, "</dev/null")
	// The stream is tee'd to the sandbox transcript path and the transcript
	// dir is created first.
	assert.Contains(t, cmd, "mkdir -p "+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeRunnerSubdir))
	assert.Contains(t, cmd, `| "$`+openCodeTeeVar+`" `+shellQuote(openCodeSandboxTranscriptPath()))
	// opencode's real exit code is re-raised past tee (which always exits 0).
	assert.Contains(t, cmd, `"$`+openCodePrintfVar+`" '%s\n' "$?" > `+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeRunRCFile))
	assert.Contains(t, cmd, `$("$`+openCodeCatVar+`" `+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeRunRCFile))
	assert.Contains(t, cmd, `case "$FULLSEND_OPENCODE_RC" in ''|*[!0-9]*) FULLSEND_OPENCODE_RC=1`)
	// No hooks signal → no integrity guard.
	assert.NotContains(t, cmd, "refusing to run unhooked")
	// CLOUD_ML_REGION is mapped to GOOGLE_CLOUD_LOCATION for Vertex region parity.
	assert.Contains(t, cmd, openCodeCloudLocationExport)

	// Runner-owned opencode.json with instructions pointing at workspace
	// AGENTS.md is rewritten after .env is sourced.
	configPath := OpenCodeRuntime{}.ConfigDir() + "/" + openCodeConfigFile
	assert.Contains(t, cmd, "> "+shellQuote(configPath))
	expectedJSON := openCodeInstructionsConfig(params.RepoDir)
	assert.Contains(t, cmd, shellQuote(expectedJSON))
	// The config write must appear after .env sourcing.
	configIdx := strings.Index(cmd, shellQuote(configPath))
	envIdx := strings.Index(cmd, "&& "+openCodeEnvLoad)
	require.NotEqual(t, -1, configIdx, "config write must be present")
	require.NotEqual(t, -1, envIdx, ".env source must be present")
	assert.Greater(t, configIdx, envIdx, "runner-owned opencode.json must be rewritten after .env is sourced")
	assert.Contains(t, cmd, `"$`+openCodePrintfVar+`" '%s' `+shellQuote(expectedJSON))
	assert.Less(t, strings.Index(cmd, "&& "+openCodeBinaryPin()), envIdx, "opencode binary must be pinned before .env")
	assert.Less(t, strings.Index(cmd, "&& "+openCodeTrustedEnvPin(trustedEnv)), envIdx, "trusted values must be pinned before .env")
	// HOME and PATH pins are embedded inside openCodeTrustedEnvPin; verify the
	// pin variables appear before .env so a hostile .env cannot redirect them.
	assert.Less(t, strings.Index(cmd, openCodeHomePinVar+`="$HOME"`), envIdx, "HOME pin must appear before .env")
	assert.Less(t, strings.Index(cmd, openCodePathPinVar+`="$PATH"`), envIdx, "PATH pin must appear before .env")
	assert.Greater(t, strings.Index(cmd, "&& "+openCodeTrustedEnvRestore()), envIdx, "trusted values must be restored after .env")
	// EnvExports re-pin (OPENCODE_CONFIG_DIR, OPENCODE_DISABLE_PROJECT_CONFIG) must
	// appear after .env sourcing — a hostile .env could otherwise redirect config
	// discovery or re-enable the workspace config walk.
	for _, envExport := range (OpenCodeRuntime{}).EnvExports() {
		exportIdx := strings.Index(cmd, "&& "+envExport)
		require.NotEqual(t, -1, exportIdx, "EnvExports entry %q must be present", envExport)
		assert.Greater(t, exportIdx, envIdx, "EnvExports entry %q must appear after .env sourcing", envExport)
	}
}

func TestOpenCodeBinaryPin(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\necho REAL\n"), 0o755))
	envFile := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("opencode() { echo FAKE; }\nexport "+openCodeBinaryVar+"=/evil\nexport PATH=/nonexistent\n"), 0o644))

	cmd := exec.Command("sh", "-c", protectedDirsShell(t, bin)+openCodeBinaryPin()+" && "+envDataLoad(envFile, openCodeEnvDataDrop...)+" && "+cliLaunch(openCodeBinaryVar, openCodeNodeVar))
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.Equal(t, "REAL", strings.TrimSpace(string(out)))
	assert.Contains(t, stderr.String(), openCodeBinaryVar+" is not taken from .env")
}

// The proposed test from #8574: the OpenCode launch reads a hostile .env and
// .env.d file as data. No construct runs, LD_* never reaches opencode, plain
// exports still apply, assignments to the read-only pins are dropped rather
// than aborting the launch, and the pinned binary starts.
func TestOpenCodeLaunch_HostileEnvDoesNotExecute(t *testing.T) {
	requirePinnedSha256sum(t)
	f := newEnvDataFixture(t)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\necho REAL\nenv\n"), 0o755))
	marker := filepath.Join(f.ws, "pwned")
	hostile := strings.Join([]string{
		"export SUB=$(touch " + marker + ")",
		"export TICK=`touch " + marker + "`",
		"opencode() { touch " + marker + "; }",
		"touch " + marker,
		"exec /bin/true",
		"export LD_PRELOAD=/tmp/evil.so",
		"export LD_LIBRARY_PATH=/tmp/evil",
		"export LD_AUDIT=/tmp/evil.so",
		"export " + openCodeBinaryVar + "=/bin/true",
		"export " + openCodePathPinVar + "=/evil",
		"export PATH=/sandbox/workspace/bin:$PATH",
	}, "\n") + "\n"
	control := envDataControlLines(f.ws)
	f.write(t, ".env", hostile+"export KEPT=ok\n"+strings.Join(control, "\n")+"\n")
	f.write(t, ".env.d/harness.env", hostile+"export FROM_ENVD=yes\n")

	trusted := openCodeTrustedEnv{ConfigContent: `{"permission":{"*":"deny"}}`, CredentialsPath: "/runner/adc.json"}
	script := strings.Join([]string{
		openCodeBinaryPin(),
		openCodeUtilityPin(),
		openCodeTrustedEnvPin(trusted),
		envDataLoadWith(f.envFile, control, openCodeEnvDataDrop),
		strings.Join(OpenCodeRuntime{}.EnvExports(), " && "),
		openCodeTrustedEnvRestore(),
		cliLaunch(openCodeBinaryVar, openCodeNodeVar),
	}, " && ")
	cmd := exec.Command("/bin/sh", "-c", protectedDirsShell(t, bin)+script)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=/home/agent"}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())

	assert.NoFileExists(t, marker, "a construct from .env or .env.d ran")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.NotEmpty(t, lines)
	assert.Equal(t, "REAL", lines[0], "the pinned opencode starts")
	env := map[string]string{}
	for _, kv := range lines[1:] {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for _, name := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "SUB", "TICK"} {
		_, ok := env[name]
		assert.False(t, ok, "%s reached opencode", name)
	}
	assert.Equal(t, "ok", env["KEPT"], "plain exports from .env still apply")
	assert.Equal(t, "yes", env["FROM_ENVD"], "plain exports from .env.d still apply")
	assert.Equal(t, bin+":/usr/bin:/bin", env["PATH"], "PATH is restored to the pre-.env pin")
	assert.NotContains(t, stderr.String(), marker, "dropped lines are reported by number, not content")
	assert.Contains(t, stderr.String(), "harness.env")
	assert.Contains(t, stderr.String(), openCodePathPinVar+" is not taken from .env")
}

func TestOpenCodeCloudLocationExport(t *testing.T) {
	probe := openCodeCloudLocationExport + ` && echo "${GOOGLE_CLOUD_LOCATION-unset}"`
	for _, tc := range []struct{ location, region, want string }{
		{"", "", "unset"}, {"", "us-east5", "us-east5"}, {"GOOGLE_CLOUD_LOCATION=", "", "unset"},
	} {
		cmd := exec.Command("sh", "-c", probe)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "CLOUD_ML_REGION=" + tc.region}
		if tc.location != "" {
			cmd.Env = append(cmd.Env, tc.location)
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Equal(t, tc.want, strings.TrimSpace(string(out)), "CLOUD_ML_REGION=%q", tc.region)
	}
}

// requirePinnedSha256sum skips tests that run openCodeUtilityPin on hosts
// (macOS) where the pinned directories have no sha256sum.
func requirePinnedSha256sum(t *testing.T) {
	t.Helper()
	for _, d := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		if _, err := os.Stat(d + "/sha256sum"); err == nil {
			return
		}
	}
	t.Skip("no sha256sum in the pinned utility directories")
}

func TestOpenCodeExitPipeline(t *testing.T) {
	requirePinnedSha256sum(t)
	rcFile := filepath.Join(t.TempDir(), "run-rc")
	transcript := filepath.Join(t.TempDir(), "output.jsonl")
	shadow := `command() { printf FAKE; }; printf() { :; }; tee() { :; }; cat() { printf 0; }; `
	command := openCodeUtilityPin() + " && " + shadow + openCodeExitPipeline(
		[]string{"/bin/sh", "-c", shellQuote("command -p printf real-output; exit 7")},
		rcFile,
		transcript,
	)
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "%s", out)
	assert.Equal(t, 7, exitErr.ExitCode())
	data, readErr := os.ReadFile(transcript)
	require.NoError(t, readErr)
	assert.Equal(t, "real-output", string(data))
}

func TestOpenCodeHooksGuardUsesUnshadowableCommands(t *testing.T) {
	requirePinnedSha256sum(t)
	missing := filepath.Join(t.TempDir(), "missing-hooks.ts")
	shadow := `command() { return 0; }; test() { return 0; }; printf() { return 0; }; sha256sum() { return 0; }; cut() { return 0; }; `
	cmd := exec.Command("sh", "-c", openCodeUtilityPin()+" && "+shadow+openCodeHooksGuard(missing))
	err := cmd.Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, openCodeHooksMissingExit, exitErr.ExitCode())
}

// The restore is the second layer behind reading .env as data: what .env
// may still assign (plain exports) is put back or cleared after it.
func TestOpenCodeTrustedEnvRestore(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	envFile := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(envFile, []byte(
		"export OPENCODE_CONFIG_CONTENT=evil\n"+
			"unset GOOGLE_APPLICATION_CREDENTIALS\n"+
			// An agent-written .env could inject OPENCODE_PERMISSION to widen
			// the trusted policy (CWE-15). Verify restore clears it.
			`export OPENCODE_PERMISSION='{"edit":"allow","bash":"allow"}'`+"\n"+
			`export OPENCODE_CONFIG=/tmp/hostile.json`+"\n"+
			// Loader/linker injection vectors (CWE-426). Verify restore clears them.
			`export LD_PRELOAD=/tmp/evil.so`+"\n"+
			`export OPENCODE_TEST_HOME=/tmp/fake-home`+"\n"+
			// HOME and PATH hijack attempt — restore must re-pin them.
			`export HOME=/tmp/agent-home`+"\n"+
			`export PATH=/tmp/agent-bin:/usr/bin:/bin`+"\n",
	), 0o644))

	trustedEnv := openCodeTrustedEnv{
		ConfigContent:   `{"permission":{"*":"deny"}}`,
		CredentialsPath: "/runner/adc.json",
	}
	origHome := "/sandbox/runner-home"
	origPath := "/usr/local/bin:/usr/bin:/bin"
	// Set a known HOME and PATH so the pin captures them.
	command := "HOME=" + shellQuote(origHome) + " PATH=" + shellQuote(origPath) +
		" sh -c " + shellQuote(
		openCodeTrustedEnvPin(trustedEnv)+" && "+envDataTrustedPin()+" && "+envDataLoad(envFile, openCodeEnvDataDrop...)+" && "+openCodeTrustedEnvRestore()+
			` && printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' "$OPENCODE_CONFIG_CONTENT" "$GOOGLE_APPLICATION_CREDENTIALS" "$HOME" "$PATH" "$OPENCODE_PERMISSION" "$OPENCODE_CONFIG" "$LD_PRELOAD"`,
	)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s%s", out, stderr.String())
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// Expect exactly 4 non-empty lines: CONFIG_CONTENT, GOOGLE_APPLICATION_CREDENTIALS,
	// HOME, PATH. Dangerous vars (OPENCODE_PERMISSION, OPENCODE_CONFIG, LD_PRELOAD)
	// must be empty and therefore collapse to blank lines that TrimSpace removes.
	require.Len(t, lines, 4, "only four non-empty lines expected (pinned values); dangerous vars must be empty: %s", strings.Join(lines, "|"))
	assert.Equal(t, `{"permission":{"*":"deny"}}`, lines[0], "OPENCODE_CONFIG_CONTENT must be restored from pin")
	assert.Equal(t, "/runner/adc.json", lines[1], "GOOGLE_APPLICATION_CREDENTIALS must be restored from pin")
	assert.Equal(t, origHome, lines[2], "HOME must be restored to the pre-.env value")
	assert.Equal(t, origPath, lines[3], "PATH must be restored to the pre-.env value")
}

func TestOpenCodeReadTrustedEnvErrors(t *testing.T) {
	t.Run("exec error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := openCodeReadTrustedEnv("sb")
		require.ErrorContains(t, err, "reading the trusted OpenCode environment")
	})

	t.Run("malformed response", func(t *testing.T) {
		binDir := t.TempDir()
		script := "#!/bin/sh\nprintf malformed\n"
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		_, err := openCodeReadTrustedEnv("sb")
		require.ErrorContains(t, err, "malformed response")
	})

	t.Run("non-zero exit", func(t *testing.T) {
		binDir := t.TempDir()
		script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  echo "sandbox env broken" >&2
  exit 3
fi
exit 0
`
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		_, err := openCodeReadTrustedEnv("sb")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exited 3")
	})
}

func TestOpenCodeTrustedEnvReadCommand(t *testing.T) {
	requirePinnedSha256sum(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	want := openCodeTrustedEnv{
		ConfigContent:   "{\"permission\":{\"bash\":\"allow\"},\"quote\":\"'\",\"line\":\"a\\nb\"}",
		CredentialsPath: "/runner/path with spaces/adc.json",
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	envFile := filepath.Join(dir, ".env")
	// .env is read as data (#8574): the function definitions and the
	// command are dropped, and the assignment to a read-only pin cannot
	// abort the read.
	content := "command() { printf forged; }\nprintf() { :; }\ntouch " + marker + "\n" +
		"export " + openCodePrintfVar + "=/bin/true\n" +
		"export OPENCODE_CONFIG_CONTENT=" + shellQuote(want.ConfigContent) + "\n" +
		"export GOOGLE_APPLICATION_CREDENTIALS=" + shellQuote(want.CredentialsPath) + "\n"
	require.NoError(t, os.WriteFile(envFile, []byte(content), 0o644))

	cmd := exec.Command("sh", "-c", openCodeTrustedEnvReadCommand(envFile))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s", stderr.String())
	got, err := parseOpenCodeTrustedEnv(string(out))
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NoFileExists(t, marker, "a construct from .env ran")
	assert.NotContains(t, stderr.String(), marker, "dropped lines are reported by number, not content")
	assert.Contains(t, stderr.String(), "line 3:")
}

func TestOpenCodeValidateTrustedEnv(t *testing.T) {
	valid := openCodeTrustedEnv{
		ConfigContent:   `{"permission":{"bash":"allow","read":{"*":"allow"}}}`,
		CredentialsPath: "/runner/adc.json",
	}
	require.NoError(t, validateOpenCodeTrustedEnv(valid))

	tests := []struct {
		name string
		env  openCodeTrustedEnv
		want string
	}{
		{name: "empty config", env: openCodeTrustedEnv{CredentialsPath: valid.CredentialsPath}, want: "OPENCODE_CONFIG_CONTENT is empty"},
		{name: "invalid config", env: openCodeTrustedEnv{ConfigContent: "{", CredentialsPath: valid.CredentialsPath}, want: "invalid JSON"},
		{name: "missing permission", env: openCodeTrustedEnv{ConfigContent: `{}`, CredentialsPath: valid.CredentialsPath}, want: "no permission policy"},
		{name: "empty permission", env: openCodeTrustedEnv{ConfigContent: `{"permission":{}}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission policy"},
		{name: "whitespace empty permission", env: openCodeTrustedEnv{ConfigContent: `{"permission":{ }}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission policy"},
		{name: "null permission", env: openCodeTrustedEnv{ConfigContent: `{"permission":null}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission policy"},
		{name: "array permission", env: openCodeTrustedEnv{ConfigContent: `{"permission":[]}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission policy"},
		{name: "string permission", env: openCodeTrustedEnv{ConfigContent: `{"permission":"deny"}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission policy"},
		{name: "invalid action", env: openCodeTrustedEnv{ConfigContent: `{"permission":{"bash":"sometimes"}}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission action"},
		{name: "invalid pattern action", env: openCodeTrustedEnv{ConfigContent: `{"permission":{"bash":{"*":"sometimes"}}}`, CredentialsPath: valid.CredentialsPath}, want: "invalid permission action"},
		{name: "empty pattern map", env: openCodeTrustedEnv{ConfigContent: `{"permission":{"bash":{}}}`, CredentialsPath: valid.CredentialsPath}, want: "must be an action or non-empty pattern map"},
		{name: "non-string pattern value", env: openCodeTrustedEnv{ConfigContent: `{"permission":{"bash":{"*":1}}}`, CredentialsPath: valid.CredentialsPath}, want: "must be an action or non-empty pattern map"},
		{name: "non-object non-string tool value", env: openCodeTrustedEnv{ConfigContent: `{"permission":{"bash":true}}`, CredentialsPath: valid.CredentialsPath}, want: "must be an action or non-empty pattern map"},
		{name: "empty credentials", env: openCodeTrustedEnv{ConfigContent: valid.ConfigContent}, want: "GOOGLE_APPLICATION_CREDENTIALS is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, validateOpenCodeTrustedEnv(tt.env), tt.want)
		})
	}
}

func TestOpenCodeRunnerEnvConcurrentAccess(t *testing.T) {
	const goroutines = 12
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("concurrent-opencode-%d", i)
			want := openCodeTrustedEnv{ConfigContent: key, CredentialsPath: "/" + key}
			recordOpenCodeTrustedEnv(key, want)
			got, ok := lookupOpenCodeTrustedEnv(key)
			if !ok || got != want {
				t.Errorf("lookupOpenCodeTrustedEnv(%q) = %#v, %v; want %#v, true", key, got, ok, want)
			}
			forgetOpenCodeTrustedEnv(key)
		}()
	}
	wg.Wait()
}

func TestOpenCodeBootstrapFailureClearsTrustedEnv(t *testing.T) {
	const sandboxName = "failed-opencode-rebootstrap"
	recordOpenCodeTrustedEnv(sandboxName, openCodeTrustedEnv{ConfigContent: "stale"})
	t.Cleanup(func() { forgetOpenCodeTrustedEnv(sandboxName) })

	err := (OpenCodeRuntime{}).Bootstrap(bootstrapInput{sandboxName: sandboxName})
	require.Error(t, err)
	_, ok := lookupOpenCodeTrustedEnv(sandboxName)
	assert.False(t, ok)
}

func TestBuildOpenCodeRunCommand_PromptOverride(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		AgentBaseName: "fix",
		RepoDir:       "/repo",
		Prompt:        "Previous attempt failed: retry with the fix.",
	}
	cmd := buildOpenCodeRunCommand(params, "fix", openCodeTrustedEnv{})
	assert.Contains(t, cmd, shellQuote("Previous attempt failed: retry with the fix."))
	assert.NotContains(t, cmd, shellQuote(DefaultAgentPrompt))
}

func TestBuildOpenCodeRunCommand_HooksGuard(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		AgentBaseName:     "fix",
		RepoDir:           "/repo",
		HooksSettingsPath: "/sandbox/opencode-config/hooks.json",
	}
	cmd := buildOpenCodeRunCommand(params, "fix", openCodeTrustedEnv{})
	// The sha256 fail-closed guard is emitted before and after .env is sourced.
	firstGuardIdx := strings.Index(cmd, "refusing to run unhooked")
	lastGuardIdx := strings.LastIndex(cmd, "refusing to run unhooked")
	envIdx := strings.Index(cmd, "&& "+openCodeEnvLoad)
	require.NotEqual(t, -1, firstGuardIdx, "hooks guard must be present")
	require.NotEqual(t, -1, envIdx)
	assert.Equal(t, 2, strings.Count(cmd, "refusing to run unhooked"))
	assert.Less(t, firstGuardIdx, envIdx, "integrity guard must run before .env is sourced")
	assert.Greater(t, lastGuardIdx, envIdx, "integrity guard must run again after .env is sourced")
	assert.Contains(t, cmd, `"$`+openCodeSHA256Var+`"`)
	assert.Contains(t, cmd, openCodeUtilityPin())
	assert.Contains(t, cmd, OpenCodeRuntime{}.openCodeHooksExtensionPath())
	assert.Contains(t, cmd, "exit 97")
}

func TestBuildOpenCodeRunCommand_ResetsOverridesAfterEnv(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	env := openCodeTrustedEnv{AgentPath: "/sandbox/opencode-config/agent/triage.md", AgentDefinition: "---\n{}\n---\nbody\n"}
	cmd := buildOpenCodeRunCommand(RunParams{AgentBaseName: "triage", RepoDir: "/repo"}, "triage", env)
	envIdx := strings.Index(cmd, "&& "+openCodeEnvLoad)
	resetIdx := strings.Index(cmd, `"$`+openCodeRmVar+`" -rf -- `+OpenCodeRuntime{}.openCodeOverridePaths(false))
	guardIdx := strings.Index(cmd, openCodeAgentGuard(env.AgentPath, env.AgentDefinition))
	require.NotEqual(t, -1, resetIdx)
	require.NotEqual(t, -1, guardIdx)
	assert.Greater(t, resetIdx, envIdx, "reset must run after .env")
	configPath := OpenCodeRuntime{}.ConfigDir() + "/" + openCodeConfigFile
	assert.Greater(t, strings.Index(cmd, "> "+shellQuote(configPath)), resetIdx, "the reset clears opencode.json before the runner rewrites it")
	assert.Less(t, guardIdx, strings.Index(cmd, " run --format json"), "agent is checked before opencode starts")
	assert.NotContains(t, cmd, shellQuote(env.AgentDefinition), "the agent body is not inlined (openshell argument limit)")
}

func TestOpenCodeAgentGuard(t *testing.T) {
	requirePinnedSha256sum(t)
	agent := filepath.Join(t.TempDir(), "triage.md")
	require.NoError(t, os.WriteFile(agent, []byte("trusted"), 0o644))
	for content, want := range map[string]int{"trusted": 0, "tampered": openCodeAgentModifiedExit} {
		require.NoError(t, os.WriteFile(agent, []byte(content), 0o644))
		cmd := exec.Command("sh", "-c", openCodeUtilityPin()+" && "+openCodeAgentGuard(agent, "trusted"))
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		err := cmd.Run()
		got := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			got = exitErr.ExitCode()
		}
		assert.Equal(t, want, got, content)
	}
}

func TestBuildOpenCodeRunCommand_AgentModelPrecedence(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	env := openCodeTrustedEnv{AgentModel: "haiku"}
	cmd := buildOpenCodeRunCommand(RunParams{AgentBaseName: "triage", RepoDir: "/repo"}, "triage", env)
	assert.Contains(t, cmd, "--model "+shellQuote("google-vertex-anthropic/claude-haiku-5-5@default"))

	cmd = buildOpenCodeRunCommand(RunParams{AgentBaseName: "triage", RepoDir: "/repo", Model: "sonnet"}, "triage", env)
	assert.Contains(t, cmd, "--model "+shellQuote("google-vertex-anthropic/claude-sonnet-5-5@default"))
}

func TestBuildOpenCodeRunCommand_DebugMode(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")

	params := RunParams{
		AgentBaseName: "triage",
		RepoDir:       "/repo",
		Debug:         "true",
	}
	cmd := buildOpenCodeRunCommand(params, "triage", openCodeTrustedEnv{})
	// Global flags must precede `run` so they apply globally (opencode_run.go comment).
	assert.Contains(t, cmd, `"`+"$"+openCodeBinaryVar+`" --print-logs --log-level DEBUG run --format json`)
	assert.Contains(t, cmd, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+openCodeDebugLogFile))
}

func TestBuildOpenCodeRunCommand_FallbackModelsIgnored(t *testing.T) {
	// Fallback models are logged as a warning in Run, not in the command
	// builder; the command should still be built without error.
	t.Setenv(openCodeProviderEnv, "")
	params := RunParams{
		AgentBaseName:  "triage",
		RepoDir:        "/repo",
		FallbackModels: []string{"sonnet", "haiku"},
	}
	cmd := buildOpenCodeRunCommand(params, "triage", openCodeTrustedEnv{})
	assert.Contains(t, cmd, `"$`+openCodeBinaryVar+`" run`)
}

func TestOpenCodeEffortFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		effort string
		want   string
		ok     bool
	}{
		{"low", "low", true},
		{"medium", "medium", true},
		{"high", "high", true},
		{"xhigh", "max", true},
		{"max", "max", true},
		{"off", "", false},
		{"minimal", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			got, ok := openCodeEffortFor(tt.effort)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildOpenCodeRunCommand_UnmappedEffortOmitsVariant(t *testing.T) {
	t.Setenv(openCodeProviderEnv, "")
	params := RunParams{
		AgentBaseName: "triage",
		RepoDir:       "/repo",
		Effort:        "off",
	}
	cmd := buildOpenCodeRunCommand(params, "triage", openCodeTrustedEnv{})
	assert.NotContains(t, cmd, "--variant", "unmapped effort should not produce --variant")
}

func TestOpenCodeRuntimeRunRequiresBootstrapState(t *testing.T) {
	const sandboxName = "missing-opencode-bootstrap-state"
	forgetOpenCodeTrustedEnv(sandboxName)
	t.Cleanup(func() { forgetOpenCodeTrustedEnv(sandboxName) })

	exitCode, err := (OpenCodeRuntime{}).Run(
		t.Context(),
		RunParams{SandboxName: sandboxName, AgentBaseName: "triage"},
		ui.New(&strings.Builder{}),
		time.Now(),
		&RunMetrics{},
	)
	assert.Equal(t, -1, exitCode)
	require.ErrorContains(t, err, "trusted environment was not recorded during bootstrap")
}

func TestOpenCodeHooksExtensionPath(t *testing.T) {
	t.Parallel()
	r := OpenCodeRuntime{}
	assert.Equal(t, "/sandbox/opencode-config/plugins/fullsend-hooks.ts", r.openCodeHooksExtensionPath())
}

func TestOpenCodeRunFailsClosedWhenHooksEnabledButAdapterMissing(t *testing.T) {
	t.Parallel()
	// When a harness enables hooks (HooksSettingsPath non-empty) but the
	// adapter bytes are nil (not yet available — #515), Run must refuse to
	// start rather than running unhooked.
	require.Nil(t, openCodeHooksExtensionBytes(), "precondition: adapter bytes are nil until #515")

	r := OpenCodeRuntime{}
	params := RunParams{
		AgentBaseName:     "fix",
		RepoDir:           "/repo",
		HooksSettingsPath: "/sandbox/opencode-config/hooks.json",
	}
	exitCode, err := r.Run(t.Context(), params, nil, time.Now(), &RunMetrics{})
	require.Error(t, err)
	assert.Equal(t, -1, exitCode)
	assert.Contains(t, err.Error(), "hooks adapter not yet available")
	assert.Contains(t, err.Error(), "#515")
}

func TestParseOpenCodeTranscriptFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// A healthy stream: one text event + a step_finish → non-error result.
	okPath := filepath.Join(dir, "ok.jsonl")
	okStream := strings.Join([]string{
		`{"type":"text","timestamp":1,"sessionID":"s1","part":{"text":"done"}}`,
		`{"type":"step_finish","timestamp":2,"sessionID":"s1","part":{"reason":"stop","cost":0.01,"tokens":{"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(okPath, []byte(okStream), 0o644))

	te, ok := parseOpenCodeTranscriptFile(okPath)
	require.True(t, ok)
	assert.False(t, te.IsError)
	assert.Equal(t, "ok.jsonl", te.Source)
	assert.Equal(t, "stop", te.Subtype)

	// An error stream trips IsError.
	errPath := filepath.Join(dir, "err.jsonl")
	errStream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"ProviderError","data":{"message":"boom"}}}` + "\n"
	require.NoError(t, os.WriteFile(errPath, []byte(errStream), 0o644))

	te, ok = parseOpenCodeTranscriptFile(errPath)
	require.True(t, ok)
	assert.True(t, te.IsError)
	assert.Contains(t, te.ErrorMessage, "boom")

	// A zero-turn (empty content) capture is treated as an error (false-success
	// guard), because parseOpenCodeStream still synthesizes a ResultEvent.
	zeroPath := filepath.Join(dir, "zero.jsonl")
	require.NoError(t, os.WriteFile(zeroPath, []byte(`{"type":"step_start","timestamp":1,"sessionID":"s1"}`+"\n"), 0o644))
	te, ok = parseOpenCodeTranscriptFile(zeroPath)
	require.True(t, ok)
	assert.True(t, te.IsError, "zero-turn stream should be flagged as error")

	// A malformed line beside a completed turn is still an error, as in Run.
	corruptPath := filepath.Join(dir, "corrupt.jsonl")
	require.NoError(t, os.WriteFile(corruptPath, []byte("{not json\n"+okStream), 0o644))
	te, ok = parseOpenCodeTranscriptFile(corruptPath)
	require.True(t, ok)
	assert.True(t, te.IsError, "malformed line should be flagged as error")

	// An empty file yields ok=false.
	emptyPath := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(emptyPath, nil, 0o644))
	_, ok = parseOpenCodeTranscriptFile(emptyPath)
	assert.False(t, ok)

	// A missing file yields ok=false.
	_, ok = parseOpenCodeTranscriptFile(filepath.Join(dir, "nope.jsonl"))
	assert.False(t, ok)
}

func TestOpenCodeParseTranscriptErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	errStream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"E","data":{"message":"kaboom"}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-output.jsonl"), []byte(errStream), 0o644))
	okStream := `{"type":"step_finish","timestamp":2,"sessionID":"s1","part":{"reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b-output.jsonl"), []byte(okStream), 0o644))

	summaries := OpenCodeRuntime{}.ParseTranscriptErrors(dir)
	require.Len(t, summaries, 1)
	assert.Equal(t, "a-output.jsonl", summaries[0].Source)
	assert.True(t, summaries[0].IsError)
}

func TestOpenCodeEmitTranscriptErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	OpenCodeRuntime{}.EmitTranscriptErrors(&buf, nil)
	// No panic, no output for an empty summary set.
	assert.Empty(t, buf.String())
}

func TestOpenCodeParseTranscriptFile_Delegates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "output.jsonl")
	stream := `{"type":"error","timestamp":1,"sessionID":"s1","error":{"name":"E","data":{"message":"nope"}}}` + "\n"
	require.NoError(t, os.WriteFile(p, []byte(stream), 0o644))

	te, ok := OpenCodeRuntime{}.ParseTranscriptFile(p)
	require.True(t, ok)
	assert.True(t, te.IsError)
	assert.Contains(t, te.ErrorMessage, "nope")

	// Missing file → ok=false.
	_, ok = OpenCodeRuntime{}.ParseTranscriptFile(filepath.Join(dir, "missing.jsonl"))
	assert.False(t, ok)
}

func TestOpenCodeExtractDebugLog_NoDebugNoop(t *testing.T) {
	t.Parallel()
	// An empty debug value is a no-op (no sandbox interaction).
	err := OpenCodeRuntime{}.ExtractDebugLog("sb", "/tmp/does-not-matter", "")
	assert.NoError(t, err)
}

func TestOpenCodeExtractDebugLog_DownloadsWhenDebugSet(t *testing.T) {
	binDir := t.TempDir()
	// A fake openshell that writes a debug log file on download.
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'debug log content\n' > "$5/$(basename "$4")"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	localPath := filepath.Join(t.TempDir(), "debug.log")
	// Exercise the code path where debug != "" (line 77 of opencode_transcript.go).
	err := OpenCodeRuntime{}.ExtractDebugLog("sb", localPath, "true")
	require.NoError(t, err)
	data, readErr := os.ReadFile(localPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "debug log content")
}

func TestOpenCodeParseTranscriptErrors_BadDir(t *testing.T) {
	t.Parallel()
	// A non-existent directory returns nil (no panic).
	summaries := OpenCodeRuntime{}.ParseTranscriptErrors("/nonexistent/dir")
	assert.Nil(t, summaries)
}

func TestOpenCodeParseTranscriptErrors_SkipsDirsAndNonJSONL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a subdirectory with .jsonl suffix that should be skipped.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir.jsonl"), 0o755))
	// Create a non-jsonl file that should be skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not jsonl"), 0o644))
	summaries := OpenCodeRuntime{}.ParseTranscriptErrors(dir)
	assert.Empty(t, summaries)
}

func TestOpenCodeExtractTranscripts_ProbeExecError(t *testing.T) {
	// When openshell is not on PATH, sandbox.Exec returns an error,
	// which ExtractTranscripts wraps as "checking transcript".
	t.Setenv("PATH", t.TempDir()) // empty PATH → openshell not found

	outDir := filepath.Join(t.TempDir(), "out")
	err := OpenCodeRuntime{}.ExtractTranscripts("sb", "triage", outDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking transcript")
}

func TestOpenCodeExtractTranscripts_CreateRejected(t *testing.T) {
	// An agentLabel containing path traversal causes root.Create to reject
	// the filename, exercising the createErr != nil path.
	binDir := t.TempDir()
	// Fake openshell that always reports "found" for the probe.
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  echo found
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	outDir := filepath.Join(t.TempDir(), "out")
	// A label with "../" triggers root.Create rejection (path containment).
	err := OpenCodeRuntime{}.ExtractTranscripts("sb", "../escape", outDir)
	// Should not error (prints a warning instead), but should not create
	// any file outside the output dir.
	assert.NoError(t, err)
	entries, _ := os.ReadDir(outDir)
	assert.Empty(t, entries)
	// Verify no file escaped to the parent directory.
	parentDir := filepath.Dir(outDir)
	escapedPath := filepath.Join(parentDir, "escape-output.jsonl")
	_, statErr := os.Stat(escapedPath)
	assert.True(t, os.IsNotExist(statErr), "file must not escape to parent directory: %s", escapedPath)
}
