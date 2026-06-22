package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func unmarshalHarness(t *testing.T, input string) Harness {
	t.Helper()
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(input), &h))
	return h
}

func TestYAMLSemantics_ExplicitNull(t *testing.T) {
	t.Run("skills_null_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nskills: null")
		assert.Nil(t, h.Skills)
	})
	t.Run("skills_tilde_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nskills: ~")
		assert.Nil(t, h.Skills)
	})
	t.Run("runner_env_null_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nrunner_env: null")
		assert.Nil(t, h.RunnerEnv)
	})
	t.Run("runner_env_tilde_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nrunner_env: ~")
		assert.Nil(t, h.RunnerEnv)
	})
	t.Run("validation_loop_null_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nvalidation_loop: null")
		assert.Nil(t, h.ValidationLoop)
	})
	t.Run("validation_loop_tilde_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nvalidation_loop: ~")
		assert.Nil(t, h.ValidationLoop)
	})
	t.Run("security_null_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity: null")
		assert.Nil(t, h.Security)
	})
}

func TestYAMLSemantics_Slices(t *testing.T) {
	fields := []struct {
		fieldName string
		absent    string
		empty     string
		populated string
		getSlice  func(h Harness) []string
	}{
		{
			fieldName: "skills",
			absent:    `agent: test.md`,
			empty:     "agent: test.md\nskills: []",
			populated: "agent: test.md\nskills:\n  - a\n  - b",
			getSlice:  func(h Harness) []string { return h.Skills },
		},
		{
			fieldName: "plugins",
			absent:    `agent: test.md`,
			empty:     "agent: test.md\nplugins: []",
			populated: "agent: test.md\nplugins:\n  - a\n  - b",
			getSlice:  func(h Harness) []string { return h.Plugins },
		},
		{
			fieldName: "providers",
			absent:    `agent: test.md`,
			empty:     "agent: test.md\nproviders: []",
			populated: "agent: test.md\nproviders:\n  - a\n  - b",
			getSlice:  func(h Harness) []string { return h.Providers },
		},
		{
			fieldName: "allowed_remote_resources",
			absent:    `agent: test.md`,
			empty:     "agent: test.md\nallowed_remote_resources: []",
			populated: "agent: test.md\nallowed_remote_resources:\n  - a\n  - b",
			getSlice:  func(h Harness) []string { return h.AllowedRemoteResources },
		},
	}

	for _, f := range fields {
		t.Run(f.fieldName+"/absent_is_nil", func(t *testing.T) {
			h := unmarshalHarness(t, f.absent)
			assert.Nil(t, f.getSlice(h))
		})
		t.Run(f.fieldName+"/empty_is_non_nil", func(t *testing.T) {
			h := unmarshalHarness(t, f.empty)
			s := f.getSlice(h)
			assert.NotNil(t, s)
			assert.Empty(t, s)
		})
		t.Run(f.fieldName+"/populated", func(t *testing.T) {
			h := unmarshalHarness(t, f.populated)
			assert.Equal(t, []string{"a", "b"}, f.getSlice(h))
		})
	}
}

func TestYAMLSemantics_StructSlices(t *testing.T) {
	t.Run("host_files/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, `agent: test.md`)
		assert.Nil(t, h.HostFiles)
	})
	t.Run("host_files/empty_is_non_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nhost_files: []")
		assert.NotNil(t, h.HostFiles)
		assert.Empty(t, h.HostFiles)
	})
	t.Run("host_files/populated", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nhost_files:\n  - src: a\n    dest: b")
		require.Len(t, h.HostFiles, 1)
		assert.Equal(t, "a", h.HostFiles[0].Src)
		assert.Equal(t, "b", h.HostFiles[0].Dest)
	})

	t.Run("api_servers/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, `agent: test.md`)
		assert.Nil(t, h.APIServers)
	})
	t.Run("api_servers/empty_is_non_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\napi_servers: []")
		assert.NotNil(t, h.APIServers)
		assert.Empty(t, h.APIServers)
	})
	t.Run("api_servers/populated", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\napi_servers:\n  - name: api\n    script: s.sh\n    port: 8080")
		require.Len(t, h.APIServers, 1)
		assert.Equal(t, "api", h.APIServers[0].Name)
		assert.Equal(t, 8080, h.APIServers[0].Port)
	})
}

