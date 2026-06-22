package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Tests in this file mutate package-level globals (githubAPIBaseURL,
// githubHTTPClient) via save/restore in defer. Do NOT use t.Parallel().

func generateTestPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestMintCommand_HasSubcommands(t *testing.T) {
	cmd := newMintCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Use] = true
	}
	assert.True(t, names["deploy"], "expected deploy subcommand")
	assert.True(t, names["enroll <org|owner/repo>"], "expected enroll subcommand")
	assert.True(t, names["unenroll <org|owner/repo>"], "expected unenroll subcommand")
	assert.True(t, names["status [org]"], "expected status subcommand")
	assert.True(t, names["token"], "expected token subcommand")
	assert.True(t, names["add-role <role>"], "expected add-role subcommand")
	assert.True(t, names["remove-role <role>"], "expected remove-role subcommand")
}

func TestMintAddRoleCmd_Flags(t *testing.T) {
	cmd := newMintAddRoleCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("slug"))
	assert.NotNil(t, cmd.Flags().Lookup("pem"))
	assert.NotNil(t, cmd.Flags().Lookup("use-existing-pem-secret"))
}

func TestMintRemoveRoleCmd_Flags(t *testing.T) {
	cmd := newMintRemoveRoleCmd()
	assert.NotNil(t, cmd.Flags().Lookup("project"))
	assert.NotNil(t, cmd.Flags().Lookup("keep-pem"))
}

func TestMintCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["mint"], "expected mint command registered in root")
}

// --- deploy command tests ---

func TestMintDeployCmd_Flags(t *testing.T) {
	cmd := newMintDeployCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")
	assert.Equal(t, "", projectFlag.DefValue)

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	sourceDirFlag := cmd.Flags().Lookup("source-dir")
	require.NotNil(t, sourceDirFlag, "expected --source-dir flag")

	skipDeployFlag := cmd.Flags().Lookup("skip-deploy")
	require.NotNil(t, skipDeployFlag, "expected --skip-deploy flag")

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")
}

func TestMintDeployCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintDeployCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintDeployCmd_InvalidRegion(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--region=invalid"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP region")
}

func TestMintDeployCmd_DryRun(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_NoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "extra"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestMintDeployCmd_PemDirFlag(t *testing.T) {
	cmd := newMintDeployCmd()

	pemDirFlag := cmd.Flags().Lookup("pem-dir")
	require.NotNil(t, pemDirFlag, "expected --pem-dir flag")
	assert.Equal(t, "", pemDirFlag.DefValue)
}

func TestMintDeployCmd_DryRunWithPemDir(t *testing.T) {
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	for _, role := range defaultMintRoles() {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=" + pemDir})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintDeployCmd_DryRunWithBadPemDir(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=/nonexistent"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pem-dir")
}

func TestMintDeployCmd_DryRunWithPemDirAsFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "notadir.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("dummy"), 0o600))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=" + tmpFile})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
}

func TestMintDeployCmd_DryRunWithInvalidPEM(t *testing.T) {
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	for _, role := range defaultMintRoles() {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), []byte("not-a-pem"), 0o600))

	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "deploy", "--project=my-project-id", "--dry-run", "--pem-dir=" + pemDir})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM for role")
}

// --- lookupAppID tests ---

func TestLookupAppID_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps/fullsend-ai-coder", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 12345, "slug": "fullsend-ai-coder", "client_id": "Iv1.abc123"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	appID, err := lookupAppID(context.Background(), "fullsend-ai-coder")
	require.NoError(t, err)
	assert.Equal(t, 12345, appID)
}

func TestLookupAppID_EscapesSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps/my%2Fapp", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 42}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	id, err := lookupAppID(context.Background(), "my/app")
	require.NoError(t, err)
	assert.Equal(t, 42, id)
}

func TestLookupAppID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, err := lookupAppID(context.Background(), "nonexistent-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLookupAppID_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, err := lookupAppID(context.Background(), "some-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestLookupAppID_RateLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"Forbidden", http.StatusForbidden},
		{"TooManyRequests", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()

			orig := githubAPIBaseURL
			githubAPIBaseURL = srv.URL
			defer func() { githubAPIBaseURL = orig }()

			_, err := lookupAppID(context.Background(), "some-app")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "rate limit")
		})
	}
}

