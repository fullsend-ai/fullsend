package harness

import (
	"strings"
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

func TestLint_AllowedRemoteResourcesWithoutAllowRuntimeFetch(t *testing.T) {
	h := &Harness{
		Agent:                  "agents/test.md",
		Role:                   "test",
		AllowedRemoteResources: []string{"https://github.com/org/"},
	}

	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "allowed_remote_resources", diags[0].Field)
	assert.Contains(t, diags[0].Message, "allow_runtime_fetch: true")
	assert.Contains(t, diags[0].Message, "deprecated")
}

func TestLint_AllowRuntimeFetchExplicit(t *testing.T) {
	h := &Harness{
		Agent:                  "agents/test.md",
		Role:                   "test",
		AllowedRemoteResources: []string{"https://github.com/org/"},
		AllowRuntimeFetch:      true,
	}

	assert.Empty(t, h.Lint())
}

func TestLint_URLSkillsSuppressFetchDeprecation(t *testing.T) {
	// URL-dir resources start the fetch service for a non-deprecated
	// reason, mirroring shouldStartFetchService.
	h := &Harness{
		Agent:                  "agents/test.md",
		Role:                   "test",
		AllowedRemoteResources: []string{"https://github.com/org/"},
		Skills:                 []SkillEntry{{Source: "https://github.com/org/skills/tree/abc/rust#sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
	}

	assert.Empty(t, h.Lint())
}

func TestLint_PreflightCheckLooksLikeScriptPath(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		ValidationLoop: &ValidationLoop{
			Script:         "scripts/validate.sh",
			PreflightCheck: "scripts/common-preflight.sh",
		},
	}

	diags := h.Lint()
	// The bare path draws the path-pattern error plus the nested-field
	// deprecation warning (ADR 0129 Decision 3).
	require.Len(t, diags, 2)
	assert.Equal(t, SeverityError, diags[0].Severity)
	assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
	assert.Contains(t, diags[0].Message, "sh -c")
	assert.Contains(t, diags[0].Message, "pre_script")
	assert.Contains(t, diags[0].Message, "scripts/common-preflight.sh")
	assert.Equal(t, SeverityWarning, diags[1].Severity)
	assert.Equal(t, "validation_loop.preflight_check", diags[1].Field)
}

func TestLint_PreflightCheckLiteralCommand(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		ValidationLoop: &ValidationLoop{
			Script:         "scripts/validate.sh",
			PreflightCheck: "python3 -c 'import jsonschema'",
		},
	}

	// A literal command draws no path-pattern error; the only diagnostic
	// is the advisory deprecation warning for the nested field.
	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
}

func TestLint_PreflightCheckPathLikeMatching(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{name: "relative script", cmd: "scripts/common-preflight.sh", want: true},
		{name: "dot-relative script", cmd: "./scripts/check.py", want: true},
		{name: "absolute script", cmd: "/usr/local/bin/preflight.rb", want: true},
		{name: "bare basename", cmd: "tool.js", want: true},
		{name: "command with args", cmd: "bash scripts/foo.sh", want: false},
		{name: "interpreter invocation", cmd: "sh scripts/a.sh", want: false},
		{name: "python inline", cmd: `python3 -c "import jsonschema"`, want: false},
		{name: "shell pipeline", cmd: "command -v jq >/dev/null", want: false},
		{name: "quoted path", cmd: "'scripts/foo.sh'", want: false},
		{name: "empty", cmd: "", want: false},
		{name: "whitespace only", cmd: "   ", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Top-level field: isolates the path heuristic from the
			// nested-field deprecation warning, which fires for any
			// non-empty nested value.
			h := &Harness{
				Agent:          "agents/test.md",
				Role:           "test",
				PreflightCheck: tc.cmd,
			}
			diags := h.Lint()
			if tc.want {
				require.Len(t, diags, 1)
				assert.Equal(t, "preflight_check", diags[0].Field)
				// Error even for paths that could execute on the host
				// (e.g. /usr/local/bin/preflight.rb): a host-specific
				// absolute path is undeclared host state (ADR 0128 Decision 2).
				assert.Equal(t, SeverityError, diags[0].Severity)
			} else {
				assert.Empty(t, diags)
			}
		})
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
		{"var-ref that expands to a bare path", "${FULLSEND_DIR}/x.sh", false},
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

