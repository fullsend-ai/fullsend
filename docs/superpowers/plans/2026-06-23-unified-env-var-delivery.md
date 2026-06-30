# Unified Env Var Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `env:` field with `runner`/`sandbox` sub-maps to the harness schema, deprecating `runner_env` and manual `.env` files per ADR 0055.

**Architecture:** Add `EnvConfig` struct to the harness package. Wire it into forge resolution, base composition, and validation. Add `Lint()` deprecation diagnostics for `runner_env`. Update the runner to expand and apply `env.runner` and generate sandbox `.env` files from `env.sandbox`. Emit deprecation warnings at runtime when `runner_env` is present.

**Tech Stack:** Go, YAML (`gopkg.in/yaml.v3`), existing harness/envfile packages

---

### Task 1: Add `EnvConfig` struct and wire into `Harness` / `ForgeConfig`

**Files:**
- Modify: `internal/harness/harness.go:195-224` (Harness struct)
- Modify: `internal/harness/forge.go:9-20` (ForgeConfig struct)
- Test: `internal/harness/harness_test.go`

- [ ] **Step 1: Write failing test for EnvConfig parsing**

In `internal/harness/harness_test.go`, add:

```go
func TestEnvConfig_ParsesFromYAML(t *testing.T) {
	yaml := `
agent: agents/test.md
role: test
env:
  runner:
    FOO: bar
  sandbox:
    BAZ: qux
`
	h, err := parseRaw([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, h.Env)
	assert.Equal(t, map[string]string{"FOO": "bar"}, h.Env.Runner)
	assert.Equal(t, map[string]string{"BAZ": "qux"}, h.Env.Sandbox)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestEnvConfig_ParsesFromYAML -v`
Expected: FAIL — `h.Env` is nil because the struct field doesn't exist yet.

- [ ] **Step 3: Add EnvConfig struct and field to Harness**

In `internal/harness/harness.go`, add the struct before the `Harness` type:

```go
// EnvConfig holds environment variable maps for runner and sandbox targets.
// Replaces runner_env (ADR 0055). Values support ${VAR} expansion from the
// host environment.
type EnvConfig struct {
	Runner  map[string]string `yaml:"runner,omitempty"`
	Sandbox map[string]string `yaml:"sandbox,omitempty"`
}
```

Add the field to the `Harness` struct, after `RunnerEnv`:

```go
Env *EnvConfig `yaml:"env,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestEnvConfig_ParsesFromYAML -v`
Expected: PASS

- [ ] **Step 5: Write failing test for EnvConfig in ForgeConfig**

In `internal/harness/forge_test.go`, add:

```go
func TestForgeConfig_EnvParsesFromYAML(t *testing.T) {
	yaml := `
agent: agents/test.md
role: test
forge:
  github:
    env:
      runner:
        GH_TOKEN: "${GH_TOKEN}"
      sandbox:
        GITHUB_PR_URL: "${GITHUB_PR_URL}"
`
	h, err := parseRaw([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, h.Forge["github"])
	require.NotNil(t, h.Forge["github"].Env)
	assert.Equal(t, map[string]string{"GH_TOKEN": "${GH_TOKEN}"}, h.Forge["github"].Env.Runner)
	assert.Equal(t, map[string]string{"GITHUB_PR_URL": "${GITHUB_PR_URL}"}, h.Forge["github"].Env.Sandbox)
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestForgeConfig_EnvParsesFromYAML -v`
Expected: FAIL — `ForgeConfig` has no `Env` field.

- [ ] **Step 7: Add Env field to ForgeConfig**

In `internal/harness/forge.go`, add to `ForgeConfig`:

```go
Env *EnvConfig `yaml:"env,omitempty"`
```

- [ ] **Step 8: Run test to verify it passes**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestForgeConfig_EnvParsesFromYAML -v`
Expected: PASS

- [ ] **Step 9: Run full harness test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -v`
Expected: All tests pass. No existing tests should break.

- [ ] **Step 10: Commit**