// --- verifyPEMMatchesApp tests ---

func TestVerifyPEMMatchesApp_Success(t *testing.T) {
	testPEM := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/app", r.URL.Path)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 12345, "slug": "test-app"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	err := verifyPEMMatchesApp(context.Background(), testPEM, 12345, "test-app")
	require.NoError(t, err)
}

func TestVerifyPEMMatchesApp_WrongKey(t *testing.T) {
	testPEM := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	err := verifyPEMMatchesApp(context.Background(), testPEM, 12345, "test-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestVerifyPEMMatchesApp_AppIDMismatch(t *testing.T) {
	testPEM := generateTestPEM(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999, "slug": "different-app"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	err := verifyPEMMatchesApp(context.Background(), testPEM, 12345, "test-app")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated as app 99999 but expected app 12345")
}

// --- listPEMFiles tests ---

func TestListPEMFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "coder.pem"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "review.pem"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o600))

	files := listPEMFiles(dir)
	assert.Equal(t, []string{"coder.pem", "review.pem"}, files)
}

func TestListPEMFiles_EmptyDir(t *testing.T) {
	files := listPEMFiles(t.TempDir())
	assert.Empty(t, files)
}

func TestListPEMFiles_NonexistentDir(t *testing.T) {
	files := listPEMFiles("/nonexistent/path")
	assert.Nil(t, files)
}

// --- loadAppSetPEMs tests ---

func TestLoadAppSetPEMs_Success(t *testing.T) {
	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)

	pemDir := t.TempDir()
	for _, role := range roles {
		err := os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600)
		require.NoError(t, err)
	}

	appIDCounter := 100
	lastLookedUpID := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintf(w, `{"id": %d, "slug": "test-app"}`, lastLookedUpID)
			return
		}
		appIDCounter++
		lastLookedUpID = appIDCounter
		fmt.Fprintf(w, `{"id": %d, "slug": "%s"}`, appIDCounter, r.URL.Path[len("/apps/"):])
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	agentPEMs, agentAppIDs, err := loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai")
	require.NoError(t, err)
	assert.Len(t, agentPEMs, len(roles))
	assert.Len(t, agentAppIDs, len(roles))

	for _, role := range roles {
		assert.Contains(t, agentPEMs, role, "expected PEM for role %s", role)
		assert.NotEmpty(t, agentPEMs[role])
		assert.Contains(t, agentAppIDs, role, "expected app ID for role %s", role)
		assert.NotEmpty(t, agentAppIDs[role])
	}
}

func TestLoadAppSetPEMs_MissingPEM(t *testing.T) {
	pemDir := t.TempDir()
	// Only write one PEM — the rest will be missing.
	err := os.WriteFile(filepath.Join(pemDir, "fullsend.pem"), []byte("fake"), 0o600)
	require.NoError(t, err)

	_, _, err = loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing PEM file for role")
}

func TestLoadAppSetPEMs_InvalidAppSet(t *testing.T) {
	_, _, err := loadAppSetPEMs(context.Background(), t.TempDir(), "INVALID CHARS")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid app set")
}

