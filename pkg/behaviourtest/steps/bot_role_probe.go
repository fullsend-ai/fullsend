package steps

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// The bot-role probe steps exercise the GitHub bot-role lookup (ADR 0107,
// the mint's POST /v1/bot-role) with real GitHub Actions OIDC tokens. For
// each scenario they:
//
//  1. create the approved mint-probe host of the leased slot
//     (test-repo-NN-mint-host, on the mint's WORKFLOW_HOST_REPOS) holding
//     the reusable probe workflow (bot_role_probe_workflow.yml);
//  2. push a shim workflow to a temporary branch of the leased repository
//     that calls the probe twice in one run: a control lookup of the
//     registered review bot and the subject lookup under test;
//  3. classify the two uploaded results and project the subject's outcome
//     onto a normalized actor and the pre-CEL dispatch gate.
//
// The control must be recognized for the subject to count, and every
// probe verifies its own OIDC token (oidc_verified), so an OIDC,
// transport, server or result failure is reported as an infrastructure
// failure and never passes as an expected no-match or lookup failure.

//go:embed bot_role_probe_workflow.yml
var botRoleProbeWorkflow []byte

const (
	// botRoleProbeWorkflowPath is where the reusable probe workflow lives
	// in the host repository. The suite's mints allow every workflow
	// basename (ALLOWED_WORKFLOW_FILES "*" on preview and stage).
	botRoleProbeWorkflowPath = ".github/workflows/bot-role-probe.yml"

	botRoleProbeControlArtifact = "bot-role-probe-control"
	botRoleProbeSubjectArtifact = "bot-role-probe-subject"
	botRoleProbeResultFile      = "bot-role-probe-result.json"

	// Probe modes (the reusable workflow's mode input).
	botRoleModeLookup        = "lookup"
	botRoleModeForcedFailure = "forced_failure"

	// Identity tokens of the identity column.
	botRoleIdentityRegisteredPrefix = "registered:"
	botRoleIdentityUnregisteredE2E  = "unregistered-e2e"
	botRoleIdentityForcedFailure    = "forced-failure"
	botRoleIdentityClaimedInEvent   = "claimed-in-event"

	// botRoleControlRole is the registered role the control looks up and
	// the role the claimed-in-event subject claims.
	botRoleControlRole = "review"

	// Values of the outcome column.
	botRoleOutcomeRecognized   = "recognized"
	botRoleOutcomeNoMatch      = "no-match"
	botRoleOutcomeLookupFailed = "lookup-failed"

	// botRoleAcceptedInvalid is the verdict for a forced failure the mint
	// answered 200: a test failure, never an infrastructure one.
	botRoleAcceptedInvalid = "accepted-invalid-credential"

	// Values of the dispatch column.
	botRoleDispatchAllowed = "allowed"
	botRoleDispatchDenied  = "denied-before-cel"

	botRoleNone = "none"
)

var (
	// botRoleLoginRe bounds a bot login interpolated into the shim.
	botRoleLoginRe = regexp.MustCompile(`(?i)^[a-z0-9-]+\[bot\]$`)
	// botRoleFullRepoRe bounds the owner/repo interpolated into the shim.
	botRoleFullRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
)

// botRoleSuiteLoginFn returns the suite's own GitHub identity: the e2e App
// the behaviour suite authenticates as. Override in tests to return a fixed
// login or error without a live GitHub SCM driver.
var botRoleSuiteLoginFn = func(ctx context.Context, w *world.World) (string, error) {
	gh, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return "", fmt.Errorf("the suite identity needs the GitHub SCM driver, got %T", w.SCM)
	}
	return gh.Client.GetAuthenticatedUser(ctx)
}

func registerBotRoleProbeSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a "([^"]*)" bot-role lookup probe$`, func(ctx context.Context, forgeName string) (context.Context, error) {
		return ctx, givenBotRoleProbe(ctx, world.FromContext(ctx), forgeName)
	})
	sc.Step(`^the lookup resolves identity "([^"]*)" for the leased repository$`, func(ctx context.Context, identity string) (context.Context, error) {
		return ctx, whenBotRoleLookup(ctx, world.FromContext(ctx), identity)
	})
	sc.Step(`^the lookup outcome is "([^"]*)"$`, func(ctx context.Context, outcome string) (context.Context, error) {
		return ctx, thenBotRoleOutcome(world.FromContext(ctx), outcome)
	})
	sc.Step(`^the actor is kind "([^"]*)" role "([^"]*)" bot_role "([^"]*)" role_verified "([^"]*)"$`, func(ctx context.Context, kind, role, botRole, verified string) (context.Context, error) {
		return ctx, thenBotRoleActor(world.FromContext(ctx), kind, role, botRole, verified)
	})
	sc.Step(`^non-label dispatch is "([^"]*)"$`, func(ctx context.Context, dispatch string) (context.Context, error) {
		return ctx, thenBotRoleDispatch(world.FromContext(ctx), dispatch)
	})
}

// givenBotRoleProbe creates the probe host repository for the leased
// repository: the slot's approved mint-probe host, so the lookup also
// passes a mint that checks the workflow host.
func givenBotRoleProbe(ctx context.Context, w *world.World, forgeName string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	if forgeName != mintProbeForge {
		return fmt.Errorf("bot-role probe: forge %q is not supported (GitHub mint bot-role lookup only)", forgeName)
	}
	if suiteSCM := w.Config.SCM; suiteSCM != "" && suiteSCM != forgeName {
		return fmt.Errorf("bot-role probe: the row targets forge %q but the suite runs BEHAVIOUR_SCM=%q", forgeName, suiteSCM)
	}
	if w.BotRoleProbe != nil {
		return fmt.Errorf("bot-role probe: already set up for this scenario")
	}
	if w.MintProbe != nil {
		return fmt.Errorf("bot-role probe: the mint probe of this scenario already owns the workflow host")
	}
	p := &world.BotRoleProbe{
		Forge:    forgeName,
		HostRepo: w.RepoName + install.MintProbeHostRepoSuffix,
	}
	// Attach before creating anything so CleanupScenario sees every repo
	// created even when a later step fails.
	w.BotRoleProbe = p
	return createProbeHost(ctx, w, probeHostSpec{
		label:        "bot-role probe",
		created:      &p.CreatedRepos,
		workflowPath: botRoleProbeWorkflowPath,
		workflow:     botRoleProbeWorkflow,
	}, p.HostRepo)
}

// botRoleIdentity is a resolved identity column value.
type botRoleIdentity struct {
	login          string
	mode           string
	claimedRole    string
	registeredRole string
}

// botRoleRegisteredRoles lists the roles of the behaviour app set whose
// registered bots the steps look up (the fullsend-test roles).
func botRoleRegisteredRoles() []string {
	return slices.Sorted(maps.Keys(mintProbeRolePermissions))
}

// registeredBotLogin returns the login of the behaviour app set's App for
// role: "<app set>-<role>[bot]".
func registeredBotLogin(role string) (string, error) {
	if _, ok := mintProbeRolePermissions[role]; !ok {
		return "", fmt.Errorf("bot-role probe: role %q is not a registered fullsend-test role (supported: %s)", role, strings.Join(botRoleRegisteredRoles(), ", "))
	}
	login := install.AppSet() + "-" + role + "[bot]"
	if !botRoleLoginRe.MatchString(login) {
		return "", fmt.Errorf("bot-role probe: app set %q gives an invalid bot login %q", install.AppSet(), login)
	}
	return login, nil
}

