package function

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	defaults := map[string]string{
		"ALLOWED_ORGS":           "test-org",
		"GCP_PROJECT_NUMBER":     "123456",
		"WIF_POOL_NAME":          "test-pool",
		"WIF_PROVIDER_NAME":      "github-oidc",
		"OIDC_AUDIENCE":          "fullsend-mint",
		"ROLE_APP_IDS":           `{"test-org/triage":"100","test-org/code":"200","test-org/review":"300","test-org/fix":"400","test-org/fullsend":"500"}`,
		"ALLOWED_WORKFLOW_FILES": "*",
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	os.Exit(m.Run())
}

func setMintEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ALLOWED_ORGS", "test-org")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("WIF_POOL_NAME", "test-pool")
	t.Setenv("WIF_PROVIDER_NAME", "github-oidc")
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("ROLE_APP_IDS", `{"test-org/triage":"100","test-org/code":"200","test-org/review":"300","test-org/fix":"400","test-org/fullsend":"500"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
}
