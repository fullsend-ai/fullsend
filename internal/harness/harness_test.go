package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_ValidHarness(t *testing.T) {
	content := `
agent: agents/hello-world.md
image: registry.example.com/sandbox:v1
skills:
  - skills/hello-world-summary
validation_loop:
  script: scripts/validate-output.sh
  max_iterations: 1
runner_env:
  REPO_NAME: "${REPO_NAME}"
timeout_minutes: 5
`
	dir := t.TempDir()
	path := filepath.Join(dir, "hello-world.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "agents/hello-world.md", h.Agent)
	assert.Equal(t, "registry.example.com/sandbox:v1", h.Image)
	assert.Equal(t, []string{"skills/hello-world-summary"}, h.Skills)
	require.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "scripts/validate-output.sh", h.ValidationLoop.Script)
	assert.Equal(t, 1, h.ValidationLoop.MaxIterations)
	assert.Equal(t, `${REPO_NAME}`, h.RunnerEnv["REPO_NAME"])
	assert.Equal(t, 5, h.TimeoutMinutes)
}

func TestResolveRelativeTo_ImageUnchanged(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Image: "registry.example.com/sandbox:v1",
	}

	require.NoError(t, h.ResolveRelativeTo("/base/dir"))

	// Image is a registry reference, not a filesystem path — must not be resolved.
	assert.Equal(t, "registry.example.com/sandbox:v1", h.Image)
}

func TestLoad_MissingAgent(t *testing.T) {
	content := `
skills:
  - skills/hello-world-summary
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent field is required")
}

func TestLoad_ValidationLoopMissingScript(t *testing.T) {
	content := `
agent: agents/test.md
validation_loop:
  max_iterations: 3
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.script is required")
}

func TestLoad_HostFiles(t *testing.T) {
	content := `
agent: agents/test.md
host_files:
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /sandbox/workspace/.gcp-credentials.json
  - src: /etc/ssl/certs/ca-certificates.crt
    dest: /etc/ssl/certs/ca-certificates.crt
  - src: env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)

	require.Len(t, h.HostFiles, 3)
	assert.Equal(t, "${GOOGLE_APPLICATION_CREDENTIALS}", h.HostFiles[0].Src)
	assert.Equal(t, "/sandbox/workspace/.gcp-credentials.json", h.HostFiles[0].Dest)
	assert.False(t, h.HostFiles[0].Expand)
	assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt", h.HostFiles[1].Src)
	assert.Equal(t, "/etc/ssl/certs/ca-certificates.crt", h.HostFiles[1].Dest)
	assert.False(t, h.HostFiles[1].Expand)
	assert.Equal(t, "env/gcp-vertex.env", h.HostFiles[2].Src)
	assert.Equal(t, "/sandbox/workspace/.env.d/gcp-vertex.env", h.HostFiles[2].Dest)
	assert.True(t, h.HostFiles[2].Expand)
}

func TestValidate_HostFileMissingSrc(t *testing.T) {
	content := `
agent: agents/test.md
host_files:
  - dest: /sandbox/workspace/.gcp-credentials.json
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host_files[0]: src is required")
}

