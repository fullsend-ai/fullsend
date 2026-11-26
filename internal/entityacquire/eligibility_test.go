package entityacquire

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	ne "github.com/fullsend-ai/fullsend/internal/normentity"
)

// TestAcquisitionThroughEligibilitySeam shows that acquired entities flow
// through the shared entity eligibility decision: a settled witness is
// eligible, incomplete evidence defers, and an inconsistent acquisition
// never reaches evaluation.
func TestAcquisitionThroughEligibilitySeam(t *testing.T) {
	const pred = `entity.comments.exists(c, c.command == "/fs-code")`
	decide := func(a Adapter) harnessdispatch.EntityDecision {
		return harnessdispatch.DecideEntityEligibility(context.Background(), harnessdispatch.EntityEvaluation{
			Harness:       "code",
			Trigger:       pred,
			EntitySources: true,
			Authorize:     func(context.Context) error { return nil },
			ResolveEntity: func(ctx context.Context) (*ne.Entity, error) {
				res, err := Acquire(ctx, Request{Predicate: pred, Adapter: a})
				if err != nil {
					return nil, err
				}
				return res.ResolvedEntity()
			},
		})
	}

	witness := &fakeAdapter{base: workItem(), rev: rev1, pages: map[Need][]*Page{
		NeedComments: pagesOf(rev1, false, commentsFrag(comment("1", "/fs-code")), commentsFrag()),
	}}
	assert.Equal(t, harnessdispatch.EntityEligible, decide(witness).Outcome)

	limited := &fakeAdapter{base: workItem(), rev: rev1,
		pages: map[Need][]*Page{NeedComments: pagesOf(rev1, false, commentsFrag(comment("1", "hi")), commentsFrag())},
		errs:  map[Need]map[int]error{NeedComments: {1: ErrLimit}},
	}
	d := decide(limited)
	assert.Equal(t, harnessdispatch.EntityDeferred, d.Outcome, d.Reason)

	changed := &fakeAdapter{base: workItem(), rev: rev1, pages: map[Need][]*Page{
		NeedComments: pagesOf("r2", false, commentsFrag(comment("1", "/fs-code"))),
	}}
	d = decide(changed)
	assert.Equal(t, harnessdispatch.EntityDeferred, d.Outcome, d.Reason)
	assert.Contains(t, d.Reason, "inconsistent")
}
