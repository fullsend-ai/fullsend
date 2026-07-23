package world

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromContext_Nil(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, FromContext(ctx))
}

func TestWithWorld_RoundTrip(t *testing.T) {
	w := &World{Org: "test-org"}
	ctx := WithWorld(context.Background(), w)
	got := FromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "test-org", got.Org)
	assert.Same(t, w, got)
}

func TestWithWorld_IndependentContexts(t *testing.T) {
	w1 := &World{Org: "org-1", IssueNumber: 1}
	w2 := &World{Org: "org-2", IssueNumber: 2}

	ctx1 := WithWorld(context.Background(), w1)
	ctx2 := WithWorld(context.Background(), w2)

	got1 := FromContext(ctx1)
	got2 := FromContext(ctx2)

	require.NotNil(t, got1)
	require.NotNil(t, got2)
	assert.NotSame(t, got1, got2)
	assert.Equal(t, "org-1", got1.Org)
	assert.Equal(t, "org-2", got2.Org)

	// Mutating one does not affect the other.
	got1.IssueNumber = 99
	assert.Equal(t, 2, got2.IssueNumber)
}