func TestLoadAppSetPEMs_InvalidPEM(t *testing.T) {
	pemDir := t.TempDir()
	testPEM := generateTestPEM(t)
	roles := defaultMintRoles()
	for _, role := range roles {
		require.NoError(t, os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600))
	}
	// Overwrite one with invalid content.
	require.NoError(t, os.WriteFile(filepath.Join(pemDir, "coder.pem"), []byte("not-a-pem"), 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app" {
			fmt.Fprintln(w, `{"id": 1, "slug": "test-app"}`)
			return
		}
		fmt.Fprintln(w, `{"id": 999, "slug": "test-app"}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, _, err := loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM for role")
}

func TestLoadAppSetPEMs_BadDir(t *testing.T) {
	_, _, err := loadAppSetPEMs(context.Background(), "/nonexistent/path", "fullsend-ai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pem-dir")
}

func TestLoadAppSetPEMs_FileNotDir(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "notadir.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("dummy"), 0o600))

	_, _, err := loadAppSetPEMs(context.Background(), tmpFile, "fullsend-ai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
}

func TestGitHubHTTPClient_HasTimeout(t *testing.T) {
	assert.Equal(t, 30*time.Second, githubHTTPClient.Timeout)
}

func TestLoadAppSetPEMs_AppNotFound(t *testing.T) {
	roles := defaultMintRoles()
	testPEM := generateTestPEM(t)
	pemDir := t.TempDir()
	for _, role := range roles {
		err := os.WriteFile(filepath.Join(pemDir, role+".pem"), testPEM, 0o600)
		require.NoError(t, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	_, _, err := loadAppSetPEMs(context.Background(), pemDir, "fullsend-ai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up app ID")
	assert.Contains(t, err.Error(), "not found")
}

// --- enroll command tests ---

func TestMintEnrollCmd_Flags(t *testing.T) {
	cmd := newMintEnrollCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
	assert.Equal(t, "us-central1", regionFlag.DefValue)

	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag, "expected --dry-run flag")

	assert.Nil(t, cmd.Flags().Lookup("app-set"))
	assert.Nil(t, cmd.Flags().Lookup("role-app-ids"))
	assert.Nil(t, cmd.Flags().Lookup("roles"))
}

func TestMintEnrollCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintEnrollCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintEnrollCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

// --- unenroll command tests ---

func TestMintUnenrollCmd_Flags(t *testing.T) {
	cmd := newMintUnenrollCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")

	deleteProviderFlag := cmd.Flags().Lookup("delete-provider")
	require.NotNil(t, deleteProviderFlag, "expected --delete-provider flag")
	assert.Equal(t, "false", deleteProviderFlag.DefValue)

	yoloFlag := cmd.Flags().Lookup("yolo")
	require.NotNil(t, yoloFlag, "expected --yolo flag")
}

func TestMintUnenrollCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestMintUnenrollCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

// --- status command tests ---

func TestMintStatusCmd_Flags(t *testing.T) {
	cmd := newMintStatusCmd()

	projectFlag := cmd.Flags().Lookup("project")
	require.NotNil(t, projectFlag, "expected --project flag")

	regionFlag := cmd.Flags().Lookup("region")
	require.NotNil(t, regionFlag, "expected --region flag")
}

func TestMintStatusCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintStatusCmd_InvalidOrg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status", "-org", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestMintStatusCmd_TooManyArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "status", "org1", "org2", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
}

// --- role aliasing tests ---

func TestResolveRole(t *testing.T) {
	assert.Equal(t, "coder", resolveRole("code"))
	assert.Equal(t, "coder", resolveRole("fix"))
	assert.Equal(t, "coder", resolveRole("coder"))
	assert.Equal(t, "triage", resolveRole("triage"))
	assert.Equal(t, "review", resolveRole("review"))
}

func TestDefaultMintRoles(t *testing.T) {
	roles := defaultMintRoles()
	assert.Equal(t, config.DefaultAgentRoles(), roles)
}

func TestRolesFromAppIDs_RoleOnly(t *testing.T) {
	roles := rolesFromAppIDs(map[string]string{
		"coder":         "100",
		"triage":        "200",
		"acme/coder":    "999",
		"widget/triage": "888",
	})
	assert.Equal(t, []string{"coder", "triage"}, roles)
}

func TestParseAllowedOrgs_SkipsPlaceholder(t *testing.T) {
	orgs := parseAllowedOrgs("widget, " + gcf.PlaceholderOrg + ", acme")
	assert.Equal(t, []string{"acme", "widget"}, orgs)
}

func TestPemSecretRoles_DeduplicatesAliases(t *testing.T) {
	roles := pemSecretRoles([]string{"fix", "coder", "triage", "fix"})
	assert.Equal(t, []string{"coder", "triage"}, roles)
}

type fakeEnrollmentVerifier struct {
	revInfo *gcf.ServiceRevisionInfo
	revErr  error
	envVars map[string]string
	envErr  error
}

func (f *fakeEnrollmentVerifier) GetServiceRevisionInfo(context.Context) (*gcf.ServiceRevisionInfo, error) {
	return f.revInfo, f.revErr
}

func (f *fakeEnrollmentVerifier) GetServiceTrafficEnvVars(context.Context) (map[string]string, error) {
	return f.envVars, f.envErr
}

func TestVerifyEnrollment_OrgPresent(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		revInfo: &gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ALLOWED_ORGS": "acme,widget",
			},
		},
	}, "widget", "my-project")
}

func TestVerifyEnrollment_OrgMissing(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		envVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, "widget", "my-project")
	assert.Contains(t, out.String(), "FAILED")
}

func TestVerifyEnrollment_FallsBackToTrafficEnvVars(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		revErr: fmt.Errorf("revision unavailable"),
		envVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, "acme", "my-project")
}

func withMintGCFClient(t *testing.T, client gcf.GCFClient) {
	t.Helper()
	old := mintGCFClientFactory
	mintGCFClientFactory = func(string) gcf.GCFClient { return client }
	t.Cleanup(func() { mintGCFClientFactory = old })
}

func mintDiscoveryClient() gcf.GCFClient {
	return gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
				"ALLOWED_ORGS": "existing-org",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
			"ALLOWED_ORGS": "existing-org",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TrafficPercent:         100,
			TemplateMatchesTraffic: true,
			TrafficEnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
				"ALLOWED_ORGS": "existing-org,acme",
			},
			RecentRevisions: []gcf.RevisionSummary{{
				Name:       "fullsend-mint-00001",
				CreateTime: "2026-06-16T12:00:00Z",
				Active:     true,
			}},
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['existing-org']",
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-app-pem":  true,
			"fullsend-triage-app-pem": true,
		}),
	)
}

func TestRunMintEnrollOrg_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", true)
	require.NoError(t, err)
}

func TestRunMintEnrollOrg_NoRoleAppIDs(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"acme/coder":"100"}`},
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no role app IDs")
}