// suiteBotLogin returns the suite's own App login, which no registered
// role of the behaviour app set uses: the unregistered third-party bot.
func suiteBotLogin(ctx context.Context, w *world.World) (string, error) {
	login, err := botRoleSuiteLoginFn(ctx, w)
	if err != nil {
		return "", fmt.Errorf("bot-role probe infrastructure failure: resolving the suite's GitHub identity: %w", err)
	}
	if !strings.HasSuffix(login, "[bot]") || !botRoleLoginRe.MatchString(login) {
		return "", fmt.Errorf("bot-role probe infrastructure failure: the suite's GitHub identity %q is not a GitHub App bot login; the unregistered identity must be the suite's e2e App", login)
	}
	for _, role := range botRoleRegisteredRoles() {
		registered, err := registeredBotLogin(role)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(login, registered) {
			return "", fmt.Errorf("bot-role probe infrastructure failure: the suite's GitHub identity %q is the registered %s bot, so it cannot stand for an unregistered bot", login, role)
		}
	}
	return login, nil
}

// resolveBotRoleIdentity maps the identity column to the subject lookup.
func resolveBotRoleIdentity(ctx context.Context, w *world.World, identity string) (botRoleIdentity, error) {
	switch {
	case strings.HasPrefix(identity, botRoleIdentityRegisteredPrefix):
		role := strings.TrimPrefix(identity, botRoleIdentityRegisteredPrefix)
		login, err := registeredBotLogin(role)
		if err != nil {
			return botRoleIdentity{}, err
		}
		return botRoleIdentity{login: login, mode: botRoleModeLookup, registeredRole: role}, nil
	case identity == botRoleIdentityForcedFailure:
		login, err := registeredBotLogin(botRoleControlRole)
		if err != nil {
			return botRoleIdentity{}, err
		}
		return botRoleIdentity{login: login, mode: botRoleModeForcedFailure, registeredRole: botRoleControlRole}, nil
	case identity == botRoleIdentityUnregisteredE2E:
		login, err := suiteBotLogin(ctx, w)
		if err != nil {
			return botRoleIdentity{}, err
		}
		return botRoleIdentity{login: login, mode: botRoleModeLookup}, nil
	case identity == botRoleIdentityClaimedInEvent:
		login, err := suiteBotLogin(ctx, w)
		if err != nil {
			return botRoleIdentity{}, err
		}
		return botRoleIdentity{login: login, mode: botRoleModeLookup, claimedRole: botRoleControlRole}, nil
	default:
		return botRoleIdentity{}, fmt.Errorf("bot-role probe: unknown identity %q (want %q<role>, %q, %q or %q)", identity,
			botRoleIdentityRegisteredPrefix, botRoleIdentityUnregisteredE2E, botRoleIdentityForcedFailure, botRoleIdentityClaimedInEvent)
	}
}

