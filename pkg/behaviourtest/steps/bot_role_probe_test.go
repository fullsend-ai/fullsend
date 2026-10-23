package steps

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// testSuiteBotLogin stands in for the suite's e2e App identity.
const testSuiteBotLogin = "suite-e2e-app[bot]"

const (
	testBotRoleRepo = "halfsend-03/test-repo-07"
	testBotRoleHost = "test-repo-07-mint-host"
)

// useSuiteLogin overrides the suite identity lookup for one test.
func useSuiteLogin(t *testing.T, login string, err error) {
	t.Helper()
	orig := botRoleSuiteLoginFn
	botRoleSuiteLoginFn = func(context.Context, *world.World) (string, error) { return login, err }
	t.Cleanup(func() { botRoleSuiteLoginFn = orig })
}

// defaultAppSet pins the behaviour app set to its default for one test.
func defaultAppSet(t *testing.T) {
	t.Helper()
	t.Setenv("BEHAVIOUR_APP_SET", "")
}

// fakeBotRoleProbeCI serves one probe run through ci.RunLister and writes
// the configured results as artifacts.
type fakeBotRoleProbeCI struct {
	ci.Driver

	run     *forge.WorkflowRun
	scm     *fakeMintProbeSCM
	results map[string]*world.BotRoleProbeResult
}

func (f *fakeBotRoleProbeCI) ListRecentRuns(context.Context, string, string, int) ([]forge.WorkflowRun, error) {
	if f.run == nil {
		return nil, nil
	}
	run := *f.run
	for key := range f.scm.branchFiles {
		if strings.Contains(key, ".github/workflows/bot-role-probe-") {
			run.Name = strings.TrimSuffix(filepath.Base(key), ".yml")
		}
	}
	return []forge.WorkflowRun{run}, nil
}

func (f *fakeBotRoleProbeCI) DownloadNamedArtifactFromRun(_ context.Context, _, _ string, _ int, artifact, destDir string) error {
	res, ok := f.results[artifact]
	if !ok {
		return fmt.Errorf("artifact %q not found", artifact)
	}
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}
	dir := filepath.Join(destDir, artifact)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, botRoleProbeResultFile), data, 0o600)
}

// botRoleResult is a well-formed result of a lookup the mint answered.
func botRoleResult(mode, login string, status int, resp *world.BotRoleProbeResponse) *world.BotRoleProbeResult {
	return &world.BotRoleProbeResult{
		Mode:           mode,
		ActorLogin:     login,
		Repo:           testBotRoleRepo,
		OIDCVerified:   true,
		Repository:     testBotRoleRepo,
		JobWorkflowRef: "halfsend-03/" + testBotRoleHost + "/.github/workflows/bot-role-probe.yml@refs/heads/main",
		HTTPStatus:     status,
		Response:       resp,
	}
}

func recognizedResult(login, role string) *world.BotRoleProbeResult {
	return botRoleResult(botRoleModeLookup, login, 200, &world.BotRoleProbeResponse{Outcome: "recognized", Role: role})
}

func noMatchResult(login string) *world.BotRoleProbeResult {
	return botRoleResult(botRoleModeLookup, login, 200, &world.BotRoleProbeResponse{Outcome: "no_match"})
}

func forcedFailureResult(login string, status int, msg string) *world.BotRoleProbeResult {
	return botRoleResult(botRoleModeForcedFailure, login, status, &world.BotRoleProbeResponse{Error: msg})
}

// botRoleWorld returns a world whose probe for identity already completed
// with the given control and subject results.
func botRoleWorld(t *testing.T, identity string, control, subject *world.BotRoleProbeResult) *world.World {
	t.Helper()
	w := mintProbeTestWorld(nil, nil)
	id, err := resolveBotRoleIdentity(context.Background(), w, identity)
	require.NoError(t, err)
	controlLogin, err := registeredBotLogin(botRoleControlRole)
	require.NoError(t, err)
	w.BotRoleProbe = &world.BotRoleProbe{
		Forge:          "github",
		HostRepo:       testBotRoleHost,
		Identity:       identity,
		Mode:           id.mode,
		ActorLogin:     id.login,
		ClaimedRole:    id.claimedRole,
		RegisteredRole: id.registeredRole,
		ControlLogin:   controlLogin,
		Run:            &forge.WorkflowRun{ID: 9, Status: "completed", Conclusion: "success"},
		Control:        control,
		Subject:        subject,
	}
	return w
}

// subjectForRow returns the mint's answer a correct mint gives the row.
func subjectForRow(t *testing.T, identity string) *world.BotRoleProbeResult {
	t.Helper()
	id, err := resolveBotRoleIdentity(context.Background(), mintProbeTestWorld(nil, nil), identity)
	require.NoError(t, err)
	switch {
	case id.mode == botRoleModeForcedFailure:
		return forcedFailureResult(id.login, 401, mintProbeAuthFailedMessage)
	case id.registeredRole != "":
		return recognizedResult(id.login, id.registeredRole)
	default:
		res := noMatchResult(id.login)
		res.ClaimedRole = id.claimedRole
		return res
	}
}

// --- feature file ---

// TestBotRoleLookupFeature_RowsMatchSteps expands every Examples row of the
// bot-role lookup feature, checks that each step matches a registered
// pattern, and runs the row's Then steps against the answer a correct
// mint gives, so a typo in the table fails here rather than after a lease.
func TestBotRoleLookupFeature_RowsMatchSteps(t *testing.T) {
	defaultAppSet(t)
	useSuiteLogin(t, testSuiteBotLogin, nil)

	f, err := os.Open(filepath.Join("..", "..", "..", "e2e", "behaviour", "features", "platform", "bot-role-lookup.feature"))
	require.NoError(t, err)
	defer f.Close()

	var steps, header []string
	var rows [][]string
	inOutline := false
	scanner := bufio.NewScanner(f)
	firstLine := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if firstLine == "" {
			firstLine = line
		}
		switch {
		case strings.HasPrefix(line, "Scenario Outline:"):
			inOutline = true
		case inOutline && (strings.HasPrefix(line, "Given ") || strings.HasPrefix(line, "When ") ||
			strings.HasPrefix(line, "Then ") || strings.HasPrefix(line, "And ")):
			steps = append(steps, line[strings.Index(line, " ")+1:])
		case strings.HasPrefix(line, "|"):
			var cells []string
			for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
				cells = append(cells, strings.TrimSpace(c))
			}
			if header == nil {
				header = cells
			} else {
				rows = append(rows, cells)
			}
		}
	}
	require.NoError(t, scanner.Err())
	assert.Equal(t, "@skip:gitlab", firstLine)
	require.Equal(t, []string{"forge", "identity", "outcome", "kind", "role", "bot_role", "verified", "dispatch"}, header)
	require.NotEmpty(t, rows)

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^the enrolled test repository$`),
		regexp.MustCompile(`^a "([^"]*)" bot-role lookup probe$`),
		regexp.MustCompile(`^the lookup resolves identity "([^"]*)" for the leased repository$`),
		regexp.MustCompile(`^the lookup outcome is "([^"]*)"$`),
		regexp.MustCompile(`^the actor is kind "([^"]*)" role "([^"]*)" bot_role "([^"]*)" role_verified "([^"]*)"$`),
		regexp.MustCompile(`^non-label dispatch is "([^"]*)"$`),
	}

	outcomes := map[string]bool{}
	for _, row := range rows {
		require.Len(t, row, len(header))
		values := map[string]string{}
		for i, h := range header {
			values[h] = row[i]
		}
		for _, step := range steps {
			expanded := step
			for h, v := range values {
				expanded = strings.ReplaceAll(expanded, "<"+h+">", v)
			}
			assert.NotContains(t, expanded, "<", "unexpanded placeholder in %q", expanded)
			matched := false
			for _, p := range patterns {
				matched = matched || p.MatchString(expanded)
			}
			assert.True(t, matched, "no step matches %q", expanded)
		}
		assert.Equal(t, "github", values["forge"])
		outcomes[values["outcome"]] = true

		t.Run(values["identity"], func(t *testing.T) {
			controlLogin, err := registeredBotLogin(botRoleControlRole)
			require.NoError(t, err)
			w := botRoleWorld(t, values["identity"], recognizedResult(controlLogin, "review"), subjectForRow(t, values["identity"]))
			require.NoError(t, thenBotRoleOutcome(w, values["outcome"]))
			require.NoError(t, thenBotRoleActor(w, values["kind"], values["role"], values["bot_role"], values["verified"]))
			require.NoError(t, thenBotRoleDispatch(w, values["dispatch"]))
		})
	}
	for _, o := range []string{botRoleOutcomeRecognized, botRoleOutcomeNoMatch, botRoleOutcomeLookupFailed} {
		assert.True(t, outcomes[o], "no row expects %q", o)
	}
}

