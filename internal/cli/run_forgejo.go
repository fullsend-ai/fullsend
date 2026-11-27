package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// forgejoForgeProfileID is the provider profile generated for the Forgejo
// instance of a --forge forgejo run.
const forgejoForgeProfileID = "fullsend-forgejo-forge"

// errForgejoTokenMissing stops a Forgejo run that has no FORGEJO_TOKEN.
// Forgejo runs never mint a GitHub token, so without it the run has no
// credential for the forge at all.
var errForgejoTokenMissing = fmt.Errorf("%s is not set: a --forge forgejo run authenticates to the Forgejo instance with the job's automatic run token and never mints a GitHub token; pass that token to fullsend run as %s", forgejoTokenEnv, forgejoTokenEnv)

// errForgejoInstanceMissing stops a Forgejo run that names no instance, so
// the sandbox would have no Forgejo host to reach.
var errForgejoInstanceMissing = fmt.Errorf("%s is not set: a --forge forgejo run needs the Forgejo instance URL so the sandbox can reach it, for example https://forgejo.example.com", forgejoURLEnv)

// errNoForgejoForgeHost is returned for a profile that issues FORGEJO_TOKEN
// while no Forgejo host is known. resolveForgejoRunInstance fails the run
// first, so it is reached only through a direct call.
var errNoForgejoForgeHost = fmt.Errorf("cannot determine the Forgejo forge host for a profile that issues %s; set %s", forgejoTokenEnv, forgejoURLEnv)

// errForgejoGitHubTokenUnused is what the GitHub token resolver returns on a
// Forgejo run. A Forgejo Actions job sets GITHUB_TOKEN to its Forgejo run
// token, so reading it would send that token to GitHub.
var errForgejoGitHubTokenUnused = errors.New("a --forge forgejo run does not use a GitHub token")

// errForgejoStatusUnsupported disables status comments on a Forgejo run:
// the Forgejo adapter does not implement the forge.Client operations the
// status notifier uses.
var errForgejoStatusUnsupported = errors.New("status comments are not supported on the forgejo forge yet")

// errForgejoPlaybackUnsupported is reported when the dummy-playback runtime
// asks for a tracking-comment client on a Forgejo run, for the same reason.
var errForgejoPlaybackUnsupported = errors.New("tracking comments are not supported on the forgejo forge yet; using the local playlist position")

// forgeUsesDirectToken reports whether forgePlatform authenticates with a
// credential the job supplies (GitLab role credentials, the Forgejo run
// token) rather than a token minted by the mint service. Mint and remint
// paths are skipped for these platforms.
func forgeUsesDirectToken(forgePlatform string) bool {
	return forgePlatform == repos.ForgeGitLab || forgePlatform == repos.ForgeForgejo
}

// runGitHubTokenResolver returns the GitHub token resolver the run uses for
// remote resource fetches. On a Forgejo run it never returns a token, so
// neither a GITHUB_TOKEN holding the Forgejo run token nor `gh auth token`
// is read; an explicit --git-token is still applied by the callers.
func runGitHubTokenResolver(forgePlatform string) func() (string, error) {
	if forgePlatform == repos.ForgeForgejo {
		return func() (string, error) { return "", errForgejoGitHubTokenUnused }
	}
	return resolveToken
}

// requireForgejoRunToken returns errForgejoTokenMissing unless FORGEJO_TOKEN
// holds a non-blank value. The token stays in the process environment, where
// provider credentials that reference ${FORGEJO_TOKEN} pick it up.
func requireForgejoRunToken(getenv func(string) string) error {
	if strings.TrimSpace(getenv(forgejoTokenEnv)) == "" {
		return errForgejoTokenMissing
	}
	return nil
}

// errForgejoTokenUnredactable stops a Forgejo run whose FORGEJO_TOKEN is too
// short for the runtime redactor to register, since the run could not scrub
// it from feedback, logs or telemetry.
var errForgejoTokenUnredactable = fmt.Errorf("%s is too short to be a run token: refusing to use a credential that cannot be redacted", forgejoTokenEnv)

