package gitlabroles

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParseRegistry(t *testing.T, raw string) Registry {
	t.Helper()
	reg, err := ParseRegistry(raw)
	require.NoError(t, err)
	return reg
}

func TestParseRegistryEmptyIsBuiltins(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "  ", "{}"} {
		reg, err := ParseRegistry(raw)
		require.NoError(t, err)
		names := make([]Role, 0, 3)
		for _, rec := range reg.Registrations() {
			names = append(names, rec.Name)
			assert.Equal(t, RoleKindBuiltin, rec.Kind)
		}
		assert.Equal(t, BuiltinRoles(), names)
	}
}

func TestBuiltinRegistryLookups(t *testing.T) {
	t.Parallel()
	reg := BuiltinRegistry()
	cases := map[string]Role{
		"poller":     RolePoller,
		"POLLER":     RolePoller,
		"analyst":    RoleAnalyst,
		"review":     RoleAnalyst,
		"triage":     RoleAnalyst,
		"prioritize": RoleAnalyst,
		"retro":      RoleAnalyst,
		"scribe":     RoleAnalyst,
		" Review ":   RoleAnalyst,
		"coder":      RoleCoder,
		"code":       RoleCoder,
		"fix":        RoleCoder,
		"CODE":       RoleCoder,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := reg.RoleFor(name)
			require.True(t, ok)
			assert.Equal(t, want, got.Name)
			assert.Equal(t, RoleKindBuiltin, got.Kind)
			require.NoError(t, reg.ValidateAgent(name))
		})
	}
	for _, name := range []string{"", "e2e", "fullsend", "unknown", "sync", "scanner"} {
		t.Run("unmapped_"+name, func(t *testing.T) {
			t.Parallel()
			got, ok := reg.RoleFor(name)
			assert.False(t, ok)
			assert.Empty(t, got.Name)
			err := reg.ValidateAgent(name)
			require.Error(t, err)
			if name == "" {
				assert.ErrorIs(t, err, ErrUnknownJob)
			} else {
				assert.ErrorIs(t, err, ErrUnregistered)
			}
		})
	}
}

func TestBuiltinCapabilities(t *testing.T) {
	t.Parallel()
	reg := BuiltinRegistry()
	poller, ok := reg.Lookup(RolePoller)
	require.True(t, ok)
	assert.True(t, poller.Has(CapWritePollState))
	assert.True(t, poller.Has(CapDispatchPipeline))
	assert.False(t, poller.Has(CapWriteRepository))
	assert.False(t, poller.Has(CapApproveMergeRequest))

	analyst, ok := reg.Lookup(RoleAnalyst)
	require.True(t, ok)
	assert.True(t, analyst.Has(CapApproveMergeRequest))
	assert.True(t, analyst.Has(CapWriteNotes))
	assert.False(t, analyst.Has(CapWriteRepository))
	assert.False(t, analyst.Has(CapWritePollState))

	coder, ok := reg.Lookup(RoleCoder)
	require.True(t, ok)
	assert.True(t, coder.Has(CapWriteRepository))
	assert.True(t, coder.Has(CapWriteMergeRequest))
	assert.False(t, coder.Has(CapApproveMergeRequest))
	assert.False(t, coder.Has(CapWritePollState))

	assert.Equal(t, forge.SecretGitLabPollerToken, poller.Credential.SecretName)
	assert.Equal(t, "fullsend-poller", poller.Credential.TokenName)
	assert.Equal(t, forge.SecretGitLabAnalystToken, analyst.Credential.SecretName)
	assert.Equal(t, "fullsend-analyst", analyst.Credential.TokenName)
	assert.Equal(t, forge.SecretGitLabCoderToken, coder.Credential.SecretName)
	assert.Equal(t, "fullsend-coder", coder.Credential.TokenName)
	assert.Equal(t, 9, len(KnownCapabilities()))
}