func TestYAMLSemantics_Maps(t *testing.T) {
	t.Run("runner_env/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, `agent: test.md`)
		assert.Nil(t, h.RunnerEnv)
	})
	t.Run("runner_env/empty_is_non_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nrunner_env: {}")
		assert.NotNil(t, h.RunnerEnv)
		assert.Empty(t, h.RunnerEnv)
	})
	t.Run("runner_env/populated", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nrunner_env:\n  FOO: bar\n  BAZ: qux")
		assert.Equal(t, map[string]string{"FOO": "bar", "BAZ": "qux"}, h.RunnerEnv)
	})

	t.Run("api_server_env/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\napi_servers:\n  - name: api\n    script: s.sh\n    port: 8080")
		require.Len(t, h.APIServers, 1)
		assert.Nil(t, h.APIServers[0].Env)
	})
	t.Run("api_server_env/empty_is_non_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\napi_servers:\n  - name: api\n    script: s.sh\n    port: 8080\n    env: {}")
		require.Len(t, h.APIServers, 1)
		assert.NotNil(t, h.APIServers[0].Env)
		assert.Empty(t, h.APIServers[0].Env)
	})
}

func TestYAMLSemantics_PointerStructs(t *testing.T) {
	t.Run("validation_loop/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, `agent: test.md`)
		assert.Nil(t, h.ValidationLoop)
	})
	t.Run("validation_loop/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nvalidation_loop: {}")
		require.NotNil(t, h.ValidationLoop)
		assert.Equal(t, "", h.ValidationLoop.Script)
		assert.Equal(t, 0, h.ValidationLoop.MaxIterations)
		assert.Equal(t, "", h.ValidationLoop.FeedbackMode)
	})
	t.Run("validation_loop/populated", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nvalidation_loop:\n  script: v.sh\n  max_iterations: 3\n  feedback_mode: append")
		require.NotNil(t, h.ValidationLoop)
		assert.Equal(t, "v.sh", h.ValidationLoop.Script)
		assert.Equal(t, 3, h.ValidationLoop.MaxIterations)
		assert.Equal(t, "append", h.ValidationLoop.FeedbackMode)
	})

	t.Run("security/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, `agent: test.md`)
		assert.Nil(t, h.Security)
	})
	t.Run("security/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity: {}")
		require.NotNil(t, h.Security)
		assert.Nil(t, h.Security.Enabled)
		assert.Equal(t, "", h.Security.FailMode)
		assert.Nil(t, h.Security.HostScanners)
		assert.Nil(t, h.Security.SandboxHooks)
		assert.Nil(t, h.Security.Escalation)
		assert.Nil(t, h.Security.Trace)
	})
}

func TestYAMLSemantics_NestedPointerStructs(t *testing.T) {
	t.Run("host_scanners/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  fail_mode: closed")
		require.NotNil(t, h.Security)
		assert.Nil(t, h.Security.HostScanners)
	})
	t.Run("host_scanners/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.HostScanners)
		assert.Nil(t, h.Security.HostScanners.UnicodeNormalizer)
		assert.Nil(t, h.Security.HostScanners.ContextInjection)
		assert.Nil(t, h.Security.HostScanners.SSRFValidator)
		assert.Nil(t, h.Security.HostScanners.SecretRedactor)
		assert.Nil(t, h.Security.HostScanners.LLMGuard)
	})

	t.Run("sandbox_hooks/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  fail_mode: closed")
		require.NotNil(t, h.Security)
		assert.Nil(t, h.Security.SandboxHooks)
	})
	t.Run("sandbox_hooks/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.SandboxHooks)
		assert.Nil(t, h.Security.SandboxHooks.Tirith)
		assert.Nil(t, h.Security.SandboxHooks.SSRFPreTool)
		assert.Nil(t, h.Security.SandboxHooks.SecretRedactPostTool)
		assert.Nil(t, h.Security.SandboxHooks.UnicodePostTool)
		assert.Nil(t, h.Security.SandboxHooks.ContextSuppressPostTool)
		assert.Nil(t, h.Security.SandboxHooks.CanaryPreTool)
		assert.Nil(t, h.Security.SandboxHooks.CanaryPostTool)
		assert.Nil(t, h.Security.SandboxHooks.ToolAllowlistPreTool)
	})

	t.Run("escalation/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  fail_mode: closed")
		require.NotNil(t, h.Security)
		assert.Nil(t, h.Security.Escalation)
	})
	t.Run("escalation/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  escalation: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.Escalation)
		assert.Equal(t, "", h.Security.Escalation.OnCritical)
		assert.Equal(t, "", h.Security.Escalation.ReviewLabel)
	})

	t.Run("trace/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  fail_mode: closed")
		require.NotNil(t, h.Security)
		assert.Nil(t, h.Security.Trace)
	})
	t.Run("trace/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  trace: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.Trace)
		assert.Nil(t, h.Security.Trace.Enabled)
	})

	t.Run("llm_guard/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    unicode_normalizer: true")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.HostScanners)
		assert.Nil(t, h.Security.HostScanners.LLMGuard)
	})
	t.Run("llm_guard/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    llm_guard: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.HostScanners)
		require.NotNil(t, h.Security.HostScanners.LLMGuard)
		assert.Nil(t, h.Security.HostScanners.LLMGuard.Enabled)
		assert.Equal(t, float64(0), h.Security.HostScanners.LLMGuard.Threshold)
		assert.Equal(t, "", h.Security.HostScanners.LLMGuard.MatchType)
	})

	t.Run("tirith/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    ssrf_pretool: true")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.SandboxHooks)
		assert.Nil(t, h.Security.SandboxHooks.Tirith)
	})
	t.Run("tirith/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    tirith: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.SandboxHooks)
		require.NotNil(t, h.Security.SandboxHooks.Tirith)
		assert.Nil(t, h.Security.SandboxHooks.Tirith.Enabled)
		assert.Equal(t, "", h.Security.SandboxHooks.Tirith.FailOn)
	})

	t.Run("tool_allowlist/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    ssrf_pretool: true")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.SandboxHooks)
		assert.Nil(t, h.Security.SandboxHooks.ToolAllowlistPreTool)
	})
	t.Run("tool_allowlist/empty_is_non_nil_zero", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    tool_allowlist_pretool: {}")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.SandboxHooks)
		require.NotNil(t, h.Security.SandboxHooks.ToolAllowlistPreTool)
		assert.Nil(t, h.Security.SandboxHooks.ToolAllowlistPreTool.Enabled)
	})
}

