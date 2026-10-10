package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/artifacts"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Inference gateway behaviour wiring (#8280). The test gateway's location
// is never committed to this repository: scenarios read it from the
// environment and skip when it is unset.
const (
	envInferenceGatewayURL      = "E2E_INFERENCE_GATEWAY_URL"
	envInferenceGatewayAudience = "E2E_INFERENCE_GATEWAY_AUDIENCE"
	defaultGatewayAudience      = "fullsend-e2e-gateway"
	// gatewayPlaceholder is expanded in http_probe args to the test
	// gateway URL (no trailing slash), e.g. "<gateway>/v1/models".
	gatewayPlaceholder = "<gateway>"
)

// gatewayConfigOriginals holds the pre-scenario .fullsend/config.yaml for
// scenarios that committed an inference.gateway block, so the After hook
// can restore it. Keyed by the scenario's World.
var gatewayConfigOriginals sync.Map

func registerGatewaySteps(sc *godog.ScenarioContext) {
	sc.Step(`^the test inference gateway is configured for the repository$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenTestInferenceGateway(world.FromContext(ctx))
	})
	sc.Step(`^the agent's probe "([^"]+)" returned HTTP (\d{3})$`, func(ctx context.Context, description, status string) (context.Context, error) {
		return ctx, assertProbeStatus(world.FromContext(ctx), description, status)
	})
	sc.Step(`^the agent's probe "([^"]+)" response (contains|does not contain) "([^"]*)"$`, func(ctx context.Context, description, mode, needle string) (context.Context, error) {
		return ctx, assertProbeBody(world.FromContext(ctx), description, mode == "contains", needle)
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		w := world.FromContext(ctx)
		if w == nil {
			return ctx, err
		}
		if restoreErr := restoreGatewayConfig(w); restoreErr != nil {
			fmt.Fprintf(os.Stderr, "behaviour: restoring config after inference gateway scenario: %v\n", restoreErr)
		}
		return ctx, err
	})
}

// testGatewayFromEnv returns the gateway URL and audience, or ok=false
// when the URL is unset (the scenario then skips).
func testGatewayFromEnv() (gatewayURL, audience string, ok bool) {
	gatewayURL = strings.TrimRight(strings.TrimSpace(os.Getenv(envInferenceGatewayURL)), "/")
	if gatewayURL == "" {
		return "", "", false
	}
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
// original for restore. It skips the scenario when the URL is unset.
func givenTestInferenceGateway(w *world.World) error {
	gatewayURL, audience, ok := testGatewayFromEnv()
	if !ok {
		return godog.ErrSkip
	}
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before configuring the inference gateway")
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	original, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	merged, err := withInferenceGateway(original, gatewayURL, audience)
	if err != nil {
		return err
	}
	// Keep the first (pre-scenario) value if the step runs twice.
	gatewayConfigOriginals.LoadOrStore(w, original)
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: configure test inference gateway", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// withInferenceGateway sets inference.gateway.{url,audience} in a config
// document, leaving all other keys untouched.
func withInferenceGateway(data []byte, gatewayURL, audience string) ([]byte, error) {
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
	inference["gateway"] = map[string]any{"url": gatewayURL, "audience": audience}
	doc["inference"] = inference
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshaling config: %w", err)
	}
	return out, nil
}

func restoreGatewayConfig(w *world.World) error {
	v, ok := gatewayConfigOriginals.LoadAndDelete(w)
	if !ok {
		return nil
	}
	original, _ := v.([]byte)
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	return w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: restore config after inference gateway scenario", original)
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

func checkProbeBody(res runtime.BehaviourOpResult, wantContains bool, needle string) error {
	if needle == "" {
		return fmt.Errorf("probe %q: empty needle", res.Description)
	}
	got := strings.Contains(res.ResponseBody, needle)
	switch {
	case wantContains && !got:
		return fmt.Errorf("probe %q: response body does not contain %q", res.Description, needle)
	case !wantContains && got:
		return fmt.Errorf("probe %q: response body unexpectedly contains %q", res.Description, needle)
	}
	return nil
}
