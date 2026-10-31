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
	// A set nested value also carries the deprecation warning
	// (TestLint_PreflightCheck_NestedFieldDeprecation), so this test
	// scopes to the path-like diagnostic, found by message rather than
	// by total count.
	var pathLike *Diagnostic
	for i := range diags {
		if strings.Contains(diags[i].Message, "looks like a script path") {
			pathLike = &diags[i]
		}
	}
	require.NotNil(t, pathLike, "expected a path-like diagnostic, got %v", diags)
	assert.Equal(t, SeverityError, pathLike.Severity)
	assert.Equal(t, "validation_loop.preflight_check", pathLike.Field)
	assert.Contains(t, pathLike.Message, "sh -c")
	assert.Contains(t, pathLike.Message, "pre_script")
	assert.Contains(t, pathLike.Message, "scripts/common-preflight.sh")
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

	// A literal command never triggers the path-like heuristic, so the
	// set nested field's deprecation warning — and nothing else —
	// is the expected diagnostic (pinned in full by
	// TestLint_PreflightCheck_NestedFieldDeprecation).
	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityWarning, diags[0].Severity)
	assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
	assert.Contains(t, diags[0].Message, "deprecated")
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
			h := &Harness{
				Agent: "agents/test.md",
				Role:  "test",
				ValidationLoop: &ValidationLoop{
					Script:         "scripts/validate.sh",
					PreflightCheck: tc.cmd,
				},
			}
			diags := h.Lint()
			// A set nested value always carries the deprecation
			// warning too (TestLint_PreflightCheck_NestedFieldDeprecation),
			// so this table scopes to the path-like diagnostic — found by
			// message, not by total count.
			var pathLike []Diagnostic
			for _, d := range diags {
				if strings.Contains(d.Message, "looks like a script path") {
					pathLike = append(pathLike, d)
				}
			}
			if tc.want {
				require.Len(t, pathLike, 1)
				assert.Equal(t, "validation_loop.preflight_check", pathLike[0].Field)
				// Error even for paths that could execute on the host
				// (e.g. /usr/local/bin/preflight.rb): a host-specific
				// absolute path is undeclared host state (ADR 0128 Decision 2).
				assert.Equal(t, SeverityError, pathLike[0].Severity)
			} else {
				assert.Empty(t, pathLike)
			}
			// Unset values (empty, whitespace-only) stay lint-clean: by
			// the lint TrimSpace emptiness convention neither the
			// path-like guard nor the deprecation fires.
			if strings.TrimSpace(tc.cmd) == "" {
				assert.Empty(t, diags)
			}
		})
	}
}

// A bare path-like top-level preflight_check value fails at lint time
// (ADR 0128 Decision 2): the author probably expected pre_script-style
// fetch/resolution, but the check runs verbatim as `sh -c` on the host
// and is never fetched, so the value would only work by
// accident of the runner's working directory. The diagnostic mirrors the
// nested field's guard (lintPreflightCheckCommand) — SeverityError,
// Field "preflight_check", the shared message shape — while
// `fullsend lock`/`run` stay non-fatal (print and continue).
func TestLint_PreflightCheck_TopLevelLooksLikeScriptPath(t *testing.T) {
	h := &Harness{
		Agent:          "agents/test.md",
		Role:           "test",
		PreflightCheck: "scripts/common-preflight.sh",
	}

	diags := h.Lint()
	require.Len(t, diags, 1)
	assert.Equal(t, SeverityError, diags[0].Severity)
	assert.Equal(t, "preflight_check", diags[0].Field)
	// The guard's message shape: the value looks like
	// a script path; the check runs verbatim as `sh -c` and is never
	// fetched — use pre_script for a fetched script or inline the command.
	assert.Contains(t, diags[0].Message, "looks like a script path")
	assert.Contains(t, diags[0].Message, "sh -c")
	assert.Contains(t, diags[0].Message, "never fetched")
	assert.Contains(t, diags[0].Message, "pre_script")
	assert.Contains(t, diags[0].Message, "inline the command")
	assert.Contains(t, diags[0].Message, "scripts/common-preflight.sh")
}