func TestRunMintEnrollOrg_PlaceholderOrgRejected(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, gcf.PlaceholderOrg, "my-project", "us-central1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "placeholder")
}

func TestRunMintEnrollOrg_Success(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", false)
	require.NoError(t, err)
}

func TestRunMintEnrollRepo_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", true)
	require.NoError(t, err)
}

func TestRunMintEnrollRepo_InvalidFormat(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollRepo(context.Background(), printer, "not-a-repo", "my-project", "us-central1", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestRunMintStatus_Healthy(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "acme")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "coder = 100")
	assert.Contains(t, out.String(), "existing-org")
}

func TestRunMintStatus_NotInstalled(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "not-installed")
}

func TestRunMintStatus_OrgNotEnrolled(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "missing-org")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "not in ALLOWED_ORGS")
}

func TestRunMintStatus_TemplateDivergence(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ROLE_APP_IDS": `{"coder":"100"}`,
				"ALLOWED_ORGS": "acme",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
			"ALLOWED_ORGS": "acme",
		}),
		gcf.WithFakeRevisionInfo(&gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TemplateRevision:       "projects/p/locations/r/services/s/revisions/fullsend-mint-00002",
			TemplateMatchesTraffic: false,
		}),
	)
	withMintGCFClient(t, client)
	out := &strings.Builder{}
	printer := ui.New(out)
	err := runMintStatus(context.Background(), printer, "my-project", "us-central1", "")
	require.NoError(t, err)
	assert.Contains(t, out.String(), "diverges")
}

func TestRunMintEnrollRepo_Success(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintEnrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false)
	require.NoError(t, err)
}

func TestRunMintUnenrollOrg_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", true, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollOrg_Success(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI: "https://mint.example.com",
			EnvVars: map[string]string{
				"ALLOWED_ORGS": "acme,other",
			},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ALLOWED_ORGS": "acme,other",
		}),
		gcf.WithFakeWIFProvider(&gcf.WIFProviderInfo{
			AttributeCondition: "assertion.repository_owner in ['acme', 'other']",
		}),
	)
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollOrg(context.Background(), printer, "acme", "my-project", "us-central1", false, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollRepo_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false, true, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollRepo_Success(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{URI: "https://mint.example.com"}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"PER_REPO_WIF_REPOS": "acme/widget,acme/other",
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", false, true, true, os.Stdin)
	require.NoError(t, err)
}

func TestRunMintUnenrollRepo_DeleteProvider(t *testing.T) {
	client := gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{URI: "https://mint.example.com"}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"PER_REPO_WIF_REPOS": "acme/widget",
		}),
	)
	withMintGCFClient(t, client)
	printer := ui.New(&strings.Builder{})
	err := runMintUnenrollRepo(context.Background(), printer, "acme/widget", "my-project", "us-central1", true, true, true, os.Stdin)
	require.NoError(t, err)
}