// whenBotRoleLookup pushes the shim, waits for its run and reads the
// control and subject results.
func whenBotRoleLookup(ctx context.Context, w *world.World, identity string) error {
	p := w.BotRoleProbe
	if p == nil {
		return fmt.Errorf("bot-role probe: no probe set up; use 'a \"github\" bot-role lookup probe' first")
	}
	if p.Run != nil {
		return fmt.Errorf("bot-role probe: a lookup was already made in this scenario")
	}
	id, err := resolveBotRoleIdentity(ctx, w, identity)
	if err != nil {
		return err
	}
	controlLogin, err := registeredBotLogin(botRoleControlRole)
	if err != nil {
		return err
	}
	p.Identity, p.Mode, p.ActorLogin, p.ClaimedRole, p.RegisteredRole = identity, id.mode, id.login, id.claimedRole, id.registeredRole
	p.ControlLogin = controlLogin

	lister, ok := w.CI.(ci.RunLister)
	if !ok {
		return fmt.Errorf("bot-role probe: CI driver %T cannot list workflow runs (ci.RunLister)", w.CI)
	}
	nonce, err := mintProbeNonce()
	if err != nil {
		return err
	}
	p.Branch = "behaviour/bot-role-probe-" + nonce
	p.WorkflowName = "bot-role-probe-" + nonce

	hostRef, err := w.SCM.GetDefaultBranch(ctx, w.Org, p.HostRepo)
	if err != nil {
		return fmt.Errorf("bot-role probe: resolving default branch for %s/%s: %w", w.Org, p.HostRepo, err)
	}
	shim, err := renderBotRoleProbeShim(botRoleProbeShimSpec{
		Name:               p.WorkflowName,
		Branch:             p.Branch,
		Org:                w.Org,
		Host:               p.HostRepo,
		HostRef:            hostRef,
		Repo:               w.RepoOwner + "/" + w.RepoName,
		ControlLogin:       p.ControlLogin,
		SubjectLogin:       p.ActorLogin,
		SubjectMode:        p.Mode,
		SubjectClaimedRole: p.ClaimedRole,
	})
	if err != nil {
		return err
	}

	if err := w.SCM.CreateBranch(ctx, w.RepoOwner, w.RepoName, p.Branch); err != nil {
		return fmt.Errorf("bot-role probe: creating branch %s: %w", p.Branch, err)
	}
	w.CreatedBranches = append(w.CreatedBranches, p.Branch)
	shimPath := ".github/workflows/" + p.WorkflowName + ".yml"
	if err := w.SCM.CommitFileToBranch(ctx, w.RepoOwner, w.RepoName, p.Branch, shimPath, "behaviour: add bot-role probe shim", shim); err != nil {
		return fmt.Errorf("bot-role probe: committing the shim to %s: %w", p.Branch, err)
	}

	run, err := waitForMintProbeRun(ctx, lister, w.RepoOwner, w.RepoName, p.WorkflowName)
	if err != nil {
		return fmt.Errorf("bot-role probe workflow failure: %w", err)
	}
	p.Run = run
	if run.Conclusion != "success" {
		return fmt.Errorf("bot-role probe workflow failure: run %d concluded %q (%s)", run.ID, run.Conclusion, run.HTMLURL)
	}
	if p.Control, err = downloadBotRoleProbeResult(ctx, w, run.ID, botRoleProbeControlArtifact); err != nil {
		return fmt.Errorf("bot-role probe result-artifact failure: %w", err)
	}
	if p.Subject, err = downloadBotRoleProbeResult(ctx, w, run.ID, botRoleProbeSubjectArtifact); err != nil {
		return fmt.Errorf("bot-role probe result-artifact failure: %w", err)
	}
	return nil
}

// botRoleProbeShimSpec is the input of renderBotRoleProbeShim.
type botRoleProbeShimSpec struct {
	Name, Branch, Org  string
	Host, HostRef      string
	Repo               string // owner/repo of the leased repository
	ControlLogin       string
	SubjectLogin       string
	SubjectMode        string
	SubjectClaimedRole string
}

