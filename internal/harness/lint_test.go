package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLint(t *testing.T) {
	t.Run("valid harness returns nil", func(t *testing.T) {
		h := &Harness{Role: "triage"}
		assert.Nil(t, h.Lint())
	})

	t.Run("role and slug set", func(t *testing.T) {
		h := &Harness{Role: "triage", Slug: "my-slug"}
		assert.Nil(t, h.Lint())
	})
}

func TestLint_RunnerEnvDeprecated(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "test",
		RunnerEnv: map[string]string{"FOO": "bar"},
	}

	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "runner_env", diags[0].Field)
	assert.Contains(t, diags[0].Message, "deprecated")
	assert.Contains(t, diags[0].Message, "env.runner")
}

func TestLint_RunnerEnvAndEnvBothPresent(t *testing.T) {
	h := &Harness{
		Agent:     "agents/test.md",
		Role:      "test",
		RunnerEnv: map[string]string{"FOO": "bar"},
		Env:       &EnvConfig{Runner: map[string]string{"BAZ": "qux"}},
	}

	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "env.runner takes precedence")
}

func TestLint_NoWarningWithoutRunnerEnv(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &EnvConfig{Runner: map[string]string{"FOO": "bar"}},
	}

	diags := h.Lint()
	assert.Empty(t, diags)
}

func TestLint_EnvSandboxWithHostFilesEnvOverlap(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &EnvConfig{Sandbox: map[string]string{"GH_TOKEN": "${GH_TOKEN}"}},
		HostFiles: []HostFile{
			{Src: "${FULLSEND_DIR}/env/review.env", Dest: "/sandbox/workspace/.env.d/review.env", Expand: true},
		},
	}

	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "env.sandbox", diags[0].Field)
	assert.Contains(t, diags[0].Message, "env.sandbox values take precedence")
}

func TestLint_EnvSandboxWithHostFilesNoOverlap(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &EnvConfig{Sandbox: map[string]string{"GH_TOKEN": "${GH_TOKEN}"}},
		HostFiles: []HostFile{
			{Src: "/path/to/ca.crt", Dest: "/sandbox/workspace/certs/ca.crt"},
		},
	}

	diags := h.Lint()
	assert.Empty(t, diags)
}

func TestLint_ForgeDeprecationWarning(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
		Forge: map[string]*ForgeConfig{
			"github": {PreScript: "scripts/gh.sh"},
		},
		hadForgeBeforeResolve: true, // simulate LoadWithOpts capturing this
	}
	diags := h.Lint()
	var found bool
	for _, d := range diags {
		if d.Field == "forge" {
			found = true
			assert.Equal(t, SeverityWarning, d.Severity)
			assert.Contains(t, d.Message, "deprecated")
			assert.Contains(t, d.Message, "overlays")
		}
	}
	assert.True(t, found, "expected forge deprecation warning")
}

func TestLint_NoForgeNoDeprecationWarning(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "fix",
	}
	diags := h.Lint()
	for _, d := range diags {
		assert.NotEqual(t, "forge", d.Field, "should not have forge warning")
	}
}

func TestLint_ImplicitRuntimeFetchWarning(t *testing.T) {
	h := &Harness{
		Agent:                  "agents/test.md",
		Role:                   "test",
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}
	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "allowed_remote_resources", diags[0].Field)
	assert.Contains(t, diags[0].Message, "deprecated")
	assert.Contains(t, diags[0].Message, "allow_runtime_fetch: true")
}

func TestLint_NoImplicitRuntimeFetchWarningWhenFlagSet(t *testing.T) {
	h := &Harness{
		Agent:                  "agents/test.md",
		Role:                   "test",
		AllowRuntimeFetch:      true,
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}
	diags := h.Lint()
	assert.Empty(t, diags)
}

func TestLint_NoImplicitRuntimeFetchWarningWithURLSkills(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Skills: []SkillEntry{
			{Source: "https://github.com/org/skills/tree/abc/rust#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		},
	}
	diags := h.Lint()
	for _, d := range diags {
		assert.NotEqual(t, "allowed_remote_resources", d.Field, "HasURLDirResources should suppress the implicit-fetch warning")
	}
}

func TestLint_NoImplicitRuntimeFetchWarningWithoutAllowedRemoteResources(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", Role: "test"}
	assert.Empty(t, h.Lint())
}

func TestLint_DeprecatedIssueURLInEnvRunner(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &EnvConfig{Runner: map[string]string{"ISSUE_URL": "${GITHUB_ISSUE_URL}"}},
	}
	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "env.runner.ISSUE_URL", diags[0].Field)
	assert.Contains(t, diags[0].Message, "GITHUB_ISSUE_URL")
	assert.Contains(t, diags[0].Message, "FULLSEND_WORK_ITEM_URL")
}

