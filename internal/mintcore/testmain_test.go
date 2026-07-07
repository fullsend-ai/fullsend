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
		"ROLE_APP_IDS":           `{"triage":"100","coder":"200","review":"300","fullsend":"500"}`,
		"ALLOWED_WORKFLOW_FILES": "*",
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	os.Exit(m.Run())
}