// renderBotRoleProbeShim renders the shim workflow pushed to the leased
// repository. It passes the leased repository's FULLSEND_MINT_URL to both
// probe jobs, so the probe exercises the mint that repository is
// installed against.
func renderBotRoleProbeShim(s botRoleProbeShimSpec) ([]byte, error) {
	for _, v := range []string{s.Name, s.Org, s.Host} {
		if !mintProbeRepoRe.MatchString(v) {
			return nil, fmt.Errorf("bot-role probe: invalid name %q", v)
		}
	}
	if !botRoleFullRepoRe.MatchString(s.Repo) {
		return nil, fmt.Errorf("bot-role probe: invalid repository %q", s.Repo)
	}
	for _, v := range []string{s.Branch, s.HostRef} {
		if v == "" || strings.ContainsAny(v, "'\"\n\r\t {}$") {
			return nil, fmt.Errorf("bot-role probe: invalid ref %q", v)
		}
	}
	for _, v := range []string{s.ControlLogin, s.SubjectLogin} {
		if !botRoleLoginRe.MatchString(v) {
			return nil, fmt.Errorf("bot-role probe: invalid bot login %q", v)
		}
	}
	if s.SubjectMode != botRoleModeLookup && s.SubjectMode != botRoleModeForcedFailure {
		return nil, fmt.Errorf("bot-role probe: invalid mode %q", s.SubjectMode)
	}
	if s.SubjectClaimedRole != "" && !mintProbeRoleRe.MatchString(s.SubjectClaimedRole) {
		return nil, fmt.Errorf("bot-role probe: invalid claimed role %q", s.SubjectClaimedRole)
	}

	job := func(login, mode, claimedRole, artifact string) string {
		return fmt.Sprintf(`    permissions:
      contents: read
      id-token: write
    uses: %s/%s/%s@%s
    with:
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      actor_login: '%s'
      repo: '%s'
      mode: '%s'
      claimed_role: '%s'
      artifact_name: '%s'
`, s.Org, s.Host, botRoleProbeWorkflowPath, s.HostRef, login, s.Repo, mode, claimedRole, artifact)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `# Behaviour-test bot-role probe shim (pkg/behaviourtest/steps/bot_role_probe.go).
# Pushed to a temporary branch of the leased test repository; scenario
# cleanup deletes the branch.
name: %s

on:
  push:
    branches: ['%s']

permissions: {}

jobs:
  # Lookup of the registered review bot: must be recognized.
  control:
`, s.Name, s.Branch)
	b.WriteString(job(s.ControlLogin, botRoleModeLookup, "", botRoleProbeControlArtifact))
	b.WriteString("\n  # The lookup under test.\n  subject:\n")
	b.WriteString(job(s.SubjectLogin, s.SubjectMode, s.SubjectClaimedRole, botRoleProbeSubjectArtifact))
	return []byte(b.String()), nil
}

// downloadBotRoleProbeResult downloads one probe job's artifact and
// parses its result.
func downloadBotRoleProbeResult(ctx context.Context, w *world.World, runID int, artifact string) (*world.BotRoleProbeResult, error) {
	data, err := downloadProbeArtifact(ctx, w, runID, artifact, botRoleProbeResultFile)
	if err != nil {
		return nil, err
	}
	return parseBotRoleProbeResult(data)
}

// parseBotRoleProbeResult parses a probe result and rejects one that does
// not state a known mode.
func parseBotRoleProbeResult(data []byte) (*world.BotRoleProbeResult, error) {
	var res world.BotRoleProbeResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", botRoleProbeResultFile, err)
	}
	switch res.Mode {
	case botRoleModeLookup, botRoleModeForcedFailure:
	default:
		return nil, fmt.Errorf("%s has unknown mode %q", botRoleProbeResultFile, res.Mode)
	}
	return &res, nil
}

// botRoleVerdict is the classification of one probe result. Exactly one of
// outcome and infra is set: outcome is a lookup answer (an outcome column
// value, or botRoleAcceptedInvalid), infra an infrastructure failure
// category. result and lookupErr are the botidentity projection of the
// answer.
type botRoleVerdict struct {
	outcome   string
	infra     string
	detail    string
	result    botidentity.BotRoleResult
	lookupErr error
}