func TestValidate_HostFileMissingDest(t *testing.T) {
	content := `
agent: agents/test.md
host_files:
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host_files[0]: dest is required")
}

func TestResolveRelativeTo(t *testing.T) {
	h := &Harness{
		Agent:      "agents/hello-world.md",
		Policy:     "policies/readonly.yaml",
		Skills:     []string{"skills/hello-world-summary"},
		PreScript:  "scripts/pre.sh",
		PostScript: "scripts/post.sh",
		AgentInput: "agent-input",
		ValidationLoop: &ValidationLoop{
			Script: "scripts/validate.sh",
		},
	}

	require.NoError(t, h.ResolveRelativeTo("/base/dir"))

	assert.Equal(t, "/base/dir/agents/hello-world.md", h.Agent)
	assert.Equal(t, "/base/dir/policies/readonly.yaml", h.Policy)
	assert.Equal(t, []string{"/base/dir/skills/hello-world-summary"}, h.Skills)
	assert.Equal(t, "/base/dir/scripts/pre.sh", h.PreScript)
	assert.Equal(t, "/base/dir/scripts/post.sh", h.PostScript)
	assert.Equal(t, "/base/dir/agent-input", h.AgentInput)
	assert.Equal(t, "/base/dir/scripts/validate.sh", h.ValidationLoop.Script)
}

func TestResolveRelativeTo_HostFiles(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		HostFiles: []HostFile{
			{Src: "env/gcp-vertex.env", Dest: "/sandbox/workspace/.env.d/gcp-vertex.env", Expand: true},
			{Src: "${GOOGLE_APPLICATION_CREDENTIALS}", Dest: "/sandbox/workspace/.gcp-credentials.json"},
			{Src: "/absolute/path/file.txt", Dest: "/sandbox/workspace/file.txt"},
		},
	}

	require.NoError(t, h.ResolveRelativeTo("/base/dir"))

	// Relative path without ${VAR} gets resolved.
	assert.Equal(t, "/base/dir/env/gcp-vertex.env", h.HostFiles[0].Src)
	// ${VAR} path is NOT resolved (expanded at bootstrap time).
	assert.Equal(t, "${GOOGLE_APPLICATION_CREDENTIALS}", h.HostFiles[1].Src)
	// Absolute path is unchanged.
	assert.Equal(t, "/absolute/path/file.txt", h.HostFiles[2].Src)
}

func TestResolveRelativeTo_AbsolutePathsUnchanged(t *testing.T) {
	h := &Harness{
		Agent: "/absolute/path/agent.md",
	}

	require.NoError(t, h.ResolveRelativeTo("/base/dir"))

	assert.Equal(t, "/absolute/path/agent.md", h.Agent)
}

func TestResolveRelativeTo_TraversalRejected(t *testing.T) {
	h := &Harness{Agent: "../../etc/shadow.md"}
	err := h.ResolveRelativeTo("/base/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside fullsend directory")
}

func TestResolveRelativeTo_HostFileTraversalRejected(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		HostFiles: []HostFile{
			{Src: "../../../etc/shadow", Dest: "/sandbox/workspace/shadow"},
		},
	}
	err := h.ResolveRelativeTo("/base/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside fullsend directory")
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading harness file")
}

func TestValidateRunnerEnv_UnsetVar(t *testing.T) {
	h := &Harness{
		Agent:     "test.md",
		RunnerEnv: map[string]string{"KEY": "${DEFINITELY_NOT_SET_VAR_XYZ}"},
	}
	err := h.ValidateRunnerEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_VAR_XYZ")
}

func TestValidateRunnerEnv_LiteralValue(t *testing.T) {
	h := &Harness{
		Agent:     "test.md",
		RunnerEnv: map[string]string{"KEY": "literal_value"},
	}
	require.NoError(t, h.ValidateRunnerEnv())
}

func TestValidateRunnerEnv_EmptyVarAllowed(t *testing.T) {
	t.Setenv("EMPTY_ALLOWED_VAR", "")
	h := &Harness{
		Agent:     "test.md",
		RunnerEnv: map[string]string{"KEY": "${EMPTY_ALLOWED_VAR}"},
	}
	require.NoError(t, h.ValidateRunnerEnv())
}

// --- Security config tests ---

func TestSecurityEnabled_Default(t *testing.T) {
	h := &Harness{Agent: "test.md"}
	assert.True(t, h.SecurityEnabled(), "nil Security should default to enabled")
}

func TestSecurityEnabled_ExplicitTrue(t *testing.T) {
	enabled := true
	h := &Harness{Agent: "test.md", Security: &SecurityConfig{Enabled: &enabled}}
	assert.True(t, h.SecurityEnabled())
}

func TestSecurityEnabled_ExplicitFalse(t *testing.T) {
	disabled := false
	h := &Harness{Agent: "test.md", Security: &SecurityConfig{Enabled: &disabled}}
	assert.False(t, h.SecurityEnabled())
}

func TestFailModeClosed_Default(t *testing.T) {
	h := &Harness{Agent: "test.md"}
	assert.True(t, h.FailModeClosed(), "nil Security should default to closed")
}

func TestFailModeClosed_ExplicitClosed(t *testing.T) {
	h := &Harness{Agent: "test.md", Security: &SecurityConfig{FailMode: "closed"}}
	assert.True(t, h.FailModeClosed())
}

func TestFailModeClosed_Open(t *testing.T) {
	h := &Harness{Agent: "test.md", Security: &SecurityConfig{FailMode: "open"}}
	assert.False(t, h.FailModeClosed())
}

func TestLoad_SecurityConfig(t *testing.T) {
	content := `
agent: agents/test.md
security:
  fail_mode: open
  host_scanners:
    unicode_normalizer: true
    context_injection: false
    llm_guard:
      threshold: 0.85
      match_type: sentence
  sandbox_hooks:
    tirith:
      fail_on: medium
    ssrf_pretool: true
  escalation:
    on_critical: review
    review_label: needs-review
  trace:
    enabled: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "sec.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, h.Security)

	assert.Equal(t, "open", h.Security.FailMode)
	assert.False(t, h.FailModeClosed())

	require.NotNil(t, h.Security.HostScanners)
	assert.True(t, BoolDefault(h.Security.HostScanners.UnicodeNormalizer, true))
	assert.False(t, BoolDefault(h.Security.HostScanners.ContextInjection, true))

	require.NotNil(t, h.Security.HostScanners.LLMGuard)
	assert.Equal(t, 0.85, h.Security.HostScanners.LLMGuard.Threshold)
	assert.Equal(t, "sentence", h.Security.HostScanners.LLMGuard.MatchType)

	require.NotNil(t, h.Security.SandboxHooks)
	require.NotNil(t, h.Security.SandboxHooks.Tirith)
	assert.Equal(t, "medium", h.Security.SandboxHooks.Tirith.FailOn)
	assert.True(t, BoolDefault(h.Security.SandboxHooks.SSRFPreTool, true))

	require.NotNil(t, h.Security.Escalation)
	assert.Equal(t, "review", h.Security.Escalation.OnCritical)
	assert.Equal(t, "needs-review", h.Security.Escalation.ReviewLabel)

	require.NotNil(t, h.Security.Trace)
	assert.True(t, BoolDefault(h.Security.Trace.Enabled, true))
}

