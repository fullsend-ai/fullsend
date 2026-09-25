package scaffold

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// canonicalForgeEnvVars is the Secret*/Var* const block in
// internal/forge/forge.go. Using the exported identifiers (not string
// literals) means a rename of a constant's value is reflected here at
// compile time — the same guarantee that block documents for Go call
// sites. Keep this list in sync when adding a new Secret* or Var*
// constant; TestGitLabCIScaffoldEnvVarsAreCanonical will fail if a
// scaffold then references a name that is in neither this set nor
// gitlabCIScaffoldLocalEnvVars.
func canonicalForgeEnvVars() map[string]struct{} {
	names := []string{
		forge.VarMintURL,
		forge.VarGCPRegion,
		forge.VarReviewClientID,
		forge.VarLastPollAtFast,
		forge.VarLastPollAtFull,
		forge.VarLabelState,
		forge.VarDispatchedKeysFast,
		forge.VarDispatchedKeysFull,
		forge.VarFailedKeysFast,
		forge.VarFailedKeysFull,
		forge.SecretGCPProjectID,
		forge.SecretGCPWIFProvider,
		forge.SecretForgeToken,
		forge.SecretGitLabPollerToken,
		forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken,
		forge.SecretDispatch,
		forge.SecretOpenAIAPIKey,
		forge.VarLegacyBotTokenSecret,
		forge.VarLegacySA,
		forge.VarLegacyWIFProvider,
		forge.VarLegacyForge,
		forge.VarDispatchHMAC,
		forge.VarPollJobURL,
		forge.VarPollMode,
		forge.VarGitLabBotToken,
		forge.VarGitLabRoleMigration,
		forge.VarGitLabRoleRegistry,
		forge.VarGitLabRoleRotation,
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		out[name] = struct{}{}
	}
	return out
}

// gitlabCIScaffoldLocalEnvVars are FULLSEND_* names the GitLab CI
// templates legitimately use that are not forge.Secret*/Var* constants:
// job-local shell variables, install-time placeholders, GitLab OIDC,
// and CLI/runtime knobs consumed by `fullsend` rather than stored as
// forge-managed secrets/vars. A new script-local FULLSEND_* name must
// be added here explicitly — do not skip unmatched tokens silently.
var gitlabCIScaffoldLocalEnvVars = map[string]struct{}{
	// select-gitlab-role-token.sh exports these for later
	// PRIVATE-TOKEN / GITLAB_TOKEN / PUSH_TOKEN use.
	"FULLSEND_JOB_TOKEN":      {},
	"FULLSEND_JOB_KIND":       {},
	"FULLSEND_JOB_AGENT":      {},
	"FULLSEND_JOB_TOKEN_NAME": {},
	// Comment glob "FULLSEND_JOB_* naming" in the role-token helper.
	"FULLSEND_JOB_": {},
	// Install-time version pin and the GitHub repo the templates
	// download/build the CLI from.
	"FULLSEND_VERSION": {},
	"FULLSEND_REPO":    {},
	// GitLab CI OIDC id_token declared in fullsend-agent.yml.
	"FULLSEND_ID_TOKEN": {},
	// Harness/CLI: status comments target MRs rather than issues.
	"FULLSEND_NOTE_TARGET": {},
	// trust-ci-server-ca.sh idempotency flag.
	"FULLSEND_CI_SERVER_CA_TRUSTED": {},
	// CLI GitLab base-URL override (internal/forge/gitlab.URLEnvVars).
	"FULLSEND_GITLAB_URL": {},
	// Concatenation prefix for custom-role secrets
	// FULLSEND_GITLAB_ROLE_<NAME>_TOKEN (see forge.go).
	"FULLSEND_GITLAB_ROLE_": {},
	// Comment-only: the diagnostic name the helper deliberately does
	// not export (FULLSEND_JOB_TOKEN_NAME is used instead).
	"FULLSEND_GITLAB_ROLE_SECRET": {},
}

