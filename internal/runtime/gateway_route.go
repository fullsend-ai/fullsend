package runtime

import "github.com/fullsend-ai/fullsend/internal/config"

// CredentialSeed is one credential route's in-sandbox seed (ADR 0092,
// ADR 0137): the env var the run-scoped provider's placeholder reaches
// the sandbox in, the POSIX sh fragment that writes that placeholder into
// a runner-owned file the agent re-reads per request, and that file's
// absolute sandbox path. The runner re-runs Seed through `sandbox exec`
// after every refresh of the route's provider, once the sandbox hands
// out the new generation under PlaceholderEnv, and greps File for it.
//
// Each route names its own triple, so one run that uses two routes (a pi
// run with openai/ and gateway/ models) keeps two independent refresh
// handoffs. A zero CredentialSeed means the runtime has no seed for the
// route: the provider is still created and refreshed, but nothing is
// re-seeded in the sandbox.
type CredentialSeed struct {
	PlaceholderEnv string
	Seed           string
	File           string
}

// IsZero reports whether the route has no seed fragment.
func (s CredentialSeed) IsZero() bool { return s.Seed == "" }

// openAIPlaceholderEnv is the env var the OpenAI provider's placeholder
// reaches the sandbox in.
const openAIPlaceholderEnv = "OPENAI_API_KEY"

// OpenAIRouteSeed returns the OpenAI route's seed for rt
// (OpenAICredentialSeeder), or a zero seed when rt has none or its seeder
// returns an empty fragment.
func OpenAIRouteSeed(rt Runtime) CredentialSeed {
	s, ok := rt.(OpenAICredentialSeeder)
	if !ok {
		return CredentialSeed{}
	}
	seed := s.OpenAIAuthSeed()
	if seed == "" {
		return CredentialSeed{}
	}
	return CredentialSeed{PlaceholderEnv: openAIPlaceholderEnv, Seed: seed, File: s.OpenAIAuthFile()}
}

// GatewayRun is the runtime-neutral per-run inference gateway setup the
// runner hands a runtime when an inference.gateway block applies
// (ADR 0137, #8280).
type GatewayRun struct {
	// Block is the resolved, complete inference.gateway block.
	Block config.InferenceGatewayConfig
	// ModelsFile is the committed models_file's bytes, read from the same
	// ref as config.yaml; nil when the block has no models_file.
	ModelsFile []byte
	// BaseURL is the gateway origin the runtime's client calls.
	BaseURL string
}

// GatewayRouteRuntime is implemented by runtimes that carry the inference
// gateway route. The runner calls PrepareGatewayRun before Bootstrap when
// a block applies and the run resolves a gateway/ model, and
// ClearGatewayRun when the run ends. GatewayCredentialSeed names the
// route's placeholder env key, seed fragment and credential file, which
// the runner's gateway refresher re-runs after each token refresh.
//
// pi and codex implement it. Runtimes without the route do not implement
// the interface, and the runner refuses a gateway/ model on them.
type GatewayRouteRuntime interface {
	PrepareGatewayRun(sandboxName string, run GatewayRun) error
	ClearGatewayRun(sandboxName string)
	GatewayCredentialSeed() CredentialSeed
}

// NeedsGatewayRoute reports whether the parent's effective model on the
// named backend resolves to the gateway provider, using the same
// resolution buildPiRunCommand gates on (provider prefix, case-folded,
// models.aliases, the FULLSEND_PI_PROVIDER default for a bare id). On codex
// the gateway/ prefix alone decides, as in translateCodexModel: codex
// consults no alias table. pi and codex carry the route.
func NeedsGatewayRoute(backend, runModel, agentModel string, configAliases map[string]string) bool {
	switch backend {
	case "pi":
		return piModelProvider(EffectiveModel(runModel, agentModel), configAliases) == piGatewayProvider
	case "codex":
		return isCodexGatewayModel(EffectiveModel(runModel, agentModel))
	default:
		return false
	}
}

// GatewayChildren lists the configured pi children whose model resolves
// to the gateway provider, resolved exactly as Bootstrap resolves them
// (see OpenAIChildren).
func GatewayChildren(backend, agentPath string, subagentsCfg map[string]*string, skillDirs []string, agentName string, configAliases map[string]string) []PiChild {
	return piChildrenOn(backend, agentPath, subagentsCfg, skillDirs, agentName, configAliases, piGatewayProvider)
}
