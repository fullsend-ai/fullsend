package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Excluded-key detection
//
// Given an overlay naming any of base, trigger, overlays, slug, role, forge,
// When validated, Then validation fails naming the overlay index and the
// refused key. Includes keys through YAML merge keys, aliases, and tagged keys.
// ---------------------------------------------------------------------------

func TestValidateOverlays_ExcludedKey_Direct(t *testing.T) {
	excluded := []string{"base", "trigger", "overlays", "slug", "role", "forge"}
	for _, key := range excluded {
		t.Run(key, func(t *testing.T) {
			yamlStr := fmt.Sprintf(`
agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github"'
    %s: rejected-value
`, key)
			var h Harness
			require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
			err := h.validateOverlays()
			require.Error(t, err, "excluded key %q in overlay must be rejected", key)
			assert.Contains(t, err.Error(), "overlays[0]")
			assert.Contains(t, err.Error(), key)
		})
	}
}

func TestValidateOverlays_ExcludedKey_ViaYAMLMergeKey(t *testing.T) {
	yamlStr := `
_inject: &inject
  trigger: 'event.entity.kind == "pr"'

agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github"'
    <<: *inject
    pre_script: scripts/gh.sh
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
	err := h.validateOverlays()
	require.Error(t, err, "excluded key via YAML merge key must be detected")
	assert.Contains(t, err.Error(), "overlays[0]")
	assert.Contains(t, err.Error(), "trigger")
}

func TestValidateOverlays_ExcludedKey_ViaAliasKey(t *testing.T) {
	yamlStr := `
_key: &k role

agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github"'
    *k : other
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
	err := h.validateOverlays()
	require.Error(t, err, "excluded key via YAML alias key must be detected")
	assert.Contains(t, err.Error(), "overlays[0]")
	assert.Contains(t, err.Error(), "role")
}

func TestValidateOverlays_ExcludedKey_ViaTaggedKey(t *testing.T) {
	yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github"'
    !!str base: other-harness
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
	err := h.validateOverlays()
	require.Error(t, err, "excluded key via tagged YAML key must be detected")
	assert.Contains(t, err.Error(), "overlays[0]")
	assert.Contains(t, err.Error(), "base")
}

// ---------------------------------------------------------------------------
// Guarded field with untrusted when
//
// Given an overlay that sets a guarded field with a when: reading a
// non-trusted event path, When validated, Then validation fails naming the
// overlay index, the guarded field, and the offending reference.
// ---------------------------------------------------------------------------