func TestMintEnrollCmd_DryRunOrg(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme", "--project=my-project-id", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestMintEnrollCmd_DryRunRepo(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "enroll", "acme/widget", "--project=my-project-id", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestMintUnenrollCmd_DryRunOrg(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "unenroll", "acme", "--project=my-project-id", "--dry-run"})
	require.NoError(t, cmd.Execute())
}

func TestVerifyEnrollment_TrafficRevisionWarning(t *testing.T) {
	out := &strings.Builder{}
	printer := ui.New(out)
	verifyEnrollment(context.Background(), printer, &fakeEnrollmentVerifier{
		revInfo: &gcf.ServiceRevisionInfo{
			TrafficRevisionShort:   "fullsend-mint-00001",
			TemplateMatchesTraffic: false,
		},
		envVars: map[string]string{
			"ALLOWED_ORGS": "acme",
		},
	}, "acme", "my-project")
	assert.Contains(t, out.String(), "may not be serving")
}

// --- confirmUnenroll tests ---

func TestConfirmUnenroll_Match(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("acme-org\n"))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.NoError(t, err)
}

func TestConfirmUnenroll_Mismatch(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("wrong-name\n"))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirmation did not match")
}

func TestConfirmUnenroll_EOF(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader(""))
	err := confirmUnenroll(printer, "acme-org", reader, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading confirmation")
}

func TestConfirmUnenroll_NonTerminal(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("acme-org\n"))
	err := confirmUnenroll(printer, "acme-org", reader, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

// --- mint add-role / remove-role tests ---

func TestValidateMintSetupRole(t *testing.T) {
	t.Parallel()
	role, err := validateMintSetupRole("coder")
	require.NoError(t, err)
	assert.Equal(t, "coder", role)

	role, err = validateMintSetupRole("e2e")
	require.NoError(t, err)
	assert.Equal(t, "e2e", role)

	_, err = validateMintSetupRole("fix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coder")
	assert.NotContains(t, err.Error(), "add role")

	_, err = validateMintSetupRole("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported role")
}

func TestValidateAppSlug(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateAppSlug("fullsend-ai-review"))
	require.NoError(t, validateAppSlug("my-app"))
	err := validateAppSlug("Bad_Slug")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid app slug")
}

