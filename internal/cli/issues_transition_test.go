package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type transitionTracker struct {
	tracker.Client
	project string
	number  int
	opts    tracker.TransitionOptions
	err     error
}

func (f *transitionTracker) TransitionIssue(_ context.Context, project string, number int, opts tracker.TransitionOptions) (*tracker.TransitionResult, error) {
	f.project, f.number, f.opts = project, number, opts
	return &tracker.TransitionResult{Changed: true, Transition: opts.Name}, f.err
}

func TestIssuesTransitionCommand(t *testing.T) {
	cmd := newIssuesCmd()
	found, _, err := cmd.Find([]string{"transition"})
	require.NoError(t, err)
	assert.Equal(t, "transition", found.Name())
	for _, name := range []string{"tracker", "project", "number", "fullsend-dir", "token", "transition", "state", "status", "resolution", "comment"} {
		require.NotNil(t, found.Flags().Lookup(name))
	}
	for _, args := range [][]string{{"--number", "1"}, {"--state", "closed"}, {"--number", "1", "unexpected"}} {
		c := newIssuesTransitionCmd()
		c.SetArgs(args)
		require.Error(t, c.Execute())
	}
}

func TestRunIssuesTransition(t *testing.T) {
	for _, name := range []string{"jira", "github", "gitlab"} {
		t.Run(name, func(t *testing.T) {
			f := &transitionTracker{}
			var out bytes.Buffer
			cfg := &issuesTransitionConfig{trackerName: name, project: "owner/repo", number: 618, opts: tracker.TransitionOptions{State: "closed"}, comment: "Completed", testClient: f, testWriter: &out}
			require.NoError(t, runIssuesTransition(context.Background(), cfg))
			assert.Equal(t, "owner/repo", f.project)
			assert.Equal(t, 618, f.number)
			assert.Equal(t, tracker.Body("Completed"), f.opts.Comment)
			assert.Contains(t, out.String(), `"changed":true`)
		})
	}
}

func TestRunIssuesTransitionErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*issuesTransitionConfig)
		want   string
	}{
		{"number", func(c *issuesTransitionConfig) { c.number = 0 }, "positive"},
		{"selectors", func(c *issuesTransitionConfig) { c.opts.Name = "Review" }, "exactly one"},
		{"state", func(c *issuesTransitionConfig) { c.opts.State = "open" }, "unsupported lifecycle"},
		{"tracker", func(c *issuesTransitionConfig) { c.trackerName = "other" }, "unsupported --tracker"},
		{"missing tracker", func(c *issuesTransitionConfig) { c.trackerName = "" }, "--tracker is required"},
		{"project", func(c *issuesTransitionConfig) { c.project = "" }, "--project"},
		{"client config", func(c *issuesTransitionConfig) { c.trackerName = "jira"; c.testClient = nil }, "Jira requires"},
		{"unsupported", func(c *issuesTransitionConfig) { c.testClient = &noTransitionTracker{} }, "not supported"},
		{"backend", func(c *issuesTransitionConfig) { c.testClient = &transitionTracker{err: errors.New("workflow denied")} }, "workflow denied"},
		{"output", func(c *issuesTransitionConfig) { c.testWriter = failingTransitionWriter{} }, "write failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &issuesTransitionConfig{trackerName: "github", project: "o/r", number: 1, opts: tracker.TransitionOptions{State: "closed"}, testClient: &transitionTracker{}, testWriter: &bytes.Buffer{}}
			tt.change(c)
			require.ErrorContains(t, runIssuesTransition(context.Background(), c), tt.want)
		})
	}
}

type noTransitionTracker struct{ tracker.Client }
type failingTransitionWriter struct{}

func (failingTransitionWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