// registerForgejoRunToken adds the FORGEJO_TOKEN value to the process-wide
// runtime secret redactor, so a script or provider that echoes it cannot
// leak it into validation feedback, the agent prompt, logs or telemetry.
// FORGEJO_TOKEN is not a runner env entry, so envLiterals does not cover it.
// A value the redactor refuses (too short) returns errForgejoTokenUnredactable.
func registerForgejoRunToken(getenv func(string) string) error {
	if !security.RegisterRuntimeSecret(strings.TrimSpace(getenv(forgejoTokenEnv))) {
		return errForgejoTokenUnredactable
	}
	return nil
}

// resolveForgejoRunInstance returns the host and port of the Forgejo
// instance a --forge forgejo run is configured for (FULLSEND_FORGEJO_URL).
// The URL must pass repos.ValidateForgejoURL (https, or http for a loopback
// host), and the host must be a concrete destination, because it is added
// to the sandbox egress allowlist and FORGEJO_TOKEN is bound to it.
func resolveForgejoRunInstance(getenv func(string) string) (host string, port int, err error) {
	raw := strings.TrimSpace(getenv(forgejoURLEnv))
	if raw == "" {
		return "", 0, errForgejoInstanceMissing
	}
	if err := repos.ValidateForgejoURL(forgejoURLEnv, raw); err != nil {
		return "", 0, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", forgejoURLEnv, err)
	}
	host = u.Hostname()
	switch p := u.Port(); {
	case p != "":
		if port, err = strconv.Atoi(p); err != nil {
			return "", 0, fmt.Errorf("%s port %q is not a valid integer", forgejoURLEnv, p)
		}
	case u.Scheme == "http":
		port = 80
	default:
		port = 443
	}
	if err := validateForgeDestination(host, port); err != nil {
		return "", 0, fmt.Errorf("%s: %w", forgejoURLEnv, err)
	}
	return host, port, nil
}

// forgejoEgressEntry renders the Forgejo instance as the host:port entry the
// sandbox SSRF hook merges into its egress allowlist.
func forgejoEgressEntry(host string, port int) string {
	if host == "" {
		return ""
	}
	return host + ":" + strconv.Itoa(port)
}