func TestParseMintAddRoleMode(t *testing.T) {
	t.Parallel()
	mode, err := parseMintAddRoleMode("my-app", "/tmp/pem", "", false)
	require.NoError(t, err)
	assert.Equal(t, addRoleModeSlugPEM, mode)

	mode, err = parseMintAddRoleMode("my-app", "", "", true)
	require.NoError(t, err)
	assert.Equal(t, addRoleModeExistingSecret, mode)

	mode, err = parseMintAddRoleMode("", "", "acme", false)
	require.NoError(t, err)
	assert.Equal(t, addRoleModeBrowser, mode)

	_, err = parseMintAddRoleMode("my-app", "/tmp/pem", "", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")

	_, err = parseMintAddRoleMode("my-app", "", "acme", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")

	_, err = parseMintAddRoleMode("", "", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specify one input mode")
}

func TestMintSetupAddRoleCmd_RequiresProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "add-role", "coder", "--slug=app", "--pem=/tmp/x.pem"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--project is required")
}

func TestMintSetupAddRoleCmd_PemAndUseExistingMutuallyExclusive(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--slug=fullsend-ai-coder",
		"--pem=/tmp/coder.pem",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestMintSetupAddRoleCmd_NoInputMode(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "add-role", "coder", "--project=my-project-id"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "specify one input mode")
}

func TestMintSetupAddRoleCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=BAD",
		"--slug=app",
		"--pem=/tmp/x.pem",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintSetupAddRoleCmd_InvalidRegion(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--region=invalid",
		"--slug=app",
		"--pem=/tmp/x.pem",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP region")
}

func TestMintSetupRemoveRoleCmd_InvalidProject(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"mint", "remove-role", "coder", "--project=BAD"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid GCP project ID")
}

func TestMintSetupAddRoleCmd_ForceOverwrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-coder-app-pem": true,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--slug=fullsend-ai-coder",
		"--use-existing-pem-secret",
		"--force",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintSetupAddRoleCmd_ExistingSecretDryRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": true,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--use-existing-pem-secret",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintSetupAddRoleCmd_AlreadyRegistered(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "coder",
		"--project=my-project-id",
		"--slug=fullsend-ai-coder",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestMintSetupRemoveRoleCmd_DryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "coder",
		"--project=my-project-id",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintSetupRemoveRoleCmd_NotRegistered(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "review",
		"--project=my-project-id",
		"--dry-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

func TestMintAddRoleCmd_BrowserDryRun(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--org=acme-corp",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintTrafficRoleAppIDs_PrefersTrafficRevision(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","review":"200"}`,
		}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	discovery := &gcf.MintDiscovery{
		URL:        "https://mint.example.com",
		RoleAppIDs: map[string]string{"coder": "100"},
	}
	roles, err := mintTrafficRoleAppIDs(context.Background(), nil, provisioner, discovery)
	require.NoError(t, err)
	assert.Equal(t, "200", roles["review"])
}

func TestConfirmUnenroll_CustomAbortLabel(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	reader := bufio.NewReader(strings.NewReader("wrong\n"))
	err := confirmUnenroll(printer, "retro", reader, true, "remove-role")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborting remove-role")
}

func TestMintAddRoleCmd_ExistingSecretRegisters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apps/fullsend-ai-review", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": true,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintAddRoleCmd_SlugPEMRegisters(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/fullsend-ai-review":
			fmt.Fprintln(w, `{"id": 88888}`)
		case "/app":
			fmt.Fprintln(w, `{"id": 88888}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeErrors(map[string]error{"GetSecret": gcf.ErrSecretNotFound}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--pem=" + pemPath,
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintRemoveRoleCmd_YoloSuccess(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "triage",
		"--project=my-project-id",
		"--yolo",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestMintTrafficRoleAppIDs_InvalidJSON(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `not-json`,
		}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	_, err := mintTrafficRoleAppIDs(context.Background(), nil, provisioner, &gcf.MintDiscovery{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing traffic ROLE_APP_IDS")
}

func TestMintTrafficRoleAppIDs_FallbackWhenTrafficEmpty(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeTrafficEnvVars(map[string]string{}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	discovery := &gcf.MintDiscovery{RoleAppIDs: map[string]string{"coder": "100"}}
	roles, err := mintTrafficRoleAppIDs(context.Background(), nil, provisioner, discovery)
	require.NoError(t, err)
	assert.Equal(t, "100", roles["coder"])
}

func TestMintAddRoleCmd_ExistingSecretMissingPEM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": false,
		}),
	))

	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--slug=fullsend-ai-review",
		"--use-existing-pem-secret",
	})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestMintRemoveRoleCmd_KeepPEMDryRun(t *testing.T) {
	withMintGCFClient(t, mintDiscoveryClient())
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "remove-role", "coder",
		"--project=my-project-id",
		"--keep-pem",
		"--dry-run",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestResolveAddRoleFromSlugPEM_InvalidPEM(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	pemPath := filepath.Join(t.TempDir(), "bad.pem")
	require.NoError(t, os.WriteFile(pemPath, []byte("not-a-pem"), 0o600))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "fullsend-ai-review",
		pemPath: pemPath,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PEM")
}

func TestResolveAddRoleFromBrowser_InvalidOrg(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "-invalid-",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "organization name")
}

func TestResolveAddRoleFromSlugPEM_MissingFile(t *testing.T) {
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "fullsend-ai-review",
		pemPath: filepath.Join(t.TempDir(), "missing.pem"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading PEM file")
}

func TestMintTrafficRoleAppIDs_FallbackOnTrafficError(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetServiceTrafficEnvVars": fmt.Errorf("unavailable"),
		}),
	))
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "my-project-id", Region: "us-central1"}, mintGCFClientFactory("my-project-id"))
	discovery := &gcf.MintDiscovery{RoleAppIDs: map[string]string{"coder": "100"}}
	out := &strings.Builder{}
	printer := ui.New(out)
	roles, err := mintTrafficRoleAppIDs(context.Background(), printer, provisioner, discovery)
	require.NoError(t, err)
	assert.Equal(t, "100", roles["coder"])
	assert.Contains(t, out.String(), "traffic-serving env vars")
}