func TestLint_DeprecatedIssueURLInEnvSandbox(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &EnvConfig{Sandbox: map[string]string{"ISSUE_URL": "${GITHUB_ISSUE_URL}"}},
	}
	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, "env.sandbox.ISSUE_URL", diags[0].Field)
}

func TestLint_DeprecatedIssueURLAsEnvKey(t *testing.T) {
	values := map[string]string{
		"literal":       "https://github.com/o/r/issues/1",
		"work item var": "${FULLSEND_WORK_ITEM_URL}",
	}
	for name, value := range values {
		t.Run("runner/"+name, func(t *testing.T) {
			h := &Harness{
				Agent: "agents/test.md",
				Role:  "test",
				Env:   &EnvConfig{Runner: map[string]string{"GITHUB_ISSUE_URL": value}},
			}
			diags := h.Lint()
			require.Len(t, diags, 1)
			assert.Equal(t, SeverityWarning, diags[0].Severity)
			assert.Equal(t, "env.runner.GITHUB_ISSUE_URL", diags[0].Field)
			assert.Contains(t, diags[0].Message, "FULLSEND_WORK_ITEM_URL")
		})
		t.Run("sandbox/"+name, func(t *testing.T) {
			h := &Harness{
				Agent: "agents/test.md",
				Role:  "test",
				Env:   &EnvConfig{Sandbox: map[string]string{"GITHUB_ISSUE_URL": value}},
			}
			diags := h.Lint()
			require.Len(t, diags, 1)
			assert.Equal(t, "env.sandbox.GITHUB_ISSUE_URL", diags[0].Field)
		})
	}
}

func TestLint_DeprecatedIssueURLMessageMentionsPrioritizeCaveat(t *testing.T) {
	assert.Contains(t, DeprecatedIssueURLWarning, "prioritize")
}

func TestLint_DeprecatedIssueURLInHostFiles(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		HostFiles: []HostFile{
			{Src: "${GITHUB_ISSUE_URL}", Dest: "/sandbox/workspace/.issue-url"},
		},
	}
	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, "host_files[0].src", diags[0].Field)
	assert.Contains(t, diags[0].Message, "GITHUB_ISSUE_URL")
}

func TestLint_NoDeprecatedIssueURLWarningWithoutReference(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env:   &EnvConfig{Runner: map[string]string{"ISSUE_URL": "${FULLSEND_WORK_ITEM_URL}"}},
	}
	assert.Empty(t, h.Lint())
}

func TestDiagnostic_String(t *testing.T) {
	t.Run("warning", func(t *testing.T) {
		d := Diagnostic{Severity: SeverityWarning, Field: "role", Message: "msg"}
		assert.Equal(t, "warning: role: msg", d.String())
	})

	t.Run("error", func(t *testing.T) {
		d := Diagnostic{Severity: SeverityError, Field: "role", Message: "msg"}
		assert.Equal(t, "error: role: msg", d.String())
	})

	t.Run("unknown severity", func(t *testing.T) {
		d := Diagnostic{Severity: DiagnosticSeverity(99), Field: "x", Message: "msg"}
		assert.Equal(t, "DiagnosticSeverity(99): x: msg", d.String())
	})
}

func TestValidateRemoteResourceAuthorization(t *testing.T) {
	hash := "#sha256=" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	agentURL := "https://github.com/org/repo/blob/main/agents/a.md" + hash

	t.Run("local references need no authorization", func(t *testing.T) {
		h := &Harness{Agent: "agents/a.md", Policy: "policies/p.yaml"}
		assert.NoError(t, h.ValidateRemoteResourceAuthorization(nil))
	})

	t.Run("deny-all rejects URL agent", func(t *testing.T) {
		h := &Harness{Agent: agentURL}
		err := h.ValidateRemoteResourceAuthorization([]string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not in allowed_remote_resources")
	})

	t.Run("org allowlist authorizes", func(t *testing.T) {
		h := &Harness{Agent: agentURL}
		assert.NoError(t, h.ValidateRemoteResourceAuthorization([]string{"https://github.com/org/"}))
	})

	t.Run("harness allowlist authorizes", func(t *testing.T) {
		h := &Harness{Agent: agentURL, AllowedRemoteResources: []string{"https://github.com/org/"}}
		assert.NoError(t, h.ValidateRemoteResourceAuthorization(nil))
	})

	t.Run("unauthorized override URL is rejected", func(t *testing.T) {
		other := "https://example.com/x.md" + hash
		h := &Harness{
			Agent:  "agents/a.md",
			Skills: []SkillEntry{{Source: "skills/s", Overrides: map[string]*string{"k": &other}}},
		}
		err := h.ValidateRemoteResourceAuthorization([]string{"https://github.com/org/"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "skills[0].overrides.k")
	})
}
