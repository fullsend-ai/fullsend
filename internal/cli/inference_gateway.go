package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// gatewayProbeTimeout bounds the one authenticated gateway request.
	gatewayProbeTimeout = 30 * time.Second
	// gatewayProbeBodyLimit bounds how much of the gateway's model list is
	// read; a larger reply cannot confirm authentication.
	gatewayProbeBodyLimit = 1 << 20
	// gatewayProbePath is the endpoint the probe calls: the OpenAI-style
	// model list, which a gateway answers per caller without running
	// inference.
	gatewayProbePath = "/v1/models"
	// gatewayProbeMaxListed caps how many authorised model ids are printed.
	gatewayProbeMaxListed = 20
)

// gatewayStatusDeps holds the environment and network access of
// 'inference gateway status', injectable for tests.
type gatewayStatusDeps struct {
	getenv         func(string) string
	fetchAssertion func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error)
	httpClient     *http.Client
	now            func() time.Time
}

// defaultGatewayStatusDeps builds the probe's HTTP client for a
// config-derived URL (go-code.md "Secure HTTP clients"): no environment
// proxy, every connection resolved and checked against internal and
// reserved addresses (repos.SafeDialContext dials a validated IP and the
// request keeps the host name, so SNI and certificate checks still apply),
// a timeout, and no redirects. probeGateway refuses a non-https URL before
// the request.
func defaultGatewayStatusDeps() gatewayStatusDeps {
	return gatewayStatusDeps{
		getenv:         os.Getenv,
		fetchAssertion: actionsoidc.FetchAssertion,
		httpClient: &http.Client{
			Timeout: gatewayProbeTimeout,
			Transport: &http.Transport{
				Proxy:       nil,
				DialContext: repos.SafeDialContext(&net.Dialer{Timeout: 10 * time.Second}, false),
			},
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
lifetime, and lists the gateway's models with it (GET /v1/models),
reporting the HTTP status and the model ids the gateway authorises for
the repository. The assertion is never printed.
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

	printStatusField(printer, "url", g.URL, sources.URLSource)
	printStatusField(printer, "audience", g.Audience, sources.AudienceSource)
	switch {
	case g.ModelsFile != "":
		printStatusField(printer, "models_file", g.ModelsFile, sources.ModelsSource)
	case len(g.Models) > 0:
		printStatusField(printer, "models", strings.Join(g.ModelIDs(), ", "), sources.ModelsSource)
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
	assertion, err := deps.fetchAssertion(ctx, actionsoidc.AssertionConfig{
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
	status, body, err := probeGateway(ctx, deps.httpClient, endpoint, assertion.Value)
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
	}
	ids, ok := gatewayModelIDs(status, body)
	switch {
	case !ok:
		printer.StepFail(fmt.Sprintf("Could not confirm authentication (HTTP %d)", status))
		printer.StepInfo("Expected a 2xx OpenAI-style model list from " + endpoint)
		return fmt.Errorf("could not confirm authentication with the gateway (HTTP %d)", status)
	case len(ids) == 0:
		printer.StepFail("Gateway authenticated the assertion, but no models are authorised for " + repo)
		printer.StepInfo("Grant " + repo + " access to at least one model on the gateway")
		return fmt.Errorf("gateway authenticated the assertion but authorises no models for %s", repo)
	}
	printer.StepDone(fmt.Sprintf("Gateway accepted the assertion; %d model(s) authorised for %s", len(ids), repo))
	shown := ids
	if len(shown) > gatewayProbeMaxListed {
		shown = shown[:gatewayProbeMaxListed]
	}
	for _, id := range shown {
		printer.StepInfo(displayModelID(id, assertion.Value))
	}
	if more := len(ids) - len(shown); more > 0 {
		printer.StepInfo(fmt.Sprintf("(and %d more)", more))
	}
	return nil
}

// gatewayJWTPattern matches a JWT-shaped value (three base64url segments,
// the first starting with "eyJ").
var gatewayJWTPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// displayModelID makes a gateway-supplied model id safe to print: the
// gateway is configured, not trusted to keep the assertion out of the
// output, so the assertion and any JWT-shaped value are redacted and
// control characters (terminal escapes included) are replaced. Every "::"
// becomes ": :", so the id cannot form a GitHub Actions workflow command
// marker (`::add-mask::`, `::error::`) in a job log either; the loop
// splits runs of three or more colons too.
func displayModelID(id, assertion string) string {
	if assertion != "" {
		id = strings.ReplaceAll(id, assertion, "<redacted>")
	}
	id = gatewayJWTPattern.ReplaceAllString(id, "<redacted-jwt>")
	id = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, id)
	for strings.Contains(id, "::") {
		id = strings.ReplaceAll(id, "::", ": :")
	}
	return id
}

// gatewayModelIDs reads an OpenAI-style model list ({"data":[{"id":...}]})
// from a 2xx reply. ok is false for any other status, a body that is not
// such a list, or one cut off at gatewayProbeBodyLimit.
func gatewayModelIDs(status int, body []byte) (ids []string, ok bool) {
	if status < 200 || status > 299 || len(body) > gatewayProbeBodyLimit {
		return nil, false
	}
	var list struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil || list.Data == nil {
		return nil, false
	}
	for _, m := range *list.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, true
}

// probeGateway sends GET endpoint with the assertion as a bearer token and
// returns the HTTP status and up to gatewayProbeBodyLimit+1 bytes of the
// reply, so the caller can tell a list that was cut off. It refuses a
// non-https endpoint. Errors never include the assertion.
func probeGateway(ctx context.Context, client *http.Client, endpoint, assertion string) (int, []byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, nil, fmt.Errorf("parsing gateway URL: %w", err)
	}
	if u.Scheme != "https" {
		return 0, nil, fmt.Errorf("gateway URL must use https, got scheme %q", u.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, gatewayProbeBodyLimit+1))
	if err != nil {
		return 0, nil, fmt.Errorf("reading gateway reply: %w", err)
	}
	return resp.StatusCode, body, nil
}