func withMintAddRoleHooks(t *testing.T, resolveToken func() (string, error), appSetup func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error)) {
	t.Helper()
	oldToken := mintAddRoleResolveToken
	oldSetup := mintAddRoleAppSetup
	if resolveToken != nil {
		mintAddRoleResolveToken = resolveToken
	}
	if appSetup != nil {
		mintAddRoleAppSetup = appSetup
	}
	t.Cleanup(func() {
		mintAddRoleResolveToken = oldToken
		mintAddRoleAppSetup = oldSetup
	})
}

func TestResolveAddRoleFromBrowser_NoToken(t *testing.T) {
	withMintAddRoleHooks(t, func() (string, error) {
		return "", fmt.Errorf("no GitHub token found")
	}, nil)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "acme-corp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no GitHub token")
}

func TestResolveAddRoleFromBrowser_Success(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(_ context.Context, _ forge.Client, _ *ui.Printer, org string, roles []string, _ string, _ string, _ bool, _ map[string]string, _ string, _ map[string]string) ([]layers.AgentCredentials, error) {
			assert.Equal(t, "acme-corp", org)
			assert.Equal(t, []string{"review"}, roles)
			return []layers.AgentCredentials{{AgentEntry: config.AgentEntry{Slug: "fullsend-ai-review"}, AppID: 424242}}, nil
		},
	)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	appID, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "Acme-Corp",
	})
	require.NoError(t, err)
	assert.Equal(t, 424242, appID)
}

func TestResolveAddRoleFromBrowser_AppSetupFails(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error) {
			return nil, fmt.Errorf("manifest flow failed")
		},
	)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "acme-corp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest flow failed")
}

func TestResolveAddRoleFromBrowser_WrongCredCount(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error) {
			return []layers.AgentCredentials{{AppID: 1}, {AppID: 2}}, nil
		},
	)
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromBrowser(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		org:  "acme-corp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected one app credential")
}