func TestValidate_SecurityInvalidFailMode(t *testing.T) {
	h := &Harness{Agent: "test.md", Security: &SecurityConfig{FailMode: "invalid"}}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail_mode")
}

func TestValidate_SecurityInvalidLLMGuardThreshold(t *testing.T) {
	h := &Harness{
		Agent: "test.md",
		Security: &SecurityConfig{
			HostScanners: &HostScanners{
				LLMGuard: &LLMGuardConfig{Threshold: 1.5},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "threshold")
}

func TestValidate_SecurityInvalidLLMGuardMatchType(t *testing.T) {
	h := &Harness{
		Agent: "test.md",
		Security: &SecurityConfig{
			HostScanners: &HostScanners{
				LLMGuard: &LLMGuardConfig{MatchType: "word"},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "match_type")
}

func TestValidate_SecurityInvalidTirithFailOn(t *testing.T) {
	h := &Harness{
		Agent: "test.md",
		Security: &SecurityConfig{
			SandboxHooks: &SandboxHooks{
				Tirith: &TirithConfig{FailOn: "low"},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail_on")
}

func TestValidate_SecurityInvalidEscalation(t *testing.T) {
	h := &Harness{
		Agent: "test.md",
		Security: &SecurityConfig{
			Escalation: &EscalationConfig{OnCritical: "ignore"},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "on_critical")
}

func TestValidate_SecurityValidConfig(t *testing.T) {
	h := &Harness{
		Agent: "test.md",
		Security: &SecurityConfig{
			FailMode: "open",
			HostScanners: &HostScanners{
				LLMGuard: &LLMGuardConfig{Threshold: 0.92, MatchType: "sentence"},
			},
			SandboxHooks: &SandboxHooks{
				Tirith: &TirithConfig{FailOn: "high"},
			},
			Escalation: &EscalationConfig{OnCritical: "review"},
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidateRunnerEnv_HostFileSrcUnset(t *testing.T) {
	h := &Harness{
		Agent: "test.md",
		HostFiles: []HostFile{
			{Src: "${DEFINITELY_NOT_SET_VAR_XYZ}", Dest: "/tmp/dest"},
		},
	}
	err := h.ValidateRunnerEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_VAR_XYZ")
}

func TestValidateRunnerEnv_PartialExpansion(t *testing.T) {
	h := &Harness{
		Agent:     "test.md",
		RunnerEnv: map[string]string{"ENDPOINT": "https://${DEFINITELY_NOT_SET_VAR_XYZ}/api"},
	}
	err := h.ValidateRunnerEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_VAR_XYZ")
}

func TestValidate_AgentNameInvalid(t *testing.T) {
	h := &Harness{Agent: "agents/test';echo hack;echo '.md"}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestValidate_AgentNameValid(t *testing.T) {
	h := &Harness{Agent: "agents/hello-world_v2.md"}
	require.NoError(t, h.Validate())
}

func TestValidate_ModelInvalid(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", Model: "sonnet'; echo hack"}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestValidate_ModelValid(t *testing.T) {
	for _, model := range []string{
		"claude-sonnet-4-6",
		"claude-sonnet-4-6@default",
		"claude-sonnet-4-6@20250514",
		"claude-opus-4-1@20250805",
	} {
		h := &Harness{Agent: "agents/test.md", Model: model}
		require.NoError(t, h.Validate(), "model %q should be valid", model)
	}
}

func TestValidate_PostScriptWithoutValidationLoop(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", PostScript: "scripts/post.sh"}
	require.NoError(t, h.Validate())
}

func TestValidate_PostScriptWithValidationLoop(t *testing.T) {
	h := &Harness{
		Agent:      "agents/test.md",
		PostScript: "scripts/post.sh",
		ValidationLoop: &ValidationLoop{
			Script:        "scripts/validate.sh",
			MaxIterations: 2,
		},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_NegativeTimeout(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", TimeoutMinutes: -1}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout_minutes must be non-negative")
}

func TestValidate_NegativeSandboxTimeout(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: -1}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox_timeout_seconds must be 0 (default) or between 30 and 600")
}

func TestValidate_SandboxTimeoutTooSmall(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: 10}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox_timeout_seconds must be 0 (default) or between 30 and 600")
}

func TestValidate_SandboxTimeoutTooLarge(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: 601}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox_timeout_seconds must be 0 (default) or between 30 and 600")
}

func TestValidate_SandboxTimeoutAtMin(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: 30}
	require.NoError(t, h.Validate())
}

func TestValidate_SandboxTimeoutAtMax(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: 600}
	require.NoError(t, h.Validate())
}

func TestValidate_ZeroSandboxTimeout(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: 0}
	require.NoError(t, h.Validate())
}

func TestValidate_PositiveSandboxTimeout(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", SandboxTimeoutSeconds: 180}
	require.NoError(t, h.Validate())
}

func TestLoad_SandboxTimeoutField(t *testing.T) {
	content := `
agent: agents/test.md
sandbox_timeout_seconds: 180
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 180, h.SandboxTimeoutSeconds)
}

func TestLoad_ModelField(t *testing.T) {
	content := `
agent: agents/test.md
model: sonnet
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "sonnet", h.Model)
}

func TestValidateFilesExist_MissingAgent(t *testing.T) {
	h := &Harness{Agent: "/nonexistent/agent.md"}
	err := h.ValidateFilesExist()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}

func TestValidateFilesExist_MissingSkill(t *testing.T) {
	dir := t.TempDir()
	agentFile := filepath.Join(dir, "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("agent"), 0o644))

	h := &Harness{
		Agent:  agentFile,
		Skills: []string{"/nonexistent/skill"},
	}
	err := h.ValidateFilesExist()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skills[0]")
}

func TestValidateFilesExist_SkipsVarPaths(t *testing.T) {
	dir := t.TempDir()
	agentFile := filepath.Join(dir, "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("agent"), 0o644))

	h := &Harness{
		Agent: agentFile,
		HostFiles: []HostFile{
			{Src: "${SOME_VAR}", Dest: "/tmp/dest"},
		},
	}
	// Should not error — ${VAR} paths are expanded at bootstrap time.
	require.NoError(t, h.ValidateFilesExist())
}

func TestValidate_PluginNameValid(t *testing.T) {
	h := &Harness{
		Agent:   "agents/test.md",
		Plugins: []string{"plugins/gopls-lsp", "plugins/my_plugin-2"},
	}
	require.NoError(t, h.Validate())
}

func TestValidate_PluginNameInvalid(t *testing.T) {
	for _, name := range []string{"my plugin", "foo;bar", "bad@name"} {
		h := &Harness{
			Agent:   "agents/test.md",
			Plugins: []string{"plugins/" + name},
		}
		err := h.Validate()
		require.Error(t, err, "expected error for plugin name %q", name)
		assert.Contains(t, err.Error(), "contains invalid characters")
	}
}

func TestResolveRelativeTo_Plugins(t *testing.T) {
	h := &Harness{
		Agent:   "agents/test.md",
		Plugins: []string{"plugins/gopls-lsp"},
	}
	require.NoError(t, h.ResolveRelativeTo("/base/dir"))
	assert.Equal(t, []string{"/base/dir/plugins/gopls-lsp"}, h.Plugins)
}

func TestResolveRelativeTo_PluginTraversalRejected(t *testing.T) {
	h := &Harness{
		Agent:   "agents/test.md",
		Plugins: []string{"../../etc/evil"},
	}
	err := h.ResolveRelativeTo("/base/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves outside fullsend directory")
}

func TestResolveRelativeTo_URLsUnchanged(t *testing.T) {
	agentURL := "https://example.com/agents/code.md#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	skillURL := "https://example.com/skills/review.md#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	h := &Harness{
		Agent:  agentURL,
		Policy: "policies/readonly.yaml",
		Skills: []string{"skills/local-skill", skillURL},
	}

	require.NoError(t, h.ResolveRelativeTo("/base/dir"))

	assert.Equal(t, agentURL, h.Agent)
	assert.Equal(t, skillURL, h.Skills[1])
	assert.Equal(t, "/base/dir/policies/readonly.yaml", h.Policy)
	assert.Equal(t, "/base/dir/skills/local-skill", h.Skills[0])
}

func TestValidateFilesExist_MissingPlugin(t *testing.T) {
	dir := t.TempDir()
	agentFile := filepath.Join(dir, "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("agent"), 0o644))

	h := &Harness{
		Agent:   agentFile,
		Plugins: []string{"/nonexistent/plugin"},
	}
	err := h.ValidateFilesExist()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[0]")
}

func TestValidateFilesExist_SkipsOptionalPaths(t *testing.T) {
	dir := t.TempDir()
	agentFile := filepath.Join(dir, "agent.md")
	require.NoError(t, os.WriteFile(agentFile, []byte("agent"), 0o644))

	h := &Harness{
		Agent: agentFile,
		HostFiles: []HostFile{
			{Src: "/tmp/does-not-exist-yet.env", Dest: "/tmp/dest", Optional: true},
		},
	}
	// Should not error — optional host files may not exist until runtime.
	require.NoError(t, h.ValidateFilesExist())
}

// --- AllowedRemoteResources tests ---

func TestHarness_AllowedRemoteResources_Parse(t *testing.T) {
	t.Run("with allowed_remote_resources", func(t *testing.T) {
		content := `
agent: agents/test.md
allowed_remote_resources:
  - https://example.com/skills/
  - https://cdn.example.com/policies/
`
		dir := t.TempDir()
		path := filepath.Join(dir, "test.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

		h, err := Load(path)
		require.NoError(t, err)
		assert.Equal(t, []string{"https://example.com/skills/", "https://cdn.example.com/policies/"}, h.AllowedRemoteResources)
	})

	t.Run("without allowed_remote_resources", func(t *testing.T) {
		content := `
agent: agents/test.md
`
		dir := t.TempDir()
		path := filepath.Join(dir, "test.yaml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

		h, err := Load(path)
		require.NoError(t, err)
		assert.Empty(t, h.AllowedRemoteResources)
	})
}

func TestValidateAllowedRemoteResources(t *testing.T) {
	t.Run("valid entries with matching org allowlist", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/skills/",
				"https://cdn.example.com/policies/",
			},
		}
		orgAllowlist := []string{
			"https://example.com/",
			"https://cdn.example.com/policies/",
		}
		require.NoError(t, h.ValidateAllowedRemoteResources(orgAllowlist))
	})

	t.Run("non-HTTPS entry", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"http://example.com/skills/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"http://example.com/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a valid HTTPS URL")
	})

	t.Run("missing trailing slash", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/skills",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must end with /")
	})

	t.Run("entry not covered by org allowlist", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://evil.com/skills/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not covered by the org allowlist")
	})

	t.Run("org entry without trailing slash", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/skills/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "org allowlist")
		assert.Contains(t, err.Error(), "must end with /")
	})

	t.Run("org entry non-HTTPS", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/skills/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"http://example.com/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "org allowlist")
		assert.Contains(t, err.Error(), "not a valid HTTPS URL")
	})

	t.Run("org entry with double encoding", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/skills/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com/%252f/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "double-encoded")
	})

	t.Run("harness entry with double encoding", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/%252fskills/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "double-encoded")
	})

	t.Run("org entry without trailing slash enables domain confusion", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com.evil.com/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not covered by the org allowlist")
	})

	t.Run("percent-encoded traversal not covered", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			AllowedRemoteResources: []string{
				"https://example.com/skills/%2e%2e/evil/",
			},
		}
		err := h.ValidateAllowedRemoteResources([]string{"https://example.com/skills/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not covered by the org allowlist")
	})
}