// classifyBotRoleResult classifies res for a probe run in mode.
func classifyBotRoleResult(res *world.BotRoleProbeResult, mode string) botRoleVerdict {
	if res == nil {
		return botRoleVerdict{infra: "result-artifact", detail: "no probe result"}
	}
	if res.Error != "" {
		switch res.Error {
		case "config", "oidc", "transport":
			return botRoleVerdict{infra: res.Error, detail: "probe-side " + res.Error + " failure"}
		default:
			return botRoleVerdict{infra: "result-artifact", detail: fmt.Sprintf("unknown probe error %q", res.Error)}
		}
	}
	if res.Mode != mode {
		return botRoleVerdict{infra: "result-artifact", detail: fmt.Sprintf("result is for mode %q, want %q", res.Mode, mode)}
	}
	if !res.OIDCVerified {
		return botRoleVerdict{infra: "oidc-unverified", detail: "the probe did not verify the OIDC token"}
	}
	if res.HTTPStatus == 0 {
		return botRoleVerdict{infra: "result-artifact", detail: "no HTTP status from the mint"}
	}
	var resp botidentity.MintBotRoleResponse
	if res.Response != nil {
		resp = botidentity.MintBotRoleResponse{Outcome: res.Response.Outcome, Role: res.Response.Role, Error: res.Response.Error}
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return botRoleVerdict{infra: "result-artifact", detail: err.Error()}
	}
	parsed, lookupErr := botidentity.ParseMintBotRoleResponse(res.HTTPStatus, body)
	detail := fmt.Sprintf("HTTP %d", res.HTTPStatus)
	if resp.Error != "" {
		detail += ": " + resp.Error
	}

	if mode == botRoleModeForcedFailure {
		switch {
		case res.HTTPStatus == 200:
			return botRoleVerdict{outcome: botRoleAcceptedInvalid, detail: detail + " (the mint accepted a credential with an invalid signature)"}
		case res.HTTPStatus == 401 && resp.Error == mintProbeAuthFailedMessage && lookupErr != nil:
			return botRoleVerdict{outcome: botRoleOutcomeLookupFailed, detail: detail, lookupErr: lookupErr}
		case res.HTTPStatus == 401:
			// Not the mint's canonical body: an intermediary or an
			// unexpected failure, which must not satisfy a lookup failure.
			return botRoleVerdict{infra: "unexpected-401", detail: detail}
		default:
			return botRoleVerdict{infra: "server", detail: detail + " (want the mint's 401 for the invalid credential)"}
		}
	}

	if lookupErr != nil {
		return botRoleVerdict{infra: "lookup", detail: lookupErr.Error()}
	}
	if parsed.Recognized() {
		return botRoleVerdict{outcome: botRoleOutcomeRecognized, detail: "recognized as " + parsed.Role, result: parsed}
	}
	return botRoleVerdict{outcome: botRoleOutcomeNoMatch, detail: "no match", result: parsed}
}

// checkBotRoleClaims verifies that a result is for the expected request
// and OIDC identity: the leased repository as the repository claim and
// the lookup's repo, the probe host as the workflow host, and login as
// the looked-up bot.
func checkBotRoleClaims(res *world.BotRoleProbeResult, repo, org, host, login string) error {
	if !strings.EqualFold(res.Repository, repo) {
		return fmt.Errorf("OIDC repository claim is %q, want %q", res.Repository, repo)
	}
	if !strings.EqualFold(res.Repo, repo) {
		return fmt.Errorf("lookup repo is %q, want %q", res.Repo, repo)
	}
	wantRef := strings.ToLower(org + "/" + host + "/" + botRoleProbeWorkflowPath + "@")
	if !strings.HasPrefix(strings.ToLower(res.JobWorkflowRef), wantRef) {
		return fmt.Errorf("OIDC job_workflow_ref claim is %q, want it hosted by %s/%s", res.JobWorkflowRef, org, host)
	}
	if !strings.EqualFold(res.ActorLogin, login) {
		return fmt.Errorf("looked-up login is %q, want %q", res.ActorLogin, login)
	}
	return nil
}

func requireBotRoleResults(w *world.World) (*world.BotRoleProbe, error) {
	p := w.BotRoleProbe
	if p == nil || p.Run == nil || p.Control == nil || p.Subject == nil {
		return nil, fmt.Errorf("bot-role probe: no completed lookup; use 'the lookup resolves identity ...' first")
	}
	return p, nil
}

// verifyBotRoleControl requires the control to have recognized the
// registered review bot for the leased repository.
func verifyBotRoleControl(w *world.World, p *world.BotRoleProbe) error {
	repo := w.RepoOwner + "/" + w.RepoName
	v := classifyBotRoleResult(p.Control, botRoleModeLookup)
	if v.infra != "" {
		return fmt.Errorf("bot-role probe control: infrastructure failure (%s): %s", v.infra, v.detail)
	}
	if err := checkBotRoleClaims(p.Control, repo, w.Org, p.HostRepo, p.ControlLogin); err != nil {
		return fmt.Errorf("bot-role probe control: infrastructure failure: %w", err)
	}
	if v.outcome != botRoleOutcomeRecognized || v.result.Role != botRoleControlRole {
		return fmt.Errorf("bot-role probe control: infrastructure failure: the registered %s bot %s was not recognized as %q (%s); "+
			"check the mint's app set and that it enrolls %s", botRoleControlRole, p.ControlLogin, botRoleControlRole, v.detail, repo)
	}
	return nil
}