// --- workflows ---

func TestBotRoleProbeWorkflow_InputsMatchShim(t *testing.T) {
	var reusable struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]any `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(botRoleProbeWorkflow, &reusable))
	assert.Equal(t, "write", reusable.Jobs["probe"].Permissions["id-token"])

	// Same pinned upload action as the mint probe.
	var mintReusable struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(mintProbeWorkflow, &mintReusable))
	require.Len(t, reusable.Jobs["probe"].Steps, 2)
	assert.Equal(t, mintReusable.Jobs["probe"].Steps[1].Uses, reusable.Jobs["probe"].Steps[1].Uses)

	shim, err := renderBotRoleProbeShim(botRoleProbeShimSpec{
		Name: "bot-role-probe-0a1b2c3d", Branch: "behaviour/bot-role-probe-0a1b2c3d", Org: "halfsend-03",
		Host: testBotRoleHost, HostRef: "main", Repo: testBotRoleRepo,
		ControlLogin: "fullsend-test-review[bot]",
		SubjectLogin: testSuiteBotLogin, SubjectMode: botRoleModeLookup, SubjectClaimedRole: "review",
	})
	require.NoError(t, err)

	var parsed struct {
		Name string `yaml:"name"`
		On   struct {
			Push struct {
				Branches []string `yaml:"branches"`
			} `yaml:"push"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Uses        string            `yaml:"uses"`
			With        map[string]string `yaml:"with"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(shim, &parsed), string(shim))
	assert.Equal(t, "bot-role-probe-0a1b2c3d", parsed.Name)
	assert.Equal(t, []string{"behaviour/bot-role-probe-0a1b2c3d"}, parsed.On.Push.Branches)
	assert.Empty(t, parsed.Permissions)
	require.Len(t, parsed.Jobs, 2)

	control, subject := parsed.Jobs["control"], parsed.Jobs["subject"]
	for name, job := range parsed.Jobs {
		assert.Equal(t, "halfsend-03/test-repo-07-mint-host/.github/workflows/bot-role-probe.yml@main", job.Uses, name)
		assert.Equal(t, "write", job.Permissions["id-token"], name)
		assert.Equal(t, "${{ vars.FULLSEND_MINT_URL }}", job.With["mint_url"], name)
		assert.Equal(t, testBotRoleRepo, job.With["repo"], name)
		for input := range job.With {
			assert.Contains(t, reusable.On.WorkflowCall.Inputs, input, "shim passes input %q the probe does not declare", input)
		}
		for input := range reusable.On.WorkflowCall.Inputs {
			assert.Contains(t, job.With, input, "shim omits probe input %q", input)
		}
	}
	assert.Equal(t, "fullsend-test-review[bot]", control.With["actor_login"])
	assert.Equal(t, botRoleModeLookup, control.With["mode"])
	assert.Empty(t, control.With["claimed_role"])
	assert.Equal(t, botRoleProbeControlArtifact, control.With["artifact_name"])
	assert.Equal(t, testSuiteBotLogin, subject.With["actor_login"])
	assert.Equal(t, "review", subject.With["claimed_role"])
	assert.Equal(t, botRoleProbeSubjectArtifact, subject.With["artifact_name"])
}

func TestRenderBotRoleProbeShim_RejectsUnsafeValues(t *testing.T) {
	base := botRoleProbeShimSpec{
		Name: "bot-role-probe-1", Branch: "behaviour/bot-role-probe-1", Org: "org",
		Host: "h", HostRef: "main", Repo: "org/r",
		ControlLogin: "app-review[bot]", SubjectLogin: "other[bot]", SubjectMode: botRoleModeLookup,
	}
	_, err := renderBotRoleProbeShim(base)
	require.NoError(t, err)
	for name, mutate := range map[string]func(*botRoleProbeShimSpec){
		"name":             func(s *botRoleProbeShimSpec) { s.Name = "a b" },
		"org":              func(s *botRoleProbeShimSpec) { s.Org = "org/evil" },
		"host":             func(s *botRoleProbeShimSpec) { s.Host = "h'x" },
		"repo":             func(s *botRoleProbeShimSpec) { s.Repo = "org" },
		"repo quote":       func(s *botRoleProbeShimSpec) { s.Repo = "org/r'\nx: y" },
		"ref":              func(s *botRoleProbeShimSpec) { s.HostRef = "main'" },
		"branch":           func(s *botRoleProbeShimSpec) { s.Branch = "" },
		"login no bot":     func(s *botRoleProbeShimSpec) { s.SubjectLogin = "octocat" },
		"login injection":  func(s *botRoleProbeShimSpec) { s.SubjectLogin = "x'[bot]" },
		"control login":    func(s *botRoleProbeShimSpec) { s.ControlLogin = "a_b[bot]" },
		"mode":             func(s *botRoleProbeShimSpec) { s.SubjectMode = "Lookup" },
		"claimed role":     func(s *botRoleProbeShimSpec) { s.SubjectClaimedRole = "Review" },
		"claimed role yml": func(s *botRoleProbeShimSpec) { s.SubjectClaimedRole = "review'\nx: y" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := base
			mutate(&spec)
			_, err := renderBotRoleProbeShim(spec)
			assert.Error(t, err)
		})
	}
	// Bot logins are case-insensitive.
	spec := base
	spec.SubjectLogin = "Some-App[BOT]"
	_, err = renderBotRoleProbeShim(spec)
	assert.NoError(t, err)
}

// --- identity resolution ---

func TestResolveBotRoleIdentity(t *testing.T) {
	defaultAppSet(t)
	useSuiteLogin(t, testSuiteBotLogin, nil)
	w := mintProbeTestWorld(nil, nil)
	ctx := context.Background()

	id, err := resolveBotRoleIdentity(ctx, w, "registered:review")
	require.NoError(t, err)
	assert.Equal(t, botRoleIdentity{login: "fullsend-test-review[bot]", mode: botRoleModeLookup, registeredRole: "review"}, id)

	id, err = resolveBotRoleIdentity(ctx, w, "registered:coder")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-test-coder[bot]", id.login)

	id, err = resolveBotRoleIdentity(ctx, w, "forced-failure")
	require.NoError(t, err)
	assert.Equal(t, botRoleIdentity{login: "fullsend-test-review[bot]", mode: botRoleModeForcedFailure, registeredRole: "review"}, id)

	id, err = resolveBotRoleIdentity(ctx, w, "unregistered-e2e")
	require.NoError(t, err)
	assert.Equal(t, botRoleIdentity{login: testSuiteBotLogin, mode: botRoleModeLookup}, id)

	id, err = resolveBotRoleIdentity(ctx, w, "claimed-in-event")
	require.NoError(t, err)
	assert.Equal(t, botRoleIdentity{login: testSuiteBotLogin, mode: botRoleModeLookup, claimedRole: "review"}, id)

	_, err = resolveBotRoleIdentity(ctx, w, "registered:e2e")
	assert.ErrorContains(t, err, "not a registered fullsend-test role")
	_, err = resolveBotRoleIdentity(ctx, w, "registered:")
	assert.ErrorContains(t, err, "not a registered fullsend-test role")
	_, err = resolveBotRoleIdentity(ctx, w, "someone")
	assert.ErrorContains(t, err, "unknown identity")
}

func TestResolveBotRoleIdentity_AppSet(t *testing.T) {
	useSuiteLogin(t, testSuiteBotLogin, nil)
	t.Setenv("BEHAVIOUR_APP_SET", "other-set")
	id, err := resolveBotRoleIdentity(context.Background(), mintProbeTestWorld(nil, nil), "registered:triage")
	require.NoError(t, err)
	assert.Equal(t, "other-set-triage[bot]", id.login)

	t.Setenv("BEHAVIOUR_APP_SET", "bad set")
	_, err = resolveBotRoleIdentity(context.Background(), mintProbeTestWorld(nil, nil), "registered:triage")
	assert.ErrorContains(t, err, "invalid bot login")
}

func TestResolveBotRoleIdentity_SuiteIdentityFailures(t *testing.T) {
	defaultAppSet(t)
	w := mintProbeTestWorld(nil, nil)
	for name, tc := range map[string]struct {
		login string
		err   error
		want  string
	}{
		"human suite identity":      {login: "octocat", want: "not a GitHub App bot login"},
		"bot suffix in the middle":  {login: "x[bot]y", want: "not a GitHub App bot login"},
		"lookup error":              {err: errors.New("rate limited"), want: "rate limited"},
		"suite is a registered bot": {login: "Fullsend-Test-Review[bot]", want: "registered review bot"},
	} {
		for _, identity := range []string{botRoleIdentityUnregisteredE2E, botRoleIdentityClaimedInEvent} {
			t.Run(name+"/"+identity, func(t *testing.T) {
				useSuiteLogin(t, tc.login, tc.err)
				_, err := resolveBotRoleIdentity(context.Background(), w, identity)
				require.Error(t, err)
				assert.ErrorContains(t, err, "infrastructure failure")
				assert.ErrorContains(t, err, tc.want)
			})
		}
	}
}

func TestBotRoleSuiteLogin_Default(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.AuthenticatedUser = testSuiteBotLogin
	w := mintProbeTestWorld(scmgh.New(fc), nil)
	login, err := botRoleSuiteLoginFn(context.Background(), w)
	require.NoError(t, err)
	assert.Equal(t, testSuiteBotLogin, login)

	_, err = botRoleSuiteLoginFn(context.Background(), mintProbeTestWorld(newFakeMintProbeSCM(), nil))
	assert.ErrorContains(t, err, "GitHub SCM driver")
}

// --- classification ---

func TestClassifyBotRoleResult(t *testing.T) {
	login := "fullsend-test-review[bot]"
	unverified := forcedFailureResult(login, 401, mintProbeAuthFailedMessage)
	unverified.OIDCVerified = false
	unverifiedLookup := noMatchResult(login)
	unverifiedLookup.OIDCVerified = false
	noStatus := noMatchResult(login)
	noStatus.HTTPStatus = 0
	for _, tc := range []struct {
		name    string
		res     *world.BotRoleProbeResult
		mode    string
		outcome string
		infra   string
		role    string
	}{
		{name: "recognized", res: recognizedResult(login, "review"), mode: botRoleModeLookup, outcome: botRoleOutcomeRecognized, role: "review"},
		{name: "no match", res: noMatchResult(login), mode: botRoleModeLookup, outcome: botRoleOutcomeNoMatch},
		{name: "missing result", res: nil, mode: botRoleModeLookup, infra: "result-artifact"},
		{name: "config error", res: &world.BotRoleProbeResult{Mode: botRoleModeLookup, Error: "config"}, mode: botRoleModeLookup, infra: "config"},
		{name: "oidc error", res: &world.BotRoleProbeResult{Mode: botRoleModeForcedFailure, Error: "oidc"}, mode: botRoleModeForcedFailure, infra: "oidc"},
		{name: "transport error", res: &world.BotRoleProbeResult{Mode: botRoleModeLookup, Error: "transport", OIDCVerified: true}, mode: botRoleModeLookup, infra: "transport"},
		{name: "unknown probe error", res: &world.BotRoleProbeResult{Mode: botRoleModeLookup, Error: "weird"}, mode: botRoleModeLookup, infra: "result-artifact"},
		{name: "mode mismatch", res: noMatchResult(login), mode: botRoleModeForcedFailure, infra: "result-artifact"},
		{name: "no HTTP status", res: noStatus, mode: botRoleModeLookup, infra: "result-artifact"},
		{name: "lookup unverified", res: unverifiedLookup, mode: botRoleModeLookup, infra: "oidc-unverified"},
		{name: "lookup 401", res: botRoleResult(botRoleModeLookup, login, 401, &world.BotRoleProbeResponse{Error: mintProbeAuthFailedMessage}), mode: botRoleModeLookup, infra: "lookup"},
		{name: "lookup 502", res: botRoleResult(botRoleModeLookup, login, 502, &world.BotRoleProbeResponse{Error: "bot-role lookup failed"}), mode: botRoleModeLookup, infra: "lookup"},
		{name: "lookup 403", res: botRoleResult(botRoleModeLookup, login, 403, &world.BotRoleProbeResponse{Error: "repo must be the requesting repository"}), mode: botRoleModeLookup, infra: "lookup"},
		{name: "lookup 200 empty body", res: botRoleResult(botRoleModeLookup, login, 200, nil), mode: botRoleModeLookup, infra: "lookup"},
		{name: "lookup 200 unknown outcome", res: botRoleResult(botRoleModeLookup, login, 200, &world.BotRoleProbeResponse{Outcome: "maybe"}), mode: botRoleModeLookup, infra: "lookup"},
		{name: "lookup recognized bad role", res: recognizedResult(login, "Review!"), mode: botRoleModeLookup, infra: "lookup"},
		{name: "lookup no_match with role", res: botRoleResult(botRoleModeLookup, login, 200, &world.BotRoleProbeResponse{Outcome: "no_match", Role: "review"}), mode: botRoleModeLookup, infra: "lookup"},
		{name: "forced failure 401", res: forcedFailureResult(login, 401, mintProbeAuthFailedMessage), mode: botRoleModeForcedFailure, outcome: botRoleOutcomeLookupFailed},
		{name: "forced failure unverified", res: unverified, mode: botRoleModeForcedFailure, infra: "oidc-unverified"},
		{name: "forced failure other 401", res: forcedFailureResult(login, 401, "Bad credentials"), mode: botRoleModeForcedFailure, infra: "unexpected-401"},
		{name: "forced failure 500", res: forcedFailureResult(login, 500, "internal"), mode: botRoleModeForcedFailure, infra: "server"},
		{name: "forced failure 403", res: forcedFailureResult(login, 403, "repo must be the requesting repository"), mode: botRoleModeForcedFailure, infra: "server"},
		{name: "forced failure 200", res: botRoleResult(botRoleModeForcedFailure, login, 200, &world.BotRoleProbeResponse{Outcome: "recognized", Role: "review"}), mode: botRoleModeForcedFailure, outcome: botRoleAcceptedInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyBotRoleResult(tc.res, tc.mode)
			assert.Equal(t, tc.outcome, v.outcome, v.detail)
			assert.Equal(t, tc.infra, v.infra, v.detail)
			assert.Equal(t, tc.role, v.result.Role)
			if v.outcome == botRoleOutcomeLookupFailed {
				assert.ErrorIs(t, v.lookupErr, botidentity.ErrLookup)
			} else {
				assert.NoError(t, v.lookupErr)
			}
		})
	}
}

func TestParseBotRoleProbeResult(t *testing.T) {
	res, err := parseBotRoleProbeResult([]byte(`{"mode":"lookup","actor_login":"a[bot]","repo":"o/r","oidc_verified":true,"http_status":200,"response":{"outcome":"no_match"}}`))
	require.NoError(t, err)
	assert.Equal(t, "no_match", res.Response.Outcome)
	_, err = parseBotRoleProbeResult([]byte(`{"mode":"guess"}`))
	assert.ErrorContains(t, err, "unknown mode")
	_, err = parseBotRoleProbeResult([]byte(`not json`))
	assert.ErrorContains(t, err, "parsing")
}

func TestCheckBotRoleClaims(t *testing.T) {
	login := "fullsend-test-review[bot]"
	ok := recognizedResult(login, "review")
	require.NoError(t, checkBotRoleClaims(ok, testBotRoleRepo, "halfsend-03", testBotRoleHost, login))
	assert.NoError(t, checkBotRoleClaims(ok, strings.ToUpper(testBotRoleRepo), "HALFSEND-03", testBotRoleHost, strings.ToUpper(login)))

	for name, mutate := range map[string]func(*world.BotRoleProbeResult){
		"repository claim": func(r *world.BotRoleProbeResult) { r.Repository = "halfsend-03/other" },
		"lookup repo":      func(r *world.BotRoleProbeResult) { r.Repo = "halfsend-03/other" },
		"workflow host": func(r *world.BotRoleProbeResult) {
			r.JobWorkflowRef = "halfsend-03/elsewhere/.github/workflows/bot-role-probe.yml@main"
		},
		"workflow file": func(r *world.BotRoleProbeResult) {
			r.JobWorkflowRef = "halfsend-03/" + testBotRoleHost + "/.github/workflows/mint-probe.yml@main"
		},
		"login": func(r *world.BotRoleProbeResult) { r.ActorLogin = "someone-else[bot]" },
	} {
		t.Run(name, func(t *testing.T) {
			res := *ok
			mutate(&res)
			assert.Error(t, checkBotRoleClaims(&res, testBotRoleRepo, "halfsend-03", testBotRoleHost, login))
		})
	}
}

// --- Then steps ---

func TestThenBotRoleOutcome_InfrastructureFailures(t *testing.T) {
	defaultAppSet(t)
	useSuiteLogin(t, testSuiteBotLogin, nil)
	review := "fullsend-test-review[bot]"
	control := recognizedResult(review, "review")

	for _, tc := range []struct {
		name     string
		identity string
		control  *world.BotRoleProbeResult
		subject  *world.BotRoleProbeResult
		expected string
		want     string
	}{
		{name: "control no match", identity: "unregistered-e2e", control: noMatchResult(review), subject: noMatchResult(testSuiteBotLogin), expected: "no-match", want: "control: infrastructure failure"},
		{name: "control recognized as another role", identity: "unregistered-e2e", control: recognizedResult(review, "coder"), subject: noMatchResult(testSuiteBotLogin), expected: "no-match", want: "control: infrastructure failure"},
		{name: "control oidc error", identity: "unregistered-e2e", control: &world.BotRoleProbeResult{Mode: botRoleModeLookup, Error: "oidc"}, subject: noMatchResult(testSuiteBotLogin), expected: "no-match", want: "control: infrastructure failure (oidc)"},
		{name: "control unverified", identity: "unregistered-e2e", control: func() *world.BotRoleProbeResult {
			r := recognizedResult(review, "review")
			r.OIDCVerified = false
			return r
		}(), subject: noMatchResult(testSuiteBotLogin), expected: "no-match", want: "oidc-unverified"},
		{name: "control wrong repository claim", identity: "unregistered-e2e", control: func() *world.BotRoleProbeResult {
			r := recognizedResult(review, "review")
			r.Repository = "halfsend-03/other"
			return r
		}(), subject: noMatchResult(testSuiteBotLogin), expected: "no-match", want: "OIDC repository claim"},
		{name: "subject probe error", identity: "unregistered-e2e", control: control, subject: &world.BotRoleProbeResult{Mode: botRoleModeLookup, Error: "transport", OIDCVerified: true}, expected: "no-match", want: "infrastructure failure (transport)"},
		{name: "subject oidc unverified", identity: "unregistered-e2e", control: control, subject: func() *world.BotRoleProbeResult {
			r := noMatchResult(testSuiteBotLogin)
			r.OIDCVerified = false
			return r
		}(), expected: "no-match", want: "oidc-unverified"},
		{name: "subject lookup error", identity: "unregistered-e2e", control: control, subject: botRoleResult(botRoleModeLookup, testSuiteBotLogin, 502, &world.BotRoleProbeResponse{Error: "bot-role lookup failed"}), expected: "no-match", want: "infrastructure failure (lookup)"},
		{name: "lookup error cannot satisfy lookup-failed", identity: "unregistered-e2e", control: control, subject: botRoleResult(botRoleModeLookup, testSuiteBotLogin, 401, &world.BotRoleProbeResponse{Error: mintProbeAuthFailedMessage}), expected: "lookup-failed", want: "infrastructure failure (lookup)"},
		{name: "subject wrong login", identity: "unregistered-e2e", control: control, subject: noMatchResult("other[bot]"), expected: "no-match", want: "looked-up login"},
		{name: "forced failure answered 500", identity: "forced-failure", control: control, subject: forcedFailureResult(review, 500, "internal"), expected: "lookup-failed", want: "infrastructure failure (server)"},
		{name: "forced failure unverified", identity: "forced-failure", control: control, subject: func() *world.BotRoleProbeResult {
			r := forcedFailureResult(review, 401, mintProbeAuthFailedMessage)
			r.OIDCVerified = false
			return r
		}(), expected: "lookup-failed", want: "oidc-unverified"},
		{name: "forced failure oidc error", identity: "forced-failure", control: control, subject: &world.BotRoleProbeResult{Mode: botRoleModeForcedFailure, Error: "oidc"}, expected: "lookup-failed", want: "infrastructure failure (oidc)"},
		{name: "forced failure answered 200", identity: "forced-failure", control: control, subject: botRoleResult(botRoleModeForcedFailure, review, 200, &world.BotRoleProbeResponse{Outcome: "recognized", Role: "review"}), expected: "lookup-failed", want: "accepted a credential with an invalid signature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := botRoleWorld(t, tc.identity, tc.control, tc.subject)
			err := thenBotRoleOutcome(w, tc.expected)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
			// The actor and dispatch steps fail the same way.
			assert.Error(t, thenBotRoleActor(w, "bot", "none", "none", "true"))
			assert.Error(t, thenBotRoleDispatch(w, "denied-before-cel"))
		})
	}
	t.Run("forced failure 200 is not an infrastructure failure", func(t *testing.T) {
		w := botRoleWorld(t, "forced-failure", control, botRoleResult(botRoleModeForcedFailure, review, 200, &world.BotRoleProbeResponse{Outcome: "recognized", Role: "review"}))
		err := thenBotRoleOutcome(w, "lookup-failed")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "infrastructure failure")
	})
}

func TestThenBotRoleOutcome_Mismatches(t *testing.T) {
	defaultAppSet(t)
	useSuiteLogin(t, testSuiteBotLogin, nil)
	review := "fullsend-test-review[bot]"
	control := recognizedResult(review, "review")

	// An unregistered bot the mint recognizes is a test failure.
	w := botRoleWorld(t, "unregistered-e2e", control, recognizedResult(testSuiteBotLogin, "review"))
	assert.ErrorContains(t, thenBotRoleOutcome(w, "no-match"), `outcome is "recognized"`)
	assert.ErrorContains(t, thenBotRoleDispatch(w, "denied-before-cel"), `is "allowed"`)
	assert.ErrorContains(t, thenBotRoleActor(w, "bot", "none", "none", "true"), "bot_role")

	// A registered bot recognized as another role.
	w = botRoleWorld(t, "registered:review", control, recognizedResult(review, "coder"))
	assert.ErrorContains(t, thenBotRoleOutcome(w, "recognized"), `recognized as "coder", want "review"`)

	// A registered bot the mint does not recognize.
	w = botRoleWorld(t, "registered:review", control, noMatchResult(review))
	assert.ErrorContains(t, thenBotRoleOutcome(w, "recognized"), `outcome is "no-match"`)
	assert.ErrorContains(t, thenBotRoleDispatch(w, "allowed"), `is "denied-before-cel"`)

	// Bad step values.
	w = botRoleWorld(t, "registered:review", control, recognizedResult(review, "review"))
	assert.ErrorContains(t, thenBotRoleOutcome(w, "maybe"), "unknown outcome")
	assert.ErrorContains(t, thenBotRoleActor(w, "bot", "none", "review", "yes"), "role_verified must be")
	assert.ErrorContains(t, thenBotRoleActor(w, "human", "write", "review", "false"), "kind is")
	assert.ErrorContains(t, thenBotRoleDispatch(w, "sometimes"), "unknown dispatch")

	// No completed lookup.
	empty := mintProbeTestWorld(nil, nil)
	assert.ErrorContains(t, thenBotRoleOutcome(empty, "recognized"), "no completed lookup")
	assert.ErrorContains(t, thenBotRoleActor(empty, "bot", "none", "none", "true"), "no completed lookup")
	assert.ErrorContains(t, thenBotRoleDispatch(empty, "allowed"), "no completed lookup")
}

func TestBotRoleActorAndDispatch(t *testing.T) {
	p := &world.BotRoleProbe{ActorLogin: "x[bot]"}
	lookupErr := fmt.Errorf("%w: HTTP 401", botidentity.ErrLookup)
	for _, tc := range []struct {
		name     string
		v        botRoleVerdict
		botRole  string
		verified bool
		allowed  bool
	}{
		{name: "recognized", v: botRoleVerdict{outcome: botRoleOutcomeRecognized, result: botidentity.BotRoleResult{Role: "review"}}, botRole: "review", verified: true, allowed: true},
		{name: "no match", v: botRoleVerdict{outcome: botRoleOutcomeNoMatch}, verified: true},
		{name: "lookup failed", v: botRoleVerdict{outcome: botRoleOutcomeLookupFailed, lookupErr: lookupErr}, verified: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := botRoleActor(p, tc.v)
			assert.Equal(t, normevent.ActorBot, actor.Kind)
			assert.Equal(t, normevent.RoleNone, actor.Role)
			assert.Equal(t, tc.botRole, actor.BotRole)
			require.NotNil(t, actor.RoleVerified)
			assert.Equal(t, tc.verified, *actor.RoleVerified)

			ev := botRoleReviewEvent(testBotRoleRepo, actor)
			require.NoError(t, ev.Validate())
			assert.Equal(t, tc.allowed, harnessdispatch.IsAuthorized(ev))
			if !tc.allowed {
				assert.NoError(t, checkBotRoleDenialDistinct(tc.v, actor))
			}
		})
	}

	// A denial must say why through role_verified.
	assert.ErrorContains(t, checkBotRoleDenialDistinct(botRoleVerdict{outcome: botRoleOutcomeNoMatch}, normevent.Actor{}), "no role_verified")
	f := false
	assert.ErrorContains(t, checkBotRoleDenialDistinct(botRoleVerdict{outcome: botRoleOutcomeNoMatch}, normevent.Actor{RoleVerified: &f}), "role_verified false, want true")
	tr := true
	assert.ErrorContains(t, checkBotRoleDenialDistinct(botRoleVerdict{outcome: botRoleOutcomeRecognized}, normevent.Actor{RoleVerified: &tr}), "denied before CEL")
}

// --- Given / When steps ---

func TestGivenBotRoleProbe(t *testing.T) {
	scmDriver := newFakeMintProbeSCM()
	w := mintProbeTestWorld(scmDriver, nil)
	require.NoError(t, givenBotRoleProbe(context.Background(), w, "github"))
	assert.Equal(t, []string{testBotRoleHost}, scmDriver.createdRepos)
	assert.Equal(t, []string{testBotRoleHost}, scmDriver.publicRepos)
	assert.Equal(t, botRoleProbeWorkflow, scmDriver.commits[testBotRoleHost+":.github/workflows/bot-role-probe.yml"])
	assert.Equal(t, testBotRoleHost, w.BotRoleProbe.HostRepo)
	assert.Equal(t, []string{testBotRoleHost}, w.BotRoleProbe.CreatedRepos)
	assert.ErrorContains(t, givenBotRoleProbe(context.Background(), w, "github"), "already set up")

	// A stale host is deleted and recreated.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.existing = map[string]bool{testBotRoleHost: true}
	w = mintProbeTestWorld(scmDriver, nil)
	require.NoError(t, givenBotRoleProbe(context.Background(), w, "github"))
	assert.Equal(t, []string{testBotRoleHost}, scmDriver.deletedRepos)
	assert.Equal(t, []string{testBotRoleHost}, w.BotRoleProbe.CreatedRepos)
}

func TestGivenBotRoleProbe_Errors(t *testing.T) {
	assert.ErrorContains(t, givenBotRoleProbe(context.Background(), &world.World{}, "github"), "no repo configured")

	w := mintProbeTestWorld(newFakeMintProbeSCM(), nil)
	assert.ErrorContains(t, givenBotRoleProbe(context.Background(), w, "gitlab"), "not supported")

	w = mintProbeTestWorld(newFakeMintProbeSCM(), nil)
	w.Config.SCM = "gitlab"
	assert.ErrorContains(t, givenBotRoleProbe(context.Background(), w, "github"), "BEHAVIOUR_SCM")

	w = mintProbeTestWorld(newFakeMintProbeSCM(), nil)
	w.MintProbe = &world.MintProbe{}
	assert.ErrorContains(t, givenBotRoleProbe(context.Background(), w, "github"), "mint probe")

	// A failure after the probe is attached still leaves it for cleanup.
	failing := newFakeMintProbeSCM()
	failing.createErr = errors.New("boom")
	w = mintProbeTestWorld(failing, nil)
	assert.ErrorContains(t, givenBotRoleProbe(context.Background(), w, "github"), "bot-role probe: creating host repository")
	require.NotNil(t, w.BotRoleProbe)
}

func TestWhenBotRoleLookup_HappyPath(t *testing.T) {
	fastMintProbePolls(t)
	defaultAppSet(t)
	useSuiteLogin(t, testSuiteBotLogin, nil)

	scmDriver := newFakeMintProbeSCM()
	subject := noMatchResult(testSuiteBotLogin)
	subject.ClaimedRole = "review"
	ciDriver := &fakeBotRoleProbeCI{
		scm: scmDriver,
		run: &forge.WorkflowRun{ID: 42, Status: "completed", Conclusion: "success"},
		results: map[string]*world.BotRoleProbeResult{
			botRoleProbeControlArtifact: recognizedResult("fullsend-test-review[bot]", "review"),
			botRoleProbeSubjectArtifact: subject,
		},
	}
	w := mintProbeTestWorld(scmDriver, ciDriver)
	require.NoError(t, givenBotRoleProbe(context.Background(), w, "github"))
	require.NoError(t, whenBotRoleLookup(context.Background(), w, "claimed-in-event"))

	p := w.BotRoleProbe
	assert.Equal(t, testSuiteBotLogin, p.ActorLogin)
	assert.Equal(t, "review", p.ClaimedRole)
	assert.Equal(t, botRoleModeLookup, p.Mode)
	assert.Equal(t, "fullsend-test-review[bot]", p.ControlLogin)
	assert.Equal(t, []string{p.Branch}, w.CreatedBranches)
	shim := scmDriver.branchFiles[p.Branch+":.github/workflows/"+p.WorkflowName+".yml"]
	require.NotEmpty(t, shim)
	assert.Contains(t, string(shim), "claimed_role: 'review'")
	assert.Contains(t, string(shim), "actor_login: '"+testSuiteBotLogin+"'")
	assert.Equal(t, 42, p.Run.ID)

	require.NoError(t, thenBotRoleOutcome(w, "no-match"))
	require.NoError(t, thenBotRoleActor(w, "bot", "none", "none", "true"))
	require.NoError(t, thenBotRoleDispatch(w, "denied-before-cel"))
	assert.ErrorContains(t, whenBotRoleLookup(context.Background(), w, "claimed-in-event"), "already made")
}

func TestWhenBotRoleLookup_Failures(t *testing.T) {
	fastMintProbePolls(t)
	defaultAppSet(t)
	useSuiteLogin(t, testSuiteBotLogin, nil)
	both := map[string]*world.BotRoleProbeResult{
		botRoleProbeControlArtifact: recognizedResult("fullsend-test-review[bot]", "review"),
		botRoleProbeSubjectArtifact: recognizedResult("fullsend-test-review[bot]", "review"),
	}
	setup := func(t *testing.T, run *forge.WorkflowRun, results map[string]*world.BotRoleProbeResult) *world.World {
		t.Helper()
		scmDriver := newFakeMintProbeSCM()
		w := mintProbeTestWorld(scmDriver, &fakeBotRoleProbeCI{scm: scmDriver, run: run, results: results})
		require.NoError(t, givenBotRoleProbe(context.Background(), w, "github"))
		return w
	}

	t.Run("run never starts", func(t *testing.T) {
		w := setup(t, nil, both)
		assert.ErrorContains(t, whenBotRoleLookup(context.Background(), w, "registered:review"), "workflow failure")
	})
	t.Run("run fails", func(t *testing.T) {
		w := setup(t, &forge.WorkflowRun{ID: 5, Status: "completed", Conclusion: "failure"}, both)
		assert.ErrorContains(t, whenBotRoleLookup(context.Background(), w, "registered:review"), `concluded "failure"`)
	})
	t.Run("missing artifact", func(t *testing.T) {
		w := setup(t, &forge.WorkflowRun{ID: 5, Status: "completed", Conclusion: "success"},
			map[string]*world.BotRoleProbeResult{botRoleProbeControlArtifact: both[botRoleProbeControlArtifact]})
		assert.ErrorContains(t, whenBotRoleLookup(context.Background(), w, "registered:review"), "result-artifact failure")
	})
	t.Run("bad identity", func(t *testing.T) {
		w := setup(t, nil, both)
		assert.ErrorContains(t, whenBotRoleLookup(context.Background(), w, "registered:e2e"), "not a registered")
		assert.Nil(t, w.BotRoleProbe.Run)
		assert.ErrorContains(t, whenBotRoleLookup(context.Background(), mintProbeTestWorld(nil, nil), "registered:review"), "no probe set up")
	})
	t.Run("CI driver cannot list runs", func(t *testing.T) {
		scmDriver := newFakeMintProbeSCM()
		w := mintProbeTestWorld(scmDriver, &fakeBranchCI{})
		require.NoError(t, givenBotRoleProbe(context.Background(), w, "github"))
		assert.ErrorContains(t, whenBotRoleLookup(context.Background(), w, "registered:review"), "ci.RunLister")
	})
}

// --- cleanup ---

func TestCleanupScenario_DeletesBotRoleProbeHosts(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		Org:             "org",
		RepoOwner:       "org",
		RepoName:        "test-repo-07",
		SCM:             scmDriver,
		CreatedBranches: []string{"behaviour/bot-role-probe-0a1b2c3d"},
		BotRoleProbe: &world.BotRoleProbe{
			CreatedRepos: []string{"test-repo-07-mint-host", "test-repo-07", ""},
		},
	}
	CleanupScenario(w)

	require.Len(t, scmDriver.deletedRepos, 1, "the leased repo and empty names are never deleted")
	assert.Equal(t, deletedRepoRecord{owner: "org", repo: "test-repo-07-mint-host"}, scmDriver.deletedRepos[0])
	require.Len(t, scmDriver.deletedBranches, 1)
}

// --- probe workflow script ---

// fakeBotRoleCurlScript stands in for curl in the bot-role probe script: it
// answers the OIDC, signing-key and mint requests from FAKE_* variables and
// records the mint request's Authorization header and body.
const fakeBotRoleCurlScript = `#!/usr/bin/env bash
url="${@: -1}"
out=''
prev=''
auth=''
body=''
for a in "$@"; do
  # Like real curl, refuse a non-numeric --retry / --retry-delay value.
  if [[ "$prev" == --retry || "$prev" == --retry-delay ]] && ! [[ "$a" =~ ^[0-9]+$ ]]; then
    echo "curl: option $prev: expected a proper numerical parameter" >&2
    exit 2
  fi
  if [[ "$prev" == -o ]]; then out="$a"; fi
  if [[ "$prev" == -H && "$a" == Authorization:* ]]; then auth="$a"; fi
  if [[ "$prev" == -d ]]; then body="$a"; fi
  prev="$a"
