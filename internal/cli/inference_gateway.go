package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// gatewayProbeTimeout bounds the one authenticated gateway request.
	gatewayProbeTimeout = 30 * time.Second
	// gatewayProbeBodyLimit bounds how much of the gateway's reply is read
	// (and discarded); only the status code is reported.
	gatewayProbeBodyLimit = 64 << 10
	// gatewayProbePath is the endpoint the probe calls. The body is a
	// deliberately empty request, so a gateway that accepts the assertion
	// answers with a client error rather than running inference.
	gatewayProbePath = "/v1/chat/completions"
	gatewayProbeBody = `{"model":"fullsend-status-probe","messages":[],"max_tokens":1}`
)

// gatewayStatusDeps holds the environment and network access of
// 'inference gateway status', injectable for tests.
type gatewayStatusDeps struct {
	getenv         func(string) string
	fetchAssertion func(context.Context, openaiwif.AssertionConfig) (*openaiwif.Assertion, error)
	httpClient     *http.Client
	now            func() time.Time
}

func defaultGatewayStatusDeps() gatewayStatusDeps {
	return gatewayStatusDeps{
		getenv:         os.Getenv,
		fetchAssertion: openaiwif.FetchAssertion,
		httpClient: &http.Client{
			Timeout: gatewayProbeTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

func newInferenceGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Inspect the inference gateway configuration",
		Long: `Commands for the self-hosted inference gateway (inference.gateway,
ADR 0137): an OpenAI/Anthropic-compatible gateway that validates the
job's GitHub OIDC token directly. Configure it with
'fullsend github setup --inference-gateway-*'.`,
	}
	cmd.AddCommand(newInferenceGatewayStatusCmd())
	return cmd
}

func newInferenceGatewayStatusCmd() *cobra.Command {
	var fullsendDir string

	cmd := &cobra.Command{
		Use:   "status <owner/repo>",
		Short: "Check inference gateway configuration and authentication",
		Long: `Prints the resolved inference.gateway block (url, audience and model
list) and the config layer each value comes from (config.yaml or
config.base.yaml), and flags a partial block.

When run inside a GitHub Actions job with id-token: write, fetches one
OIDC assertion for the configured audience, reports its expiry and
lifetime, and sends one authenticated request to the gateway, reporting
only the HTTP status. The assertion is never printed.
Outside Actions, says so and stops at the config checks.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, repo, err := parseOrgOrRepo(args[0])
			if err != nil {
				return err
			}
			if repo == "" {
				return fmt.Errorf("expected owner/repo format, got org-only %q", args[0])
			}
			printer := ui.New(cmd.OutOrStdout())
			return runInferenceGatewayStatus(cmd.Context(), printer, repo, fullsendDir, defaultGatewayStatusDeps())
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".fullsend", "path to the .fullsend configuration directory")

	return cmd
}

// gatewayStatusSources is the effective inference.gateway block with the
// layer each part was resolved from.
type gatewayStatusSources struct {
	Block          config.InferenceGatewayConfig
	URLSource      string
	AudienceSource string
	ModelsSource   string
}

// resolveGatewayStatusSources loads the layered config exactly as a run
// does (config.yaml over config.base.yaml) and attributes each field to
// the overlay when the overlay sets it, otherwise to the base. url and
// audience layer independently; the model list is one unit.
func resolveGatewayStatusSources(fullsendDir string) (gatewayStatusSources, error) {
	var s gatewayStatusSources
	reader, err := config.LoadConfig(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return s, fmt.Errorf("loading config from %s: %w", fullsendDir, err)
	}
	perRepo, ok := reader.(config.PerRepoConfigReader)
	if !ok {
		return s, fmt.Errorf("%s did not load as a per-repo config; inference.gateway is per-repo", fullsendDir)
	}
	s.Block = perRepo.ConfigInferenceGateway().Trimmed()

	var overlay config.InferenceGatewayConfig
	data, err := os.ReadFile(filepath.Join(fullsendDir, "config.yaml"))
	switch {
	case err == nil:
		parsed, perr := config.ParsePerRepoConfig(data)
		if perr != nil {
			return s, fmt.Errorf("parsing %s: %w", filepath.Join(fullsendDir, "config.yaml"), perr)
		}
		overlay = parsed.ConfigInferenceGateway().Trimmed()
	case errors.Is(err, os.ErrNotExist):
	default:
		return s, fmt.Errorf("reading %s: %w", filepath.Join(fullsendDir, "config.yaml"), err)
	}

	source := func(overlaySet, effectiveSet bool) string {
		switch {
		case overlaySet:
			return "config.yaml"
		case effectiveSet:
			return "config.base.yaml"
		}
		return ""
	}
	s.URLSource = source(overlay.URL != "", s.Block.URL != "")
	s.AudienceSource = source(overlay.Audience != "", s.Block.Audience != "")
	s.ModelsSource = source(overlay.HasModelList(), s.Block.HasModelList())
	return s, nil
}

func runInferenceGatewayStatus(ctx context.Context, printer *ui.Printer, repo, fullsendDir string, deps gatewayStatusDeps) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("Inference Gateway Status: " + repo)
	printer.Blank()

	sources, err := resolveGatewayStatusSources(fullsendDir)
	if err != nil {
		printer.StepFail("Could not read the configuration")
		return err
	}
	g := sources.Block

	printOpenAIStatusField(printer, "url", g.URL, sources.URLSource)
	printOpenAIStatusField(printer, "audience", g.Audience, sources.AudienceSource)
	switch {
	case g.ModelsFile != "":
		printOpenAIStatusField(printer, "models_file", g.ModelsFile, sources.ModelsSource)
	case len(g.Models) > 0:
		printOpenAIStatusField(printer, "models", strings.Join(g.ModelIDs(), ", "), sources.ModelsSource)
	default:
		printer.StepInfo("models: (not set; pi gateway/ models need a model list)")
	}
	printer.Blank()

	if g.IsZero() {
		printer.StepFail("No inference.gateway block configured")
		printer.StepInfo("Configure it with 'fullsend github setup --inference-gateway-url <url> --inference-gateway-audience <aud>'")
		return fmt.Errorf("no inference.gateway block configured for %s", repo)
	}
	if missing := g.Missing(); len(missing) > 0 {
		printer.StepWarn("Partial inference.gateway block: missing " + strings.Join(missing, ", "))
		printer.StepInfo("url and audience must both be set; a run refuses a partial block")
		return fmt.Errorf("inference.gateway is partially configured: missing %s", strings.Join(missing, ", "))
	}
	if err := g.Validate(); err != nil {
		printer.StepFail("Invalid inference.gateway block")
		return err
	}
	printer.StepDone("url and audience are set")
	printer.Blank()

	oidcURL := deps.getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	oidcToken := deps.getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if oidcURL == "" || oidcToken == "" {
		printer.StepInfo("Not inside a GitHub Actions job with id-token: write")
		printer.StepInfo("The assertion and gateway request can only be tested from a GitHub Actions workflow")
		return nil
	}

	// The assertion proves the job it runs in, so a request made from
	// another repository's job says nothing about repo's access.
	current := strings.TrimSpace(deps.getenv("GITHUB_REPOSITORY"))
	switch {
	case current == "":
		printer.StepWarn("GITHUB_REPOSITORY is not set, so the gateway check could not be attributed to " + repo)
		printer.StepInfo("Run the command from a GitHub Actions job in " + repo)
		return nil
	case !strings.EqualFold(current, repo):
		printer.StepWarn(fmt.Sprintf("This job runs in %s, so it cannot test %s: the gateway would see %s's identity", current, repo, current))
		printer.StepInfo("Run the command from a job in " + repo + " to test its access")
		return nil
	}

	printer.StepStart("Fetching OIDC assertion for audience " + g.Audience)
	assertion, err := deps.fetchAssertion(ctx, openaiwif.AssertionConfig{
		Audience:         g.Audience,
		OIDCRequestURL:   oidcURL,
		OIDCRequestToken: oidcToken,
	})
	if err != nil {
		printer.StepFail("Assertion fetch failed")
		return fmt.Errorf("fetching OIDC assertion: %w", err)
	}
	printer.StepDone("Assertion fetched")
	printer.KeyValue("exp", assertion.ExpiresAt.UTC().Format(time.RFC3339))
	printer.KeyValue("lifetime", assertion.Lifetime().Round(time.Second).String())
	printer.KeyValue("expires_in", assertion.ExpiresAt.Sub(deps.now()).Round(time.Second).String())
	printer.Blank()

	endpoint := strings.TrimRight(g.URL, "/") + gatewayProbePath
	printer.StepStart("Sending one authenticated request to " + endpoint)
	status, err := probeGateway(ctx, deps.httpClient, endpoint, assertion.Value)
	if err != nil {
		printer.StepFail("Gateway request failed")
		return fmt.Errorf("gateway request: %w", err)
	}
	printer.KeyValue("http_status", fmt.Sprintf("%d", status))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		printer.StepFail(fmt.Sprintf("Gateway refused the assertion (HTTP %d)", status))
		printer.StepInfo("Check that the gateway trusts the GitHub OIDC issuer, expects audience " + g.Audience + ", and admits " + repo)
		return fmt.Errorf("gateway refused the assertion for %s: HTTP %d", repo, status)
	case status >= 300 && status < 400:
		printer.StepWarn(fmt.Sprintf("Gateway answered with a redirect (HTTP %d); redirects are not followed", status))
		return fmt.Errorf("gateway answered with a redirect: HTTP %d", status)
	case status >= 500:
		printer.StepWarn(fmt.Sprintf("Gateway error (HTTP %d)", status))
		return fmt.Errorf("gateway error: HTTP %d", status)
	}
	printer.StepDone(fmt.Sprintf("Gateway accepted the assertion for %s (HTTP %d)", repo, status))
	return nil
}

// probeGateway POSTs the probe body to endpoint with the assertion as a
// bearer token and returns the HTTP status. Errors never include the
// assertion; at most gatewayProbeBodyLimit bytes of the reply are read.
func probeGateway(ctx context.Context, client *http.Client, endpoint, assertion string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(gatewayProbeBody))
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, gatewayProbeBodyLimit))
	return resp.StatusCode, nil
}