func TestValidateResourceTypes(t *testing.T) {
	t.Run("URL in pre_script", func(t *testing.T) {
		h := &Harness{
			Agent:     "agents/test.md",
			PreScript: "https://example.com/scripts/pre.sh",
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a local path")
	})

	t.Run("URL in post_script", func(t *testing.T) {
		h := &Harness{
			Agent:      "agents/test.md",
			PostScript: "https://example.com/scripts/post.sh",
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a local path")
	})

	t.Run("URL in agent without hash", func(t *testing.T) {
		h := &Harness{
			Agent: "https://example.com/agents/test.md",
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "integrity hash")
	})

	t.Run("URL in agent with valid hash", func(t *testing.T) {
		h := &Harness{
			Agent: "https://example.com/agents/test.md#sha256=abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		}
		require.NoError(t, h.ValidateResourceTypes())
	})

	t.Run("all-local harness", func(t *testing.T) {
		h := &Harness{
			Agent:      "agents/test.md",
			Policy:     "policies/readonly.yaml",
			Skills:     []string{"skills/summarize"},
			PreScript:  "scripts/pre.sh",
			PostScript: "scripts/post.sh",
			HostFiles:  []HostFile{{Src: "/etc/ssl/certs/ca.crt", Dest: "/tmp/ca.crt"}},
			APIServers: []APIServer{{Name: "api", Script: "scripts/api.sh", Port: 8080}},
			ValidationLoop: &ValidationLoop{
				Script:        "scripts/validate.sh",
				MaxIterations: 1,
			},
		}
		require.NoError(t, h.ValidateResourceTypes())
	})

	t.Run("URL in skills without hash", func(t *testing.T) {
		h := &Harness{
			Agent:  "agents/test.md",
			Skills: []string{"https://example.com/skills/summarize.md"},
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "integrity hash")
	})

	t.Run("URL in validation_loop.script", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			ValidationLoop: &ValidationLoop{
				Script:        "https://example.com/scripts/validate.sh",
				MaxIterations: 1,
			},
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a local path")
	})

	t.Run("URL in host_files src", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			HostFiles: []HostFile{
				{Src: "https://example.com/file.txt", Dest: "/tmp/file.txt"},
			},
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a local path")
	})

	t.Run("URL in api_servers script", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			APIServers: []APIServer{
				{Name: "api", Script: "https://example.com/api.sh", Port: 8080},
			},
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a local path")
	})

	t.Run("URL in agent_input", func(t *testing.T) {
		h := &Harness{
			Agent:      "agents/test.md",
			AgentInput: "https://example.com/input.txt",
		}
		err := h.ValidateResourceTypes()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent_input must be a local path")
	})
}