// botRoleSubjectVerdict verifies the control and the subject's identity
// and returns the subject's lookup answer. Infrastructure failures and a
// forced failure the mint accepted are errors.
func botRoleSubjectVerdict(w *world.World) (*world.BotRoleProbe, botRoleVerdict, error) {
	p, err := requireBotRoleResults(w)
	if err != nil {
		return nil, botRoleVerdict{}, err
	}
	if err := verifyBotRoleControl(w, p); err != nil {
		return nil, botRoleVerdict{}, err
	}
	v := classifyBotRoleResult(p.Subject, p.Mode)
	if v.infra != "" {
		return nil, botRoleVerdict{}, fmt.Errorf("bot-role probe: infrastructure failure (%s): %s; it cannot satisfy an expected lookup outcome", v.infra, v.detail)
	}
	if err := checkBotRoleClaims(p.Subject, w.RepoOwner+"/"+w.RepoName, w.Org, p.HostRepo, p.ActorLogin); err != nil {
		return nil, botRoleVerdict{}, fmt.Errorf("bot-role probe: infrastructure failure: %w", err)
	}
	if v.outcome == botRoleAcceptedInvalid {
		return nil, botRoleVerdict{}, fmt.Errorf("bot-role probe: %s", v.detail)
	}
	return p, v, nil
}

// thenBotRoleOutcome checks the subject's lookup outcome.
func thenBotRoleOutcome(w *world.World, expected string) error {
	switch expected {
	case botRoleOutcomeRecognized, botRoleOutcomeNoMatch, botRoleOutcomeLookupFailed:
	default:
		return fmt.Errorf("bot-role probe: unknown outcome %q (want %q, %q or %q)", expected, botRoleOutcomeRecognized, botRoleOutcomeNoMatch, botRoleOutcomeLookupFailed)
	}
	p, v, err := botRoleSubjectVerdict(w)
	if err != nil {
		return err
	}
	if v.outcome != expected {
		return fmt.Errorf("bot-role probe: outcome is %q (%s), want %q", v.outcome, v.detail, expected)
	}
	if expected == botRoleOutcomeRecognized && p.RegisteredRole != "" && v.result.Role != p.RegisteredRole {
		return fmt.Errorf("bot-role probe: %q was recognized as %q, want %q", p.ActorLogin, v.result.Role, p.RegisteredRole)
	}
	return nil
}

// botRoleActor projects the subject's answer onto a normalized actor.
func botRoleActor(p *world.BotRoleProbe, v botRoleVerdict) normevent.Actor {
	actor := normevent.Actor{ID: p.ActorLogin}
	botidentity.ApplyBotRoleResult(&actor, v.result, v.lookupErr)
	return actor
}