func TestValidateOverlays_GuardedFieldUntrustedWhen(t *testing.T) {
	cases := []struct {
		name      string
		field     string
		fieldYAML string
		untrusted string
	}{
		{"agent", "agent", "agent: agents/secure.md", "event.entity.title"},
		{"image", "image", "image: ghcr.io/secure:latest", "event.transition.kind"},
		{"security", "security", "security:\n      fail_mode: open", "event.state.labels"},
		{"plugins", "plugins", "plugins:\n      - plugins/extra", "event.state.diff.files_changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yamlStr := fmt.Sprintf(`
agent: agents/test.md
role: fix
overlays:
  - when: '%s == "test"'
    %s
`, tc.untrusted, tc.fieldYAML)
			var h Harness
			require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
			err := h.validateOverlays()
			require.Error(t, err, "guarded field %q with untrusted path %q must fail", tc.field, tc.untrusted)
			assert.Contains(t, err.Error(), "overlays[0]")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", tc.field), "the error must name the guarded field")
			assert.Contains(t, err.Error(), fmt.Sprintf("%q", tc.untrusted), "the error must name the offending reference")
		})
	}
}

// ---------------------------------------------------------------------------
// Unguarded fields with untrusted when pass
//
// Given an overlay that sets only model, effort, description, or doc, When
// its when: reads untrusted facts, Then validation passes (unguarded).
// ---------------------------------------------------------------------------

func TestValidateOverlays_UnguardedFieldUntrustedWhenPasses(t *testing.T) {
	unguarded := []struct {
		name      string
		fieldYAML string
	}{
		{"model", "model: gpt-4"},
		{"effort", "effort: high"},
		{"description", "description: override desc"},
		{"doc", "doc: override-doc.md"},
	}
	for _, tc := range unguarded {
		t.Run(tc.name, func(t *testing.T) {
			yamlStr := fmt.Sprintf(`
agent: agents/test.md
role: fix
overlays:
  - when: 'event.entity.title == "fix typo"'
    %s
`, tc.fieldYAML)
			var h Harness
			require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
			err := h.validateOverlays()
			assert.NoError(t, err, "unguarded field %q with untrusted when must pass", tc.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Every non-zero OverlayFields field reaches the merged harness
//
// Given every non-zero OverlayFields field applied to a matching overlay,
// When resolved, Then the field reaches the merged harness via
// mergeOverlayFields.
// ---------------------------------------------------------------------------

func TestOverlayFields_AllFieldsReachMergedHarness(t *testing.T) {
	yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github"'
    model: gpt-4-overlay
    effort: high
    description: overlay-description
    doc: overlay-doc.md
    image: ghcr.io/overlay-image:latest
    agent_input: overlay-input
    timeout_minutes: 120
    sandbox_timeout_seconds: 3600
    readonly_repo: true
    allow_runtime_fetch: true
    max_runtime_fetches: 50
    plugins:
      - plugins/overlay
    api_servers:
      - name: overlay-api
        script: scripts/overlay-api.sh
        port: 8080
    privilege_levels:
      run: admin
    security:
      fail_mode: open
    allowed_remote_resources:
      - "https://example.com/*"
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
	err := h.ResolveOverlays(nil, "github", nil)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4-overlay", h.Model, "model must reach harness")
	assert.Equal(t, "high", h.Effort, "effort must reach harness")
	assert.Equal(t, "overlay-description", h.Description, "description must reach harness")
	assert.Equal(t, "overlay-doc.md", h.Doc, "doc must reach harness")
	assert.Equal(t, "ghcr.io/overlay-image:latest", h.Image, "image must reach harness")
	assert.Equal(t, "overlay-input", h.AgentInput, "agent_input must reach harness")
	assert.Equal(t, 120, h.TimeoutMinutes, "timeout_minutes must reach harness")
	assert.Equal(t, 3600, h.SandboxTimeoutSeconds, "sandbox_timeout_seconds must reach harness")
	assert.True(t, h.ReadonlyRepo, "readonly_repo must reach harness")
	assert.True(t, h.AllowRuntimeFetch, "allow_runtime_fetch must reach harness")
	require.NotNil(t, h.MaxRuntimeFetches, "max_runtime_fetches must reach harness")
	assert.Equal(t, 50, *h.MaxRuntimeFetches)
	require.NotEmpty(t, h.Plugins, "plugins must reach harness")
	assert.Equal(t, "plugins/overlay", h.Plugins[0].Path)
	require.NotEmpty(t, h.APIServers, "api_servers must reach harness")
	assert.Equal(t, "overlay-api", h.APIServers[0].Name)
	require.NotNil(t, h.PrivilegeLevels, "privilege_levels must reach harness")
	assert.Equal(t, "admin", h.PrivilegeLevels["run"])
	require.NotNil(t, h.Security, "security must reach harness")
	require.NotEmpty(t, h.AllowedRemoteResources, "allowed_remote_resources must reach harness")
	assert.Equal(t, "https://example.com/*", h.AllowedRemoteResources[0])
}

// ---------------------------------------------------------------------------
// Field partition coverage
//
// Given a new field added to Harness without placement, When tests run, Then
// TestOverlayFields_EveryHarnessFieldHasOneHome fails.
// ---------------------------------------------------------------------------

func TestOverlayFields_EveryHarnessFieldHasOneHome(t *testing.T) {
	excluded := map[string]bool{
		"base": true, "trigger": true, "overlays": true,
		"slug": true, "role": true, "forge": true,
	}

	harnessKeys := collectYAMLKeys(reflect.TypeOf(Harness{}))
	overlayKeys := collectYAMLKeys(reflect.TypeOf(OverlayEntry{}))
	delete(overlayKeys, "when")

	for key := range harnessKeys {
		if excluded[key] {
			assert.False(t, overlayKeys[key],
				"excluded key %q must not be in OverlayEntry", key)
			continue
		}
		assert.True(t, overlayKeys[key],
			"Harness field %q is not overlay-settable and not excluded; "+
				"place it in OverlayFields, ForgeConfig, or the excluded set", key)
	}

	for key := range overlayKeys {
		assert.True(t, harnessKeys[key],
			"OverlayEntry field %q has no matching Harness field", key)
	}
}

func collectYAMLKeys(t reflect.Type) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "-" || tag == "" {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			for _, p := range parts[1:] {
				if p == "inline" {
					for k, v := range collectYAMLKeys(f.Type) {
						keys[k] = v
					}
					break
				}
			}
			continue
		}
		keys[name] = true
	}
	return keys
}

// ---------------------------------------------------------------------------
// Trusted-path pinning
//
// Given TestOverlayTrustedPathsMatchSpec, When the normative event spec
// trusted-field list changes, Then the test fails.
// ---------------------------------------------------------------------------

func TestOverlayTrustedPathsMatchSpec(t *testing.T) {
	// The trusted list is the normative event spec's, not a copy kept here:
	// the positive rows come from the "Trusted fields" section of
	// docs/normative/normalized-event/v1/README.md, so a path the spec adds
	// fails this test until the code trusts it. (The code's own list is held
	// to the same section by TestTrustedOverlayEventPaths_MatchSpecDoc.)
	trusted := specTrustedOverlayEventPaths(t)
	require.NotEmpty(t, trusted, "no bullets found under '### Trusted fields' in the spec")

	for _, path := range trusted {
		t.Run("trusted/"+path, func(t *testing.T) {
			yamlStr := fmt.Sprintf(`
agent: agents/test.md
role: fix
overlays:
  - when: '%s == "test-value"'
    image: ghcr.io/guarded:latest
`, path)
			var h Harness
			require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
			err := h.validateOverlays()
			assert.NoError(t, err, "trusted path %s must allow guarded field in overlay", path)
		})
	}

	untrusted := []string{
		"event.entity.title",
		"event.transition.kind",
		"event.state.labels",
		"event.actor.is_entity_author",
		"event.state.diff.files_changed",
	}
	for _, path := range untrusted {
		t.Run("untrusted/"+path, func(t *testing.T) {
			yamlStr := fmt.Sprintf(`
agent: agents/test.md
role: fix
overlays:
  - when: '%s == "test-value"'
    image: ghcr.io/guarded:latest
`, path)
			var h Harness
			require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
			err := h.validateOverlays()
			assert.Error(t, err, "untrusted path %s must reject guarded field in overlay", path)
		})
	}

	// Pin the count: a guarded overlay with a "when:" that reads ONLY
	// runtime.forge or config (no event paths) must also pass.
	t.Run("runtime_forge_only", func(t *testing.T) {
		yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github"'
    image: ghcr.io/guarded:latest
`
		var h Harness
		require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
		err := h.validateOverlays()
		assert.NoError(t, err, "runtime.forge alone must allow guarded field")
	})

	t.Run("config_only", func(t *testing.T) {
		yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'config.tracker == "jira"'
    image: ghcr.io/guarded:latest
`
		var h Harness
		require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))
		err := h.validateOverlays()
		assert.NoError(t, err, "config alone must allow guarded field")
	})
}

// ---------------------------------------------------------------------------
// Guarded overlay runtime error behavior
//
// Given a guarded overlay at run time with an event, When a non-missing CEL
// error occurs, Then the run fails. When a missing-key error occurs, Then
// the overlay is a non-match.
// ---------------------------------------------------------------------------

func TestResolveOverlays_GuardedNonMissingCELError_FailsRun(t *testing.T) {
	yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'event.repo == "test" && event.source.system / 0 == 0'
    image: ghcr.io/guarded:latest
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))

	event := map[string]any{
		"repo":   "test",
		"source": map[string]any{"system": "github"},
	}
	err := h.ResolveOverlays(event, "github", nil)
	require.Error(t, err, "non-missing CEL error on guarded overlay with event must fail the run")
}

func TestResolveOverlays_GuardedMissingKeyCELError_IsNonMatch(t *testing.T) {
	yamlStr := `
agent: agents/test.md
role: fix
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
overlays:
  - when: 'event.source.system == "github"'
    image: ghcr.io/guarded:latest
`
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))

	event := map[string]any{
		"repo": "test",
		// source.system is missing → missing-key error
	}
	err := h.ResolveOverlays(event, "github", nil)
	require.NoError(t, err, "missing-key CEL error on guarded overlay must be a non-match, not a failure")
	assert.Equal(t, "ghcr.io/fullsend-ai/fullsend-sandbox:latest", h.Image,
		"overlay must not have been applied (non-match)")
}

// ---------------------------------------------------------------------------
// No event context → all errors non-match
//
// Given any overlay without an event (nil event context), When evaluated,
// Then every error is a non-match. Tests cover lock, dispatch, and bare
// run paths.
// ---------------------------------------------------------------------------

func TestResolveOverlays_NoEventContextIsAlwaysNonMatch(t *testing.T) {
	t.Run("lock_path", func(t *testing.T) {
		yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'event.source.system == "github"'
    image: ghcr.io/guarded:latest
`
		var h Harness
		require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))

		err := h.ResolveOverlays(nil, "github", nil)
		require.NoError(t, err, "nil event: all CEL errors must be non-match (lock path)")
	})

	t.Run("dispatch_path", func(t *testing.T) {
		yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'event.entity.kind == "pr" && event.source.system == "github"'
    image: ghcr.io/guarded:latest
`
		var h Harness
		require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))

		err := h.ResolveOverlays(nil, "", nil)
		require.NoError(t, err, "nil event: all CEL errors must be non-match (dispatch path)")
	})

	t.Run("bare_run", func(t *testing.T) {
		yamlStr := `
agent: agents/test.md
role: fix
overlays:
  - when: 'runtime.forge == "github" && int("not a number") > 0'
    image: ghcr.io/guarded:latest
`
		var h Harness
		require.NoError(t, yaml.Unmarshal([]byte(yamlStr), &h))

		// The conversion fails at evaluation, not on a missing key, so this
		// reaches the non-missing branch without an event.
		err := h.ResolveOverlays(nil, "github", nil)
		require.NoError(t, err, "nil event: even non-missing errors must be non-match (bare run)")
	})
}

// specTrustedOverlayEventPaths parses the "Trusted fields" section of the
// normalized event spec into sorted event.* paths. A bullet lists dotted
// paths in backticks; a bare name after one ("and `is_fork`") shares the
// previous path's parent.
func specTrustedOverlayEventPaths(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "README.md"))
	require.NoError(t, err)
	code := regexp.MustCompile("`([^`]+)`")
	var paths []string
	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "### Trusted fields"):
			inSection = true
			continue
		case inSection && strings.HasPrefix(line, "#"):
			inSection = false
		}
		if !inSection || !strings.HasPrefix(line, "- ") {
			continue
		}
		prev := ""
		for _, m := range code.FindAllStringSubmatch(line, -1) {
			tok := m[1]
			if !strings.Contains(tok, ".") && prev != "" {
				tok = prev[:strings.LastIndex(prev, ".")+1] + tok
			}
			paths = append(paths, "event."+tok)
			prev = tok
		}
	}
	sort.Strings(paths)
	return paths
}
