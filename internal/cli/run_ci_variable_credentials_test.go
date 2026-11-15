package cli

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const testCIServerURL = "https://gitlab.example.com"

// unsignedTestJWT builds a dummy, unsigned JWT carrying iss; it is never
// verified, only shaped like a GitLab id_tokens token.
func unsignedTestJWT(iss string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." +
		enc([]byte(`{"iss":"`+iss+`","aud":"fullsend","sub":"project_path:group/project"}`)) + "." +
		enc([]byte("dummy-signature"))
}

func jiraDef(value string) harness.ProviderDef {
	return harness.ProviderDef{Name: "jira", Type: "fullsend-jira", Credentials: map[string]string{"JIRA_TOKEN": value}}
}

const jiraCIVariableLine = `provider "jira": credential JIRA_TOKEN comes from CI/CD variable FULLSEND_JIRA_TOKEN, a long-lived value masked by exact match only; no keyless replacement on GitLab yet`

func TestReportCIVariableCredentials(t *testing.T) {
	const dummyValue = "dummy-ci-variable-value-0001"
	tests := []struct {
		name   string
		policy ciVariableCredentialPolicy
		defs   []harness.ProviderDef
		env    map[string]string
		want   string
	}{
		{
			name:   "non-JWT variable warns",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": dummyValue, "CI_SERVER_URL": testCIServerURL},
			want:   "  ! " + jiraCIVariableLine + "\n",
		},
		{
			name:   "bare $VAR reference warns",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs:   []harness.ProviderDef{jiraDef("$FULLSEND_JIRA_TOKEN")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": dummyValue},
			want:   "  ! " + jiraCIVariableLine + "\n",
		},
		{
			name:   "id_tokens JWT from this instance prints nothing",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": unsignedTestJWT(testCIServerURL), "CI_SERVER_URL": testCIServerURL},
			want:   "",
		},
		{
			name:   "JWT from another issuer warns",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": unsignedTestJWT("https://other.example.com"), "CI_SERVER_URL": testCIServerURL},
			want:   "  ! " + jiraCIVariableLine + "\n",
		},
		{
			name:   "unset variable prints nothing",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"CI_SERVER_URL": testCIServerURL},
			want:   "",
		},
		{
			name:   "literal value prints nothing",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs:   []harness.ProviderDef{jiraDef("literal-dummy-value"), jiraDef("prefix-${FULLSEND_JIRA_TOKEN}"), jiraDef("")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": dummyValue},
			want:   "",
		},
		{
			name:   "opt-out prints an info line instead",
			policy: ciVariableCredentialPolicy{forge: "gitlab", allow: true},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": dummyValue},
			want:   "    " + jiraCIVariableLine + "\n",
		},
		{
			name:   "github forge prints nothing",
			policy: ciVariableCredentialPolicy{forge: "github"},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": dummyValue},
			want:   "",
		},
		{
			name:   "undetected forge prints nothing",
			policy: ciVariableCredentialPolicy{},
			defs:   []harness.ProviderDef{jiraDef("${FULLSEND_JIRA_TOKEN}")},
			env:    map[string]string{"FULLSEND_JIRA_TOKEN": dummyValue},
			want:   "",
		},
		{
			name:   "provider with a keyless replacement names it",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs: []harness.ProviderDef{{Name: "vertex", Type: "fullsend-vertex-ai", Credentials: map[string]string{
				"GOOGLE_KEY": "${MY_VERTEX_KEY}",
			}}},
			env:  map[string]string{"MY_VERTEX_KEY": dummyValue},
			want: "  ! provider \"vertex\": credential GOOGLE_KEY comes from CI/CD variable MY_VERTEX_KEY, a long-lived value masked by exact match only; keyless replacement: an `id_tokens:` OIDC token (FULLSEND_ID_TOKEN) exchanged through the gitlab-oidc Workload Identity provider\n",
		},
		{
			name:   "keys are reported in order",
			policy: ciVariableCredentialPolicy{forge: "gitlab"},
			defs: []harness.ProviderDef{{Name: "custom", Type: "custom-type", Credentials: map[string]string{
				"B_TOKEN": "${B_VAR}",
				"A_TOKEN": "${A_VAR}",
			}}},
			env: map[string]string{"A_VAR": dummyValue, "B_VAR": dummyValue},
			want: "  ! provider \"custom\": credential A_TOKEN comes from CI/CD variable A_VAR, a long-lived value masked by exact match only; no keyless replacement on GitLab yet\n" +
				"  ! provider \"custom\": credential B_TOKEN comes from CI/CD variable B_VAR, a long-lived value masked by exact match only; no keyless replacement on GitLab yet\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			reportCIVariableCredentials(tt.policy, tt.defs, mapGetenv(tt.env), ui.New(&buf))
			assert.Equal(t, tt.want, buf.String())
			assert.NotContains(t, buf.String(), dummyValue, "a credential value is never printed")
		})
	}
}

