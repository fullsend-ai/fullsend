package steps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// The CLI mint status steps check that an operator who knows only the
// mint address and has an authorized GitHub account gets the mint's
// status through `fullsend mint status --mint-url <address>`. They run
// the suite's CLI binary as an operator would, with no --project, in a
// clean environment: no GitHub Actions OIDC variables, no GCP project
// or credentials, no FULLSEND_MINT_URL, a fresh HOME so no gh login is
// found, and GH_TOKEN set to the credential under test. The CLI's
// normal GitHub credential discovery picks GH_TOKEN up. The /v1/status
// endpoint is an implementation detail of the CLI; nothing here calls
// it directly.
//
// A failure is reported in one of four classes, so a reader can tell
// them apart: authentication (the mint rejected the credential),
// transport (the CLI could not reach the mint), deployment (no suite
// mint address, or the address answered but not as a mint's status
// API) and assertion (the CLI succeeded but reported the wrong facts,
// or an expected failure did not happen). The mint answers a GitHub
// token it does not accept with the same 401 whether the token is
// invalid, its user is not an active member of the status group, or
// the mint was deployed without GitHub status auth, so a rejected write
// actor credential is an authentication failure whose message names
// all three causes.

const (
	// Mint address phrases, as in step text.
	mintStatusAddressSuite       = "suite mint address"
	mintStatusAddressUnreachable = "unreachable mint address"
	mintStatusAddressNonMint     = "suite mint address with a non-mint path"

	// Credential phrases, as in step text.
	mintStatusCredentialWrite   = "write actor credential"
	mintStatusCredentialInvalid = "invalid credential"

	// Outcome and failure classes.
	mintStatusOutcomeOK             = "ok"
	mintStatusClassAuthentication   = "authentication"
	mintStatusClassTransport        = "transport"
	mintStatusClassDeployment       = "deployment"
	mintStatusClassAssertion        = "assertion"
	mintStatusOutcomeUnclassifiable = "unclassified"

	// mintStatusUnreachableURL is an HTTPS address nothing listens on:
	// the connection is refused locally, so the CLI fails in transport
	// without any request leaving the runner.
	mintStatusUnreachableURL = "https://127.0.0.1:1"

	// mintStatusNonMintPath is appended to the suite mint address for
	// the deployment control. The mint answers 404 for any path other
	// than its API, so the credential stays with the suite's own mint.
	mintStatusNonMintPath = "/not-a-mint"

	// mintStatusAuthMethod is the CLI's name for GitHub user-token auth
	// (mintclient.StatusAuthGitHub).
	mintStatusAuthMethod = "GitHub"
)

// mintStatusTimeout bounds one CLI run. The CLI retries a failing
// request a few times with a 30s HTTP timeout each; this leaves room
// for that and kills a hung run. Overridable in tests.
var mintStatusTimeout = 3 * time.Minute

// mintStatusPassthroughEnv are the only runner environment variables
// the CLI inherits: what a process needs to find binaries, write
// temporary files and reach the network through the runner's proxy and
// trust store. Everything else, including OIDC, GCP and GitHub token
// variables, is withheld.
var mintStatusPassthroughEnv = []string{
	"PATH", "TMPDIR",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
}

// Markers in the CLI's output (runMintStatusAPI in internal/cli and the
// errors of mintclient.QueryStatus) that classify a failed run.
var (
	mintStatusAuthMarkers      = []string{"authentication failed for /v1/status"}
	mintStatusTransportMarkers = []string{"requesting status:"}
	mintStatusDeployMarkers    = []string{
		"status endpoint returned HTTP",
		"parsing status response",
		"mint URL must use HTTPS",
		"invalid mint URL",
		"constructing status URL",
	}
)