func TestParseRegistryCustomOwnCredential(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"responsibility": "read-only scanning",
			"credential": "own",
			"capabilities": ["read_issues", "write_notes"],
			"agents": ["scanner"]
		}]
	}`)
	rec, ok := reg.Lookup(Role("scanner"))
	require.True(t, ok)
	assert.Equal(t, RoleKindCustom, rec.Kind)
	assert.Equal(t, CredentialOwn, rec.Credential.Kind)
	assert.Equal(t, CustomSecretName(Role("scanner")), rec.Credential.SecretName)
	assert.Equal(t, "fullsend-role-scanner", rec.Credential.TokenName)
	assert.True(t, rec.Has(CapReadIssues))
	assert.True(t, rec.Has(CapWriteNotes))
	assert.False(t, rec.Has(CapWriteRepository))
	mapped, ok := reg.RoleFor("scanner")
	require.True(t, ok)
	assert.Equal(t, rec.Name, mapped.Name)
	require.NoError(t, reg.ValidateAgent("scanner"))
}

func TestParseRegistryCustomReuseCredential(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "deployer",
			"credential": "reuse",
			"reuse": "coder",
			"capabilities": ["write_repository", "write_merge_request"],
			"agents": ["deploy"]
		}]
	}`)
	rec, ok := reg.Lookup(Role("deployer"))
	require.True(t, ok)
	assert.Equal(t, CredentialReuse, rec.Credential.Kind)
	assert.Equal(t, RoleCoder, rec.Credential.ReuseOf)
	assert.Equal(t, forge.SecretGitLabCoderToken, rec.Credential.SecretName)
	assert.Empty(t, rec.Credential.TokenName)
	mapped, ok := reg.RoleFor("deploy")
	require.True(t, ok)
	assert.Equal(t, Role("deployer"), mapped.Name)
}

func TestResolveCustomReuseUsesTargetSecret(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "deployer",
			"credential": "reuse",
			"reuse": "coder",
			"agents": ["deploy"]
		}]
	}`)
	present := map[string]bool{
		forge.SecretForgeToken:       true,
		forge.SecretGitLabCoderToken: true,
	}
	src, err := Resolve(Request{
		Mode:     ModeEnforced,
		Job:      AgentJob("deploy"),
		Registry: reg,
		Present:  present,
	})
	require.NoError(t, err)
	assert.Equal(t, Role("deployer"), src.Role)
	assert.Equal(t, RoleKindCustom, src.Kind)
	assert.Equal(t, forge.SecretGitLabCoderToken, src.SecretName)
	assert.True(t, src.Reused)
	assert.False(t, src.Shared)
}

func TestParseRegistryRejectsBuiltinCollision(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"coder","credential":"own","agents":["pwn"]}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "already registered")
}

func TestParseRegistryRejectsAgentCollision(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","agents":["review"]}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "already mapped")
}

func TestParseRegistryRejectsUnknownCapability(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","capabilities":["sudo"]}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "unknown capability")
}

func TestParseRegistryRejectsSecretValue(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","secret_name":"glpat-secretvalue"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryRejectsUnknownField(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","token":"glpat-xxx"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestParseRegistryRejectsHarnessYAML(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry("role: scanner\nsource: ./agents/scanner.md\n")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsRepoConfigShape(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"agents":{"scanner":{"role":"scanner"}}}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsInvalidName(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"Scanner"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsDoubleHyphen(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"sc--anner"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsWrongOwnSecretName(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","secret_name":"FULLSEND_FORGE_TOKEN"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), CustomSecretName(Role("scanner")))
}

func TestParseRegistryRejectsReuseUnregistered(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"reuse","reuse":"ghost"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "unregistered")
}

func TestParseRegistryRejectsReuseCycle(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[
		{"name":"a","credential":"reuse","reuse":"b"},
		{"name":"b","credential":"reuse","reuse":"a"}
	]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "cycle")
}

func TestParseRegistryRejectsOwnWithReuse(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"own","reuse":"coder"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsReuseWithoutTarget(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"reuse"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsInvalidCredentialKind(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"inline"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{not json`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestLoadRegistry(t *testing.T) {
	t.Parallel()
	raw := `{"roles":[{"name":"scanner","agents":["scanner"]}]}`
	getenv := func(k string) string {
		if k == forge.VarGitLabRoleRegistry {
			return raw
		}
		return ""
	}
	reg, err := LoadRegistry(getenv)
	require.NoError(t, err)
	_, ok := reg.Lookup(Role("scanner"))
	assert.True(t, ok)
}