```bash
git add internal/harness/harness.go internal/harness/forge.go internal/harness/harness_test.go internal/harness/forge_test.go
git commit -S -s -m "$(cat <<'EOF'
feat(harness): add EnvConfig struct with runner/sandbox sub-maps

Add the env: field to Harness and ForgeConfig per ADR 0055. This is the
schema-only change — merge, resolution, and runtime behavior follow in
subsequent commits.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Wire `env:` into forge resolution (`mergeForgeConfig`)

**Files:**
- Modify: `internal/harness/forge.go:112-136` (mergeForgeConfig)
- Test: `internal/harness/forge_test.go`

- [ ] **Step 1: Write failing test for env merge in forge resolution**

In `internal/harness/forge_test.go`, add:

```go
func TestResolveForge_MergesEnv(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner:  map[string]string{"SHARED": "base"},
			Sandbox: map[string]string{"SHARED_SB": "base"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				Env: &EnvConfig{
					Runner:  map[string]string{"GH_TOKEN": "tok"},
					Sandbox: map[string]string{"PR_URL": "url"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))

	require.NotNil(t, h.Env)
	assert.Equal(t, "base", h.Env.Runner["SHARED"])
	assert.Equal(t, "tok", h.Env.Runner["GH_TOKEN"])
	assert.Equal(t, "base", h.Env.Sandbox["SHARED_SB"])
	assert.Equal(t, "url", h.Env.Sandbox["PR_URL"])
}

func TestResolveForge_EnvForgeOverridesTopLevel(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "top"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {
				Env: &EnvConfig{
					Runner: map[string]string{"KEY": "forge"},
				},
			},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, "forge", h.Env.Runner["KEY"])
}

