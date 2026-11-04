package scaffold

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const testExistingProviderAudience = "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/shared-pool/providers/gitlab-instance"

// agentIDTokenAudience returns the FULLSEND_ID_TOKEN audience of the agent
// job in the rendered agent template.
func agentIDTokenAudience(t *testing.T, content []byte) string {
	t.Helper()
	// The template's first document is the spec header; jobs live in the last.
	var doc map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var next map[string]any
		if err := dec.Decode(&next); err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
		doc = next
	}
	for name, raw := range doc {
		if !strings.HasSuffix(name, " agent") {
			continue
		}
		job, ok := raw.(map[string]any)
		require.True(t, ok)
		tokens, ok := job["id_tokens"].(map[string]any)
		require.True(t, ok)
		token, ok := tokens["FULLSEND_ID_TOKEN"].(map[string]any)
		require.True(t, ok)
		aud, ok := token["aud"].(string)
		require.True(t, ok)
		return aud
	}
	t.Fatal("agent job not found")
	return ""
}

func TestRenderGitLabAgentAudience_DefaultUnchanged(t *testing.T) {
	shipped := []byte(gitlabPerRepoText(t, GitLabAgentTemplatePath))
	for _, audience := range []string{"", DefaultGitLabIDTokenAudience} {
		rendered, err := RenderGitLabAgentAudience(shipped, audience)
		require.NoError(t, err)
		assert.Equal(t, string(shipped), string(rendered), "audience %q must keep the template byte for byte", audience)
	}
	assert.Contains(t, string(shipped), "\n      aud: \"fullsend\"\n")
	assert.Equal(t, DefaultGitLabIDTokenAudience, agentIDTokenAudience(t, shipped))
}

func TestRenderGitLabAgentAudience_Custom(t *testing.T) {
	shipped := []byte(gitlabPerRepoText(t, GitLabAgentTemplatePath))
	rendered, err := RenderGitLabAgentAudience(shipped, testExistingProviderAudience)
	require.NoError(t, err)

	assert.Equal(t, testExistingProviderAudience, agentIDTokenAudience(t, rendered))
	assert.Contains(t, string(rendered), "\n      aud: \""+testExistingProviderAudience+"\"\n")
	assert.NotContains(t, string(rendered), `aud: "fullsend"`)
	// Only the audience line changes.
	assert.Equal(t, strings.Count(string(shipped), "\n"), strings.Count(string(rendered), "\n"))
	assert.Equal(t, string(shipped), strings.Replace(string(rendered), testExistingProviderAudience, DefaultGitLabIDTokenAudience, 1))
}

func TestRenderGitLabAgentAudience_RequiresShippedLine(t *testing.T) {
	_, err := RenderGitLabAgentAudience([]byte("job:\n  script: [true]\n"), testExistingProviderAudience)
	require.ErrorContains(t, err, "expected exactly one id_tokens line")

	twice := []byte("a:\n      aud: \"fullsend\"\nb:\n      aud: \"fullsend\"\n")
	_, err = RenderGitLabAgentAudience(twice, testExistingProviderAudience)
	require.ErrorContains(t, err, "found 2")
}

// inferenceCredentialFragment returns the run-agent-job.sh block that writes
// the Vertex credential config, from its comment to the end of its if block.
func inferenceCredentialFragment(t *testing.T) string {
	t.Helper()
	script := gitlabPerRepoText(t, gitlabRunAgentJobScriptPath)
	start := strings.Index(script, "# Inference credential setup")
	require.GreaterOrEqual(t, start, 0, "credential setup block not found")
	end := strings.Index(script[start:], "\n# OpenAI static key")
	require.Greater(t, end, 0, "end of credential setup block not found")
	return script[start : start+end]
}

// runInferenceCredentialFragment runs the credential block under /bin/sh and
// returns the credential file it wrote.
func runInferenceCredentialFragment(t *testing.T, env ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fragment.sh")
	body := "set -eu\n" + inferenceCredentialFragment(t) + "\ncat \"${GOOGLE_APPLICATION_CREDENTIALS}\"\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	cmd := exec.Command("/bin/sh", path)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + dir,
		"FULLSEND_GCP_WIF_PROVIDER=projects/123456789/locations/global/workloadIdentityPools/shared-pool/providers/gitlab-instance",
		"FULLSEND_GCP_PROJECT_ID=example-project",
		"FULLSEND_ID_TOKEN=header.payload.signature",
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRunAgentJobCredentialConfig_DirectFederationUnchanged(t *testing.T) {
	out, err := runInferenceCredentialFragment(t)
	require.NoError(t, err, out)
	want := `{
  "type": "external_account",
  "audience": "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/shared-pool/providers/gitlab-instance",
  "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
  "token_url": "https://sts.googleapis.com/v1/token",
  "credential_source": { "file": "/sandbox/workspace/.gcp-oidc-token" }
}
`
	assert.Equal(t, want, out)
	assert.NotContains(t, out, "service_account_impersonation_url")
}

func TestRunAgentJobCredentialConfig_Impersonation(t *testing.T) {
	out, err := runInferenceCredentialFragment(t, "FULLSEND_GCP_SERVICE_ACCOUNT=agent-sa@example-project.iam.gserviceaccount.com")
	require.NoError(t, err, out)
	var creds map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &creds), out)
	assert.Equal(t, "external_account", creds["type"])
	assert.Equal(t, "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/shared-pool/providers/gitlab-instance", creds["audience"])
	assert.Equal(t, "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/agent-sa@example-project.iam.gserviceaccount.com:generateAccessToken", creds["service_account_impersonation_url"])
	assert.Equal(t, map[string]any{"file": "/sandbox/workspace/.gcp-oidc-token"}, creds["credential_source"])
}

func TestRunAgentJobCredentialConfig_RejectsMalformedServiceAccount(t *testing.T) {
	for _, sa := range []string{
		`agent-sa@example-project.iam.gserviceaccount.com","x":"y`,
		"agent-sa@a@example-project.iam.gserviceaccount.com",
		"agent-sa@example-project.example.com",
		"Agent-SA@example-project.iam.gserviceaccount.com",
	} {
		out, err := runInferenceCredentialFragment(t, "FULLSEND_GCP_SERVICE_ACCOUNT="+sa)
		require.Error(t, err, "service account %q must be rejected: %s", sa, out)
		assert.Contains(t, out, "FULLSEND_GCP_SERVICE_ACCOUNT", sa)
		assert.NotContains(t, out, "service_account_impersonation_url", sa)
	}
}
