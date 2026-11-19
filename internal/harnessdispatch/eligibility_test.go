package harnessdispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	ne "github.com/fullsend-ai/fullsend/internal/normentity"
)

// eligibilityEntity returns an open GitHub issue labeled "bug" whose labels
// have the given coverage.
func eligibilityEntity(t *testing.T, labels ne.CoverageStatus) *ne.Entity {
	t.Helper()
	id, err := ne.NewID("github", "github.com", "work_item", "100")
	require.NoError(t, err)
	return &ne.Entity{
		Kind:     ne.KindWorkItem,
		Source:   ne.Source{System: ne.SystemGitHub, Host: "github.com", Repository: "o/r", RepositoryID: "1"},
		ID:       id,
		SourceID: "100",
		Key:      "o/r#7",
		Number:   7,
		URL:      "https://github.com/o/r/issues/7",
		State:    ne.Known(ne.StateOpen),
		Labels:   ne.List[string]{Known: []string{"bug"}, Coverage: ne.Coverage{Status: labels}},
	}
}

func allow(context.Context) error { return nil }

func resolveTo(e *ne.Entity) func(context.Context) (*ne.Entity, error) {
	return func(context.Context) (*ne.Entity, error) { return e, nil }
}

func TestDecideEntityEligibility(t *testing.T) {
	ev := mustEvent(t, "issue-opened.json")
	completeEntity := eligibilityEntity(t, ne.CoverageComplete)
	partialEntity := eligibilityEntity(t, ne.CoveragePartial)
	unknownState := eligibilityEntity(t, ne.CoverageComplete)
	unknownState.State = ne.Unknown[ne.State](ne.ReasonFailed)
	malformed := eligibilityEntity(t, "bogus")

	const guarded = `event != null && event.entity.kind == "work_item" && entity.state == "open"`

	tests := []struct {
		name    string
		eval    EntityEvaluation
		want    EntityOutcome
		reason  string
		resolve bool // whether ResolveEntity must have been called
	}{
		{name: "predicate true", eval: EntityEvaluation{Trigger: `entity.state == "open"`}, want: EntityEligible, resolve: true},
		{name: "predicate false with complete coverage", eval: EntityEvaluation{Trigger: `"wontfix" in entity.labels`}, want: EntityNonMatch, resolve: true},
		{name: "partial coverage stays deferred", eval: EntityEvaluation{Trigger: `"wontfix" in entity.labels`, ResolveEntity: resolveTo(partialEntity)}, want: EntityDeferred, reason: "unresolved", resolve: true},
		{name: "partial coverage cannot prove absence", eval: EntityEvaluation{Trigger: `!("wontfix" in entity.labels)`, ResolveEntity: resolveTo(partialEntity)}, want: EntityDeferred, resolve: true},
		{name: "partial coverage with known match", eval: EntityEvaluation{Trigger: `"bug" in entity.labels`, ResolveEntity: resolveTo(partialEntity)}, want: EntityEligible, resolve: true},
		{name: "unknown field stays deferred", eval: EntityEvaluation{Trigger: `entity.state != "open"`, ResolveEntity: resolveTo(unknownState)}, want: EntityDeferred, resolve: true},
		{name: "failed read is deferred, not false", eval: EntityEvaluation{Trigger: `entity.state == "open"`, ResolveEntity: func(context.Context) (*ne.Entity, error) {
			return nil, errors.New("rate limited")
		}}, want: EntityDeferred, reason: "rate limited", resolve: true},
		{name: "missing entity is deferred", eval: EntityEvaluation{Trigger: `entity.state == "open"`, ResolveEntity: resolveTo(nil)}, want: EntityDeferred, resolve: true},
		{name: "malformed entity is deferred", eval: EntityEvaluation{Trigger: `entity.state == "open"`, ResolveEntity: resolveTo(malformed)}, want: EntityDeferred, reason: "malformed", resolve: true},
		{name: "authorization denied", eval: EntityEvaluation{Trigger: `entity.state == "open"`, Authorize: func(context.Context) error {
			return errors.New("actor below human_min_role")
		}}, want: EntityDenied, reason: "authorization"},
		{name: "missing trusted provenance is denied", eval: EntityEvaluation{Trigger: `entity.state == "open"`, Authorize: func(context.Context) error {
			return errors.New("fullsend invocation provenance is unverifiable")
		}}, want: EntityDenied, reason: "provenance"},
		{name: "no authorization gate", eval: EntityEvaluation{Trigger: `entity.state == "open"`, Authorize: nil}, want: EntityDenied, reason: "no authorization gate"},
		{name: "no entity resolver", eval: EntityEvaluation{Trigger: `entity.state == "open"`, ResolveEntity: nil}, want: EntityDenied, reason: "no entity resolver"},
		{name: "no entity sources", eval: EntityEvaluation{Trigger: `entity.state == "open"`, EntitySources: false}, want: EntityDenied, reason: "event-only path"},
		{name: "empty predicate", eval: EntityEvaluation{Trigger: ""}, want: EntityDenied, reason: "invalid"},
		{name: "invalid predicate", eval: EntityEvaluation{Trigger: `entity.state ==`}, want: EntityDenied, reason: "invalid"},
		{name: "present event, guarded access", eval: EntityEvaluation{Trigger: guarded, Event: ev}, want: EntityEligible, resolve: true},
		{name: "null event, guarded access", eval: EntityEvaluation{Trigger: guarded}, want: EntityNonMatch, resolve: true},
		{name: "null event, unguarded access", eval: EntityEvaluation{Trigger: `event.entity.kind == "work_item"`}, want: EntityDenied, reason: "evaluating predicate", resolve: true},
		{name: "present event, unguarded access", eval: EntityEvaluation{Trigger: `event.entity.kind == "work_item"`, Event: ev}, want: EntityEligible, resolve: true},
		{name: "entity-only predicate, null event", eval: EntityEvaluation{Trigger: `entity.state == "open" && "bug" in entity.labels`}, want: EntityEligible, resolve: true},
		{name: "entity-only predicate, present event", eval: EntityEvaluation{Trigger: `entity.state == "open" && "bug" in entity.labels`, Event: ev}, want: EntityEligible, resolve: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.eval
			e.Harness = "h"
			// Fill in defaults unless the case is about that dependency.
			switch tt.name {
			case "no entity sources":
			default:
				e.EntitySources = true
			}
			if e.Authorize == nil && tt.name != "no authorization gate" {
				e.Authorize = allow
			}
			resolved := false
			resolve := e.ResolveEntity
			if resolve == nil && tt.name != "no entity resolver" {
				resolve = resolveTo(completeEntity)
			}
			if resolve != nil {
				e.ResolveEntity = func(ctx context.Context) (*ne.Entity, error) {
					resolved = true
					return resolve(ctx)
				}
			}

			got := DecideEntityEligibility(context.Background(), e)
			assert.Equal(t, tt.want, got.Outcome, got.Reason)
			assert.Contains(t, got.Reason, "h: ")
			if tt.reason != "" {
				assert.Contains(t, got.Reason, tt.reason)
			}
			assert.Equal(t, tt.resolve, resolved, "entity resolution runs only after the authorization gate passes")
			assert.Equal(t, tt.want == EntityEligible, got.Outcome.MayStartSandbox())
		})
	}
}

