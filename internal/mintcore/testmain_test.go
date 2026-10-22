package mintcore

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	defaults := map[string]string{
		"GCP_PROJECT_NUMBER":     "123456",
		"ROLE_APP_IDS":           `{"triage":"100","coder":"200","review":"300","fullsend":"500","retro":"600","prioritize":"700"}`,
		"ALLOWED_WORKFLOW_FILES": "*",
		// The default test token's repository is enrolled per-repo so
		// callers are authorized unless a test overrides enrollment.
		"PER_REPO_WIF_REPOS": "test-org/test-repo",
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
	os.Exit(m.Run())
}