func TestResolveForge_EnvInheritedWhenForgeNil(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner:  map[string]string{"INHERITED": "yes"},
			Sandbox: map[string]string{"ALSO": "inherited"},
		},
		Forge: map[string]*ForgeConfig{
			"github": {},
		},
	}

	require.NoError(t, h.ResolveForge("github"))

	require.NotNil(t, h.Env)
	assert.Equal(t, "yes", h.Env.Runner["INHERITED"])
	assert.Equal(t, "inherited", h.Env.Sandbox["ALSO"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestResolveForge_.*Env -v`
Expected: FAIL — `mergeForgeConfig` doesn't handle `Env`.

- [ ] **Step 3: Add env merge logic to `mergeForgeConfig`**

In `internal/harness/forge.go`, add to `mergeForgeConfig` after the `ValidationLoop` block:

```go
	// Env: merge sub-maps independently; forge keys win (ADR 0055)
	if fc.Env != nil {
		if h.Env == nil {
			h.Env = &EnvConfig{}
		}
		if fc.Env.Runner != nil {
			if h.Env.Runner == nil {
				h.Env.Runner = make(map[string]string, len(fc.Env.Runner))
			}
			for k, v := range fc.Env.Runner {
				h.Env.Runner[k] = v
			}
		}
		if fc.Env.Sandbox != nil {
			if h.Env.Sandbox == nil {
				h.Env.Sandbox = make(map[string]string, len(fc.Env.Sandbox))
			}
			for k, v := range fc.Env.Sandbox {
				h.Env.Sandbox[k] = v
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestResolveForge_.*Env -v`
Expected: PASS

- [ ] **Step 5: Run full harness test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -v`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/forge.go internal/harness/forge_test.go
git commit -S -s -m "$(cat <<'EOF'
feat(harness): merge env: in forge resolution

Wire EnvConfig into mergeForgeConfig so forge.<platform>.env sub-maps
merge with top-level env following the same per-variable additive merge
semantics as runner_env (ADR 0045).

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Wire `env:` into base composition (`mergeBaseIntoChild`)

**Files:**
- Modify: `internal/harness/compose.go:372-477` (mergeBaseIntoChild)
- Modify: `internal/harness/compose.go:827-864` (mergeForgeConfigInto)
- Test: `internal/harness/compose_test.go`

- [ ] **Step 1: Write failing test for env merge in base composition**

In `internal/harness/compose_test.go`, add:

```go
func TestMergeBaseIntoChild_Env(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"BASE_R": "r1"},
			Sandbox: map[string]string{"BASE_S": "s1"},
		},
	}
	child := &Harness{
		Env: &EnvConfig{
			Sandbox: map[string]string{"CHILD_S": "s2"},
		},
	}

	mergeBaseIntoChild(base, child)

	require.NotNil(t, child.Env)
	assert.Equal(t, "r1", child.Env.Runner["BASE_R"])
	assert.Equal(t, "s1", child.Env.Sandbox["BASE_S"])
	assert.Equal(t, "s2", child.Env.Sandbox["CHILD_S"])
}

func TestMergeBaseIntoChild_EnvChildWins(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "base"},
		},
	}
	child := &Harness{
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "child"},
		},
	}

	mergeBaseIntoChild(base, child)
	assert.Equal(t, "child", child.Env.Runner["KEY"])
}

func TestMergeBaseIntoChild_EnvInheritedWhenChildNil(t *testing.T) {
	base := &Harness{
		Env: &EnvConfig{
			Runner:  map[string]string{"R": "val"},
			Sandbox: map[string]string{"S": "val"},
		},
	}
	child := &Harness{}

	mergeBaseIntoChild(base, child)

	require.NotNil(t, child.Env)
	assert.Equal(t, "val", child.Env.Runner["R"])
	assert.Equal(t, "val", child.Env.Sandbox["S"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestMergeBaseIntoChild_Env -v`
Expected: FAIL — `mergeBaseIntoChild` doesn't handle `Env`.

- [ ] **Step 3: Add env merge logic to `mergeBaseIntoChild`**

In `internal/harness/compose.go`, in the `mergeBaseIntoChild` function, after the `RunnerEnv` merge block (around line 460), add:

```go
	// Env: merge sub-maps independently, child keys win (ADR 0055)
	if base.Env != nil {
		if child.Env == nil {
			child.Env = base.Env
		} else {
			if base.Env.Runner != nil {
				if child.Env.Runner == nil {
					child.Env.Runner = make(map[string]string, len(base.Env.Runner))
				}
				for k, v := range base.Env.Runner {
					if _, exists := child.Env.Runner[k]; !exists {
						child.Env.Runner[k] = v
					}
				}
			}
			if base.Env.Sandbox != nil {
				if child.Env.Sandbox == nil {
					child.Env.Sandbox = make(map[string]string, len(base.Env.Sandbox))
				}
				for k, v := range base.Env.Sandbox {
					if _, exists := child.Env.Sandbox[k]; !exists {
						child.Env.Sandbox[k] = v
					}
				}
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestMergeBaseIntoChild_Env -v`
Expected: PASS

- [ ] **Step 5: Add env merge to `mergeForgeConfigInto` (for base forge blocks)**

In `internal/harness/compose.go`, in the `mergeForgeConfigInto` function, after the `RunnerEnv` block, add:

```go
	// Env: merge sub-maps, child keys win (ADR 0055)
	if base.Env != nil {
		if child.Env == nil {
			child.Env = base.Env
		} else {
			if base.Env.Runner != nil {
				if child.Env.Runner == nil {
					child.Env.Runner = make(map[string]string, len(base.Env.Runner))
				}
				for k, v := range base.Env.Runner {
					if _, exists := child.Env.Runner[k]; !exists {
						child.Env.Runner[k] = v
					}
				}
			}
			if base.Env.Sandbox != nil {
				if child.Env.Sandbox == nil {
					child.Env.Sandbox = make(map[string]string, len(base.Env.Sandbox))
				}
				for k, v := range base.Env.Sandbox {
					if _, exists := child.Env.Sandbox[k]; !exists {
						child.Env.Sandbox[k] = v
					}
				}
			}
		}
	}
```

- [ ] **Step 6: Run full harness test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -v`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/harness/compose.go internal/harness/compose_test.go
git commit -S -s -m "$(cat <<'EOF'
feat(harness): merge env: in base composition

Wire EnvConfig into mergeBaseIntoChild and mergeForgeConfigInto so
env.runner and env.sandbox sub-maps merge correctly through base: chains
following the same per-variable additive rules as runner_env.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Add `Lint()` deprecation diagnostics for `runner_env`

**Files:**
- Modify: `internal/harness/lint.go:40-42` (Lint method)
- Test: `internal/harness/lint_test.go`

- [ ] **Step 1: Write failing tests for Lint deprecation warnings**

In `internal/harness/lint_test.go`, add:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestLint_ -v`
Expected: FAIL — `Lint()` returns nil.

- [ ] **Step 3: Implement Lint deprecation checks**

Replace the `Lint()` method body in `internal/harness/lint.go`:

```go
func (h *Harness) Lint() []Diagnostic {
	var diags []Diagnostic

	if len(h.RunnerEnv) > 0 {
		msg := "runner_env is deprecated; use env.runner instead (see ADR 0055)"
		if h.Env != nil && len(h.Env.Runner) > 0 {
			msg = "runner_env is deprecated and env.runner takes precedence; migrate to env.runner (see ADR 0055)"
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Field:    "runner_env",
			Message:  msg,
		})
	}

	return diags
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestLint_ -v`
Expected: PASS

- [ ] **Step 5: Run full harness test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -v`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/lint.go internal/harness/lint_test.go
git commit -S -s -m "$(cat <<'EOF'
feat(harness): lint deprecation warnings for runner_env

Lint() now emits a warning whenever runner_env is present, regardless of
whether env: also exists. When both are present, the warning notes that
env.runner takes precedence. Per ADR 0055.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Extend `ValidateRunnerEnvWith` to check `env:` and expand in the runner

**Files:**
- Modify: `internal/harness/harness.go:490-519` (ValidateRunnerEnvWith)
- Modify: `internal/cli/run.go:327-348` (expand env.runner, apply precedence)
- Test: `internal/harness/harness_test.go`

- [ ] **Step 1: Write failing test for ValidateRunnerEnvWith checking env field**

In `internal/harness/harness_test.go`, add:

```go
func TestValidateRunnerEnvWith_ChecksEnvRunner(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner: map[string]string{"KEY": "${MISSING_VAR}"},
		},
	}
	lookup := func(key string) (string, bool) { return "", false }
	err := h.ValidateRunnerEnvWith(lookup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MISSING_VAR")
}

func TestValidateRunnerEnvWith_ChecksEnvSandbox(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Sandbox: map[string]string{"KEY": "${ALSO_MISSING}"},
		},
	}
	lookup := func(key string) (string, bool) { return "", false }
	err := h.ValidateRunnerEnvWith(lookup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ALSO_MISSING")
}

func TestValidateRunnerEnvWith_EnvAllSet(t *testing.T) {
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &EnvConfig{
			Runner:  map[string]string{"KEY": "${SET_VAR}"},
			Sandbox: map[string]string{"KEY2": "literal"},
		},
	}
	lookup := func(key string) (string, bool) {
		if key == "SET_VAR" {
			return "val", true
		}
		return "", false
	}
	err := h.ValidateRunnerEnvWith(lookup)
	require.NoError(t, err)
}

func TestValidateRunnerEnvWith_NilEnvNoError(t *testing.T) {
	h := &Harness{Agent: "agents/test.md", Role: "test"}
	err := h.ValidateRunnerEnvWith(func(string) (string, bool) { return "", false })
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestValidateRunnerEnvWith_ChecksEnv -v`
Expected: FAIL — `ValidateRunnerEnvWith` doesn't check `Env`.

- [ ] **Step 3: Extend `ValidateRunnerEnvWith` to check `Env` field**

In `internal/harness/harness.go`, in `ValidateRunnerEnvWith`, after the `HostFiles` loop and before the `return nil`, add:

```go
	if h.Env != nil {
		for k, v := range h.Env.Runner {
			if err := checkVarRefs(fmt.Sprintf("env.runner[%s]", k), v); err != nil {
				return err
			}
		}
		for k, v := range h.Env.Sandbox {
			if err := checkVarRefs(fmt.Sprintf("env.sandbox[%s]", k), v); err != nil {
				return err
			}
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestValidateRunnerEnvWith -v`
Expected: PASS

- [ ] **Step 5: Wire env.runner expansion and precedence into the runner**

In `internal/cli/run.go`, find the block (around line 342) that expands `RunnerEnv`. After the existing expansion loop for `RunnerEnv`, add:

```go
	// Expand ${VAR} references in env.runner and env.sandbox (ADR 0055).
	if h.Env != nil {
		for k, v := range h.Env.Runner {
			h.Env.Runner[k] = os.Expand(v, expander)
		}
		for k, v := range h.Env.Sandbox {
			h.Env.Sandbox[k] = os.Expand(v, expander)
		}
	}

	// ADR 0055: env.runner takes precedence over runner_env.
	// Emit deprecation warning when runner_env is present.
	if len(h.RunnerEnv) > 0 {
		if h.Env != nil && len(h.Env.Runner) > 0 {
			fmt.Fprintln(os.Stderr, "WARNING: runner_env is deprecated and env.runner takes precedence; migrate to env.runner (see ADR 0055)")
		} else {
			fmt.Fprintln(os.Stderr, "WARNING: runner_env is deprecated; use env.runner instead (see ADR 0055)")
		}
	}

	// Build effective runner env: start with runner_env, overlay env.runner.
	effectiveRunnerEnv := make(map[string]string)
	for k, v := range h.RunnerEnv {
		effectiveRunnerEnv[k] = v
	}
	if h.Env != nil {
		for k, v := range h.Env.Runner {
			effectiveRunnerEnv[k] = v
		}
	}
	h.RunnerEnv = effectiveRunnerEnv
```

This preserves backward compatibility: `RunnerEnv` is still what gets passed to `envToList()` everywhere, but now it's the merged result with `env.runner` winning on collision.

- [ ] **Step 6: Run full test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ ./internal/cli/ -v`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/harness/harness.go internal/harness/harness_test.go internal/cli/run.go
git commit -S -s -m "$(cat <<'EOF'
feat(runner): validate, expand, and apply env.runner with precedence

Extend ValidateRunnerEnvWith to also check env.runner and env.sandbox
var refs. The runner expands ${VAR} references in both sub-maps, then
merges env.runner over runner_env (env.runner wins on collision).
Deprecation warnings are emitted to stderr whenever runner_env is
present. Per ADR 0055.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Generate sandbox `.env` file from `env.sandbox`

**Files:**
- Modify: `internal/cli/run.go:1217-1268` (bootstrapEnv function)
- Test: `internal/cli/run_test.go`

- [ ] **Step 1: Write failing test for sandbox env generation**

In `internal/cli/run_test.go`, add (or find the appropriate test location):

```go
func TestBuildSandboxEnvLines_FromEnvSandbox(t *testing.T) {
	h := &harness.Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Env: &harness.EnvConfig{
			Sandbox: map[string]string{
				"GITHUB_PR_URL": "https://github.com/org/repo/pull/1",
				"GH_TOKEN":      "tok123",
			},
		},
	}

	lines := buildSandboxEnvLines(h)
	assert.Contains(t, lines, "export GITHUB_PR_URL='https://github.com/org/repo/pull/1'")
	assert.Contains(t, lines, "export GH_TOKEN='tok123'")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/cli/ -run TestBuildSandboxEnvLines -v`
Expected: FAIL — function doesn't exist.

- [ ] **Step 3: Implement `buildSandboxEnvLines`**

In `internal/cli/run.go`, add a helper function:

```go
// buildSandboxEnvLines generates export lines for env.sandbox values (ADR 0055).
// Values have already been expanded by the caller. Each value is single-quoted
// with internal single quotes escaped.
func buildSandboxEnvLines(h *harness.Harness) []string {
	if h.Env == nil || len(h.Env.Sandbox) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h.Env.Sandbox))
	for k := range h.Env.Sandbox {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		v := h.Env.Sandbox[k]
		escaped := strings.ReplaceAll(v, "'", "'\\''")
		lines = append(lines, fmt.Sprintf("export %s='%s'", k, escaped))
	}
	return lines
}
```

Add `"sort"` to the import block if not already present.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/cli/ -run TestBuildSandboxEnvLines -v`
Expected: PASS

- [ ] **Step 5: Wire `buildSandboxEnvLines` into `bootstrapEnv`**

In `internal/cli/run.go`, in the `bootstrapEnv` function, find the line that appends the `.env.d` sourcing loop (around line 1264):

```go
	// Source all env files from .env.d/ (populated by host_files with expand: true).
	lines = append(lines, fmt.Sprintf("for f in %s/.env.d/*.env; do [ -f \"$f\" ] && . \"$f\"; done", sandbox.SandboxWorkspace))
```

Add the `env.sandbox` lines **before** the `.env.d` sourcing loop so that manual `.env` files (if still present) override generated values (last-writer-wins per ADR 0055):

```go
	// ADR 0055: export env.sandbox vars. Placed before .env.d sourcing so
	// manual .env files (if still present during migration) win on collision.
	lines = append(lines, buildSandboxEnvLines(h)...)

	// Source all env files from .env.d/ (populated by host_files with expand: true).
	lines = append(lines, fmt.Sprintf("for f in %s/.env.d/*.env; do [ -f \"$f\" ] && . \"$f\"; done", sandbox.SandboxWorkspace))
```

- [ ] **Step 6: Run full test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/cli/ ./internal/harness/ -v`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/run.go internal/cli/run_test.go
git commit -S -s -m "$(cat <<'EOF'
feat(runner): generate sandbox env from env.sandbox

The runner now exports env.sandbox key-value pairs into the sandbox's
.env file at bootstrap. These are placed before the .env.d sourcing
loop so that manual .env files (if still present during migration)
take precedence per ADR 0055's last-writer-wins guarantee.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Integration test — full harness load with env:

**Files:**
- Test: `internal/harness/integration_test.go`

- [ ] **Step 1: Write integration test**

In `internal/harness/integration_test.go`, add a test that exercises the full load pipeline with `env:`, `forge:`, and `base:`:

```go
func TestLoadWithBase_EnvMergesThroughFullPipeline(t *testing.T) {
	dir := t.TempDir()

	baseYAML := `
agent: agents/test.md
role: test
env:
  runner:
    BASE_R: base_r
    SHARED: from_base
  sandbox:
    BASE_S: base_s
forge:
  github:
    env:
      runner:
        GH_R: gh_r
      sandbox:
        GH_S: gh_s
`
	childYAML := `
base: base.yaml
env:
  runner:
    SHARED: from_child
    CHILD_R: child_r
  sandbox:
    CHILD_S: child_s
`

	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(baseYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.yaml"), []byte(childYAML), 0o644))
	// Create the referenced agent file
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents/test.md"), []byte("# test"), 0o644))

	ctx := context.Background()
	h, _, err := LoadWithBase(ctx, filepath.Join(dir, "child.yaml"), ComposeOpts{
		WorkspaceRoot: dir,
		ForgePlatform: "github",
	})
	require.NoError(t, err)

	require.NotNil(t, h.Env)

	// Base composition: child wins on SHARED
	assert.Equal(t, "from_child", h.Env.Runner["SHARED"])
	// Base composition: BASE_R inherited
	assert.Equal(t, "base_r", h.Env.Runner["BASE_R"])
	// Child's own
	assert.Equal(t, "child_r", h.Env.Runner["CHILD_R"])
	// Forge resolution: GH_R merged in
	assert.Equal(t, "gh_r", h.Env.Runner["GH_R"])

	// Sandbox side
	assert.Equal(t, "base_s", h.Env.Sandbox["BASE_S"])
	assert.Equal(t, "child_s", h.Env.Sandbox["CHILD_S"])
	assert.Equal(t, "gh_s", h.Env.Sandbox["GH_S"])
}
```

- [ ] **Step 2: Run test**

Run: `cd /home/rbean/code/fullsend-0 && go test ./internal/harness/ -run TestLoadWithBase_EnvMergesThroughFullPipeline -v`
Expected: PASS (all prior tasks should make this work).

- [ ] **Step 3: Run full test suite**

Run: `cd /home/rbean/code/fullsend-0 && go test ./... 2>&1 | tail -30`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/harness/integration_test.go
git commit -S -s -m "$(cat <<'EOF'
test(harness): integration test for env: through full load pipeline

Exercises base composition + forge resolution together to verify
env.runner and env.sandbox merge correctly end-to-end.

Assisted-by: Claude Opus 4.6 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Stage and lint all changes

- [ ] **Step 1: Run linters**

```bash
cd /home/rbean/code/fullsend-0 && git add -A && make lint
```

Expected: All linters pass. Fix any issues.

- [ ] **Step 2: Run full test suite one more time**

```bash
cd /home/rbean/code/fullsend-0 && make go-test
```

Expected: All tests pass.

- [ ] **Step 3: Run go vet**

```bash
cd /home/rbean/code/fullsend-0 && make go-vet
```

Expected: No issues.
