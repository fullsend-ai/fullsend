package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/devmint"
)

func newMintRunCmd() *cobra.Command {
	var dataDir string
	var port int
	var bind string
	var tunnel bool
	var insecureNoAuth bool
	var oidcAudience string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a standalone token mint (no GCP required)",
		Long: `Start a local HTTP server that implements the mint token API and creates
real GitHub App installation tokens — no GCP infrastructure required.

The standalone mint replaces Secret Manager (stores PEMs on disk), Workload
Identity Federation (verifies OIDC JWTs via JWKS), and Cloud Functions (runs
as a plain HTTP server). It is the full mint, minus the cloud.

Workflow:
  1. Start the mint:     fullsend mint run --data-dir ~/.fullsend-mint
  2. Install fullsend:   fullsend admin install --mint-url http://localhost:8321
     (the installer writes PEM files to the mint's data-dir/pems/ directory)
  3. Set FULLSEND_MINT_URL to the mint URL in your GitHub org/repo variables

PEMs are loaded once at startup from {data-dir}/pems/<role>.pem.
Restart the mint to pick up new or changed PEM files.

Use --tunnel to start a cloudflared quick tunnel so GitHub Actions runners can
reach the mint on your laptop.

Use --insecure-no-auth to disable OIDC verification (for local testing only).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				dataDir = os.Getenv("FULLSEND_MINT_RUN_DATA_DIR")
			}
			if dataDir == "" {
				return fmt.Errorf("--data-dir is required (or set FULLSEND_MINT_RUN_DATA_DIR)")
			}

			if insecureNoAuth && tunnel {
				return fmt.Errorf("--insecure-no-auth and --tunnel cannot be used together: " +
					"this would expose unauthenticated token minting over a public URL")
			}

			logger := log.New(os.Stderr, "", log.LstdFlags)

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if tunnel {
				tunnelURL, cleanup, err := devmint.StartTunnel(ctx, port, logger)
				if err != nil {
					return fmt.Errorf("starting tunnel: %w", err)
				}
				defer cleanup()
				logger.Printf("Tunnel URL: %s", tunnelURL)
				logger.Printf("Set FULLSEND_MINT_URL=%s in your GitHub org/repo variables", tunnelURL)
			}

			srv := devmint.New(devmint.Options{
				DataDir:        dataDir,
				Bind:           bind,
				Port:           port,
				Logger:         logger,
				InsecureNoAuth: insecureNoAuth,
				OIDCAudience:   oidcAudience,
			})
			return srv.Start(ctx)
		},
	}

	cmd.Flags().StringVar(&dataDir, "data-dir", "", "directory for PEM and config persistence (or FULLSEND_MINT_RUN_DATA_DIR)")
	cmd.Flags().IntVar(&port, "port", 8321, "port to listen on")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "address to bind to")
	cmd.Flags().BoolVar(&tunnel, "tunnel", false, "start a cloudflared tunnel for remote access")
	cmd.Flags().BoolVar(&insecureNoAuth, "insecure-no-auth", false, "disable OIDC verification (local testing only)")
	cmd.Flags().StringVar(&oidcAudience, "oidc-audience", "fullsend-mint", "expected OIDC audience claim")

	return cmd
}