func TestIsGitLabIDToken(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"RS256"}`))
	claims := enc([]byte(`{"iss":"` + testCIServerURL + `"}`))
	tests := []struct {
		name   string
		value  string
		server string
		want   bool
	}{
		{name: "matching issuer", value: unsignedTestJWT(testCIServerURL), server: testCIServerURL, want: true},
		{name: "empty signature segment", value: header + "." + claims + ".", server: testCIServerURL, want: true},
		{name: "other issuer", value: unsignedTestJWT("https://other.example.com"), server: testCIServerURL},
		{name: "CI_SERVER_URL unset", value: unsignedTestJWT(testCIServerURL), server: ""},
		{name: "two segments", value: header + "." + claims, server: testCIServerURL},
		{name: "four segments", value: header + "." + claims + ".x.y", server: testCIServerURL},
		{name: "empty header", value: "." + claims + ".sig", server: testCIServerURL},
		{name: "claims not base64url", value: header + ".!!!." + "sig", server: testCIServerURL},
		{name: "claims not JSON", value: header + "." + enc([]byte("not json")) + ".sig", server: testCIServerURL},
		{name: "header not JSON", value: enc([]byte("not json")) + "." + claims + ".sig", server: testCIServerURL},
		{name: "signature not base64url", value: header + "." + claims + ".!!!", server: testCIServerURL},
		{name: "plain token", value: "glpat-dummy-value", server: testCIServerURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isGitLabIDToken(tt.value, tt.server))
		})
	}
}

func TestCredentialVariable(t *testing.T) {
	for value, want := range map[string]string{
		"${FOO}":     "FOO",
		"$FOO_BAR1":  "FOO_BAR1",
		"${_X}":      "_X",
		"literal":    "",
		"":           "",
		"${FOO}bar":  "",
		"$FOO$BAR":   "",
		"${FOO-bar}": "",
		"${1FOO}":    "",
		" ${FOO}":    "",
	} {
		got, ok := credentialVariable(value)
		assert.Equal(t, want != "", ok, "value %q", value)
		assert.Equal(t, want, got, "value %q", value)
	}
}

func setStaticOpenAIKeyEnv(t *testing.T, key string) {
	t.Helper()
	for _, k := range []string{"FULLSEND_OPENAI_AUDIENCE", "FULLSEND_OPENAI_IDENTITY_PROVIDER_ID", "FULLSEND_OPENAI_SERVICE_ACCOUNT_ID", "ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CI", "true")
	t.Setenv("CI_SERVER_URL", testCIServerURL)
}

const openAIGitHubStaticKeyWarning = "OpenAI credential is a static OPENAI_API_KEY in CI; prefer Workload Identity Federation (FULLSEND_OPENAI_AUDIENCE, FULLSEND_OPENAI_IDENTITY_PROVIDER_ID, FULLSEND_OPENAI_SERVICE_ACCOUNT_ID) so no long-lived key is stored"

const openAIGitLabCIVariableLine = `provider "openai": credential OPENAI_API_KEY comes from CI/CD variable OPENAI_API_KEY, a long-lived value masked by exact match only; no keyless replacement on GitLab yet`

func TestEnsureOpenAIProvider_StaticKeyNotice(t *testing.T) {
	const key = "sk-dummy-static-key-for-tests-0001"
	tests := []struct {
		name    string
		policy  ciVariableCredentialPolicy
		want    string
		notWant []string
	}{
		{
			name:    "github keeps the WIF recommendation",
			policy:  ciVariableCredentialPolicy{forge: "github"},
			want:    "  ! " + openAIGitHubStaticKeyWarning + "\n",
			notWant: []string{"CI/CD variable"},
		},
		{
			name:    "gitlab warns about the CI/CD variable instead",
			policy:  ciVariableCredentialPolicy{forge: "gitlab"},
			want:    "  ! " + openAIGitLabCIVariableLine + "\n",
			notWant: []string{"FULLSEND_OPENAI_AUDIENCE", "Workload Identity"},
		},
		{
			name:    "gitlab opt-out prints an info line",
			policy:  ciVariableCredentialPolicy{forge: "gitlab", allow: true},
			want:    "    " + openAIGitLabCIVariableLine + "\n",
			notWant: []string{"! " + openAIGitLabCIVariableLine, "FULLSEND_OPENAI_AUDIENCE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeOpenshellRecorder(t)
			setStaticOpenAIKeyEnv(t, key)
			var buf strings.Builder
			pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType, Credentials: map[string]string{"OPENAI_API_KEY": ""}}
			_, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", config.OpenAIWIFConfig{}, piBackend(), tt.policy, ui.New(&buf))
			require.NoError(t, err)
			out := buf.String()
			assert.Contains(t, out, tt.want)
			for _, s := range tt.notWant {
				assert.NotContains(t, out, s)
			}
			assert.NotContains(t, out, key, "the key is never printed")
		})
	}
}

func TestEnsureOpenAIProvider_GitLabIDTokenKeyPrintsNothingNew(t *testing.T) {
	fakeOpenshellRecorder(t)
	setStaticOpenAIKeyEnv(t, unsignedTestJWT(testCIServerURL))
	var buf strings.Builder
	pd := harness.ProviderDef{Name: "openai", Type: openAIProviderType}
	_, err := ensureOpenAIProvider(context.Background(), pd, "fs-cod-feedface", config.OpenAIWIFConfig{}, piBackend(), ciVariableCredentialPolicy{forge: "gitlab"}, ui.New(&buf))
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "CI/CD variable")
	assert.NotContains(t, buf.String(), "FULLSEND_OPENAI_AUDIENCE")
}