// Step patterns of the mint status steps.
const (
	mintStatusAddressStep = `^the (` + mintStatusAddressNonMint + `|` + mintStatusAddressUnreachable + `|` + mintStatusAddressSuite + `)$`
	mintStatusRunStep     = `^the operator runs mint status with the (` + mintStatusCredentialWrite + `|` + mintStatusCredentialInvalid + `)$`
	mintStatusOutcomeStep = `^the mint status outcome is "([^"]+)"$`
	mintStatusFactsStep   = `^mint status reports the suite mint's status facts$`
	mintStatusAbsentStep  = `^the credential is absent from the mint status output and artifacts$`
)

func registerMintStatusSteps(sc *godog.ScenarioContext) {
	sc.Step(mintStatusAddressStep, func(ctx context.Context, address string) (context.Context, error) {
		return ctx, givenMintStatusAddress(ctx, world.FromContext(ctx), address)
	})
	sc.Step(mintStatusRunStep, func(ctx context.Context, credential string) (context.Context, error) {
		return ctx, whenOperatorRunsMintStatus(ctx, world.FromContext(ctx), credential)
	})
	sc.Step(mintStatusOutcomeStep, func(ctx context.Context, outcome string) (context.Context, error) {
		return ctx, thenMintStatusOutcome(world.FromContext(ctx), outcome)
	})
	sc.Step(mintStatusFactsStep, func(ctx context.Context) (context.Context, error) {
		return ctx, thenMintStatusReportsFacts(world.FromContext(ctx))
	})
	sc.Step(mintStatusAbsentStep, func(ctx context.Context) (context.Context, error) {
		return ctx, thenMintStatusCredentialAbsent(world.FromContext(ctx))
	})
}

// mintStatusError is a classified mint status failure. Its message
// starts with the class.
type mintStatusError struct {
	Class string
	Msg   string
}

func (e *mintStatusError) Error() string { return e.Class + ": " + e.Msg }

func mintStatusFailure(class, format string, args ...any) error {
	return &mintStatusError{Class: class, Msg: fmt.Sprintf(format, args...)}
}

// suiteMintURL returns the URL of the mint serving the leased repo's org
// (World.MintURL, from the lease), or else the mint the suite's install
// driver was configured with.
func suiteMintURL(ctx context.Context, w *world.World) (string, error) {
	if u := strings.TrimSpace(w.MintURL); u != "" {
		return u, nil
	}
	// Every pool org is served by its mint group's mint, deployed with
	// the slot lease. A scenario that has not leased yet leases a slot
	// here, so the mint it queries is the leased org's, and the lease
	// records the mint's status group and workflow hosts in the World.
	if _, ok := w.Driver.(install.LeaseDriver); ok && w.LeasedRepoName == "" {
		if err := givenEnrolledTestRepository(ctx, w); err != nil {
			return "", mintStatusFailure(mintStatusClassDeployment,
				"suite mint address unknown: leasing a pool slot for its org's mint: %v", err)
		}
		if u := strings.TrimSpace(w.MintURL); u != "" {
			return u, nil
		}
	}
	reporter, ok := w.Driver.(install.MintURLReporter)
	if !ok {
		return "", mintStatusFailure(mintStatusClassDeployment,
			"suite mint address unknown: install driver %T does not report its mint URL", w.Driver)
	}
	mintURL := strings.TrimSpace(reporter.MintURL())
	if mintURL == "" {
		return "", mintStatusFailure(mintStatusClassDeployment,
			"suite mint address unknown: install driver reported no mint URL")
	}
	return mintURL, nil
}

// givenMintStatusAddress records the mint address the operator will
// pass as --mint-url.
func givenMintStatusAddress(ctx context.Context, w *world.World, address string) error {
	var mintURL string
	switch address {
	case mintStatusAddressSuite, mintStatusAddressNonMint:
		u, err := suiteMintURL(ctx, w)
		if err != nil {
			return err
		}
		mintURL = u
		if address == mintStatusAddressNonMint {
			mintURL = strings.TrimRight(mintURL, "/") + mintStatusNonMintPath
		}
	case mintStatusAddressUnreachable:
		mintURL = mintStatusUnreachableURL
	default:
		return fmt.Errorf("unknown mint address %q", address)
	}
	w.MintStatus = &world.MintStatus{MintURL: mintURL}
	return nil
}

