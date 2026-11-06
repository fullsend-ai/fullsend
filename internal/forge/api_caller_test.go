package forge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAPICaller(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, APICallerOther, APICaller(ctx), "an unlabelled context is other")

	outer := WithAPICaller(ctx, "claim")
	assert.Equal(t, "claim", APICaller(outer))

	inner := WithAPICaller(outer, "lock")
	assert.Equal(t, "lock", APICaller(inner), "an inner label replaces the outer one")
	assert.Equal(t, "claim", APICaller(outer), "the outer context keeps its label")

	assert.Equal(t, "claim", APICaller(context.WithoutCancel(outer)), "a detached context keeps its label")
}
