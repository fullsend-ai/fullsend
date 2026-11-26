package behaviourtest

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

// plainDriver is an install.Driver that reports no agent source.
type plainDriver struct{}

func (plainDriver) AllocateRepo(context.Context) (string, error) { return "", nil }
func (plainDriver) DeallocateRepo(context.Context, string) error { return nil }
func (plainDriver) Finalize(context.Context) error               { return nil }
func (plainDriver) Capacity() int                                { return 1 }

// reportingDriver is an install.Driver that reports an agent source.
type reportingDriver struct {
	plainDriver
	env map[string]string
}

func (d reportingDriver) AgentSourceEnv() map[string]string { return d.env }

var _ install.AgentSourceEnvReporter = reportingDriver{}

// TestApplyAgentSourceEnvReachesCLIProcess proves a reporter's variables
// reach a child process started the way the suite starts the CLI (from the
// inherited process environment), without the test setting them by hand.
func TestApplyAgentSourceEnvReachesCLIProcess(t *testing.T) {
	driver := reportingDriver{env: map[string]string{
		"FULLSEND_FORGEJO_URL":         "http://forgejo.test:3000",
		"FULLSEND_FORGEJO_AGENTS_REPO": "fullsend-ai/agents",
		"FULLSEND_FORGEJO_AGENTS_REF":  "abc123",
	}}
	applyAgentSourceEnv(t, driver)

	for name, want := range driver.env {
		assert.Equal(t, want, os.Getenv(name))
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPrintAgentSourceEnv$", "-test.v")
	cmd.Env = append(os.Environ(), "AGENT_SOURCE_ENV_HELPER=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "FULLSEND_FORGEJO_AGENTS_REF=abc123")
	assert.Contains(t, string(out), "FULLSEND_FORGEJO_URL=http://forgejo.test:3000")
}

// TestHelperPrintAgentSourceEnv is the child process of
// TestApplyAgentSourceEnvReachesCLIProcess.
func TestHelperPrintAgentSourceEnv(t *testing.T) {
	if os.Getenv("AGENT_SOURCE_ENV_HELPER") != "1" {
		t.Skip("child process helper")
	}
	for _, name := range []string{"FULLSEND_FORGEJO_URL", "FULLSEND_FORGEJO_AGENTS_REPO", "FULLSEND_FORGEJO_AGENTS_REF"} {
		_, _ = os.Stdout.WriteString(name + "=" + os.Getenv(name) + "\n")
	}
}

func TestApplyAgentSourceEnvNonReporter(t *testing.T) {
	t.Setenv("FULLSEND_FORGEJO_URL", "")
	applyAgentSourceEnv(t, plainDriver{})
	assert.Empty(t, os.Getenv("FULLSEND_FORGEJO_URL"))
}