func TestYAMLSemantics_PointerBools(t *testing.T) {
	t.Run("security_enabled/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity: {}")
		require.NotNil(t, h.Security)
		assert.Nil(t, h.Security.Enabled)
	})
	t.Run("security_enabled/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  enabled: true")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.Enabled)
		assert.True(t, *h.Security.Enabled)
	})
	t.Run("security_enabled/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  enabled: false")
		require.NotNil(t, h.Security)
		require.NotNil(t, h.Security.Enabled)
		assert.False(t, *h.Security.Enabled)
	})

	t.Run("unicode_normalizer/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners: {}")
		require.NotNil(t, h.Security.HostScanners)
		assert.Nil(t, h.Security.HostScanners.UnicodeNormalizer)
	})
	t.Run("unicode_normalizer/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    unicode_normalizer: true")
		require.NotNil(t, h.Security.HostScanners.UnicodeNormalizer)
		assert.True(t, *h.Security.HostScanners.UnicodeNormalizer)
	})
	t.Run("unicode_normalizer/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    unicode_normalizer: false")
		require.NotNil(t, h.Security.HostScanners.UnicodeNormalizer)
		assert.False(t, *h.Security.HostScanners.UnicodeNormalizer)
	})

	t.Run("context_injection/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners: {}")
		assert.Nil(t, h.Security.HostScanners.ContextInjection)
	})
	t.Run("context_injection/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    context_injection: true")
		require.NotNil(t, h.Security.HostScanners.ContextInjection)
		assert.True(t, *h.Security.HostScanners.ContextInjection)
	})
	t.Run("context_injection/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    context_injection: false")
		require.NotNil(t, h.Security.HostScanners.ContextInjection)
		assert.False(t, *h.Security.HostScanners.ContextInjection)
	})

	t.Run("ssrf_validator/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners: {}")
		assert.Nil(t, h.Security.HostScanners.SSRFValidator)
	})
	t.Run("ssrf_validator/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    ssrf_validator: true")
		require.NotNil(t, h.Security.HostScanners.SSRFValidator)
		assert.True(t, *h.Security.HostScanners.SSRFValidator)
	})
	t.Run("ssrf_validator/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    ssrf_validator: false")
		require.NotNil(t, h.Security.HostScanners.SSRFValidator)
		assert.False(t, *h.Security.HostScanners.SSRFValidator)
	})

	t.Run("secret_redactor/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners: {}")
		assert.Nil(t, h.Security.HostScanners.SecretRedactor)
	})
	t.Run("secret_redactor/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    secret_redactor: true")
		require.NotNil(t, h.Security.HostScanners.SecretRedactor)
		assert.True(t, *h.Security.HostScanners.SecretRedactor)
	})
	t.Run("secret_redactor/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    secret_redactor: false")
		require.NotNil(t, h.Security.HostScanners.SecretRedactor)
		assert.False(t, *h.Security.HostScanners.SecretRedactor)
	})

	t.Run("ssrf_pretool/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		require.NotNil(t, h.Security.SandboxHooks)
		assert.Nil(t, h.Security.SandboxHooks.SSRFPreTool)
	})
	t.Run("ssrf_pretool/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    ssrf_pretool: false")
		require.NotNil(t, h.Security.SandboxHooks.SSRFPreTool)
		assert.False(t, *h.Security.SandboxHooks.SSRFPreTool)
	})

	t.Run("secret_redact_posttool/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		assert.Nil(t, h.Security.SandboxHooks.SecretRedactPostTool)
	})
	t.Run("secret_redact_posttool/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    secret_redact_posttool: false")
		require.NotNil(t, h.Security.SandboxHooks.SecretRedactPostTool)
		assert.False(t, *h.Security.SandboxHooks.SecretRedactPostTool)
	})

	t.Run("unicode_posttool/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		assert.Nil(t, h.Security.SandboxHooks.UnicodePostTool)
	})
	t.Run("unicode_posttool/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    unicode_posttool: true")
		require.NotNil(t, h.Security.SandboxHooks.UnicodePostTool)
		assert.True(t, *h.Security.SandboxHooks.UnicodePostTool)
	})

	t.Run("context_suppress_posttool/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		assert.Nil(t, h.Security.SandboxHooks.ContextSuppressPostTool)
	})
	t.Run("context_suppress_posttool/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    context_suppress_posttool: false")
		require.NotNil(t, h.Security.SandboxHooks.ContextSuppressPostTool)
		assert.False(t, *h.Security.SandboxHooks.ContextSuppressPostTool)
	})

	t.Run("canary_pretool/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		assert.Nil(t, h.Security.SandboxHooks.CanaryPreTool)
	})
	t.Run("canary_pretool/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    canary_pretool: true")
		require.NotNil(t, h.Security.SandboxHooks.CanaryPreTool)
		assert.True(t, *h.Security.SandboxHooks.CanaryPreTool)
	})

	t.Run("canary_posttool/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks: {}")
		assert.Nil(t, h.Security.SandboxHooks.CanaryPostTool)
	})
	t.Run("canary_posttool/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    canary_posttool: false")
		require.NotNil(t, h.Security.SandboxHooks.CanaryPostTool)
		assert.False(t, *h.Security.SandboxHooks.CanaryPostTool)
	})

	t.Run("tirith_enabled/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    tirith: {}")
		require.NotNil(t, h.Security.SandboxHooks.Tirith)
		assert.Nil(t, h.Security.SandboxHooks.Tirith.Enabled)
	})
	t.Run("tirith_enabled/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    tirith:\n      enabled: false")
		require.NotNil(t, h.Security.SandboxHooks.Tirith)
		require.NotNil(t, h.Security.SandboxHooks.Tirith.Enabled)
		assert.False(t, *h.Security.SandboxHooks.Tirith.Enabled)
	})

	t.Run("trace_enabled/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  trace: {}")
		require.NotNil(t, h.Security.Trace)
		assert.Nil(t, h.Security.Trace.Enabled)
	})
	t.Run("trace_enabled/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  trace:\n    enabled: true")
		require.NotNil(t, h.Security.Trace.Enabled)
		assert.True(t, *h.Security.Trace.Enabled)
	})

	t.Run("llm_guard_enabled/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    llm_guard: {}")
		require.NotNil(t, h.Security.HostScanners.LLMGuard)
		assert.Nil(t, h.Security.HostScanners.LLMGuard.Enabled)
	})
	t.Run("llm_guard_enabled/explicit_false", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  host_scanners:\n    llm_guard:\n      enabled: false")
		require.NotNil(t, h.Security.HostScanners.LLMGuard.Enabled)
		assert.False(t, *h.Security.HostScanners.LLMGuard.Enabled)
	})

	t.Run("tool_allowlist_enabled/absent_is_nil", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    tool_allowlist_pretool: {}")
		require.NotNil(t, h.Security.SandboxHooks.ToolAllowlistPreTool)
		assert.Nil(t, h.Security.SandboxHooks.ToolAllowlistPreTool.Enabled)
	})
	t.Run("tool_allowlist_enabled/explicit_true", func(t *testing.T) {
		h := unmarshalHarness(t, "agent: test.md\nsecurity:\n  sandbox_hooks:\n    tool_allowlist_pretool:\n      enabled: true")
		require.NotNil(t, h.Security.SandboxHooks.ToolAllowlistPreTool.Enabled)
		assert.True(t, *h.Security.SandboxHooks.ToolAllowlistPreTool.Enabled)
	})
}
