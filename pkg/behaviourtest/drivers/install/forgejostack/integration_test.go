package forgejostack

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// integrationEnv opts in to TestStack_Integration, which pulls the
// pinned images and runs the real stack on the host engine.
const integrationEnv = "FULLSEND_FORGEJO_STACK_TEST"

// TestStack_Integration starts the pinned Forgejo and Runner stack on
// the Docker or Podman socket ResolveSocket selects (set DOCKER_HOST or
// CONTAINER_HOST to choose the engine; run it once per engine), checks
// the canonical URL from this process and from inside the runner
// container (Start already proved it from a job container), and checks
// that teardown leaves no stack container behind. It runs only when
// FULLSEND_FORGEJO_STACK_TEST=1.
func TestStack_Integration(t *testing.T) {
	if os.Getenv(integrationEnv) != "1" {
		t.Skipf("set %s=1 to run the Forgejo stack against a real Docker or Podman engine", integrationEnv)
	}
	socket, err := ResolveSocket(os.Getenv, SocketExists)
	require.NoError(t, err)
	engine := NewSocketEngine(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	s, err := Start(ctx, Config{Engine: engine, SocketPath: socket, Logf: t.Logf})
	require.NoError(t, err)
	tornDown := false
	t.Cleanup(func() {
		if !tornDown {
			_ = s.Teardown(context.Background())
		}
	})

	resp, err := http.Get(s.URL() + "api/healthz")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "canonical URL from the behaviour-test process")

	// The seeded agent-definition repository is public and serves the
	// fixture at the pinned commit.
	ref := s.AgentsRef()
	require.Regexp(t, commitSHAPattern, ref)
	for path, want := range AgentFixtureFiles() {
		resp, err := http.Get(s.URL() + AgentsOwner + "/" + AgentsRepo + "/raw/commit/" + ref + "/" + path)
		require.NoError(t, err)
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		assert.Equal(t, want, string(got), path)
	}

	res, err := engine.Exec(ctx, s.containerID("runner"), []string{"wget", "-q", "-O", "/dev/null", s.URL() + "api/healthz"})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode, "canonical URL from the runner container: %s", res.Stderr)

	tornDown = true
	require.NoError(t, s.Teardown(ctx))
	left, err := engine.ListContainers(ctx, StackLabel, s.Name())
	require.NoError(t, err)
	assert.Empty(t, left, "teardown leaves no stack containers behind")
}