func TestHasURLReferences(t *testing.T) {
	tests := []struct {
		name string
		h    Harness
		want bool
	}{
		{
			name: "local only",
			h:    Harness{Agent: "agents/test.md", Policy: "policies/p.yaml", Skills: []string{"skills/a"}},
			want: false,
		},
		{
			name: "empty fields",
			h:    Harness{Agent: "agents/test.md"},
			want: false,
		},
		{
			name: "URL agent",
			h:    Harness{Agent: "https://example.com/agents/test.md#sha256=abc"},
			want: true,
		},
		{
			name: "URL policy",
			h:    Harness{Agent: "agents/test.md", Policy: "https://example.com/p.yaml#sha256=abc"},
			want: true,
		},
		{
			name: "URL skill",
			h:    Harness{Agent: "agents/test.md", Skills: []string{"skills/a", "https://example.com/s.md#sha256=abc"}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.h.HasURLReferences())
		})
	}
}

func TestMatchesAllowedPrefix(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		AllowedRemoteResources: []string{
			"https://example.com/skills/",
			"https://cdn.example.com/policies/",
		},
	}

	t.Run("matching URL", func(t *testing.T) {
		assert.True(t, h.MatchesAllowedPrefix("https://example.com/skills/summarize.md"))
	})

	t.Run("non-matching URL", func(t *testing.T) {
		assert.False(t, h.MatchesAllowedPrefix("https://evil.com/skills/summarize.md"))
	})

	t.Run("double-encoded URL", func(t *testing.T) {
		assert.False(t, h.MatchesAllowedPrefix("https://example.com/skills/%2561gent.md"))
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		assert.True(t, h.MatchesAllowedPrefix("https://EXAMPLE.COM/skills/summarize.md"))
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		assert.False(t, h.MatchesAllowedPrefix("https://example.com/skills/../evil/payload"))
	})

	t.Run("dot segment in matching path", func(t *testing.T) {
		assert.True(t, h.MatchesAllowedPrefix("https://example.com/skills/./summarize.md"))
	})

	t.Run("percent-encoded traversal rejected", func(t *testing.T) {
		assert.False(t, h.MatchesAllowedPrefix("https://example.com/skills/%2e%2e/evil/payload"))
	})

	t.Run("percent-encoded dot segment in matching path", func(t *testing.T) {
		assert.True(t, h.MatchesAllowedPrefix("https://example.com/skills/%2e/summarize.md"))
	})

	t.Run("backslash traversal rejected", func(t *testing.T) {
		assert.False(t, h.MatchesAllowedPrefix(`https://example.com/skills\..\secret`))
	})

	t.Run("trailing slash from query not path", func(t *testing.T) {
		assert.False(t, h.MatchesAllowedPrefix("https://evil.com/path?ref=v1/"))
	})
}

