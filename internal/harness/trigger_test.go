package harness

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTriggerExpression(t *testing.T) {
	assert.NoError(t, ValidateTriggerExpression(""))
	assert.NoError(t, ValidateTriggerExpression(`event.entity.kind == "work_item"`))
	assert.Error(t, ValidateTriggerExpression(`event.entity.kind ==`))
	assert.Error(t, ValidateTriggerExpression(`"not a bool"`))
}

func TestEvaluateTrigger(t *testing.T) {
	expr := `event.entity.kind == "work_item" && event.transition.label.name == "ready-for-ping"`
	event := map[string]any{
		"entity": map[string]any{
			"kind": "work_item",
		},
		"transition": map[string]any{
			"label": map[string]any{
				"name": "ready-for-ping",
			},
		},
	}
	ok, err := EvaluateTrigger(expr, event)
	require.NoError(t, err)
	assert.True(t, ok)

	event["entity"] = map[string]any{"kind": "change_proposal"}
	ok, err = EvaluateTrigger(expr, event)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_RuntimeForge(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "github"}}
	ok, err := EvaluateOverlay(`runtime.forge == "github"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvaluateOverlay(`runtime.forge == "github"`, event, "gitlab", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_EventField(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	ok, err := EvaluateOverlay(`event.source.system == "jira"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_ConfigVariable(t *testing.T) {
	event := map[string]any{}
	config := map[string]any{"tracker": "jira"}
	ok, err := EvaluateOverlay(`config.tracker == "jira"`, event, "", config)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_EmptyExpression(t *testing.T) {
	ok, err := EvaluateOverlay("", nil, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_WhitespaceExpression(t *testing.T) {
	ok, err := EvaluateOverlay("   ", nil, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_InvalidCEL(t *testing.T) {
	_, err := EvaluateOverlay(`runtime.forge ==`, nil, "", nil)
	require.Error(t, err)
}

func TestEvaluateOverlay_NonBoolResult(t *testing.T) {
	_, err := EvaluateOverlay(`"not a bool"`, nil, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not bool")
}

func TestEvaluateOverlay_NilConfig(t *testing.T) {
	event := map[string]any{}
	// nil config should be substituted with empty map
	ok, err := EvaluateOverlay(`runtime.forge == "github"`, event, "github", nil)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEvaluateOverlay_HasGuard(t *testing.T) {
	// has() guard should work for missing keys in empty events
	event := map[string]any{}
	ok, err := EvaluateOverlay(`has(event.source) && event.source.system == "jira"`, event, "", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEvaluateOverlay_CombinedExpression(t *testing.T) {
	event := map[string]any{"source": map[string]any{"system": "jira"}}
	ok, err := EvaluateOverlay(
		`event.source.system == "jira" && runtime.forge == "github"`,
		event, "github", nil,
	)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNewOverlayEnv(t *testing.T) {
	env, err := NewOverlayEnv()
	require.NoError(t, err)
	require.NotNil(t, env)
}

// --- overlayMissingKey tests ---

func TestOverlayMissingKey_NilError(t *testing.T) {
	assert.False(t, overlayMissingKey(nil))
}

func TestOverlayMissingKey_NoSuchKey(t *testing.T) {
	assert.True(t, overlayMissingKey(fmt.Errorf("no such key: source")))
}

func TestOverlayMissingKey_OtherError(t *testing.T) {
	assert.False(t, overlayMissingKey(fmt.Errorf("type mismatch")))
}

func TestOverlayMissingKey_Wrapped(t *testing.T) {
	assert.True(t, overlayMissingKey(fmt.Errorf("overlays[0].when: %w", fmt.Errorf("no such key: source"))))
}

// The objects a guarded overlay may probe with has() are the normalized
// event schema's required set, so the two cannot drift.
func TestOverlayRequiredEventObjects_MatchSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "normative", "normalized-event", "v1", "normalized-event.schema.json"))
	require.NoError(t, err)
	var schema struct {
		Required []string `json:"required"`
	}
	require.NoError(t, json.Unmarshal(data, &schema))
	want := make([]string, 0, len(schema.Required))
	for _, name := range schema.Required {
		want = append(want, "event."+name)
	}
	slices.Sort(want)
	assert.Equal(t, want, slices.Sorted(maps.Keys(overlayRequiredEventObjects)))
}

// --- guardedOverlayWhenTrusted tests ---

func compileOverlayExpr(t *testing.T, expr string) *cel.Ast {
	t.Helper()
	env, err := NewOverlayEnv()
	require.NoError(t, err)
	ast, issues := env.Compile(expr)
	require.Nil(t, issues, "CEL compile: %v", issues)
	return ast
}

func TestGuardedOverlayWhenTrusted_Trusted(t *testing.T) {
	for _, expr := range []string{
		`true`,
		`event.repo == "org/repo"`,
		`runtime.forge == "github" && config.tracker == "jira"`,
		`has(event.source)`,
		`has(event.source) && event.source.system == "jira"`,
		`has(event.source.system)`,
		`has(event.state.change_proposal.is_fork)`,
		`event != null`,
		`null != event`,
		`event == null`,
		`event != null && event.repo == "org/a"`,
		`event.repo in ["org/a", "org/b"]`,
		`{"github": true, "gitlab": false}[event.repo] == true`,
		`["org/a", "org/b"].exists(r, r == event.repo)`,
		`event.state.change_proposal.is_fork == true`,
		`!event.state.change_proposal.is_fork && event.actor.role == "maintainer"`,
		`event.repo.startsWith("org/")`,
		`{event.repo: true}[event.repo]`,
		`has(runtime.forge) && runtime.forge == "gitlab"`,
		`config.teams.exists(team, team == "core")`,
		`config.teams.map(team, team + "!").exists_one(name, name == "core!")`,
		`[0].exists(a, [a].exists(b, b == a))`,
		`config.teams.all(team, config.lead != team)`,
		// A comprehension variable named event or runtime is the local.
		`config.agents.exists(event, event.name == "code")`,
		`config.agents.exists(runtime, runtime.name == "code")`,
	} {
		t.Run(expr, func(t *testing.T) {
			ref, ok := guardedOverlayWhenTrusted(compileOverlayExpr(t, expr))
			assert.True(t, ok, "offending reference: %q", ref)
		})
	}
}

// Every shape that reads an untrusted fact, including the ones that reach
// it through a trusted path's parent object, is refused and names the
// offending reference (ADR 0112 Decision 2).
func TestGuardedOverlayWhenTrusted_Untrusted(t *testing.T) {
	cases := map[string]string{
		`event.entity.kind == "work_item"`:                                 "event.entity.kind",
		`event.entity.kind in ["work_item"]`:                               "event.entity.kind",
		`["bug"].exists(k, k == event.entity.kind)`:                        "event.entity.kind",
		`event.repo.extra == "x"`:                                          "event.repo.extra",
		`event == runtime`:                                                 "event",
		`event["repo"] == "x"`:                                             "event",
		`[event][0].repo == "x"`:                                           "event",
		`has(event.custom)`:                                                "event.custom",
		`has(event.state.change_proposal)`:                                 "event.state.change_proposal",
		`event.entity["title"] == "x"`:                                     "event.entity",
		`event.state.change_proposal["head_ref"] == "x"`:                   "event.state.change_proposal",
		`"bug" in event.state["labels"]`:                                   "event.state",
		`dyn(event.entity).title == "x"`:                                   "event.entity",
		`(event.repo == "a" ? event.entity : event.entity).title == "x"`:   "event.entity",
		`event.entity.exists(k, k == "title")`:                             "event.entity",
		`"title" in event.entity`:                                          "event.entity",
		`size(event.state) > 3`:                                            "event.state",
		`string(event.entity) == "x"`:                                      "event.entity",
		`event.entity == {"title": "x"}`:                                   "event.entity",
		`event.state.change_proposal != null`:                              "event.state.change_proposal",
		`event.actor.role == "maintainer" && event.actor["login"] == "me"`: "event.actor",
		`{event.entity.kind: 1}.size() == 1`:                               "event.entity.kind",
		`{"a": event.entity}.a == "x"`:                                     "event.entity",
		// Only runtime.forge is a trusted runtime fact; the docs promise no
		// other identifier, so a future variable is untrusted until listed.
		`runtime.ci == "github-actions"`:             "runtime.ci",
		`runtime == null`:                            "runtime",
		`has(runtime.forge) && runtime.other == "x"`: "runtime.other",
		`runtime.forge.platform == "github"`:         "runtime.forge.platform",
		`runtime.forge["x"] == "x"`:                  "runtime.forge",
		`event.repo[0] == "x"`:                       "event.repo",
		// Comprehension scopes: the range is read in the outer scope, a
		// comprehension variable does not outlive its macro, and nesting
		// keeps the outer scope's rules.
		`[event.entity.kind].exists(k, k == "x")`:                "event.entity.kind",
		`[0].exists(runtime, runtime == 0) && runtime.ci == "x"`: "runtime.ci",
		`[0].all(event, event == 0) && event.entity.kind == "x"`: "event.entity.kind",
		`[0].exists(a, [a].exists(b, b == event.entity.kind))`:   "event.entity.kind",
		`[0].map(config, .event.entity.kind).size() == 1`:        "event.entity.kind",
		// An inner comprehension's variable does not leak into its parent.
		`[0].exists(a, [a].exists(event, true) || event["entity"]["kind"] == "pr")`: "event",
		// A type identifier is not a declared variable.
		`type(config.x) == int`: "int",
		// A comprehension variable named event shadows the global, and the
		// checker keeps the global reference as ".event".
		`[0].exists(event, .event.title == "please")`:                                                         "event.title",
		`[0].exists(runtime, .runtime.other == "x")`:                                                          "runtime.other",
		`[0].all(event, .event.entity.kind == "x")`:                                                           "event.entity.kind",
		`google.protobuf.Value{string_value: event.entity.title} == google.protobuf.Value{string_value: "x"}`: "event.entity.title",
	}
	for expr, want := range cases {
		t.Run(expr, func(t *testing.T) {
			ref, ok := guardedOverlayWhenTrusted(compileOverlayExpr(t, expr))
			assert.False(t, ok)
			assert.Equal(t, want, ref)
		})
	}
}

// An expression kind the walker does not know is refused, not trusted.
func TestWalkTrustedOverlayExpr_UnknownKindRefused(t *testing.T) {
	ref, ok := walkTrustedOverlayExpr(celast.NewExprFactory().NewUnspecifiedExpr(1), nil)
	assert.False(t, ok)
	assert.Equal(t, "unsupported expression", ref)
}

// The trusted list is the normative spec's: the "Trusted fields" section of
// docs/normative/normalized-event/v1/README.md and trustedOverlayEventPaths
// must name the same paths.
func TestTrustedOverlayEventPaths_MatchSpecDoc(t *testing.T) {
	want := specTrustedOverlayEventPaths(t)
	require.NotEmpty(t, want, "no bullets found under '### Trusted fields' in the spec")
	assert.Equal(t, want, slices.Sorted(maps.Keys(trustedOverlayEventPaths)),
		"trustedOverlayEventPaths in trigger.go must equal the spec's Trusted fields list")
}