// mintStatusCredential returns the token for a credential phrase.
func mintStatusCredential(w *world.World, credential string) (string, error) {
	switch credential {
	case mintStatusCredentialWrite:
		if w.WriteToken == "" {
			return "", fmt.Errorf("TEST_ACTOR_WRITE_PAT not set")
		}
		return w.WriteToken, nil
	case mintStatusCredentialInvalid:
		// A random value no GitHub token can match, so GitHub rejects
		// it when the mint looks up its user.
		b := make([]byte, 20)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generating invalid credential: %w", err)
		}
		return "fullsend-behaviour-invalid-" + hex.EncodeToString(b), nil
	default:
		return "", fmt.Errorf("unknown credential %q", credential)
	}
}

// mintStatusEnv builds the CLI's environment: the passthrough
// variables from environ, a fresh HOME and gh config directory under
// home, plain output, and GH_TOKEN set to token.
func mintStatusEnv(environ []string, home, token string) []string {
	env := make([]string, 0, len(mintStatusPassthroughEnv)+6)
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && slices.Contains(mintStatusPassthroughEnv, name) {
			env = append(env, kv)
		}
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GH_CONFIG_DIR="+filepath.Join(home, ".config", "gh"),
		"NO_COLOR=1",
		"GH_TOKEN="+token,
	)
}

// mintStatusArgs are the CLI arguments: the mint address only, so no
// --project, and the API-based status path.
func mintStatusArgs(mintURL string) []string {
	return []string{"mint", "status", "--mint-url", mintURL}
}

// whenOperatorRunsMintStatus runs `fullsend mint status --mint-url` as
// an operator holding the named credential. A run that exits non-zero
// is recorded, not returned: the outcome step classifies it. Only a
// run that could not be started fails here.
func whenOperatorRunsMintStatus(ctx context.Context, w *world.World, credential string) error {
	ms := w.MintStatus
	if ms == nil {
		return fmt.Errorf("no mint address: a mint address step must run first")
	}
	if w.CLIBinary == "" {
		return fmt.Errorf("no fullsend CLI binary configured for the suite")
	}
	token, err := mintStatusCredential(w, credential)
	if err != nil {
		return err
	}
	RegisterRedactionLiterals(w)
	registerSecretForms(token)

	home, err := os.MkdirTemp("", "behaviour-mint-status-home-*")
	if err != nil {
		return fmt.Errorf("creating CLI home: %w", err)
	}
	defer os.RemoveAll(home)

	runCtx, cancel := context.WithTimeout(ctx, mintStatusTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, w.CLIBinary, mintStatusArgs(ms.MintURL)...)
	cmd.Dir = home
	cmd.Env = mintStatusEnv(os.Environ(), home, token)
	// Stop waiting for output a killed CLI's children may still hold.
	cmd.WaitDelay = 10 * time.Second
	out, runErr := cmd.CombinedOutput()

	raw := string(out)
	ms.Credential = credential
	ms.Ran = true
	ms.CredentialInOutput = strings.Contains(raw, token)
	ms.Output = strings.ReplaceAll(RedactText(raw), token, "***")
	ms.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)
	ms.ExitCode = 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return fmt.Errorf("running fullsend mint status: %w", runErr)
		}
		ms.ExitCode = exitErr.ExitCode()
	}

	logRedacted(w, "mint status (%s, %s): exit %d", credential, ms.MintURL, ms.ExitCode)
	saveMintStatusOutput(w, ms)
	return nil
}