func TestMatchingAllowedPrefix(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		AllowedRemoteResources: []string{
			"https://example.com/skills/",
			"https://cdn.example.com/policies/",
		},
	}

	t.Run("returns matching prefix", func(t *testing.T) {
		assert.Equal(t, "https://example.com/skills/", h.MatchingAllowedPrefix("https://example.com/skills/summarize.md"))
	})

	t.Run("returns second prefix when matched", func(t *testing.T) {
		assert.Equal(t, "https://cdn.example.com/policies/", h.MatchingAllowedPrefix("https://cdn.example.com/policies/readonly.yaml"))
	})

	t.Run("returns empty for non-matching URL", func(t *testing.T) {
		assert.Equal(t, "", h.MatchingAllowedPrefix("https://evil.com/skills/summarize.md"))
	})

	t.Run("returns empty for path traversal", func(t *testing.T) {
		assert.Equal(t, "", h.MatchingAllowedPrefix("https://example.com/skills/../evil/payload"))
	})

	t.Run("preserves original prefix casing", func(t *testing.T) {
		h2 := &Harness{
			AllowedRemoteResources: []string{"https://Example.Com/Skills/"},
		}
		assert.Equal(t, "https://Example.Com/Skills/", h2.MatchingAllowedPrefix("https://example.com/skills/test.md"))
	})
}