// fullsendEnvTokenRE matches FULLSEND_[A-Z0-9_]+ that is not a substring
// of a longer identifier and is not embedded in an __PLACEHOLDER__.
// The lookbehind equivalent is a non-identifier character (or start of
// text) so "__FULLSEND_VERSION__" is not extracted as FULLSEND_VERSION__.
var fullsendEnvTokenRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(FULLSEND_[A-Z0-9_]+)`)

func extractFullsendEnvTokens(content string) []string {
	seen := make(map[string]struct{})
	for _, m := range fullsendEnvTokenRE.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		seen[m[1]] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// isDerivedGitLabRoleSecret reports custom-role credential names of the
// form FULLSEND_GITLAB_ROLE_<NAME>_TOKEN documented in forge.go. Named
// forge.VarGitLabRole* constants (migration/registry/rotation) do not
// end in _TOKEN and are covered by canonicalForgeEnvVars instead.
func isDerivedGitLabRoleSecret(name string) bool {
	const prefix = "FULLSEND_GITLAB_ROLE_"
	const suffix = "_TOKEN"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	mid := name[len(prefix) : len(name)-len(suffix)]
	if mid == "" {
		return false
	}
	for _, r := range mid {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func unrecognizedGitLabCIScaffoldEnvTokens(content string) []string {
	allowed := canonicalForgeEnvVars()
	for name := range gitlabCIScaffoldLocalEnvVars {
		allowed[name] = struct{}{}
	}
	var unknown []string
	for _, tok := range extractFullsendEnvTokens(content) {
		if _, ok := allowed[tok]; ok {
			continue
		}
		if isDerivedGitLabRoleSecret(tok) {
			continue
		}
		unknown = append(unknown, tok)
	}
	return unknown
}

func TestGitLabCIScaffoldEnvVarsAreCanonical(t *testing.T) {
	var scanned []string
	var failures []string
	err := WalkGitLabPerRepo(func(path string, content []byte) error {
		if !strings.HasPrefix(path, ".gitlab/ci/") {
			return nil
		}
		scanned = append(scanned, path)
		unknown := unrecognizedGitLabCIScaffoldEnvTokens(string(content))
		if len(unknown) > 0 {
			failures = append(failures, path+": "+strings.Join(unknown, ", "))
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, scanned, "expected to scan GitLab CI scaffold templates under .gitlab/ci/")
	assert.Contains(t, scanned, ".gitlab/ci/fullsend-poll.yml")
	assert.Contains(t, scanned, ".gitlab/ci/fullsend-agent.yml")
	assert.Contains(t, scanned, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	if len(failures) > 0 {
		t.Errorf("scaffold FULLSEND_* names absent from forge.Secret*/Var* constants and the script-local allowlist:\n  %s",
			strings.Join(failures, "\n  "))
	}
}

func TestExtractFullsendEnvTokens_LongestMatchNotSubstring(t *testing.T) {
	tokens := extractFullsendEnvTokens("${FULLSEND_JOB_TOKEN} FULLSEND_JOB_KIND")
	assert.Equal(t, []string{"FULLSEND_JOB_KIND", "FULLSEND_JOB_TOKEN"}, tokens)
}

func TestExtractFullsendEnvTokens_SkipsVersionPlaceholder(t *testing.T) {
	tokens := extractFullsendEnvTokens(`FULLSEND_VERSION="__FULLSEND_VERSION__"`)
	assert.Equal(t, []string{"FULLSEND_VERSION"}, tokens)
	assert.NotContains(t, tokens, "FULLSEND_VERSION__")
}

func TestUnrecognizedGitLabCIScaffoldEnvTokens_RejectsUnknown(t *testing.T) {
	unknown := unrecognizedGitLabCIScaffoldEnvTokens("export FULLSEND_NOT_A_REAL_SECRET=1\n")
	assert.Equal(t, []string{"FULLSEND_NOT_A_REAL_SECRET"}, unknown)
}

func TestUnrecognizedGitLabCIScaffoldEnvTokens_AcceptsCanonicalAndAllowlisted(t *testing.T) {
	content := fmt.Sprintf(
		"export %s=1\nexport %s=1\nexport FULLSEND_JOB_TOKEN=1\nexport FULLSEND_GITLAB_ROLE_SCANNER_TOKEN=1\n",
		forge.SecretGitLabPollerToken,
		forge.SecretForgeToken,
	)
	assert.Empty(t, unrecognizedGitLabCIScaffoldEnvTokens(content))
}

func TestCanonicalForgeEnvVars_UsesExportedConstants(t *testing.T) {
	got := canonicalForgeEnvVars()
	assert.Contains(t, got, forge.SecretForgeToken)
	assert.Contains(t, got, forge.SecretGitLabPollerToken)
	assert.Contains(t, got, forge.SecretGitLabAnalystToken)
	assert.Contains(t, got, forge.SecretGitLabCoderToken)
	assert.Contains(t, got, forge.VarGitLabRoleMigration)
	assert.NotContains(t, got, "FULLSEND_JOB_TOKEN")
}