// thenBotRoleActor checks the normalized actor fields of the subject.
func thenBotRoleActor(w *world.World, kind, role, botRole, verified string) error {
	var wantVerified bool
	switch verified {
	case "true":
		wantVerified = true
	case "false":
	default:
		return fmt.Errorf("bot-role probe: role_verified must be \"true\" or \"false\", got %q", verified)
	}
	wantBotRole := botRole
	if botRole == botRoleNone {
		wantBotRole = ""
	}
	p, v, err := botRoleSubjectVerdict(w)
	if err != nil {
		return err
	}
	actor := botRoleActor(p, v)
	gotVerified := "unset"
	if actor.RoleVerified != nil {
		gotVerified = fmt.Sprint(*actor.RoleVerified)
	}
	var diffs []string
	if string(actor.Kind) != kind {
		diffs = append(diffs, fmt.Sprintf("kind is %q, want %q", actor.Kind, kind))
	}
	if string(actor.Role) != role {
		diffs = append(diffs, fmt.Sprintf("role is %q, want %q", actor.Role, role))
	}
	if actor.BotRole != wantBotRole {
		diffs = append(diffs, fmt.Sprintf("bot_role is %q, want %q", actor.BotRole, botRole))
	}
	if actor.RoleVerified == nil || *actor.RoleVerified != wantVerified {
		diffs = append(diffs, fmt.Sprintf("role_verified is %s, want %s", gotVerified, verified))
	}
	if len(diffs) > 0 {
		return fmt.Errorf("bot-role probe: actor %s: %s", p.ActorLogin, strings.Join(diffs, "; "))
	}
	return nil
}

// botRoleReviewEvent builds a minimal valid non-label GitHub event: a
// review submitted on a change proposal of repo by actor.
func botRoleReviewEvent(repo string, actor normevent.Actor) *normevent.Event {
	return &normevent.Event{
		Repo: repo,
		Entity: normevent.Entity{
			Kind: normevent.EntityChangeProposal,
			ID:   1,
			URL:  "https://github.com/" + repo + "/pull/1",
		},
		Transition: normevent.Transition{
			Kind:   normevent.TransitionReviewSubmitted,
			Review: &normevent.Review{State: "approved", ReviewerID: actor.ID},
		},
		Actor:  actor,
		State:  normevent.State{Labels: []string{}},
		Source: normevent.Source{System: normevent.SystemGitHub, RawType: "pull_request_review", RawAction: "submitted"},
	}
}

// thenBotRoleDispatch checks the pre-CEL dispatch gate
// (harnessdispatch.IsAuthorized) for a non-label event by the subject's
// actor. A denial must also keep a no-match (role_verified true) apart from
// a failed lookup (role_verified false).
func thenBotRoleDispatch(w *world.World, expected string) error {
	if expected != botRoleDispatchAllowed && expected != botRoleDispatchDenied {
		return fmt.Errorf("bot-role probe: unknown dispatch %q (want %q or %q)", expected, botRoleDispatchAllowed, botRoleDispatchDenied)
	}
	p, v, err := botRoleSubjectVerdict(w)
	if err != nil {
		return err
	}
	ev := botRoleReviewEvent(w.RepoOwner+"/"+w.RepoName, botRoleActor(p, v))
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("bot-role probe: the normalized event for %s is invalid: %w", p.ActorLogin, err)
	}
	got := botRoleDispatchDenied
	if harnessdispatch.IsAuthorized(ev) {
		got = botRoleDispatchAllowed
	}
	if got != expected {
		return fmt.Errorf("bot-role probe: non-label dispatch for %q (%q) is %q, want %q", p.ActorLogin, v.outcome, got, expected)
	}
	if got == botRoleDispatchDenied {
		if err := checkBotRoleDenialDistinct(v, ev.Actor); err != nil {
			return err
		}
	}
	return nil
}

// checkBotRoleDenialDistinct requires a denied actor to say why through
// role_verified: true for a completed no-match, false for a failed lookup.
func checkBotRoleDenialDistinct(v botRoleVerdict, actor normevent.Actor) error {
	if actor.RoleVerified == nil {
		return errors.New("bot-role probe: a denied bot carries no role_verified, so a no-match cannot be told from a lookup failure")
	}
	var want bool
	switch v.outcome {
	case botRoleOutcomeNoMatch:
		want = true
	case botRoleOutcomeLookupFailed:
		want = false
	default:
		return fmt.Errorf("bot-role probe: a %q lookup was denied before CEL", v.outcome)
	}
	if *actor.RoleVerified != want {
		return fmt.Errorf("bot-role probe: a %q carries role_verified %t, want %t", v.outcome, *actor.RoleVerified, want)
	}
	return nil
}