func TestDecideEntityEligibility_CancelledContextDefers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := DecideEntityEligibility(ctx, EntityEvaluation{
		Harness:       "h",
		Trigger:       `entity.state == "open"`,
		EntitySources: true,
		Authorize:     allow,
		ResolveEntity: resolveTo(eligibilityEntity(t, ne.CoverageComplete)),
	})
	assert.Equal(t, EntityDeferred, got.Outcome)
	assert.False(t, got.Outcome.MayStartSandbox())
}

func TestDecideEntityEligibility_CancelDuringAuthorizeDefers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := DecideEntityEligibility(ctx, EntityEvaluation{
		Harness:       "h",
		Trigger:       `entity.state == "open"`,
		EntitySources: true,
		Authorize: func(ctx context.Context) error {
			cancel()
			return ctx.Err()
		},
		ResolveEntity: resolveTo(eligibilityEntity(t, ne.CoverageComplete)),
	})
	assert.Equal(t, EntityDeferred, got.Outcome)
	assert.False(t, got.Outcome.MayStartSandbox())
}

func TestDecideEntityEligibility_CancelDuringEvaluationDefers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	list := "[0,1,2,3,4,5,6,7,8,9,0,1,2,3,4,5,6,7,8,9,0,1,2,3,4,5,6,7,8,9,0,1,2,3,4,5,6,7,8,9,0,1,2,3,4,5,6,7,8,9]"
	got := DecideEntityEligibility(ctx, EntityEvaluation{
		Harness:       "h",
		Trigger:       list + ".all(a, " + list + ".all(b, a + b >= 0))",
		EntitySources: true,
		Authorize:     func(context.Context) error { return nil },
		ResolveEntity: func(context.Context) (*ne.Entity, error) {
			cancel()
			return eligibilityEntity(t, ne.CoverageComplete), nil
		},
	})
	assert.Equal(t, EntityDeferred, got.Outcome)
	assert.False(t, got.Outcome.MayStartSandbox())
}