done
case "$url" in
  *audience=*)
    printf '%s' "${FAKE_OIDC_RESPONSE}"
    ;;
  */.well-known/jwks)
    [[ -n "${FAKE_JWKS}" ]] || exit 22
    printf '%s' "${FAKE_JWKS}"
    ;;
  */v1/bot-role)
    printf '%s' "$auth" >"${FAKE_DIR}/auth"
    printf '%s' "$body" >"${FAKE_DIR}/body"
    printf '%s' "$url" >"${FAKE_DIR}/url"
    printf '%s\n' "$@" >"${FAKE_DIR}/args"
    [[ "${FAKE_MINT_STATUS}" != transport ]] || exit 7
    printf '%s' "${FAKE_MINT_BODY}" >"$out"
    printf '%s' "${FAKE_MINT_STATUS}"
    ;;
esac
`

type botRoleScriptRun struct {
	oidc        probeOIDC
	oidcToken   string // raw OIDC value; "" uses a signed, valid JWT
	mode        string
	login       string
	repo        string
	claimedRole string
	mintURL     string
	mintStatus  string
	mintBody    string
}

type botRoleScriptOutput struct {
	res    *world.BotRoleProbeResult
	raw    map[string]any
	token  string // the valid OIDC token the probe was given
	auth   string // the Authorization header sent to the mint ("" when not called)
	body   string
	url    string
	args   string // the mint curl arguments, one per line
	output string
}

func runBotRoleProbeScript(t *testing.T, tc botRoleScriptRun) botRoleScriptOutput {
	t.Helper()
	for _, tool := range []string{"jq", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	var reusable struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(botRoleProbeWorkflow, &reusable))
	script := reusable.Jobs["probe"].Steps[0].Run
	require.NotEmpty(t, script)

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	work := filepath.Join(dir, "work")
	require.NoError(t, os.Mkdir(bin, 0o755))
	require.NoError(t, os.Mkdir(work, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeBotRoleCurlScript), 0o755))
	scriptPath := filepath.Join(dir, "step.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o600))

	token, jwks := signedProbeOIDC(t, tc.oidc)
	oidc := token
	if tc.oidcToken != "" {
		oidc = tc.oidcToken
	}
	oidcResponse, err := json.Marshal(map[string]string{"value": oidc})
	require.NoError(t, err)

	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", scriptPath)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MINT_URL="+def(tc.mintURL, "https://mint.test/"),
		"ACTOR_LOGIN="+def(tc.login, "app-review[bot]"),
		"REPO="+def(tc.repo, "o/r"),
		"MODE="+def(tc.mode, botRoleModeLookup),
		"CLAIMED_ROLE="+tc.claimedRole,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=request-token",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://tokens.test/?api=1",
		"FAKE_OIDC_RESPONSE="+string(oidcResponse),
		"FAKE_JWKS="+jwks,
		"FAKE_MINT_STATUS="+tc.mintStatus,
		"FAKE_MINT_BODY="+tc.mintBody,
		"FAKE_DIR="+dir,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	data, err := os.ReadFile(filepath.Join(work, botRoleProbeResultFile))
	require.NoError(t, err, string(output))
	res, err := parseBotRoleProbeResult(data)
	if tc.mode == "" || tc.mode == botRoleModeLookup || tc.mode == botRoleModeForcedFailure {
		require.NoError(t, err, string(data))
	} else {
		// A result for an unknown mode is never accepted.
		require.Error(t, err, string(data))
	}
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	entries, err := os.ReadDir(work)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "only the result file is left behind")

	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
	return botRoleScriptOutput{res: res, raw: raw, token: token, auth: read("auth"), body: read("body"), url: read("url"), args: read("args"), output: string(output)}
}

func TestBotRoleProbeScript_Lookup(t *testing.T) {
	out := runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "200", mintBody: `{"outcome":"recognized","role":"review"}`})
	assert.Equal(t, "https://mint.test/v1/bot-role", out.url, "the trailing slash of the mint URL is dropped")
	assert.Equal(t, "Authorization: Bearer "+out.token, out.auth)
	assert.Contains(t, strings.Split(out.args, "\n"), "=https", "the mint request is restricted to https")
	assert.JSONEq(t, `{"actor_login":"app-review[bot]","repo":"o/r"}`, out.body)
	assert.True(t, out.res.OIDCVerified)
	assert.Equal(t, 200, out.res.HTTPStatus)
	assert.Equal(t, &world.BotRoleProbeResponse{Outcome: "recognized", Role: "review"}, out.res.Response)
	assert.Equal(t, "o/r", out.res.Repository)
	assert.Contains(t, out.res.JobWorkflowRef, "/.github/workflows/")
	assert.Empty(t, out.res.Error)
	v := classifyBotRoleResult(out.res, botRoleModeLookup)
	assert.Equal(t, botRoleOutcomeRecognized, v.outcome)
	assert.NotContains(t, out.output, "recognized", "the mint's answer is not echoed")

	out = runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "200", mintBody: `{"outcome":"no_match"}`})
	assert.Equal(t, botRoleOutcomeNoMatch, classifyBotRoleResult(out.res, botRoleModeLookup).outcome)
}

func TestBotRoleProbeScript_ClaimedRole(t *testing.T) {
	out := runBotRoleProbeScript(t, botRoleScriptRun{claimedRole: "review", mintStatus: "200", mintBody: `{"outcome":"no_match"}`})
	assert.JSONEq(t, `{"actor_login":"app-review[bot]","repo":"o/r","role":"review"}`, out.body)
	assert.Equal(t, "review", out.res.ClaimedRole)
}

func TestBotRoleProbeScript_ForcedFailure(t *testing.T) {
	out := runBotRoleProbeScript(t, botRoleScriptRun{mode: botRoleModeForcedFailure, mintStatus: "401", mintBody: `{"error":"authentication failed"}`})
	parts := strings.Split(out.token, ".")
	require.Len(t, parts, 3)
	sent := strings.TrimPrefix(out.auth, "Authorization: Bearer ")
	sentParts := strings.Split(sent, ".")
	require.Len(t, sentParts, 3)
	assert.Equal(t, parts[0], sentParts[0], "the verified header is kept")
	assert.Equal(t, parts[1], sentParts[1], "the verified payload is kept")
	assert.NotEqual(t, parts[2], sentParts[2], "the signature is replaced")
	sig, err := base64.RawURLEncoding.DecodeString(sentParts[2])
	require.NoError(t, err)
	assert.Equal(t, "invalid-signature", string(sig))
	assert.Contains(t, out.output, "::add-mask::"+sent)

	assert.True(t, out.res.OIDCVerified)
	assert.Equal(t, botRoleModeForcedFailure, out.res.Mode)
	v := classifyBotRoleResult(out.res, botRoleModeForcedFailure)
	assert.Equal(t, botRoleOutcomeLookupFailed, v.outcome, v.detail)
}

func TestBotRoleProbeScript_SanitizesResponse(t *testing.T) {
	long := strings.Repeat("x", 400)
	out := runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "502",
		mintBody: `{"error":"` + long + `","token":"secret-value","outcome":7,"extra":{"a":1}}`})
	assert.Nil(t, out.res.Response, "a mistyped kept key invalidates the whole response")
	assert.NotContains(t, out.raw, "response")
	for key := range out.raw {
		assert.Contains(t, []string{"mode", "actor_login", "repo", "oidc_verified", "repository", "job_workflow_ref", "http_status", "claimed_role", "error"}, key)
	}
	assert.Equal(t, "lookup", classifyBotRoleResult(out.res, botRoleModeLookup).infra)

	// Unknown keys are dropped and kept strings are truncated.
	out = runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "502",
		mintBody: `{"error":"` + long + `","token":"secret-value","extra":{"a":1}}`})
	resp, ok := out.raw["response"].(map[string]any)
	require.True(t, ok, out.raw)
	assert.Equal(t, map[string]any{"error": strings.Repeat("x", 300)}, resp)

	for name, body := range map[string]string{
		"role on no_match":     `{"outcome":"no_match","role":123}`,
		"error on no_match":    `{"outcome":"no_match","error":123}`,
		"role on recognized":   `{"outcome":"recognized","role":123}`,
		"null error":           `{"outcome":"no_match","error":null}`,
		"object role":          `{"outcome":"recognized","role":{"a":"review"}}`,
		"numeric outcome":      `{"outcome":7}`,
		"valid role bad error": `{"outcome":"recognized","role":"review","error":false}`,
	} {
		out = runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "200", mintBody: body})
		assert.Nil(t, out.res.Response, name)
		assert.Equal(t, "lookup", classifyBotRoleResult(out.res, botRoleModeLookup).infra,
			name+": a mistyped field must not pass as a valid lookup")
	}

	const hostile = "::error::injected ##[error]injected"
	out = runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "403", mintBody: `{"error":"` + hostile + `"}`})
	assert.Equal(t, hostile, out.res.Response.Error, "the artifact keeps the mint's message")
	assert.NotContains(t, out.output, "injected")
	assert.Contains(t, out.output, "bot-role probe result: error=- http_status=403 oidc_verified=true")

	for _, body := range []string{`not json`, `["outcome"]`, `{"outcome":"no_match"} {"outcome":"recognized"}`, ``} {
		out = runBotRoleProbeScript(t, botRoleScriptRun{mintStatus: "200", mintBody: body})
		assert.Nil(t, out.res.Response, body)
		assert.Equal(t, "lookup", classifyBotRoleResult(out.res, botRoleModeLookup).infra, body)
	}
}

func TestBotRoleProbeScript_ProbeFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		run   botRoleScriptRun
		error string
	}{
		"bad mode":            {run: botRoleScriptRun{mode: "guess"}, error: "config"},
		"bad login":           {run: botRoleScriptRun{login: "octocat"}, error: "config"},
		"bad repo":            {run: botRoleScriptRun{repo: "o"}, error: "config"},
		"bad claimed role":    {run: botRoleScriptRun{claimedRole: "Review"}, error: "config"},
		"non-url mint url":    {run: botRoleScriptRun{mintURL: "-"}, error: "config"},
		"plaintext mint url":  {run: botRoleScriptRun{mintURL: "http://mint.test"}, error: "config"},
		"host-less mint url":  {run: botRoleScriptRun{mintURL: "https://"}, error: "config"},
		"unusable oidc token": {run: botRoleScriptRun{oidcToken: "abc%0Adef"}, error: "oidc"},
		"bad signature":       {run: botRoleScriptRun{oidc: probeOIDC{wrongKey: true}}, error: "oidc"},
		"wrong audience":      {run: botRoleScriptRun{oidc: probeOIDC{claims: map[string]any{"aud": "other"}}}, error: "oidc"},
		"transport":           {run: botRoleScriptRun{mintStatus: "transport"}, error: "transport"},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.run.mintStatus == "" {
				tc.run.mintStatus = "200"
				tc.run.mintBody = `{"outcome":"recognized","role":"review"}`
			}
			out := runBotRoleProbeScript(t, tc.run)
			assert.Equal(t, tc.error, out.raw["error"])
			assert.NotContains(t, out.raw, "http_status")
			assert.NotContains(t, out.raw, "response")
			if tc.error != "transport" {
				assert.Empty(t, out.auth, "the mint is not called")
				assert.Equal(t, false, out.raw["oidc_verified"])
			}
			if out.res != nil {
				assert.Equal(t, tc.error, classifyBotRoleResult(out.res, out.res.Mode).infra)
			}
		})
	}
}