func TestLoadRegistryNilGetenvUsesEnv(t *testing.T) {
	t.Setenv(forge.VarGitLabRoleRegistry, `{"roles":[{"name":"scanner"}]}`)
	reg, err := LoadRegistry(nil)
	require.NoError(t, err)
	_, ok := reg.Lookup(Role("scanner"))
	assert.True(t, ok)
}

func TestPresenceFromIncludesCustomSecrets(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner"}]}`)
	secret := CustomSecretName(Role("scanner"))
	getenv := func(k string) string {
		if k == secret {
			return "present"
		}
		if k == forge.SecretForgeToken {
			return "shared"
		}
		return ""
	}
	present := PresenceFrom(getenv, reg)
	assert.True(t, present[secret])
	assert.True(t, present[forge.SecretForgeToken])
	assert.False(t, present[forge.SecretGitLabCoderToken])
}

func TestDiagnoseIncludesCustomRoles(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner"}]}`)
	rep := Diagnose(ModeEnforced, map[string]bool{
		forge.SecretForgeToken:            true,
		forge.SecretGitLabPollerToken:     true,
		forge.SecretGitLabAnalystToken:    true,
		forge.SecretGitLabCoderToken:      true,
		CustomSecretName(Role("scanner")): false,
	}, reg)
	assert.False(t, rep.Ready)
	assert.True(t, rep.Partial)
	assert.Contains(t, rep.Missing, Role("scanner"))
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "scanner (custom)")
	assert.Contains(t, joined, "missing (required)")
	assert.Contains(t, joined, "partial role configuration: 3/4")
}

func TestDiagnoseCustomConfigured(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner","credential":"reuse","reuse":"analyst"}]}`)
	present := map[string]bool{
		forge.SecretForgeToken:         true,
		forge.SecretGitLabPollerToken:  true,
		forge.SecretGitLabAnalystToken: true,
		forge.SecretGitLabCoderToken:   true,
	}
	rep := Diagnose(ModeEnforced, present, reg)
	assert.True(t, rep.Ready)
	assert.Empty(t, rep.Missing)
	joined := strings.Join(rep.Diagnostics, "\n")
	assert.Contains(t, joined, "reuse=analyst")
	assert.Contains(t, joined, "all role credentials configured")
}

func TestZeroRegistryBehavesAsBuiltin(t *testing.T) {
	t.Parallel()
	var reg Registry
	rec, ok := reg.Lookup(RoleCoder)
	require.True(t, ok)
	assert.Equal(t, RoleCoder, rec.Name)
	assert.Len(t, reg.Registrations(), 3)
}

func TestCustomSecretNameHyphen(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "FULLSEND_GITLAB_ROLE_CI_CHECK_TOKEN", CustomSecretName(Role("ci-check")))
	assert.Equal(t, "fullsend-role-ci-check", CustomTokenName(Role("ci-check")))
}

func TestParseRegistryRejectsSecretNameCollision(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[
		{"name":"ci-check","credential":"own"},
		{"name":"ci_check","credential":"own"}
	]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "FULLSEND_GITLAB_ROLE_CI_CHECK_TOKEN")
}

func TestParseRegistryRejectsTrailingData(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"roles":[]}{"roles":[]}`,
		`{"roles":[]} junk`,
	} {
		_, err := ParseRegistry(raw)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidRegistry)
		assert.Contains(t, err.Error(), "trailing data")
	}
}

func TestParseRegistryRejectsNameStealingBuiltinAgentAlias(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"review","credential":"own"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "collides with agent mapping")
	reg := BuiltinRegistry()
	rec, ok := reg.RoleFor("review")
	require.True(t, ok)
	assert.Equal(t, RoleAnalyst, rec.Name)
}

func TestParseRegistryRejectsAgentStealingEarlierCustomRoleName(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[
		{"name":"scanner"},
		{"name":"other","agents":["scanner"]}
	]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "already mapped")

	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner"}]}`)
	rec, ok := reg.RoleFor("scanner")
	require.True(t, ok)
	assert.Equal(t, Role("scanner"), rec.Name)
}