func TestDecideEntityEligibility_CancelDuringResolutionDefersSimplePredicate(t *testing.T) {
	for _, trigger := range []string{"true", "false"} {
		t.Run(trigger, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got := DecideEntityEligibility(ctx, EntityEvaluation{
				Harness:       "h",
				Trigger:       trigger,
				EntitySources: true,
				Authorize:     allow,
				ResolveEntity: func(context.Context) (*ne.Entity, error) {
					cancel()
					return eligibilityEntity(t, ne.CoverageComplete), nil
				},
			})
			assert.Equal(t, EntityDeferred, got.Outcome)
			assert.False(t, got.Outcome.MayStartSandbox())
		})
	}
}

func TestEntityOutcome(t *testing.T) {
	assert.Equal(t, EntityDenied, EntityOutcome(0), "the zero outcome fails closed")
	for o, want := range map[EntityOutcome]string{
		EntityDenied:     "denied",
		EntityDeferred:   "deferred",
		EntityNonMatch:   "non-match",
		EntityEligible:   "eligible",
		EntityOutcome(9): "denied",
	} {
		assert.Equal(t, want, o.String())
		assert.Equal(t, o == EntityEligible, o.MayStartSandbox(), o.String())
	}
}

// TestEventOnlyHarnessUnchanged pins that a harness without entity sources
// keeps its event-only routing: MatchHarnesses evaluates its trigger over the
// event alone, and the entity-source seam does not route it.
func TestEventOnlyHarnessUnchanged(t *testing.T) {
	ev := mustEvent(t, "issue-opened.json")
	const trigger = `event.entity.kind == "work_item"`
	matched, err := MatchHarnesses([]TriggeredHarness{{
		Name:    "event-only",
		Harness: &harness.Harness{Trigger: trigger},
	}}, ev)
	require.NoError(t, err)
	require.Len(t, matched, 1)

	// The event-only trigger environment still has no entity variable.
	_, err = harness.EvaluateTrigger(`entity.state == "open"`, map[string]any{})
	require.Error(t, err)

	got := DecideEntityEligibility(context.Background(), EntityEvaluation{
		Harness:       "event-only",
		Trigger:       trigger,
		Event:         ev,
		Authorize:     allow,
		ResolveEntity: resolveTo(eligibilityEntity(t, ne.CoverageComplete)),
	})
	assert.Equal(t, EntityDenied, got.Outcome)
}