// --- Role and slug field tests ---

func TestLoad_RoleAndSlug(t *testing.T) {
	content := `
agent: agents/triage.md
role: triage
slug: fullsend-ai-triage
`
	dir := t.TempDir()
	path := filepath.Join(dir, "triage.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "triage", h.Role)
	assert.Equal(t, "fullsend-ai-triage", h.Slug)
}

func TestLoad_RoleAndSlugAbsent(t *testing.T) {
	content := `
agent: agents/test.md
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)
	assert.Empty(t, h.Role)
	assert.Empty(t, h.Slug)
}

func TestValidate_RoleValid(t *testing.T) {
	for _, role := range []string{"triage", "code-reviewer", "bug_triage", "a"} {
		h := &Harness{Agent: "agents/test.md", Role: role}
		require.NoError(t, h.Validate(), "role %q should be valid", role)
	}
}

func TestValidate_RoleInvalid(t *testing.T) {
	for _, role := range []string{"Triage", "1role", "role!"} {
		h := &Harness{Agent: "agents/test.md", Role: role}
		err := h.Validate()
		require.Error(t, err, "role %q should be invalid", role)
		assert.Contains(t, err.Error(), "contains invalid characters")
	}
}

func TestValidate_RoleDoubleHyphen(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", Role: "my--role"}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "double hyphens")
}

func TestValidate_SlugValid(t *testing.T) {
	for _, slug := range []string{"fullsend-ai-triage", "Custom_App", "a1"} {
		h := &Harness{Agent: "agents/test.md", Slug: slug}
		require.NoError(t, h.Validate(), "slug %q should be valid", slug)
	}
}

func TestValidate_SlugInvalid(t *testing.T) {
	for _, slug := range []string{"-slug", "slug!name", "my slug"} {
		h := &Harness{Agent: "agents/test.md", Slug: slug}
		err := h.Validate()
		require.Error(t, err, "slug %q should be invalid", slug)
		assert.Contains(t, err.Error(), "slug")
	}
}

// --- LoadRaw tests ---

func TestLoadRaw_ReturnsUnvalidatedHarness(t *testing.T) {
	// LoadRaw should not call Validate(), so a harness missing the required
	// 'agent' field should load without error.
	content := `
skills:
  - skills/a
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := LoadRaw(path)
	require.NoError(t, err)
	assert.Empty(t, h.Agent)
	assert.Equal(t, []string{"skills/a"}, h.Skills)
}

func TestLoadRaw_PreservesForgeMap(t *testing.T) {
	content := `
agent: agents/test.md
forge:
  github:
    pre_script: scripts/pre-gh.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := LoadRaw(path)
	require.NoError(t, err)
	require.NotNil(t, h.Forge)
	require.Contains(t, h.Forge, "github")
	assert.Equal(t, "scripts/pre-gh.sh", h.Forge["github"].PreScript)
}

func TestLoadRaw_FileNotFound(t *testing.T) {
	_, err := LoadRaw("/nonexistent/harness.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading harness file")
}

// --- LoadWithOpts tests ---

func TestLoadWithOpts_AppliesForgeResolution(t *testing.T) {
	content := `
agent: agents/test.md
pre_script: scripts/pre-common.sh
skills:
  - skills/common
forge:
  github:
    pre_script: scripts/pre-gh.sh
    skills:
      - skills/gh-specific
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := LoadWithOpts(path, LoadOpts{ForgePlatform: "github"})
	require.NoError(t, err)
	assert.Equal(t, "scripts/pre-gh.sh", h.PreScript)
	assert.Equal(t, []string{"skills/common", "skills/gh-specific"}, h.Skills)
	assert.Nil(t, h.Forge, "forge map should be consumed after ResolveForge")
}

func TestLoadWithOpts_NoForge_SameAsLoad(t *testing.T) {
	content := `
agent: agents/test.md
pre_script: scripts/pre.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := LoadWithOpts(path, LoadOpts{})
	require.NoError(t, err)
	assert.Equal(t, "scripts/pre.sh", h.PreScript)
}

func TestLoadWithOpts_EmptyPlatform_PreservesForge(t *testing.T) {
	content := `
agent: agents/test.md
forge:
  github:
    pre_script: scripts/pre-gh.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := LoadWithOpts(path, LoadOpts{ForgePlatform: ""})
	require.NoError(t, err)
	assert.NotNil(t, h.Forge, "forge map should be preserved when platform is empty")
}

func TestLoadWithOpts_InvalidPlatform(t *testing.T) {
	content := `
agent: agents/test.md
forge:
  github:
    pre_script: scripts/pre-gh.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := LoadWithOpts(path, LoadOpts{ForgePlatform: "bitbucket"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid")
}

func TestLoadWithOpts_ValidationAfterForge(t *testing.T) {
	// A harness with a forge override that produces valid state should pass.
	// The validation_loop in the forge block replaces the top-level one.
	content := `
agent: agents/test.md
forge:
  github:
    validation_loop:
      script: scripts/validate-gh.sh
      max_iterations: 2
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := LoadWithOpts(path, LoadOpts{ForgePlatform: "github"})
	require.NoError(t, err)
	require.NotNil(t, h.ValidationLoop)
	assert.Equal(t, "scripts/validate-gh.sh", h.ValidationLoop.Script)
}

func TestLoadWithOpts_PlatformNotConfigured(t *testing.T) {
	content := `
agent: agents/test.md
forge:
  github:
    pre_script: scripts/pre-gh.sh
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := LoadWithOpts(path, LoadOpts{ForgePlatform: "gitlab"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

// --- Runtime fetch field tests ---

func TestValidate_AllowRuntimeFetchWithoutAllowedResources(t *testing.T) {
	h := &Harness{
		Agent:             "agents/code.md",
		AllowRuntimeFetch: true,
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allow_runtime_fetch requires at least one entry in allowed_remote_resources")
}

func TestValidate_MaxRuntimeFetchesWithoutAllowRuntimeFetch(t *testing.T) {
	v := 5
	h := &Harness{
		Agent:             "agents/code.md",
		MaxRuntimeFetches: &v,
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_runtime_fetches requires allow_runtime_fetch to be true")
}

func TestValidate_MaxRuntimeFetchesNegative(t *testing.T) {
	v := -1
	h := &Harness{
		Agent:                  "agents/code.md",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/fullsend-ai/library/"},
		MaxRuntimeFetches:      &v,
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_runtime_fetches must be between 1 and 1000")
}

func TestValidate_MaxRuntimeFetchesExceedsUpperBound(t *testing.T) {
	v := 1001
	h := &Harness{
		Agent:                  "agents/code.md",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/fullsend-ai/library/"},
		MaxRuntimeFetches:      &v,
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_runtime_fetches must be between 1 and 1000")
}

func TestValidate_AllowRuntimeFetchValid(t *testing.T) {
	v := 5
	h := &Harness{
		Agent:                  "agents/code.md",
		AllowRuntimeFetch:      true,
		MaxRuntimeFetches:      &v,
		AllowedRemoteResources: []string{"https://github.com/fullsend-ai/library/"},
	}
	err := h.Validate()
	require.NoError(t, err)
}

func TestValidate_AllowRuntimeFetchDefaultMaxFetches(t *testing.T) {
	h := &Harness{
		Agent:                  "agents/code.md",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/fullsend-ai/library/"},
	}
	err := h.Validate()
	require.NoError(t, err)
	assert.Nil(t, h.MaxRuntimeFetches)
	assert.Equal(t, 10, h.EffectiveMaxRuntimeFetches())
}

func TestValidate_MaxRuntimeFetchesExplicitZero(t *testing.T) {
	v := 0
	h := &Harness{
		Agent:                  "agents/code.md",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/fullsend-ai/library/"},
		MaxRuntimeFetches:      &v,
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_runtime_fetches must be between 1 and 1000")
}

func TestLoad_RuntimeFetchFields(t *testing.T) {
	content := `
agent: agents/code.md
allowed_remote_resources:
  - https://github.com/fullsend-ai/library/
allow_runtime_fetch: true
max_runtime_fetches: 15
`
	dir := t.TempDir()
	path := filepath.Join(dir, "fetch.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)

	assert.True(t, h.AllowRuntimeFetch)
	require.NotNil(t, h.MaxRuntimeFetches)
	assert.Equal(t, 15, *h.MaxRuntimeFetches)
	assert.Equal(t, 15, h.EffectiveMaxRuntimeFetches())
}

func TestLoad_RuntimeFetchFieldsOmitted(t *testing.T) {
	content := `
agent: agents/code.md
`
	dir := t.TempDir()
	path := filepath.Join(dir, "minimal.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	h, err := Load(path)
	require.NoError(t, err)

	assert.False(t, h.AllowRuntimeFetch)
	assert.Nil(t, h.MaxRuntimeFetches)
}

// --- ValidForgePlatform tests ---

func TestValidForgePlatform(t *testing.T) {
	assert.True(t, ValidForgePlatform("github"))
	assert.True(t, ValidForgePlatform("gitlab"))
	assert.False(t, ValidForgePlatform("bitbucket"))
	assert.False(t, ValidForgePlatform(""))
}