// saveMintStatusOutput writes the redacted CLI output under
// BEHAVIOUR_ARTIFACT_DIR for debugging. Best-effort: a write failure is
// logged and does not fail the scenario.
func saveMintStatusOutput(w *world.World, ms *world.MintStatus) {
	artifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if artifactDir == "" {
		return
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		logRedacted(w, "mint status: creating artifact dir: %v", err)
		return
	}
	dir, err := os.MkdirTemp(artifactDir, "mint-status-"+pathSlug(w.ScenarioName, 60, "unnamed")+"-")
	if err != nil {
		logRedacted(w, "mint status: creating artifact dir: %v", err)
		return
	}
	content := fmt.Sprintf("fullsend %s\ncredential: %s\nexit code: %d\ntimed out: %t\n\n%s",
		strings.Join(mintStatusArgs(ms.MintURL), " "), ms.Credential, ms.ExitCode, ms.TimedOut, ms.Output)
	if err := writeRedactedArtifact(filepath.Join(dir, "cli-output.txt"), content); err != nil {
		logRedacted(w, "mint status: %v", err)
	}
}

// classifyMintStatus returns the outcome of a CLI run: "ok" for a zero
// exit, a failure class, or "unclassified" when no marker matches.
func classifyMintStatus(ms *world.MintStatus) string {
	switch {
	case ms.TimedOut:
		return mintStatusClassTransport
	case ms.ExitCode == 0:
		return mintStatusOutcomeOK
	case containsAny(ms.Output, mintStatusAuthMarkers):
		return mintStatusClassAuthentication
	case containsAny(ms.Output, mintStatusTransportMarkers):
		return mintStatusClassTransport
	case containsAny(ms.Output, mintStatusDeployMarkers):
		return mintStatusClassDeployment
	default:
		return mintStatusOutcomeUnclassifiable
	}
}

func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// mintStatusFailureDetail explains a failed run for the step error.
func mintStatusFailureDetail(w *world.World, ms *world.MintStatus, outcome string) string {
	var b strings.Builder
	if ms.TimedOut {
		fmt.Fprintf(&b, "fullsend mint status did not finish within %s", mintStatusTimeout)
	} else {
		fmt.Fprintf(&b, "fullsend mint status --mint-url %s exited %d", ms.MintURL, ms.ExitCode)
	}
	if outcome == mintStatusClassAuthentication && ms.Credential == mintStatusCredentialWrite {
		group := w.MintStatusGroup
		if group == "" {
			group = "the mint's status group (ORG/" + install.MintStatusTeamSlug + ")"
		}
		login := w.WriteLogin
		if login == "" {
			login = "the write actor"
		}
		fmt.Fprintf(&b, "; the mint rejected the write actor's GitHub token: the token is invalid, %s is not an active member of %s, or the mint was not deployed with --status-auth=oidc,github --status-github-group=%s",
			login, group, group)
	}
	fmt.Fprintf(&b, "\n%s", ms.Output)
	return b.String()
}

// thenMintStatusOutcome checks the CLI run's outcome. "ok" requires a
// successful run; a failure class requires a failed run of that class.
// No other result passes: a run of another class fails with its own
// class, and a success where a failure was expected fails as an
// assertion.
func thenMintStatusOutcome(w *world.World, want string) error {
	switch want {
	case mintStatusOutcomeOK, mintStatusClassAuthentication, mintStatusClassTransport, mintStatusClassDeployment:
	default:
		return fmt.Errorf("unknown mint status outcome %q", want)
	}
	ms := w.MintStatus
	if ms == nil || !ms.Ran {
		return fmt.Errorf("mint status was not run")
	}
	got := classifyMintStatus(ms)
	if got == want {
		return nil
	}
	switch {
	case want == mintStatusOutcomeOK && got != mintStatusOutcomeUnclassifiable:
		return mintStatusFailure(got, "%s", mintStatusFailureDetail(w, ms, got))
	case got == mintStatusOutcomeOK:
		return mintStatusFailure(mintStatusClassAssertion,
			"fullsend mint status --mint-url %s with the %s succeeded, want a %s failure\n%s",
			ms.MintURL, ms.Credential, want, ms.Output)
	default:
		return mintStatusFailure(mintStatusClassAssertion,
			"mint status outcome %q, want %q: %s", got, want, mintStatusFailureDetail(w, ms, got))
	}
}

