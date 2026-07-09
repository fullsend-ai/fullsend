//go:build e2e || behaviour

package admin

import (
	"os"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/cli"
	"github.com/stretchr/testify/assert"
)

func TestMintEnrollProjectID(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")
	cfg := EnvConfig{
		MintURL:      cli.DefaultMintURL,
		GCPProjectID: "inference-only-project",
	}
	assert.Equal(t, DefaultHostedMintGCPProject, MintEnrollProjectID(cfg))

	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "override-mint-project")
	assert.Equal(t, "override-mint-project", MintEnrollProjectID(cfg))

	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")
	cfg.MintURL = "https://mint.example.com"
	assert.Equal(t, "inference-only-project", MintEnrollProjectID(cfg))
}

func TestMintEnrollProjectID_EmptyWithoutHostedMint(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "")
	cfg := EnvConfig{
		MintURL:      "https://mint.example.com",
		GCPProjectID: "",
	}
	assert.Empty(t, MintEnrollProjectID(cfg))
}

func TestMintEnrollProjectID_RespectsEnvOverride(t *testing.T) {
	t.Setenv("E2E_GCP_MINT_PROJECT_ID", "from-env")
	cfg := EnvConfig{MintURL: cli.DefaultMintURL}
	assert.Equal(t, "from-env", MintEnrollProjectID(cfg))
	_ = os.Unsetenv("E2E_GCP_MINT_PROJECT_ID")
}
