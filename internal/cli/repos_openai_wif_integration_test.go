package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	githubforge "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/stretchr/testify/require"
)

// This integration uses the shipped workflow sources, not a synthetic WIF
// contract, and follows install -> status -> runtime credential exchange.
func TestOpenAIWIFInstallStatusRuntimeIntegration(t *testing.T) {
	for _, vendor := range []bool{false, true} {
		t.Run(fmt.Sprintf("vendor=%t", vendor), func(t *testing.T) { openAIWIFInstallStatusRuntimeIntegration(t, vendor) })
	}
}

func openAIWIFInstallStatusRuntimeIntegration(t *testing.T, vendor bool) {
	t.Helper()
	fc := newInstallFakeClient("acme/api")
	manifestYAML := strings.ReplaceAll(testManifestYAML, "vertex-wif", "openai-wif")
	if vendor {
		manifestYAML = strings.ReplaceAll(strings.ReplaceAll(manifestYAML, "  fullsend_ref: v1.0.0\n", ""), "    - name: acme/api", "    - name: acme/api\n      vendor: true")
	}
	manifest := writeTestManifest(t, manifestYAML)
	opts := useOpenAIInputs(githubManagedInstallOpts(manifest, fc))
	opts.openAIAPIKey = ""
	opts.roles = config.PerRepoDefaultRoles()
	opts.rolesChanged = true
	if vendor {
		opts.vendor, opts.vendorChanged = true, true
		opts.fullsendSource = "../.."
		opts.fullsendBinary = amd64VendorBinary(t)
	}
	caller, err := scaffold.FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)
	fc.FileContentsRef["fullsend-ai/fullsend/internal/scaffold/fullsend-repo/.github/workflows/prioritize.yml@v1.0.0"] = caller
	for _, path := range []string{"reusable-dispatch.yml", "reusable-prioritize.yml"} {
		data, err := os.ReadFile("../../.github/workflows/" + path)
		require.NoError(t, err)
		fc.FileContentsRef["fullsend-ai/fullsend/.github/workflows/"+path+"@v1.0.0"] = data
	}
	for _, name := range []string{forge.VarOpenAIAudience, forge.VarOpenAIIdentityProviderID, forge.VarOpenAIServiceAccountID} {
		fc.VariableValues["acme/api/"+name] = "test-" + name
	}
	require.NoError(t, runReposInstall(context.Background(), opts))
	for _, secret := range []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, forge.SecretOpenAIAPIKey} {
		require.False(t, fc.Secrets["acme/api/"+secret])
	}
	result, err := statusJSON(t, manifest, fc)
	require.NoError(t, err, "%+v", result.Repos)
	require.Len(t, result.Repos, 1)
	require.Empty(t, result.Repos[0].Error)
	require.Empty(t, result.Repos[0].Drifts)
	env := map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "https://oidc.example/token", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runner-token"}
	for _, name := range []string{forge.VarOpenAIAudience, forge.VarOpenAIIdentityProviderID, forge.VarOpenAIServiceAccountID} {
		env[name] = fc.VariableValues["acme/api/"+name]
	}
	stubOpenAIExchange(t, func(_ context.Context, cfg openaiwif.Config) (*openaiwif.Token, error) {
		require.Equal(t, env[forge.VarOpenAIAudience], cfg.Audience)
		require.Equal(t, env[forge.VarOpenAIIdentityProviderID], cfg.IdentityProviderID)
		require.Equal(t, env[forge.VarOpenAIServiceAccountID], cfg.ServiceAccountID)
		return &openaiwif.Token{Value: "short-lived-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	cred, err := resolveOpenAICredential(context.Background(), openAITestEnv(env), config.OpenAIWIFConfig{})
	require.NoError(t, err)
	require.Equal(t, "wif", cred.source)
	require.Equal(t, "short-lived-token", cred.value)
	// Keep the status check sensitive to the effective installed workflow.
	if vendor {
		fc.FileContents["acme/api/.github/workflows/reusable-prioritize.yml"] = []byte("on: {}\njobs: {}\n")
	} else {
		fc.FileContentsRef["fullsend-ai/fullsend/.github/workflows/reusable-prioritize.yml@v1.0.0"] = []byte("on: {}\njobs: {}\n")
	}
	result, err = statusJSON(t, manifest, fc)
	require.Error(t, err)
	require.NotEmpty(t, result.Repos[0].Drifts)
}

// Opt-in live acceptance: a dedicated repo must already have the OpenAI WIF
// variables, mint enrollment, and an OpenAI-model triage agent (see the guide).
// It must have no GCP/API-key secrets. This test installs callers, checks status,
// opens a smoke issue, and requires a successful real WIF-backed agent run.
func TestOpenAIWIFLiveInstallAndRun(t *testing.T) {
	fullName := os.Getenv("FULLSEND_OPENAI_WIF_LIVE_REPO")
	if fullName == "" {
		t.Skip("set FULLSEND_OPENAI_WIF_LIVE_REPO to enable paid live acceptance")
	}
	parts := strings.Split(fullName, "/")
	require.Len(t, parts, 2)
	token := os.Getenv("GH_TOKEN")
	require.NotEmpty(t, token)
	mintURL, ref := os.Getenv("FULLSEND_OPENAI_WIF_LIVE_MINT_URL"), os.Getenv("FULLSEND_OPENAI_WIF_LIVE_REF")
	require.NotEmpty(t, mintURL)
	require.NotEmpty(t, ref)
	client := githubforge.New(token)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	checkSecrets := func() {
		for _, name := range []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider, forge.SecretOpenAIAPIKey} {
			present, err := client.RepoSecretExists(ctx, parts[0], parts[1], name)
			require.NoError(t, err)
			require.False(t, present, "dedicated OpenAI-only repo must not have %s", name)
		}
	}
	checkSecrets()
	manifest := writeTestManifest(t, fmt.Sprintf("version: 1\ngithub:\n  mint_url: %q\n  fullsend_ref: %q\n  inference:\n    auth: openai-wif\n  repos:\n    - name: %q\n", mintURL, ref, fullName))
	require.NoError(t, runReposInstall(ctx, &reposInstallConfig{manifest: manifest, concurrency: 1, direct: true, rolesChanged: true, roles: config.PerRepoDefaultRoles(), testClient: client}))
	result, err := statusJSON(t, manifest, client)
	require.NoError(t, err, "%+v", result.Repos)
	require.Len(t, result.Repos, 1)
	require.True(t, result.Repos[0].Installed)
	require.Empty(t, result.Repos[0].Error)
	require.Empty(t, result.Repos[0].Drifts)
	checkSecrets()
	started := time.Now()
	issue, err := client.CreateIssue(ctx, parts[0], parts[1], "OpenAI WIF install acceptance smoke", "Smoke test: report whether this repository installs Fullsend successfully. No code changes are required.")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, client.CloseIssue(cleanup, parts[0], parts[1], issue.Number))
	})
	for {
		runs, err := client.ListWorkflowRunsSince(ctx, parts[0], parts[1], "fullsend.yaml", started)
		require.NoError(t, err)
		for _, run := range runs {
			if run.Event != "issues" || run.Status != "completed" {
				continue
			}
			require.Equal(t, "success", run.Conclusion, run.HTMLURL)
			logs, err := client.GetWorkflowRunLogs(ctx, parts[0], parts[1], run.ID)
			require.NoError(t, err)
			require.Contains(t, logs, "OpenAI credential ready", "the run must execute an OpenAI agent, not skip dispatch")
			require.NotContains(t, logs, "OpenAI credential is a static OPENAI_API_KEY")
			t.Logf("live install/status/OpenAI agent evidence: %s", run.HTMLURL)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Second):
		}
	}
}