// mintStatusReport is what `fullsend mint status --mint-url` printed.
type mintStatusReport struct {
	AuthMethod        string
	URL               string
	Org               string
	OrgReported       bool
	Roles             []string
	WorkflowHostRepos []string
	SummaryAuth       string
	SummaryRoles      int
	SummaryRolesSet   bool
}

var mintStatusSummaryRoles = regexp.MustCompile(`^Roles: (\d+)$`)

// parseMintStatusOutput reads the CLI's human-readable status report
// (runMintStatusAPI in internal/cli). Sections start with a "→ "
// header and end at a blank line; the summary box's lines are framed
// by "│".
func parseMintStatusOutput(output string) mintStatusReport {
	var r mintStatusReport
	section := ""
	for line := range strings.SplitSeq(output, "\n") {
		text := strings.TrimSpace(line)
		if inner, ok := strings.CutPrefix(text, "│"); ok {
			inner = strings.TrimSpace(strings.TrimSuffix(inner, "│"))
			if v, ok := strings.CutPrefix(inner, "Auth: "); ok {
				r.SummaryAuth = v
			}
			if m := mintStatusSummaryRoles.FindStringSubmatch(inner); m != nil {
				r.SummaryRoles, _ = strconv.Atoi(m[1])
				r.SummaryRolesSet = true
			}
			continue
		}
		if text == "" {
			section = ""
			continue
		}
		if header, ok := strings.CutPrefix(text, "→ "); ok {
			section = header
			if section == "Caller Organization" {
				r.OrgReported = true
			}
			continue
		}
		if v, ok := strings.CutPrefix(text, "✓ Authenticated via "); ok {
			r.AuthMethod = v
			continue
		}
		if v, ok := strings.CutPrefix(text, "URL: "); ok && section == "" {
			r.URL = v
			continue
		}
		switch section {
		case "Caller Organization":
			r.Org = text
		case "Roles":
			if text != "(none)" {
				r.Roles = append(r.Roles, text)
			}
		case "Workflow Host Repos":
			r.WorkflowHostRepos = append(r.WorkflowHostRepos, text)
		}
	}
	return r
}

// mintStatusFacts checks a status report against what the suite's
// mint must report to the write actor. ownHost is the scenario's own
// workflow host (ORG/REPO), which must be reported. wantHosts is the
// mint's configured WORKFLOW_HOST_REPOS (every org of its mint group);
// the report must list exactly those. With no wantHosts, every
// reported host must be in ownHost's org. It returns every mismatch.
func mintStatusFacts(r mintStatusReport, mintURL, ownHost string, wantHosts []string) []string {
	var problems []string
	if r.AuthMethod != mintStatusAuthMethod {
		problems = append(problems, fmt.Sprintf("authenticated via %q, want %q (GitHub user token)", r.AuthMethod, mintStatusAuthMethod))
	}
	if r.SummaryAuth != mintStatusAuthMethod {
		problems = append(problems, fmt.Sprintf("summary auth %q, want %q", r.SummaryAuth, mintStatusAuthMethod))
	}
	if r.URL != mintURL {
		problems = append(problems, fmt.Sprintf("URL %q, want %q", r.URL, mintURL))
	}
	if r.OrgReported {
		problems = append(problems, fmt.Sprintf("caller organization %q reported; a GitHub user-token status carries none (OIDC auth?)", r.Org))
	}
	if len(r.Roles) == 0 {
		problems = append(problems, "no roles reported")
	}
	if !r.SummaryRolesSet || r.SummaryRoles != len(r.Roles) {
		problems = append(problems, fmt.Sprintf("summary role count does not match the %d listed roles", len(r.Roles)))
	}
	// The fullsend-test roles, which the mint scope scenarios require
	// the suite's mint to allow.
	for _, role := range slices.Sorted(maps.Keys(mintProbeRolePermissions)) {
		if !slices.Contains(r.Roles, role) {
			problems = append(problems, fmt.Sprintf("role %q not reported", role))
		}
	}
	own := strings.ToLower(ownHost)
	reported := make(map[string]bool, len(r.WorkflowHostRepos))
	for _, repo := range r.WorkflowHostRepos {
		reported[strings.ToLower(repo)] = true
	}
	if len(wantHosts) > 0 {
		want := make(map[string]bool, len(wantHosts))
		for _, repo := range wantHosts {
			want[strings.ToLower(repo)] = true
		}
		for _, repo := range r.WorkflowHostRepos {
			if !want[strings.ToLower(repo)] {
				problems = append(problems, fmt.Sprintf("workflow host repo %q is not a workflow host of the mint's group", repo))
			}
		}
		for _, repo := range wantHosts {
			if !reported[strings.ToLower(repo)] {
				problems = append(problems, fmt.Sprintf("workflow host repo %q of the mint's group not reported", repo))
			}
		}
	} else {
		org, _, _ := strings.Cut(own, "/")
		for _, repo := range r.WorkflowHostRepos {
			if !strings.HasPrefix(strings.ToLower(repo), org+"/") {
				problems = append(problems, fmt.Sprintf("workflow host repo %q is outside org %s", repo, org))
			}
		}
	}
	if !reported[own] {
		problems = append(problems, fmt.Sprintf("workflow host repos %v do not include %s", r.WorkflowHostRepos, ownHost))
	}
	return problems
}

