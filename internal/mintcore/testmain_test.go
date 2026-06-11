package mintcore

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	defaults := map[string]string{
		"ALLOWED_ORGS":           "test-org",
		"GCP_PROJECT_NUMBER":     "123456",
		"OIDC_AUDIENCE":          "fullsend-mint",
		"ROLE_APP_IDS":           `{"test-org/triage":"100","test-org/coder":"200","test-org/review":"300","test-org/fix":"400","test-org/fullsend":"500"}`,
		"ALLOWED_WORKFLOW_FILES": "*",
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	os.Exit(m.Run())
}