// A literal inline command is the intended authoring pattern (ADR 0128,
// agents#1418) and must stay lint-clean: the path-like heuristic may not
// fire on a command with arguments or quoting.
func TestLint_PreflightCheck_TopLevelLiteralCommand(t *testing.T) {
	h := &Harness{
		Agent:          "agents/test.md",
		Role:           "test",
		PreflightCheck: `python3 -c "import jsonschema"`,
	}

	assert.Empty(t, h.Lint())
}

// A whitespace-only value is unset by the lint emptiness convention —
// strings.TrimSpace, matching the nested guard — and must stay lint-clean
// (docs/contributing/harness-fields.md: absent, empty, or whitespace-only
// is treated as unset).
func TestLint_PreflightCheck_TopLevelWhitespaceOnly(t *testing.T) {
	h := &Harness{
		Agent:          "agents/test.md",
		Role:           "test",
		PreflightCheck: "   ",
	}

	assert.Empty(t, h.Lint())
}

// A harness that sets the nested validation_loop.preflight_check gets a
// non-fatal migration signal naming its replacement (ADR 0129
// Decision 3): the top-level preflight_check is usable once this feature
// ships, so every nested use is deprecated (removal
// stays behind a separately decided version gate, ADR 0127). The
// diagnostic mirrors this file's deprecation house style (runner_env,
// forge) — SeverityWarning, Field "validation_loop.preflight_check", and
// exactly this message:
//
//	validation_loop.preflight_check is deprecated; use the top-level
//	preflight_check instead and keep the nested check in place until
//	every pinned consumer runs a CLI that executes the top-level field
//	(see ADR 0129)
//
// The negative subtests pin the concern's scope: a migrated harness
// (top-level only — a literal command, so the path-like guard stays
// quiet), a harness with neither field, and a whitespace-only nested
// value, which is unset by the lint TrimSpace emptiness convention the
// path-like guards use.
func TestLint_PreflightCheck_NestedFieldDeprecation(t *testing.T) {
	// noDeprecation asserts the concern-scoped negative: no diagnostic on
	// the nested field for this harness.
	noDeprecation := func(t *testing.T, h *Harness) {
		t.Helper()
		for _, d := range h.Lint() {
			assert.NotEqual(t, "validation_loop.preflight_check", d.Field,
				"should not have nested-field deprecation warning")
		}
	}

	t.Run("set nested field emits deprecation warning", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Role:  "test",
			ValidationLoop: &ValidationLoop{
				Script:         "scripts/validate.sh",
				PreflightCheck: `python3 -c "import jsonschema"`,
			},
		}

		diags := h.Lint()
		require.Len(t, diags, 1)
		assert.Equal(t, SeverityWarning, diags[0].Severity)
		assert.Equal(t, "validation_loop.preflight_check", diags[0].Field)
		// The message names the replacement:
		// "deprecated" plus the top-level preflight_check field.
		assert.Contains(t, diags[0].Message, "deprecated")
		assert.Contains(t, diags[0].Message, "top-level preflight_check")
		// The retention clause keeps the migration window explicit
		// (ADR 0129 Decision 5).
		assert.Contains(t, diags[0].Message, "keep the nested check in place")
	})

	t.Run("top-level only emits no deprecation", func(t *testing.T) {
		h := &Harness{
			Agent:          "agents/test.md",
			Role:           "test",
			PreflightCheck: `python3 -c "import jsonschema"`,
		}
		noDeprecation(t, h)
	})

	t.Run("neither field emits nothing for this concern", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Role:  "test",
		}
		noDeprecation(t, h)
	})

	t.Run("whitespace-only nested value is unset", func(t *testing.T) {
		h := &Harness{
			Agent: "agents/test.md",
			Role:  "test",
			ValidationLoop: &ValidationLoop{
				Script:         "scripts/validate.sh",
				PreflightCheck: "   ",
			},
		}
		noDeprecation(t, h)
	})
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