func TestLint_GithubIssueURLDeprecated(t *testing.T) {
	t.Run("runner_env", func(t *testing.T) {
		h := &Harness{
			RunnerEnv: map[string]string{
				"GITHUB_ISSUE_URL": "https://example.com/issue/1",
			},
		}
		diags := h.Lint()
		var found bool
		for _, d := range diags {
			if d.Field == "runner_env" && strings.Contains(d.Message, "GITHUB_ISSUE_URL is deprecated") {
				found = true
				assert.Equal(t, SeverityWarning, d.Severity)
				assert.Contains(t, d.Message, "FULLSEND_WORK_ITEM_URL")
			}
		}
		assert.True(t, found, "expected GITHUB_ISSUE_URL deprecation diagnostic in runner_env")
	})

	t.Run("env.runner and env.sandbox", func(t *testing.T) {
		h := &Harness{
			Env: &EnvConfig{
				Runner: map[string]string{
					"GITHUB_ISSUE_URL": "${GITHUB_ISSUE_URL}",
				},
				Sandbox: map[string]string{
					"GITHUB_ISSUE_URL": "${GITHUB_ISSUE_URL}",
				},
			},
		}
		diags := h.Lint()
		var foundRunner, foundSandbox bool
		for _, d := range diags {
			if d.Field == "env.runner" && strings.Contains(d.Message, "GITHUB_ISSUE_URL is deprecated") {
				foundRunner = true
				assert.Equal(t, SeverityWarning, d.Severity)
			}
			if d.Field == "env.sandbox" && strings.Contains(d.Message, "GITHUB_ISSUE_URL is deprecated") {
				foundSandbox = true
				assert.Equal(t, SeverityWarning, d.Severity)
			}
		}
		assert.True(t, foundRunner, "expected GITHUB_ISSUE_URL deprecation diagnostic in env.runner")
		assert.True(t, foundSandbox, "expected GITHUB_ISSUE_URL deprecation diagnostic in env.sandbox")
	})

	t.Run("value expansion forms", func(t *testing.T) {
		h := &Harness{
			Env: &EnvConfig{
				Runner: map[string]string{
					"ISSUE":        "$GITHUB_ISSUE_URL",
					"ISSUE_BRACED": "${GITHUB_ISSUE_URL}",
				},
			},
		}
		diags := h.Lint()
		var foundBare, foundBraced bool
		for _, d := range diags {
			if d.Field != "env.runner" || !strings.Contains(d.Message, "GITHUB_ISSUE_URL is deprecated") {
				continue
			}
			if strings.Contains(d.Message, `(entry "ISSUE")`) {
				foundBare = true
			}
			if strings.Contains(d.Message, `(entry "ISSUE_BRACED")`) {
				foundBraced = true
			}
		}
		assert.True(t, foundBare, "expected diagnostic for $GITHUB_ISSUE_URL value")
		assert.True(t, foundBraced, "expected diagnostic for ${GITHUB_ISSUE_URL} value")
	})

	t.Run("literal substring is not a reference", func(t *testing.T) {
		h := &Harness{
			Env: &EnvConfig{
				Runner: map[string]string{
					"DOCS":  "see GITHUB_ISSUE_URL in the migration guide",
					"OTHER": "$GITHUB_ISSUE_URLS", // longer name: a different variable
				},
			},
			HostFiles: []HostFile{{Src: "docs/GITHUB_ISSUE_URL.txt", Dest: "/sandbox/x.txt"}},
		}
		assert.Empty(t, h.Lint())
	})

	t.Run("host_files src", func(t *testing.T) {
		h := &Harness{
			HostFiles: []HostFile{{Src: "${GITHUB_ISSUE_URL}/report.txt", Dest: "/sandbox/report.txt"}},
		}
		diags := h.Lint()
		require.Len(t, diags, 1)
		assert.Equal(t, "host_files[0].src", diags[0].Field)
		assert.Contains(t, diags[0].Message, "GITHUB_ISSUE_URL is deprecated")
	})

	t.Run("forge", func(t *testing.T) {
		h := &Harness{
			Forge: map[string]*ForgeConfig{
				"github": {
					Env: &EnvConfig{
						Runner: map[string]string{"GITHUB_ISSUE_URL": "x"},
					},
				},
			},
		}
		diags := h.Lint()
		var foundForge bool
		for _, d := range diags {
			if d.Field == "forge[github].env.runner" && strings.Contains(d.Message, "GITHUB_ISSUE_URL is deprecated") {
				foundForge = true
			}
		}
		assert.True(t, foundForge, "expected GITHUB_ISSUE_URL deprecation diagnostic in forge")
	})
}