func TestParseRegistryRejectsSecretValueAsName(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"glpat-secretvalue"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryRejectsSecretValueAsReuse(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"reuse","reuse":"glpat-secretvalue"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryRejectsSecretValueAsCredential(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","credential":"glpat-secretvalue"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryRejectsSecretValueAsCapability(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","capabilities":["glpat-secretvalue"]}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryRejectsSecretValueAsAgent(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","agents":["glpat-secretvalue"]}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryRejectsSecretValueAsResponsibility(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","responsibility":"glpat-secretvalue"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.NotContains(t, err.Error(), "glpat-secretvalue")
}

func TestParseRegistryExplicitMatchingSecretName(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{
		"roles": [{
			"name": "scanner",
			"secret_name": "FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"
		}]
	}`)
	rec, ok := reg.Lookup(Role("scanner"))
	require.True(t, ok)
	assert.Equal(t, "FULLSEND_GITLAB_ROLE_SCANNER_TOKEN", rec.Credential.SecretName)
}

func TestParseRegistryReuseSecretNameMustMatch(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{
		"name":"deployer",
		"credential":"reuse",
		"reuse":"coder",
		"secret_name":"FULLSEND_GITLAB_POLLER_TOKEN"
	}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestRoleForSecretUnknown(t *testing.T) {
	t.Parallel()
	role, ok := BuiltinRegistry().roleForSecret(forge.SecretForgeToken)
	assert.False(t, ok)
	assert.Empty(t, role)
}

func TestLooksLikeSecretValue(t *testing.T) {
	t.Parallel()
	assert.False(t, looksLikeSecretValue(""))
	assert.False(t, looksLikeSecretValue("FULLSEND_GITLAB_ROLE_SCANNER_TOKEN"))
	assert.True(t, looksLikeSecretValue("glpat-abc"))
	assert.True(t, looksLikeSecretValue("GLPAT-abc"))
	assert.True(t, looksLikeSecretValue("gldt-abc"))
}

func TestParseRegistryRejectsInvalidSecretNameChars(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","secret_name":"not a var"}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestParseRegistryRejectsInvalidAgentName(t *testing.T) {
	t.Parallel()
	_, err := ParseRegistry(`{"roles":[{"name":"scanner","agents":["Nope"]}]}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
}

func TestRegistrationHasEmpty(t *testing.T) {
	t.Parallel()
	assert.False(t, Registration{}.Has(CapReadIssues))
}

func TestNewRegistryRejectsInvalidCredentialKind(t *testing.T) {
	t.Parallel()
	_, err := newRegistry([]Registration{{
		Name:       Role("scanner"),
		Kind:       RoleKindCustom,
		Credential: CredentialRef{Kind: CredentialKind("inline")},
	}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "invalid credential kind")
}

func TestNewRegistryRejectsOwnWithReuseTarget(t *testing.T) {
	t.Parallel()
	_, err := newRegistry([]Registration{{
		Name: Role("scanner"),
		Kind: RoleKindCustom,
		Credential: CredentialRef{
			Kind:    CredentialOwn,
			ReuseOf: RoleCoder,
		},
	}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRegistry)
	assert.Contains(t, err.Error(), "cannot name a reuse target")
}

func TestChainedReuseResolvesToBuiltinSecret(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{"roles":[
		{"name":"mid","credential":"reuse","reuse":"coder"},
		{"name":"leaf","credential":"reuse","reuse":"mid","agents":["leaf"]}
	]}`)
	leaf, ok := reg.Lookup(Role("leaf"))
	require.True(t, ok)
	assert.Equal(t, forge.SecretGitLabCoderToken, leaf.Credential.SecretName)
	src, err := Resolve(Request{
		Mode:     ModeEnforced,
		Job:      AgentJob("leaf"),
		Registry: reg,
		Present:  map[string]bool{forge.SecretGitLabCoderToken: true},
	})
	require.NoError(t, err)
	assert.Equal(t, forge.SecretGitLabCoderToken, src.SecretName)
	assert.True(t, src.Reused)
}

func TestResolveCustomUnconfiguredMigratingFallback(t *testing.T) {
	t.Parallel()
	reg := mustParseRegistry(t, `{"roles":[{"name":"scanner","agents":["scanner"]}]}`)
	src, err := Resolve(Request{
		Mode:     ModeMigrating,
		Job:      AgentJob("scanner"),
		Registry: reg,
		Present:  map[string]bool{forge.SecretForgeToken: true},
	})
	require.NoError(t, err)
	assert.Equal(t, Role("scanner"), src.Role)
	assert.Equal(t, RoleKindCustom, src.Kind)
	assert.True(t, src.Shared)
	assert.True(t, src.Fallback)
}

func TestResolveMalformedRegistryMissingPoller(t *testing.T) {
	t.Parallel()
	reg := Registry{
		roles:   []Registration{{Name: Role("only"), Kind: RoleKindCustom}},
		byName:  map[Role]int{Role("only"): 0},
		byAgent: map[string]Role{},
	}
	_, err := Resolve(Request{
		Mode:     ModeEnforced,
		Job:      PollerJob(),
		Registry: reg,
		Present:  map[string]bool{forge.SecretForgeToken: true},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnregistered)
}

func TestLoadRegistryEmpty(t *testing.T) {
	t.Parallel()
	reg, err := LoadRegistry(func(string) string { return "" })
	require.NoError(t, err)
	assert.Len(t, reg.Registrations(), 3)
}

func TestMarshalCustomRolesRoundTrip(t *testing.T) {
	t.Parallel()
	raw := `{
		"roles": [
			{
				"name": "scanner",
				"responsibility": "read-only scanning",
				"credential": "own",
				"capabilities": ["read_issues", "write_notes"],
				"agents": ["scanner"]
			},
			{
				"name": "deployer",
				"credential": "reuse",
				"reuse": "coder",
				"capabilities": ["write_repository", "write_merge_request"],
				"agents": ["deploy"]
			}
		]
	}`
	reg := mustParseRegistry(t, raw)
	got, err := MarshalCustomRoles(reg)
	require.NoError(t, err)
	assert.NotContains(t, got, "glpat-")
	assert.NotContains(t, got, forge.SecretGitLabCoderToken)

	round, err := ParseRegistry(got)
	require.NoError(t, err)
	scanner, ok := round.Lookup(Role("scanner"))
	require.True(t, ok)
	assert.Equal(t, RoleKindCustom, scanner.Kind)
	assert.Equal(t, CredentialOwn, scanner.Credential.Kind)
	assert.Equal(t, CustomSecretName(Role("scanner")), scanner.Credential.SecretName)
	deployer, ok := round.Lookup(Role("deployer"))
	require.True(t, ok)
	assert.Equal(t, CredentialReuse, deployer.Credential.Kind)
	assert.Equal(t, RoleCoder, deployer.Credential.ReuseOf)
	assert.Equal(t, forge.SecretGitLabCoderToken, deployer.Credential.SecretName)
}

func TestMarshalCustomRolesBuiltinsOnly(t *testing.T) {
	t.Parallel()
	got, err := MarshalCustomRoles(BuiltinRegistry())
	require.NoError(t, err)
	assert.Equal(t, `{"roles":[]}`, got)
	reg, err := ParseRegistry(got)
	require.NoError(t, err)
	assert.Equal(t, BuiltinRoles(), roleNames(reg))
}

func TestIsRoleProjectTokenName(t *testing.T) {
	t.Parallel()
	assert.True(t, IsRoleProjectTokenName(PollerTokenName))
	assert.True(t, IsRoleProjectTokenName(AnalystTokenName))
	assert.True(t, IsRoleProjectTokenName(CoderTokenName))
	assert.True(t, IsRoleProjectTokenName(CustomTokenName(Role("scanner"))))
	assert.False(t, IsRoleProjectTokenName(SharedTokenName))
	assert.False(t, IsRoleProjectTokenName("other-token"))
	assert.False(t, IsRoleProjectTokenName(""))
}

func roleNames(reg Registry) []Role {
	out := make([]Role, 0, 3)
	for _, rec := range reg.Registrations() {
		out = append(out, rec.Name)
	}
	return out
}
