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

func TestLint_PreflightCheckPathPattern(t *testing.T) {
	t.Run("top-level bare path flagged at error severity", func(t *testing.T) {
		h := &Harness{PreflightCheck: "scripts/common-preflight.sh"}

		diags := h.Lint()
		require.Len(t, diags, 1)
		assert.Equal(t, SeverityError, diags[0].Severity)
		assert.Equal(t, "preflight_check", diags[0].Field)
		assert.Contains(t, diags[0].Message, "scripts/common-preflight.sh")
		assert.Contains(t, diags[0].Message, "literal sh -c command")
		assert.Contains(t, diags[0].Message, "not fetched like pre_script")
		assert.Contains(t, diags[0].Message, "inline the probe")
	})

	t.Run("nested bare path flagged at error severity", func(t *testing.T) {
		h := &Harness{
			ValidationLoop: &ValidationLoop{PreflightCheck: "scripts/common-preflight.sh"},
		}

		diags := h.Lint()
		// The bare path draws the path-pattern error plus the nested-field
		// deprecation warning.
		require.Len(t, diags, 2)
		assert.Equal(t, SeverityError, diags[0].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
		assert.Contains(t, diags[0].Message, "scripts/common-preflight.sh")
		assert.Contains(t, diags[0].Message, "literal sh -c command")
		assert.Contains(t, diags[0].Message, "not fetched like pre_script")
		assert.Contains(t, diags[0].Message, "inline the probe")
		assert.Equal(t, SeverityWarning, diags[1].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[1].Field)
	})

	t.Run("both fields flagged independently", func(t *testing.T) {
		h := &Harness{
			PreflightCheck: "scripts/common-preflight.sh",
			ValidationLoop: &ValidationLoop{PreflightCheck: "scripts/other-preflight.sh"},
		}

		diags := h.Lint()
		// Both path-pattern errors plus the nested-field deprecation warning.
		require.Len(t, diags, 3)
		assert.Equal(t, "preflight_check", diags[0].Field)
		assert.Equal(t, SeverityError, diags[0].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[1].Field)
		assert.Equal(t, SeverityError, diags[1].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[2].Field)
		assert.Equal(t, SeverityWarning, diags[2].Severity)
	})

	t.Run("inline command unflagged", func(t *testing.T) {
		h := &Harness{PreflightCheck: "python3 -c 'import jsonschema'"}
		assert.Empty(t, h.Lint())
	})

	t.Run("nested inline command unflagged", func(t *testing.T) {
		h := &Harness{
			ValidationLoop: &ValidationLoop{PreflightCheck: "python3 -c 'import jsonschema'"},
		}
		// No path-pattern error — the only diagnostic is the advisory
		// deprecation warning for the nested field.
		diags := h.Lint()
		require.Len(t, diags, 1)
		assert.Equal(t, SeverityWarning, diags[0].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
	})

	t.Run("validation loop without preflight check unflagged", func(t *testing.T) {
		h := &Harness{ValidationLoop: &ValidationLoop{Script: "scripts/validate.sh"}}
		assert.Empty(t, h.Lint())
	})
}

// ADR 0129 Decision 3: the nested field is deprecated now that the
// top-level replacement ships in the same version, so Lint must warn the
// moment the replacement is usable. The warning is advisory — nested
// checks still run during migration — and must say both things the ADR
// requires: prefer the top-level preflight_check, and keep the nested
// check in place until every pinned consumer runs a CLI that executes
// the top-level field (consumer rollout is external, agents#1753).
func TestLint_NestedPreflightCheckDeprecated(t *testing.T) {
	t.Run("nested-only harness warns", func(t *testing.T) {
		h := &Harness{
			ValidationLoop: &ValidationLoop{
				Script:         "scripts/validate.sh",
				PreflightCheck: "python3 -c 'import jsonschema'",
			},
		}

		diags := h.Lint()
		require.Len(t, diags, 1)
		assert.Equal(t, SeverityWarning, diags[0].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
		assert.Contains(t, diags[0].Message, "deprecated")
		// Both halves of the ADR 0129 wording, pinned so the diagnostic
		// cannot drift into urging removal before consumer verification.
		assert.Contains(t, diags[0].Message, "prefer the top-level preflight_check")
		assert.Contains(t, diags[0].Message, "keep the nested check in place")
		assert.Contains(t, diags[0].Message, "every pinned consumer")
		assert.Contains(t, diags[0].Message, "fullsend-ai/agents#1753")
		assert.Contains(t, diags[0].Message, "ADR 0129")
	})

	t.Run("warning also fires when both checks are set", func(t *testing.T) {
		// A harness mid-migration still uses the deprecated nested field,
		// so it still warns: the author can drop the nested check only
		// after consumer verification finishes.
		h := &Harness{
			PreflightCheck: "python3 -c 'import jsonschema'",
			ValidationLoop: &ValidationLoop{
				Script:         "scripts/validate.sh",
				PreflightCheck: "which jq",
			},
		}

		var found bool
		for _, d := range h.Lint() {
			if d.Field == "validation_loop.preflight_check" && d.Severity == SeverityWarning {
				found = true
			}
		}
		assert.True(t, found, "nested field stays deprecated while both checks are configured")
	})

	t.Run("validation loop without a nested check does not warn", func(t *testing.T) {
		h := &Harness{ValidationLoop: &ValidationLoop{Script: "scripts/validate.sh"}}
		assert.Empty(t, h.Lint())
	})

	t.Run("top-level check alone does not warn", func(t *testing.T) {
		h := &Harness{PreflightCheck: "python3 -c 'import jsonschema'"}
		assert.Empty(t, h.Lint())
	})
}

func TestLintPreflightCheckCommand(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		wantFlag bool
	}{
		{"bare relative sh path", "scripts/common-preflight.sh", true},
		{"bare filename", "common-preflight.sh", true},
		{"dot-slash path", "./scripts/common-preflight.sh", true},
		{"dot-dot path", "../scripts/common-preflight.sh", true},
		{"absolute path", "/usr/local/lib/common-preflight.sh", true},
		{"python script", "scripts/probe.py", true},
		{"ruby script", "scripts/probe.rb", true},
		{"js script", "scripts/probe.js", true},
		{"inline python probe", "python3 -c 'import jsonschema'", false},
		{"which command", "which jq", false},
		{"test command with var ref", "test -d ${FULLSEND_DIR}", false},
		{"explicit shell invocation", "sh scripts/probe.sh", false},
		{"bash extension missed by heuristic", "scripts/probe.bash", false},
		{"trailing arguments missed by heuristic", "scripts/probe.sh --verbose", false},
		{"empty value", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := lintPreflightCheckCommand("preflight_check", tc.value)
			if !tc.wantFlag {
				assert.Nil(t, d)
				return
			}
			require.NotNil(t, d)
			assert.Equal(t, SeverityError, d.Severity)
			assert.Equal(t, "preflight_check", d.Field)
			assert.Contains(t, d.Message, "literal sh -c command")
			assert.Contains(t, d.Message, "not fetched like pre_script")
			assert.Contains(t, d.Message, "inline the probe")
		})
	}
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
