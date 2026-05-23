package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidTraceparent(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", true},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", true},
		{"", false},
		{"not-a-traceparent", false},
		{"00-INVALID-00f067aa0ba902b7-01", false},
		{"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false}, // wrong version
	}

	for _, tc := range tests {
		assert.Equal(t, tc.valid, IsValidTraceparent(tc.input), "input: %q", tc.input)
	}
}

func TestContextFromTraceparent(t *testing.T) {
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := ContextFromTraceparent(context.Background(), tp)
	require.NotNil(t, ctx)

	result := Traceparent(ctx)
	// The extracted context should produce a valid traceparent with same trace ID.
	assert.Contains(t, result, "4bf92f3577b34da6a3ce929d0e0e4736")
}

func TestContextFromTraceparent_Empty(t *testing.T) {
	ctx := ContextFromTraceparent(context.Background(), "")
	result := Traceparent(ctx)
	assert.Empty(t, result)
}

func TestWorkItemID(t *testing.T) {
	assert.Equal(t, "owner/repo#42", WorkItemID("owner/repo", 42))
	assert.Equal(t, "", WorkItemID("", 42))
	assert.Equal(t, "", WorkItemID("owner/repo", 0))
	assert.Equal(t, "", WorkItemID("owner/repo", -1))
}

func TestWorkItemFromURL(t *testing.T) {
	tests := []struct {
		url    string
		expect string
	}{
		{"https://github.com/org/repo/issues/123", "org/repo#123"},
		{"https://github.com/org/repo/pull/456", "org/repo#456"},
		{"https://github.com/org/repo/issues/123/", "org/repo#123"},
		{"https://not-github.com/too/short", ""},
		{"https://github.com/org/repo/actions/runs/123", ""},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.expect, workItemFromURL(tc.url), "url: %s", tc.url)
	}
}