// generateForgejoForgeProfile writes a provider profile for the Forgejo
// instance, the Forgejo counterpart of generateGitLabForgeProfile. It
// carries no credential. It returns the temp file path and a cleanup
// function.
func generateForgejoForgeProfile(host string, port int) (string, func(), error) {
	profile := forgeProfileSpec{
		ID:          forgejoForgeProfileID,
		DisplayName: "Fullsend Forgejo (auto)",
		Description: "Forgejo API and Git operations for fullsend agents (auto-generated from FULLSEND_FORGEJO_URL)",
		Category:    "source_control",
		Endpoints: []forgeProfileEndpoint{
			{Host: host, Port: port, Protocol: "rest", Access: "read-write", Enforcement: "enforce"},
		},
		Binaries: []string{"**/curl", "**/git", "**/node", "**/pre-commit"},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&profile); err != nil {
		return "", nil, fmt.Errorf("marshaling Forgejo profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", nil, fmt.Errorf("closing YAML encoder: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "fullsend-forgejo-profile-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir for Forgejo profile: %w", err)
	}
	profilePath := filepath.Join(tmpDir, forgejoForgeProfileID+".yaml")
	if err := os.WriteFile(profilePath, buf.Bytes(), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("writing Forgejo profile: %w", err)
	}
	return profilePath, func() { os.RemoveAll(tmpDir) }, nil
}

// errForgejoTokenUnderOtherKey stops a Forgejo run whose provider definition
// hands the run token to the sandbox under a credential key other than
// FORGEJO_TOKEN, or references it from a config value. The run token is
// FORGEJO_TOKEN and also GITHUB_TOKEN, which a Forgejo Actions job sets to
// the same value and which a Forgejo run never overwrites with a minted
// token. The host/port binding
// (checkForgejoCredentialBinding) matches profiles by credential key, so a
// provider under another key would send the run token wherever its
// profile's endpoints point, and a config value is passed inline on the
// provider CLI's argv, outside the child-environment credential path.
var errForgejoTokenUnderOtherKey = errors.New("provider references the run token (FORGEJO_TOKEN or GITHUB_TOKEN) outside a FORGEJO_TOKEN credential; a --forge forgejo run only hands the run token to the sandbox as the FORGEJO_TOKEN credential, where it is bound to the Forgejo instance")

// forgejoRunTokenEnvs are the environment variables that carry the Forgejo
// run token on a Forgejo job: FORGEJO_TOKEN, and GITHUB_TOKEN, which the
// runner sets to the same value.
var forgejoRunTokenEnvs = []string{forgejoTokenEnv, "GITHUB_TOKEN"}

// referencesForgejoToken reports whether value references the run token
// through ${FORGEJO_TOKEN} or ${GITHUB_TOKEN} (or the unbraced forms).
func referencesForgejoToken(value string) bool {
	for _, ref := range harness.EnvRefNames(value) {
		for _, name := range forgejoRunTokenEnvs {
			if ref == name {
				return true
			}
		}
	}
	return false
}

// rejectForgejoTokenUnderOtherKey returns errForgejoTokenUnderOtherKey for
// the first provider definition with a credential value other than the
// FORGEJO_TOKEN key, or any config value, that references the run token
// (${FORGEJO_TOKEN} or ${GITHUB_TOKEN}).
func rejectForgejoTokenUnderOtherKey(defs []harness.ProviderDef) error {
	for _, def := range defs {
		for key, value := range def.Credentials {
			if key != forgejoTokenEnv && referencesForgejoToken(value) {
				return fmt.Errorf("provider %q credential %q: %w", def.Name, key, errForgejoTokenUnderOtherKey)
			}
		}
		for key, value := range def.Config {
			if referencesForgejoToken(value) {
				return fmt.Errorf("provider %q config %q: %w", def.Name, key, errForgejoTokenUnderOtherKey)
			}
		}
	}
	return nil
}

// errForgejoTokenProfileNotLoaded stops a Forgejo run whose provider receives
// FORGEJO_TOKEN but whose profile is not one of the profiles this run loads.
var errForgejoTokenProfileNotLoaded = errors.New("provider receives FORGEJO_TOKEN but its profile is not listed in openshell.profiles; the profile must be loaded so it can be bound to the Forgejo instance, because a profile already installed on the gateway is not validated")

// requireForgejoTokenProfileLoaded returns errForgejoTokenProfileNotLoaded
// for the first provider definition that references the run token in a
// credential while its type names a profile outside profiles.
// checkForgejoCredentialBinding validates only the loaded profiles, so a
// bare-name provider resolving to a pre-existing gateway profile would
// otherwise receive the run token unchecked.
func requireForgejoTokenProfileLoaded(defs []harness.ProviderDef, profiles []resolve.ResolvedProfile) error {
	loaded := make(map[string]bool, len(profiles))
	for _, rp := range profiles {
		loaded[rp.ID] = true
	}
	for _, def := range defs {
		for _, value := range def.Credentials {
			if referencesForgejoToken(value) && !loaded[def.Type] {
				return fmt.Errorf("provider %q type %q: %w", def.Name, def.Type, errForgejoTokenProfileNotLoaded)
			}
		}
	}
	return nil
}

// checkForgejoCredentialBinding returns an error unless every profile that
// issues FORGEJO_TOKEN names only the Forgejo instance's host and port
// (checkCredentialBinding). OpenShell resolves a credential placeholder at
// every endpoint of the profile that issued it, so a profile naming another
// host would send the Forgejo run token there. Unlike the GitLab binding,
// profiles are not rewritten: such a profile stops the run.
func checkForgejoCredentialBinding(profiles []resolve.ResolvedProfile, host string, port int) error {
	for _, rp := range profiles {
		data, err := os.ReadFile(rp.LocalPath)
		if err != nil {
			return fmt.Errorf("reading profile %q: %w", rp.ID, err)
		}
		if err := checkCredentialBinding(data, forgejoTokenEnv, errNoForgejoForgeHost, host, port); err != nil {
			return fmt.Errorf("profile %q: %w", rp.ID, err)
		}
	}
	return nil
}