// mintStatusOwnHost is the scenario's own workflow host: its leased
// repository, or its org's first pool repository when it leased none.
func mintStatusOwnHost(w *world.World) string {
	repo := w.LeasedRepoName
	if repo == "" {
		repo = install.PoolSlotName(1)
	}
	return w.Org + "/" + repo
}

// thenMintStatusReportsFacts checks the successful run's output for
// the suite mint's status facts. Every mismatch is an assertion
// failure.
func thenMintStatusReportsFacts(w *world.World) error {
	ms := w.MintStatus
	if ms == nil || !ms.Ran {
		return fmt.Errorf("mint status was not run")
	}
	if got := classifyMintStatus(ms); got != mintStatusOutcomeOK {
		return mintStatusFailure(got, "%s", mintStatusFailureDetail(w, ms, got))
	}
	problems := mintStatusFacts(parseMintStatusOutput(ms.Output), ms.MintURL, mintStatusOwnHost(w), w.MintWorkflowHostRepos)
	if len(problems) > 0 {
		return mintStatusFailure(mintStatusClassAssertion, "mint status facts:\n  %s\n%s",
			strings.Join(problems, "\n  "), ms.Output)
	}
	return nil
}

// thenMintStatusCredentialAbsent checks that the CLI did not print the
// credential it ran with, and that no file under BEHAVIOUR_ARTIFACT_DIR
// holds the write actor's PAT.
func thenMintStatusCredentialAbsent(w *world.World) error {
	ms := w.MintStatus
	if ms == nil || !ms.Ran {
		return fmt.Errorf("mint status was not run")
	}
	if ms.CredentialInOutput {
		return mintStatusFailure(mintStatusClassAssertion,
			"fullsend mint status printed the %s", ms.Credential)
	}
	artifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if artifactDir == "" || w.WriteToken == "" {
		return nil
	}
	found, err := filesContaining(artifactDir, w.WriteToken)
	if err != nil {
		return fmt.Errorf("scanning behaviour artifacts: %w", err)
	}
	if len(found) > 0 {
		return mintStatusFailure(mintStatusClassAssertion, "%s",
			RedactText(fmt.Sprintf("write actor PAT found in behaviour artifacts: %s", strings.Join(found, ", "))))
	}
	return nil
}

// filesContaining returns the paths, relative to root, of regular files
// under root whose content contains needle. A missing root holds none.
// Scenarios may run concurrently and write into root, so in-progress
// ".artifact-*" temp files are skipped and a file that vanishes between
// listing and reading is ignored.
func filesContaining(root, needle string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".artifact-") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if strings.Contains(string(data), needle) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			found = append(found, rel)
		}
		return nil
	})
	return found, err
}
