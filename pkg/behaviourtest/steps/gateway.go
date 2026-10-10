package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/artifacts"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Inference gateway behaviour wiring (#8280). The test gateway's location
// is never committed to this repository: scenarios read it from the
// environment and skip when it is unset.
const (
	envInferenceGatewayURL      = "E2E_INFERENCE_GATEWAY_URL"
	envInferenceGatewayAudience = "E2E_INFERENCE_GATEWAY_AUDIENCE"
	// envInferenceGatewayTestKey is the test gateway's API key for the
	// api-key mode (ADR 0138), authorised for the echo model only. It
	// gates the scenario and is registered for redaction. The step sets it
	// as the enrolled repository's FULLSEND_INFERENCE_GATEWAY_API_KEY
	// secret for the harness run, and CleanupScenario deletes it.
	envInferenceGatewayTestKey = "E2E_INFERENCE_GATEWAY_TEST_KEY"
	defaultGatewayAudience     = "fullsend-e2e-gateway"
	// gatewayPlaceholder is expanded in http_probe args to the test
	// gateway URL (no trailing slash), e.g. "<gateway>/v1/models".
	gatewayPlaceholder = "<gateway>"
)

func registerGatewaySteps(sc *godog.ScenarioContext) {
	// Checked first in each gateway scenario, before a repo is leased, so
	// a run without the gateway settings skips without spending API calls
	// on repo allocation and install.
	sc.Step(`^the test inference gateway is available$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenTestInferenceGatewayAvailable(false)
	})
	sc.Step(`^the test inference gateway is available with an API key$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenTestInferenceGatewayAvailable(true)
	})
	sc.Step(`^the test inference gateway is configured for the repository$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenTestInferenceGateway(world.FromContext(ctx))
	})
	sc.Step(`^the test inference gateway is configured for the repository with an API key$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenTestInferenceGatewayAPIKey(world.FromContext(ctx))
	})
	sc.Step(`^the agent's probe "([^"]+)" returned HTTP (\d{3})$`, func(ctx context.Context, description, status string) (context.Context, error) {
		return ctx, assertProbeStatus(world.FromContext(ctx), description, status)
	})
	sc.Step(`^the agent's probe "([^"]+)" response (contains|does not contain) "([^"]*)"$`, func(ctx context.Context, description, mode, needle string) (context.Context, error) {
		return ctx, assertProbeBody(world.FromContext(ctx), description, mode == "contains", needle)
	})
	sc.Step(`^the harness workflow logs show the inference gateway provider was cleaned up$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenGatewayProviderCleanedUp(world.FromContext(ctx))
	})
}

// gatewayProviderCleanupMarkers are the runner's log lines for a removed
// run-scoped gateway provider (cleanupRunScopedProvider): deleted, or
// already gone. The provider name starts with "inference-gateway-".
var gatewayProviderCleanupMarkers = []string{
	"Run-scoped provider deleted: inference-gateway-",
	"Run-scoped provider already gone: inference-gateway-",
}

// thenGatewayProviderCleanedUp checks the completed harness run's logs
// (recorded by "the harness ... workflow completes successfully") for the
// gateway provider's cleanup.
func thenGatewayProviderCleanedUp(w *world.World) error {
	if w.WorkflowRun == nil {
		return fmt.Errorf("no workflow run recorded; assert the harness workflow completed first")
	}
	logs, err := w.CI.GetRunLogs(context.Background(), w.RepoOwner, w.RepoName, w.WorkflowRun.ID)
	if err != nil {
		return fmt.Errorf("reading workflow logs: %w", err)
	}
	return gatewayProviderCleanupLogged(logs)
}

// gatewayProviderCleanupLogged reports an error unless logs show the
// run-scoped gateway provider was removed.
func gatewayProviderCleanupLogged(logs string) error {
	for _, m := range gatewayProviderCleanupMarkers {
		if strings.Contains(logs, m) {
			return nil
		}
	}
	return fmt.Errorf("workflow logs do not show the run-scoped inference gateway provider was cleaned up (want one of %q)", gatewayProviderCleanupMarkers)
}

// testGatewayFromEnv returns the gateway URL and audience, or ok=false
// when the URL is unset (the scenario then skips).
func testGatewayFromEnv() (gatewayURL, audience string, ok bool) {
	gatewayURL = strings.TrimRight(strings.TrimSpace(os.Getenv(envInferenceGatewayURL)), "/")
	if gatewayURL == "" {
		return "", "", false
	}
	// The URL embeds the E2E project number: keep it out of artifacts.
	registerSecretForms(gatewayURL)
	audience = strings.TrimSpace(os.Getenv(envInferenceGatewayAudience))
	if audience == "" {
		audience = defaultGatewayAudience
	}
	return gatewayURL, audience, true
}

// expandGatewayPlaceholder replaces <gateway> in http_probe args.
func expandGatewayPlaceholder(args string) (string, error) {
	if !strings.Contains(args, gatewayPlaceholder) {
		return args, nil
	}
	gatewayURL, _, ok := testGatewayFromEnv()
	if !ok {
		return "", fmt.Errorf("%s is unset; http_probe args reference %s", envInferenceGatewayURL, gatewayPlaceholder)
	}
	return strings.ReplaceAll(args, gatewayPlaceholder, gatewayURL), nil
}

// givenTestInferenceGateway commits an inference.gateway block (url +
// audience from the environment) into the enrolled repo's
// .fullsend/config.yaml, keeping every other key, and records the
// original on the World (GatewayConfigOriginal) for CleanupScenario to
// restore. It skips the scenario when the URL is unset.
func givenTestInferenceGateway(w *world.World) error {
	gatewayURL, audience, ok := testGatewayFromEnv()
	if !ok {
		return godog.ErrSkip
	}
	return commitInferenceGateway(w, map[string]any{"url": gatewayURL, "audience": audience, "models": testGatewayModels()})
}

// testGatewayModels is the model list the test gateway serves to pi
// (which runs offline and cannot discover it). Some projects' org policy
// refuses strict tool schemas on partner models, so the Anthropic model
// sets the compat flag that turns them off. The dummy runtime ignores the
// list.
func testGatewayModels() map[string]any {
	return map[string]any{
		"claude-haiku-5-5": map[string]any{
			"api":    "anthropic-messages",
			"compat": map[string]any{"supportsStrictTools": false},
		},
	}
}

// givenTestInferenceGatewayAPIKey commits an api-key inference.gateway
// block (url and auth: api-key, no audience) the same way. It skips the
// scenario when the URL or the test key is unset, and registers the key
// for redaction so it never reaches the suite's logs.
// givenTestInferenceGatewayAvailable skips the scenario when the test
// gateway URL (and, with withKey, the test key) is unset.
func givenTestInferenceGatewayAvailable(withKey bool) error {
	if _, _, ok := testGatewayFromEnv(); !ok {
		return godog.ErrSkip
	}
	if withKey && strings.TrimSpace(os.Getenv(envInferenceGatewayTestKey)) == "" {
		return godog.ErrSkip
	}
	return nil
}

func givenTestInferenceGatewayAPIKey(w *world.World) error {
	gatewayURL, _, ok := testGatewayFromEnv()
	key := strings.TrimSpace(os.Getenv(envInferenceGatewayTestKey))
	if !ok || key == "" {
		return godog.ErrSkip
	}
	registerSecretForms(key)
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before configuring the inference gateway")
	}
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("inference gateway api-key test requires GitHub SCM driver")
	}
	// Mark first, so cleanup still runs after a partly failed create.
	w.GatewayAPIKeySecretSet = true
	if err := ghDriver.Client.CreateRepoSecret(context.Background(), w.Org, w.RepoName, forge.SecretInferenceGatewayAPIKey, key); err != nil {
		return fmt.Errorf("setting repository secret %s: %w", forge.SecretInferenceGatewayAPIKey, err)
	}
	return commitInferenceGateway(w, map[string]any{"url": gatewayURL, "auth": config.GatewayAuthAPIKey, "models": testGatewayModels()})
}

// deleteGatewayAPIKeySecret deletes the repository secret that
// givenTestInferenceGatewayAPIKey set. A secret that is already gone is
// not an error.
func deleteGatewayAPIKeySecret(w *world.World) error {
	if !w.GatewayAPIKeySecretSet {
		return nil
	}
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("inference gateway api-key test requires GitHub SCM driver")
	}
	err := ghDriver.Client.DeleteRepoSecret(context.Background(), w.Org, w.RepoName, forge.SecretInferenceGatewayAPIKey)
	if err != nil && !forge.IsNotFound(err) {
		return err
	}
	w.GatewayAPIKeySecretSet = false
	return nil
}

// commitInferenceGateway commits gateway as the enrolled repo's
// inference.gateway block, recording the original config for
// restoreGatewayConfig.
func commitInferenceGateway(w *world.World, gateway map[string]any) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before configuring the inference gateway")
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	original, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	merged, err := withInferenceGateway(original, gateway)
	if err != nil {
		return err
	}
	// Keep the first (pre-scenario) value if the step runs twice.
	if !w.GatewayConfigOverridden {
		w.GatewayConfigOriginal = original
		w.GatewayConfigOverridden = true
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: configure test inference gateway", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// withInferenceGateway sets inference.gateway to gateway in a config
// document, leaving all other keys untouched.
func withInferenceGateway(data []byte, gateway map[string]any) ([]byte, error) {
	doc := map[string]any{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	inference, _ := doc["inference"].(map[string]any)
	if inference == nil {
		inference = map[string]any{}
	}
	inference["gateway"] = gateway
	doc["inference"] = inference
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling config: %w", err)
	}
	return out, nil
}

// restoreGatewayConfig commits back the pre-scenario config recorded by
// givenTestInferenceGateway. It is a no-op when the scenario never
// configured the gateway. CleanupScenario calls it first: a godog After
// hook registered here would run after suite.afterScenario has already
// deallocated (deleted) the leased repo.
func restoreGatewayConfig(w *world.World) error {
	if !w.GatewayConfigOverridden {
		return nil
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: restore config after inference gateway scenario", w.GatewayConfigOriginal); err != nil {
		// Keep the original so a cleanupRetry attempt can commit it again.
		return err
	}
	w.GatewayConfigOverridden = false
	w.GatewayConfigOriginal = nil
	return nil
}

func findProbeResult(w *world.World, description string) (runtime.BehaviourOpResult, error) {
	if err := ensureArtifacts(w); err != nil {
		return runtime.BehaviourOpResult{}, err
	}
	data, err := artifacts.FindBehaviourResults(w.ArtifactDir)
	if err != nil {
		return runtime.BehaviourOpResult{}, err
	}
	return probeResultFrom(data, description)
}

func probeResultFrom(data []byte, description string) (runtime.BehaviourOpResult, error) {
	var results runtime.BehaviourResults
	if err := json.Unmarshal(data, &results); err != nil {
		return runtime.BehaviourOpResult{}, fmt.Errorf("parsing behaviour-results.json: %w", err)
	}
	description = strings.TrimSpace(description)
	for _, res := range results.Operations {
		if res.Description == description {
			return res, nil
		}
	}
	return runtime.BehaviourOpResult{}, fmt.Errorf("operation %q not found in behaviour-results.json", description)
}

func assertProbeStatus(w *world.World, description, status string) error {
	res, err := findProbeResult(w, description)
	if err != nil {
		return err
	}
	return checkProbeStatus(res, status)
}

func checkProbeStatus(res runtime.BehaviourOpResult, status string) error {
	want, err := strconv.Atoi(status)
	if err != nil {
		return fmt.Errorf("invalid status %q: %w", status, err)
	}
	if res.HTTPStatus != want {
		return fmt.Errorf("probe %q: expected HTTP %d, got %d (error: %s)", res.Description, want, res.HTTPStatus, res.Error)
	}
	return nil
}

func assertProbeBody(w *world.World, description string, wantContains bool, needle string) error {
	res, err := findProbeResult(w, description)
	if err != nil {
		return err
	}
	return checkProbeBody(res, wantContains, needle)
}

// jwtNeedlePrefix starts every JWT. The recorded probe body has JWT-shaped
// substrings redacted (runtime.BehaviourOpResult.BodyHadJWT), so a needle
// with this prefix also matches when the flag says one was redacted.
const jwtNeedlePrefix = "eyJ"

func checkProbeBody(res runtime.BehaviourOpResult, wantContains bool, needle string) error {
	if needle == "" {
		return fmt.Errorf("probe %q: empty needle", res.Description)
	}
	got := strings.Contains(res.ResponseBody, needle) ||
		(res.BodyHadJWT && strings.HasPrefix(needle, jwtNeedlePrefix))
	switch {
	case wantContains && !got:
		return fmt.Errorf("probe %q: response body does not contain %q", res.Description, needle)
	case !wantContains && got:
		return fmt.Errorf("probe %q: response body unexpectedly contains %q", res.Description, needle)
	}
	return nil
}