func TestMintAddRoleCmd_BrowserRegisters(t *testing.T) {
	withMintAddRoleHooks(t,
		func() (string, error) { return "test-token", nil },
		func(context.Context, forge.Client, *ui.Printer, string, []string, string, string, bool, map[string]string, string, map[string]string) ([]layers.AgentCredentials, error) {
			return []layers.AgentCredentials{{AgentEntry: config.AgentEntry{Slug: "fullsend-ai-review"}, AppID: 55555}}, nil
		},
	)
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
	))
	cmd := newRootCmd()
	cmd.SetArgs([]string{
		"mint", "add-role", "review",
		"--project=my-project-id",
		"--org=acme-corp",
	})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestRunMintSetupAddRole_DiscoveryFails(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient())
	printer := ui.New(&strings.Builder{})
	err := runMintSetupAddRole(context.Background(), printer, mintSetupAddRoleConfig{
		role:    "review",
		project: "my-project-id",
		region:  "us-central1",
		slug:    "fullsend-ai-review",
		pemPath: "/tmp/missing.pem",
		mode:    addRoleModeSlugPEM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mint not found")
}

func TestRunMintSetupAddRole_AddRoleFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": true,
		}),
		gcf.WithFakeErrors(map[string]error{
			"UpdateServiceEnvVars": fmt.Errorf("permission denied"),
		}),
	))

	printer := ui.New(&strings.Builder{})
	err := runMintSetupAddRole(context.Background(), printer, mintSetupAddRoleConfig{
		role:                 "review",
		project:              "my-project-id",
		region:               "us-central1",
		slug:                 "fullsend-ai-review",
		mode:                 addRoleModeExistingSecret,
		useExistingPEMSecret: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registering role on mint")
	assert.NotContains(t, err.Error(), "use-existing-pem-secret")
}

func TestRunMintSetupAddRole_AddRoleFailsAfterPEMStored(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/fullsend-ai-review":
			fmt.Fprintln(w, `{"id": 88888}`)
		case "/app":
			fmt.Fprintln(w, `{"id": 88888}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100"}`,
		}),
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": false,
		}),
		gcf.WithFakeErrors(map[string]error{
			"UpdateServiceEnvVars": fmt.Errorf("permission denied"),
		}),
	))

	printer := ui.New(&strings.Builder{})
	err := runMintSetupAddRole(context.Background(), printer, mintSetupAddRoleConfig{
		role:    "review",
		project: "my-project-id",
		region:  "us-central1",
		slug:    "fullsend-ai-review",
		pemPath: pemPath,
		mode:    addRoleModeSlugPEM,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registering role on mint")
	assert.Contains(t, err.Error(), "use-existing-pem-secret")
	assert.Contains(t, err.Error(), "gcloud secrets delete")
}

func TestRunMintSetupRemoveRole_RemoveFails(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
		}),
		gcf.WithFakeErrors(map[string]error{
			"UpdateServiceEnvVars": fmt.Errorf("permission denied"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintSetupRemoveRole(context.Background(), printer, "triage", "my-project-id", "us-central1", false, false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removing role from mint")
}

func TestRunMintSetupRemoveRole_DeletePEMFails(t *testing.T) {
	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{
			URI:     "https://mint.example.com",
			EnvVars: map[string]string{"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`},
		}),
		gcf.WithFakeTrafficEnvVars(map[string]string{
			"ROLE_APP_IDS": `{"coder":"100","triage":"200"}`,
		}),
		gcf.WithFakeErrors(map[string]error{
			"DeleteSecret": fmt.Errorf("permission denied"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	err := runMintSetupRemoveRole(context.Background(), printer, "triage", "my-project-id", "us-central1", false, false, true, os.Stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting PEM secret")
	assert.Contains(t, err.Error(), "gcloud secrets delete")
}

func TestResolveAddRoleFromSlugPEM_LookupFails(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, gcf.NewFakeGCFClient())
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "missing-app",
		pemPath: pemPath,
	})
	require.Error(t, err)
}

func TestResolveAddRoleFromSlugPEM_StoreFails(t *testing.T) {
	testPEM := generateTestPEM(t)
	pemPath := filepath.Join(t.TempDir(), "review.pem")
	require.NoError(t, os.WriteFile(pemPath, testPEM, 0o600))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/fullsend-ai-review":
			fmt.Fprintln(w, `{"id": 88888}`)
		case "/app":
			fmt.Fprintln(w, `{"id": 88888}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeSecrets(map[string]bool{
			"fullsend-review-app-pem": false,
		}),
		gcf.WithFakeErrors(map[string]error{
			"CreateSecret": fmt.Errorf("permission denied"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, mintGCFClientFactory("p"))
	_, err := resolveAddRoleFromSlugPEM(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role:    "review",
		slug:    "fullsend-ai-review",
		pemPath: pemPath,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storing PEM")
}

func TestResolveAddRoleFromExistingSecret_CheckFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id": 99999}`)
	}))
	defer srv.Close()

	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()

	withMintGCFClient(t, gcf.NewFakeGCFClient(
		gcf.WithFakeErrors(map[string]error{
			"GetSecret": fmt.Errorf("api unavailable"),
		}),
	))
	printer := ui.New(&strings.Builder{})
	provisioner := gcf.NewProvisioner(gcf.Config{ProjectID: "p"}, mintGCFClientFactory("p"))
	_, err := resolveAddRoleFromExistingSecret(context.Background(), printer, provisioner, mintSetupAddRoleConfig{
		role: "review",
		slug: "fullsend-ai-review",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking PEM secret")
}
