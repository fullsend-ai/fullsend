package repos

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitlabOpenAIKeyVar is the GitLab CI/CD variable a project owner sets to
// run GPT there (OpenAI WIF is GitHub Actions only, ADR 0092). fullsend
// never provisions it and never deletes it — see gitlabUninstallSecrets —
// so under the openai route it is probed for presence only.
const gitlabOpenAIKeyVar = "OPENAI_API_KEY"

// InferenceRoute is what a per-repo installation must provision for
// inference (#7481). It is read from the repository's committed config
// layers (.fullsend/config.yaml over .fullsend/config.base.yaml); a repo
// without a config declares nothing and gets the code default, vertex.
type InferenceRoute struct {
	// Provider is the resolved inference.provider: vertex (GCP project +
	// WIF provider secrets) or openai (an OpenAI route instead).
	Provider string
	// OpenAIWIF is true when the committed config carries the complete
	// inference.openai trio, which is a route on its own (no secret).
	OpenAIWIF bool
	// OpenAIWIFConfig preserves a committed WIF route when scaffold repair
	// regenerates config.yaml. The identifiers are not secrets.
	OpenAIWIFConfig config.OpenAIWIFConfig
	// FromConfig is true when at least one config layer exists on the
	// default branch, i.e. Provider reflects the repository rather than
	// the code default. Converge uses the manifest's provider for a repo
	// that has no config yet (a fresh install).
	FromConfig bool
}

// OpenAI reports whether the route needs an OpenAI credential rather
// than the GCP pair.
func (r InferenceRoute) OpenAI() bool {
	return r.Provider == config.InferenceProviderOpenAI
}

// ProbeInferenceRoute reads the repository's committed per-repo config
// layers from the default branch and returns the inference route they
// declare. A missing layer is not an error; an unparsable one is,
// because install and status must not guess at what such a repo needs.
func ProbeInferenceRoute(ctx context.Context, client forge.Client, owner, repo string) (InferenceRoute, error) {
	var overlay, base []byte
	haveOverlay, haveBase := false, false
	if data, err := client.GetFileContent(ctx, owner, repo, ".fullsend/config.yaml"); err == nil {
		overlay, haveOverlay = data, true
	} else if !forge.IsNotFound(err) {
		return InferenceRoute{}, fmt.Errorf("reading .fullsend/config.yaml: %w", err)
	}
	if data, err := client.GetFileContent(ctx, owner, repo, ".fullsend/config.base.yaml"); err == nil {
		base, haveBase = data, true
	} else if !forge.IsNotFound(err) {
		return InferenceRoute{}, fmt.Errorf("reading .fullsend/config.base.yaml: %w", err)
	}
	route := InferenceRoute{Provider: config.DefaultPerRepoInferenceProvider}
	if !haveOverlay && !haveBase {
		return route, nil
	}
	parsed, err := config.ParsePerRepoConfigWriterLayered(overlay, base)
	if err != nil {
		return InferenceRoute{}, fmt.Errorf("per-repo config of %s/%s: %w", owner, repo, err)
	}
	route.FromConfig = true
	if p := parsed.ConfigInferenceProvider(); p != "" {
		route.Provider = p
	}
	ids := parsed.ConfigInferenceOpenAI().Trimmed()
	if missing := ids.Missing(); !ids.IsZero() && len(missing) > 0 {
		// The runner treats a partial trio as an error, never as a
		// fallback to the static key (resolveOpenAICredential), so an
		// openai-route install must not report it healthy on the
		// strength of FULLSEND_OPENAI_API_KEY. A vertex repository is
		// left alone: the block only matters to its GPT runs.
		if route.OpenAI() {
			return InferenceRoute{}, fmt.Errorf("inference.openai in the per-repo config of %s/%s is partially configured: missing %s", owner, repo, strings.Join(missing, ", "))
		}
	} else {
		route.OpenAIWIF = !ids.IsZero()
		route.OpenAIWIFConfig = ids
	}
	return route, nil
}

// requiredSecretsForRoute returns the secret names that must exist for a
// complete installation on the given route, assuming no GitLab role
// migration (see requiredSecretsForRouteMode).
func requiredSecretsForRoute(forgeName string, route InferenceRoute) []string {
	return requiredSecretsForRouteMode(forgeName, route, "", false)
}

// requiredSecretsForRouteMode composes two independent decisions. The
// forge decides the non-inference credentials (requiredSecretsForForgeMode:
// on GitLab, the forge token until role migration is enforced). The route
// decides the inference credentials: vertex keeps the GCP pair; openai
// replaces it with FULLSEND_OPENAI_API_KEY on GitHub unless the config
// commits the OpenAI WIF trio, and on GitLab (no WIF there) with the
// project's own OPENAI_API_KEY variable, which is probed, never managed.
func requiredSecretsForRouteMode(forgeName string, route InferenceRoute, migrationMode string, migrationExists bool) []string {
	base := requiredSecretsForForgeMode(forgeName, migrationMode, migrationExists)
	if !route.OpenAI() {
		return base
	}
	out := make([]string, 0, len(base)+1)
	for _, s := range base {
		if s == forge.SecretGCPProjectID || s == forge.SecretGCPWIFProvider {
			continue
		}
		out = append(out, s)
	}
	switch {
	case forgeName == ForgeGitLab:
		out = append(out, gitlabOpenAIKeyVar)
	case !route.OpenAIWIF:
		out = append(out, forge.SecretOpenAIAPIKey)
	}
	return out
}

// openAIRouteSecret is the secret component whose presence proves the
// openai route is configured on a repository with no OpenAI WIF trio.
func openAIRouteSecret(forgeName string) string {
	if forgeName == ForgeGitLab {
		return gitlabOpenAIKeyVar
	}
	return forge.SecretOpenAIAPIKey
}
