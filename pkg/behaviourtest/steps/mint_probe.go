package steps

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// The mint probe steps exercise the GitHub per-repo mint request
// contract with real GitHub Actions OIDC tokens. For each scenario they:
//
//  1. create a temporary, public host repository in the pool org holding
//     the reusable probe workflow (mint_probe_workflow.yml): the approved
//     host test-repo-NN-mint-host, which the install driver lists on the
//     mint's WORKFLOW_HOST_REPOS, and for unapproved-host rows also
//     test-repo-NN-mint-unapproved, which is on no list;
//  2. push a shim workflow to a temporary branch of the leased repository
//     that calls the probe twice in one run: a control request (same
//     role and level, the leased repository only, approved host) and the
//     subject request under test;
//  3. classify the two uploaded results.
//
// The leased repository is the OIDC repository claim and the host
// repository the job_workflow_ref claim, so the shim and the workflow
// host are always different repositories. The control must mint for the
// subject to count: a host denial answers 401 "authentication failed",
// the same as an OIDC verification failure. A control minted from the same
// shim in the same run rules out a failure common to both requests, and the
// probe verifying each OIDC token itself (oidc_verified) rules out a failure
// specific to the subject's token. Probe errors
// (OIDC, transport, server, malformed or missing results, failed runs)
// are reported as infrastructure failures and never satisfy an expected
// denial.

//go:embed mint_probe_workflow.yml
var mintProbeWorkflow []byte

const (
	mintProbeForge = "github"

	// mintProbeWorkflowPath is where the reusable probe workflow lives in
	// a host repository.
	mintProbeWorkflowPath = ".github/workflows/mint-probe.yml"

	// mintProbeUnapprovedHostSuffix names the unapproved host for the
	// leased repo: test-repo-NN + suffix. It must never be listed in the
	// mint's WORKFLOW_HOST_REPOS (see install.MintProbeHostRepoSuffix).
	mintProbeUnapprovedHostSuffix = "-mint-unapproved"

	mintProbeControlArtifact = "mint-probe-control"
	mintProbeSubjectArtifact = "mint-probe-subject"
	mintProbeResultFile      = "mint-probe-result.json"

	mintProbeHostApproved   = "approved"
	mintProbeHostUnapproved = "unapproved"

	// Values of the repos, target repo and selection columns.
	mintProbeReposShim  = "shim repo"
	mintProbeReposOther = "other repo"
	mintProbeReposAll   = "*"
	mintProbeNone       = "none"

	// Values of the outcome column.
	mintProbeOutcomeMinted      = "minted"
	mintProbeOutcomeScopeDenied = "scope-denied"
	mintProbeOutcomeHostDenied  = "host-denied"
)

// Poll budgets for the probe run, overridable in tests.
var (
	mintProbeAppearTimeout    = 5 * time.Minute
	mintProbeCompleteTimeout  = 15 * time.Minute
	mintProbePollInterval     = 10 * time.Second
	mintProbeArtifactAttempts = 5
	mintProbeArtifactDelay    = 3 * time.Second
)

// mintProbeRolePermissions duplicates canonicalRolePermissions in
// internal/mintcore/github.go for the roles of the fullsend-test app set
// (docs/guides/dev/e2e-testing.md#test-github-apps). pkg/behaviourtest
// must not import internal/mintcore (docs/guides/dev/behaviour-testing.md),
// so change both together.
var mintProbeRolePermissions = map[string]map[string]map[string]string{
	"triage": {
		"write": {"contents": "read", "issues": "write", "metadata": "read"},
		"read":  {"contents": "read", "issues": "read", "metadata": "read"},
	},
	"coder": {
		"write": {"contents": "write", "packages": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
		"read":  {"contents": "read", "packages": "read", "pull_requests": "read", "issues": "read", "checks": "read", "metadata": "read"},
	},
	"review": {
		"write": {"contents": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
		"read":  {"contents": "read", "pull_requests": "read", "issues": "read", "checks": "read", "metadata": "read"},
	},
	"retro": {
		"write": {"actions": "read", "contents": "read", "pull_requests": "write", "issues": "write", "metadata": "read"},
		"read":  {"actions": "read", "contents": "read", "pull_requests": "read", "issues": "read", "metadata": "read"},
	},
	"prioritize": {
		"write": {"contents": "read", "issues": "write", "organization_projects": "write", "metadata": "read"},
		"read":  {"contents": "read", "issues": "read", "organization_projects": "read", "metadata": "read"},
	},
	"fullsend": {
		"write": {"actions": "write", "actions_variables": "read", "contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read"},
		"read":  {"actions": "read", "actions_variables": "read", "contents": "read", "pull_requests": "read", "metadata": "read"},
	},
}

// mintProbeOptionalPermissions duplicates optionalRolePermissions in
// internal/mintcore/github.go: permissions the mint may drop while an
// installation has not accepted an App permission update.
var mintProbeOptionalPermissions = map[string]map[string]bool{
	"coder": {"packages": true},
}

// mintProbeScopeDenials are the mint's 403 messages for a same-org repos
// scope it refuses (validateReposScope in internal/mintcore). Only these
// count as a scope denial: any other 403 (for example "role not allowed")
// is a misconfiguration, not the policy under test.
var mintProbeScopeDenials = []string{
	"same-org mint requires non-empty repos",
	"per-repo mint requires repos to be exactly the requesting repository",
}

// mintProbeAuthFailedMessage is the mint's 401 body for both a workflow-host
// denial and an OIDC verification failure (handler.go in internal/mintcore).
// The mint stays generic on purpose, so the probe workflow verifies the OIDC
// token itself (signature, issuer, audience, time claims) and reports
// oidc_verified; a 401 only counts as a host denial for a verified token.
const mintProbeAuthFailedMessage = "authentication failed"

// mintProbeRoleRe and mintProbeLevelRe bound the values interpolated into
// the shim workflow.
var (
	mintProbeRoleRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	mintProbeLevelRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	mintProbeRepoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

func registerMintProbeSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a "([^"]*)" mint probe through the (approved|unapproved) workflow host$`, func(ctx context.Context, forgeName, arrangement string) (context.Context, error) {
		return ctx, givenMintProbe(ctx, world.FromContext(ctx), forgeName, arrangement)
	})
	sc.Step(`^the mint probe requests role "([^"]*)" at level "([^"]*)" for repos "([^"]*)"$`, func(ctx context.Context, role, level, repos string) (context.Context, error) {
		return ctx, whenMintProbeRequests(ctx, world.FromContext(ctx), role, level, repos)
	})
	sc.Step(`^the mint probe outcome is "([^"]*)"$`, func(ctx context.Context, outcome string) (context.Context, error) {
		return ctx, thenMintProbeOutcome(world.FromContext(ctx), outcome)
	})
	sc.Step(`^the mint probe target repository is "([^"]*)"$`, func(ctx context.Context, target string) (context.Context, error) {
		return ctx, thenMintProbeTargetRepo(world.FromContext(ctx), target)
	})
	sc.Step(`^the mint probe repository selection is "([^"]*)"$`, func(ctx context.Context, selection string) (context.Context, error) {
		return ctx, thenMintProbeSelection(world.FromContext(ctx), selection)
	})
}

// givenMintProbe creates the probe host repositories for the leased
// repository: the approved host always (the control request runs through
// it), and the unapproved host when the subject request must use one.
func givenMintProbe(ctx context.Context, w *world.World, forgeName, arrangement string) error {
	if err := ensureScenarioRepo(w); err != nil {
		return err
	}
	if forgeName != mintProbeForge {
		return fmt.Errorf("mint probe: forge %q is not supported (GitHub per-repo mint only)", forgeName)
	}
	if suiteSCM := w.Config.SCM; suiteSCM != "" && suiteSCM != forgeName {
		return fmt.Errorf("mint probe: the row targets forge %q but the suite runs BEHAVIOUR_SCM=%q", forgeName, suiteSCM)
	}
	if w.MintProbe != nil {
		return fmt.Errorf("mint probe: already set up for this scenario")
	}
	if arrangement != mintProbeHostApproved && arrangement != mintProbeHostUnapproved {
		return fmt.Errorf("mint probe: unknown workflow host arrangement %q", arrangement)
	}

	p := &world.MintProbe{
		Forge:            forgeName,
		HostArrangement:  arrangement,
		ApprovedHostRepo: w.RepoName + install.MintProbeHostRepoSuffix,
	}
	p.SubjectHostRepo = p.ApprovedHostRepo
	if arrangement == mintProbeHostUnapproved {
		p.SubjectHostRepo = w.RepoName + mintProbeUnapprovedHostSuffix
	}
	// Attach before creating anything so CleanupScenario sees every repo
	// created even when a later step fails.
	w.MintProbe = p

	hosts := []string{p.ApprovedHostRepo}
	if p.SubjectHostRepo != p.ApprovedHostRepo {
		hosts = append(hosts, p.SubjectHostRepo)
	}
	for _, host := range hosts {
		if err := createMintProbeHost(ctx, w, p, host); err != nil {
			return err
		}
	}
	return nil
}

// createMintProbeHost creates a public host repository and commits the
// reusable mint probe workflow to its default branch.
//
// Host repositories are temporary repos named after the leased slot, not
// leased pool slots (issue #8387 asks for probe workflows in leased repos).
// The slot lease already prevents name collisions and cleanup deletes the
// hosts. A temporary repo is needed because the unapproved host must not
// be on the mint's --workflow-host-repos list, and every pool repo is.
func createMintProbeHost(ctx context.Context, w *world.World, p *world.MintProbe, host string) error {
	return createProbeHost(ctx, w, probeHostSpec{
		label:        "mint probe",
		created:      &p.CreatedRepos,
		workflowPath: mintProbeWorkflowPath,
		workflow:     mintProbeWorkflow,
	}, host)
}

// probeHostSpec describes the reusable workflow a probe host repository
// carries. The mint probe and the bot-role probe share the host lifecycle.
type probeHostSpec struct {
	// label prefixes error messages ("mint probe", "bot-role probe").
	label string
	// created receives every host repository created, for cleanup.
	created *[]string
	// workflowPath and workflow are the reusable workflow committed to
	// the host's default branch.
	workflowPath string
	workflow     []byte
}

// createProbeHost creates a public host repository and commits spec's
// reusable workflow to its default branch.
func createProbeHost(ctx context.Context, w *world.World, spec probeHostSpec, host string) error {
	if host == w.RepoName {
		return fmt.Errorf("%s: host repository %q must differ from the leased repository", spec.label, host)
	}
	// The GitHub SCM driver's CreateRepo succeeds for a repository that
	// already exists, so a leftover from an earlier run would otherwise be
	// adopted as scenario-owned. Delete a leftover first, so only a repo
	// this run created is recorded for cleanup.
	if _, err := w.SCM.GetDefaultBranch(ctx, w.Org, host); err == nil {
		if err := w.SCM.DeleteRepo(ctx, w.Org, host); err != nil && !forge.IsNotFound(err) {
			return fmt.Errorf("%s: deleting stale host repository %s/%s: %w", spec.label, w.Org, host, err)
		}
		// GitHub keeps serving, and can keep reserving, a deleted repo's
		// name for a while, so wait for the deletion to propagate before
		// creating the replacement.
		if err := awaitProbeHostGone(ctx, w, spec.label, host); err != nil {
			return err
		}
	} else if !forge.IsNotFound(err) {
		return fmt.Errorf("%s: checking for an existing host repository %s/%s: %w", spec.label, w.Org, host, err)
	}
	if err := createProbeHostRepo(ctx, w, spec, host); err != nil {
		return err
	}
	// Another repository can call a reusable workflow only when the
	// host is public (or explicitly shared), and orgs may force new
	// repositories private.
	if err := w.SCM.EnsureRepoPublic(ctx, w.Org, host); err != nil {
		return fmt.Errorf("%s: host repository %s/%s must be public: %w", spec.label, w.Org, host, err)
	}
	if err := w.SCM.CommitFile(ctx, w.Org, host, spec.workflowPath, "behaviour: add "+spec.label+" workflow", spec.workflow); err != nil {
		return fmt.Errorf("%s: committing the probe workflow to %s/%s: %w", spec.label, w.Org, host, err)
	}
	if err := waitForFileAccessible(ctx, w, w.Org, host, spec.workflowPath); err != nil {
		return fmt.Errorf("%s: %w", spec.label, err)
	}
	return nil
}

// mintProbeRepoMaxAttempts and mintProbeRepoRetryDelay bound the
// propagation waits around host repository deletion and creation (the
// delay doubles each attempt). Overridden in tests to avoid slow loops.
var (
	mintProbeRepoMaxAttempts = 5
	mintProbeRepoRetryDelay  = time.Second
)

// awaitProbeHostGone polls until the deleted host repository returns
// not-found, so the replacement is created against a free name. It fails
// when the repository is still visible after the bounded backoff, since
// creating then would run against a repository that is still being deleted.
func awaitProbeHostGone(ctx context.Context, w *world.World, label, host string) error {
	delay := mintProbeRepoRetryDelay
	for attempt := 1; attempt <= mintProbeRepoMaxAttempts; attempt++ {
		_, err := w.SCM.GetDefaultBranch(ctx, w.Org, host)
		if forge.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: checking deletion of host repository %s/%s: %w", label, w.Org, host, err)
		}
		if attempt < mintProbeRepoMaxAttempts {
			if err := mintProbeSleep(ctx, delay); err != nil {
				return fmt.Errorf("%s: waiting for host repository %s/%s to be deleted: %w", label, w.Org, host, err)
			}
			delay *= 2
		}
	}
	return fmt.Errorf("%s: stale host repository %s/%s still visible after %d attempts to delete it", label, w.Org, host, mintProbeRepoMaxAttempts)
}

// createProbeHostRepo creates the host repository and waits until it
// is visible. The GitHub SCM driver reports success when GitHub says the
// name already exists, which is what a name still reserved after a delete
// looks like, so a create that does not become visible is retried with
// bounded, context-aware backoff.
//
// The host is recorded for cleanup as soon as the first CreateRepo
// succeeds, before the visibility checks that can fail, so a later error
// or cancellation does not leave a created public repo behind. Any
// leftover was already deleted by the caller, so a repo that exists at
// this point is one this run created; cleanup tolerates a repo that
// never became visible.
func createProbeHostRepo(ctx context.Context, w *world.World, spec probeHostSpec, host string) error {
	delay := mintProbeRepoRetryDelay
	for attempt := 1; attempt <= mintProbeRepoMaxAttempts; attempt++ {
		if err := w.SCM.CreateRepo(ctx, w.Org, host, "behaviour test: "+spec.label+" workflow host"); err != nil {
			return fmt.Errorf("%s: creating host repository %s/%s: %w", spec.label, w.Org, host, err)
		}
		if !slices.Contains(*spec.created, host) {
			*spec.created = append(*spec.created, host)
		}
		_, err := w.SCM.GetDefaultBranch(ctx, w.Org, host)
		if err == nil {
			return nil
		}
		if !forge.IsNotFound(err) {
			return fmt.Errorf("%s: checking new host repository %s/%s: %w", spec.label, w.Org, host, err)
		}
		if attempt < mintProbeRepoMaxAttempts {
			if err := mintProbeSleep(ctx, delay); err != nil {
				return fmt.Errorf("%s: waiting for host repository %s/%s to appear: %w", spec.label, w.Org, host, err)
			}
			delay *= 2
		}
	}
	return fmt.Errorf("%s: host repository %s/%s not visible after %d create attempts", spec.label, w.Org, host, mintProbeRepoMaxAttempts)
}

// whenMintProbeRequests pushes the shim, waits for its run and reads the
// control and subject results.
func whenMintProbeRequests(ctx context.Context, w *world.World, role, level, reposToken string) error {
	p := w.MintProbe
	if p == nil {
		return fmt.Errorf("mint probe: no probe set up; use 'a \"github\" mint probe through the ... workflow host' first")
	}
	if p.Run != nil {
		return fmt.Errorf("mint probe: a request was already made in this scenario")
	}
	if _, err := mintProbeExpectedPermissions(role, level); err != nil {
		return err
	}
	repos, err := resolveMintProbeRepos(w, p, reposToken)
	if err != nil {
		return err
	}
	p.Role, p.Level, p.Repos = role, level, repos

	lister, ok := w.CI.(ci.RunLister)
	if !ok {
		return fmt.Errorf("mint probe: CI driver %T cannot list workflow runs (ci.RunLister)", w.CI)
	}

	nonce, err := mintProbeNonce()
	if err != nil {
		return err
	}
	p.Branch = "behaviour/mint-probe-" + nonce
	p.WorkflowName = "mint-probe-" + nonce

	approvedRef, err := w.SCM.GetDefaultBranch(ctx, w.Org, p.ApprovedHostRepo)
	if err != nil {
		return fmt.Errorf("mint probe: resolving default branch for %s/%s: %w", w.Org, p.ApprovedHostRepo, err)
	}
	subjectRef := approvedRef
	if p.SubjectHostRepo != p.ApprovedHostRepo {
		if subjectRef, err = w.SCM.GetDefaultBranch(ctx, w.Org, p.SubjectHostRepo); err != nil {
			return fmt.Errorf("mint probe: resolving default branch for %s/%s: %w", w.Org, p.SubjectHostRepo, err)
		}
	}
	shim, err := renderMintProbeShim(mintProbeShimSpec{
		Name:        p.WorkflowName,
		Branch:      p.Branch,
		Org:         w.Org,
		Role:        role,
		Level:       level,
		ControlHost: p.ApprovedHostRepo,
		ControlRef:  approvedRef,
		SubjectHost: p.SubjectHostRepo,
		SubjectRef:  subjectRef,
		ShimRepo:    w.RepoName,
		Repos:       repos,
	})
	if err != nil {
		return err
	}

	if err := w.SCM.CreateBranch(ctx, w.RepoOwner, w.RepoName, p.Branch); err != nil {
		return fmt.Errorf("mint probe: creating branch %s: %w", p.Branch, err)
	}
	w.CreatedBranches = append(w.CreatedBranches, p.Branch)
	shimPath := ".github/workflows/" + p.WorkflowName + ".yml"
	if err := w.SCM.CommitFileToBranch(ctx, w.RepoOwner, w.RepoName, p.Branch, shimPath, "behaviour: add mint probe shim", shim); err != nil {
		return fmt.Errorf("mint probe: committing the shim to %s: %w", p.Branch, err)
	}

	run, err := waitForMintProbeRun(ctx, lister, w.RepoOwner, w.RepoName, p.WorkflowName)
	if err != nil {
		return fmt.Errorf("mint probe workflow failure: %w", err)
	}
	p.Run = run
	if run.Conclusion != "success" {
		return fmt.Errorf("mint probe workflow failure: run %d concluded %q (%s)", run.ID, run.Conclusion, run.HTMLURL)
	}

	if p.Control, err = downloadMintProbeResult(ctx, w, run.ID, mintProbeControlArtifact); err != nil {
		return fmt.Errorf("mint probe result-artifact failure: %w", err)
	}
	if p.Subject, err = downloadMintProbeResult(ctx, w, run.ID, mintProbeSubjectArtifact); err != nil {
		return fmt.Errorf("mint probe result-artifact failure: %w", err)
	}
	return nil
}

// resolveMintProbeRepos maps the repos column to the mint request's repos.
// "other repo" is the approved host: a repository of the same org that
// exists and is covered by the role's App installation, so a denial
// cannot come from a missing repository.
func resolveMintProbeRepos(w *world.World, p *world.MintProbe, token string) ([]string, error) {
	switch token {
	case mintProbeReposShim:
		return []string{w.RepoName}, nil
	case mintProbeReposOther:
		return []string{p.ApprovedHostRepo}, nil
	case mintProbeReposAll:
		return []string{"*"}, nil
	default:
		return nil, fmt.Errorf("mint probe: unknown repos value %q (want %q, %q or %q)", token, mintProbeReposShim, mintProbeReposOther, mintProbeReposAll)
	}
}

// mintProbeExpectedPermissions returns the permissions a minted token of
// role at level must carry.
func mintProbeExpectedPermissions(role, level string) (map[string]string, error) {
	levels, ok := mintProbeRolePermissions[role]
	if !ok {
		return nil, fmt.Errorf("mint probe: role %q is not a fullsend-test role (supported: %s)", role, strings.Join(slices.Sorted(maps.Keys(mintProbeRolePermissions)), ", "))
	}
	perms, ok := levels[level]
	if !ok {
		return nil, fmt.Errorf("mint probe: role %q has no level %q (want read or write)", role, level)
	}
	return perms, nil
}

func mintProbeNonce() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint probe: generating nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// mintProbeShimSpec is the input of renderMintProbeShim.
type mintProbeShimSpec struct {
	Name, Branch, Org       string
	Role, Level             string
	ControlHost, ControlRef string
	SubjectHost, SubjectRef string
	ShimRepo                string
	Repos                   []string
}

// renderMintProbeShim renders the shim workflow pushed to the leased
// repository. It passes the leased repository's FULLSEND_MINT_URL to both
// probe jobs, so the probe exercises the mint that repository is
// installed against.
func renderMintProbeShim(s mintProbeShimSpec) ([]byte, error) {
	if !mintProbeRoleRe.MatchString(s.Role) {
		return nil, fmt.Errorf("mint probe: invalid role %q", s.Role)
	}
	if !mintProbeLevelRe.MatchString(s.Level) {
		return nil, fmt.Errorf("mint probe: invalid level %q", s.Level)
	}
	for _, v := range []string{s.Name, s.Org, s.ControlHost, s.SubjectHost, s.ShimRepo} {
		if !mintProbeRepoRe.MatchString(v) {
			return nil, fmt.Errorf("mint probe: invalid name %q", v)
		}
	}
	for _, v := range []string{s.Branch, s.ControlRef, s.SubjectRef} {
		if v == "" || strings.ContainsAny(v, "'\"\n\r\t {}$") {
			return nil, fmt.Errorf("mint probe: invalid ref %q", v)
		}
	}
	for _, r := range s.Repos {
		if r != "*" && !mintProbeRepoRe.MatchString(r) {
			return nil, fmt.Errorf("mint probe: invalid repos entry %q", r)
		}
	}
	controlRepos, err := json.Marshal([]string{s.ShimRepo})
	if err != nil {
		return nil, err
	}
	subjectRepos, err := json.Marshal(s.Repos)
	if err != nil {
		return nil, err
	}

	job := func(host, ref, repos, artifact string) string {
		return fmt.Sprintf(`    permissions:
      contents: read
      id-token: write
    uses: %s/%s/%s@%s
    with:
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      role: '%s'
      level: '%s'
      repos_json: '%s'
      artifact_name: '%s'
`, s.Org, host, mintProbeWorkflowPath, ref, s.Role, s.Level, repos, artifact)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `# Behaviour-test mint probe shim (pkg/behaviourtest/steps/mint_probe.go).
# Pushed to a temporary branch of the leased test repository; scenario
# cleanup deletes the branch.
name: %s

on:
  push:
    branches: ['%s']

permissions: {}

jobs:
  # Same-repository request through the approved host: must mint.
  control:
`, s.Name, s.Branch)
	b.WriteString(job(s.ControlHost, s.ControlRef, string(controlRepos), mintProbeControlArtifact))
	b.WriteString("\n  # The request under test.\n  subject:\n")
	b.WriteString(job(s.SubjectHost, s.SubjectRef, string(subjectRepos), mintProbeSubjectArtifact))
	return []byte(b.String()), nil
}

// mintProbeSleep waits for d or until ctx is done.
func mintProbeSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// waitForMintProbeRun finds the shim's run by its unique workflow name and
// waits for it to complete.
func waitForMintProbeRun(ctx context.Context, lister ci.RunLister, owner, repo, name string) (*forge.WorkflowRun, error) {
	var lastErr error
	find := func() *forge.WorkflowRun {
		runs, err := lister.ListRecentRuns(ctx, owner, repo, 50)
		if err != nil {
			lastErr = err
			return nil
		}
		var found *forge.WorkflowRun
		for i := range runs {
			if runs[i].Name == name && (found == nil || runs[i].ID > found.ID) {
				found = &runs[i]
			}
		}
		return found
	}
	describe := func(what string) error {
		if lastErr != nil {
			return fmt.Errorf("%s (last listing error: %v)", what, lastErr)
		}
		return errors.New(what)
	}

	var run *forge.WorkflowRun
	deadline := time.Now().Add(mintProbeAppearTimeout)
	for run == nil {
		if run = find(); run != nil {
			break
		}
		if !time.Now().Before(deadline) {
			return nil, describe(fmt.Sprintf("workflow %q did not start within %s", name, mintProbeAppearTimeout))
		}
		if err := mintProbeSleep(ctx, mintProbePollInterval); err != nil {
			return nil, err
		}
	}

	deadline = time.Now().Add(mintProbeCompleteTimeout)
	for run.Status != "completed" {
		if !time.Now().Before(deadline) {
			return nil, describe(fmt.Sprintf("workflow %q run %d did not complete within %s (%s)", name, run.ID, mintProbeCompleteTimeout, run.HTMLURL))
		}
		if err := mintProbeSleep(ctx, mintProbePollInterval); err != nil {
			return nil, err
		}
		if latest := find(); latest != nil {
			run = latest
		}
	}
	return run, nil
}

// downloadMintProbeResult downloads one probe job's artifact and parses
// its result. Artifacts can trail the run's completion briefly, so the
// download is retried.
func downloadMintProbeResult(ctx context.Context, w *world.World, runID int, artifact string) (*world.MintProbeResult, error) {
	data, err := downloadProbeArtifact(ctx, w, runID, artifact, mintProbeResultFile)
	if err != nil {
		return nil, err
	}
	return parseMintProbeResult(data)
}

// downloadProbeArtifact downloads one probe job's artifact and returns
// the content of file in it, retrying the download a bounded number of
// times.
func downloadProbeArtifact(ctx context.Context, w *world.World, runID int, artifact, file string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "probe-artifact-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	for attempt := 1; ; attempt++ {
		err = w.CI.DownloadNamedArtifactFromRun(ctx, w.RepoOwner, w.RepoName, runID, artifact, dir)
		if err == nil || attempt >= mintProbeArtifactAttempts {
			break
		}
		if sleepErr := mintProbeSleep(ctx, mintProbeArtifactDelay); sleepErr != nil {
			return nil, sleepErr
		}
	}
	if err != nil {
		return nil, fmt.Errorf("downloading artifact %q from run %d: %w", artifact, runID, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, artifact, file))
	if err != nil {
		return nil, fmt.Errorf("artifact %q: %w", artifact, err)
	}
	return data, nil
}

// parseMintProbeResult parses a probe result and rejects one that does not
// state a known outcome.
func parseMintProbeResult(data []byte) (*world.MintProbeResult, error) {
	var res world.MintProbeResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", mintProbeResultFile, err)
	}
	switch res.Outcome {
	case "minted", "denied", "error":
	default:
		return nil, fmt.Errorf("%s has unknown outcome %q", mintProbeResultFile, res.Outcome)
	}
	return &res, nil
}

// mintProbeVerdict is the classification of one probe result. Exactly one
// of outcome and infra is set: outcome is a policy answer (one of the
// outcome column values, or "other-denied" for a denial that is not the
// policy under test), infra an infrastructure failure category.
type mintProbeVerdict struct {
	outcome string
	infra   string
	detail  string
}

func classifyMintProbeResult(res *world.MintProbeResult) mintProbeVerdict {
	if res == nil {
		return mintProbeVerdict{infra: "result-artifact", detail: "no probe result"}
	}
	switch res.Outcome {
	case "minted":
		return mintProbeVerdict{outcome: mintProbeOutcomeMinted, detail: "token minted"}
	case "denied":
		detail := fmt.Sprintf("HTTP %d: %s", res.HTTPStatus, res.Message)
		switch {
		case res.HTTPStatus == 403 && slices.Contains(mintProbeScopeDenials, res.Message):
			return mintProbeVerdict{outcome: mintProbeOutcomeScopeDenied, detail: detail}
		case res.HTTPStatus == 401 && res.Message == mintProbeAuthFailedMessage && !res.OIDCVerified:
			// The mint answers a host denial and an OIDC failure alike; only
			// a token the probe verified itself makes this a host denial.
			return mintProbeVerdict{infra: "oidc-unverified", detail: detail + " (the probe did not verify the OIDC token)"}
		case res.HTTPStatus == 401 && res.Message == mintProbeAuthFailedMessage:
			return mintProbeVerdict{outcome: mintProbeOutcomeHostDenied, detail: detail}
		case res.HTTPStatus == 401:
			// Not the mint's canonical body: an intermediary or an
			// unexpected failure, which must not satisfy a host denial.
			return mintProbeVerdict{infra: "unexpected-401", detail: detail}
		default:
			return mintProbeVerdict{outcome: "other-denied", detail: detail}
		}
	case "error":
		detail := res.Message
		if res.HTTPStatus != 0 {
			detail = fmt.Sprintf("HTTP %d: %s", res.HTTPStatus, res.Message)
		}
		switch res.Error {
		case "config", "oidc", "transport", "response", "revoke":
			return mintProbeVerdict{infra: res.Error, detail: detail}
		case "http":
			return mintProbeVerdict{infra: "server", detail: detail}
		default:
			return mintProbeVerdict{infra: "result-artifact", detail: fmt.Sprintf("unknown probe error %q: %s", res.Error, detail)}
		}
	default:
		return mintProbeVerdict{infra: "result-artifact", detail: fmt.Sprintf("unknown outcome %q", res.Outcome)}
	}
}

// checkMintProbeClaims verifies the OIDC claims the probe sent: the
// leased repository as the caller and host as the workflow host.
func checkMintProbeClaims(res *world.MintProbeResult, org, shimRepo, host string) error {
	wantRepo := org + "/" + shimRepo
	if !strings.EqualFold(res.Repository, wantRepo) {
		return fmt.Errorf("OIDC repository claim is %q, want %q", res.Repository, wantRepo)
	}
	wantRef := strings.ToLower(org + "/" + host + "/" + mintProbeWorkflowPath + "@")
	if !strings.HasPrefix(strings.ToLower(res.JobWorkflowRef), wantRef) {
		return fmt.Errorf("OIDC job_workflow_ref claim is %q, want it hosted by %s/%s", res.JobWorkflowRef, org, host)
	}
	return nil
}

// checkMintProbePermissions compares the granted permissions with the
// role's canonical set at the level. Optional permissions may be absent;
// nothing else may differ.
func checkMintProbePermissions(res *world.MintProbeResult, role, level string) error {
	want, err := mintProbeExpectedPermissions(role, level)
	if err != nil {
		return err
	}
	var diffs []string
	for perm, access := range want {
		got, ok := res.GrantedPermissions[perm]
		switch {
		case !ok && mintProbeOptionalPermissions[role][perm]:
		case !ok:
			diffs = append(diffs, fmt.Sprintf("missing %s:%s", perm, access))
		case got != access:
			diffs = append(diffs, fmt.Sprintf("%s is %s, want %s", perm, got, access))
		}
	}
	for perm, access := range res.GrantedPermissions {
		if _, ok := want[perm]; !ok {
			diffs = append(diffs, fmt.Sprintf("unexpected %s:%s", perm, access))
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		return fmt.Errorf("granted permissions for %s at %s differ: %s", role, level, strings.Join(diffs, "; "))
	}
	return nil
}

// checkMintProbeSingleRepoScope verifies a token scoped to exactly
// org/repo with repository_selection "selected".
func checkMintProbeSingleRepoScope(res *world.MintProbeResult, org, repo string) error {
	want := org + "/" + repo
	if len(res.GrantedRepos) != 1 || !strings.EqualFold(res.GrantedRepos[0], want) {
		return fmt.Errorf("granted repositories are %v, want [%s]", res.GrantedRepos, want)
	}
	if res.RepositorySelection != "selected" {
		return fmt.Errorf("repository_selection is %q, want %q", res.RepositorySelection, "selected")
	}
	return nil
}

func requireMintProbeResults(w *world.World) (*world.MintProbe, error) {
	p := w.MintProbe
	if p == nil || p.Run == nil || p.Control == nil || p.Subject == nil {
		return nil, fmt.Errorf("mint probe: no completed request; use 'the mint probe requests role ...' first")
	}
	return p, nil
}

// verifyMintProbeControl requires the control request to have minted a
// token for the leased repository through the approved host.
func verifyMintProbeControl(w *world.World, p *world.MintProbe) error {
	v := classifyMintProbeResult(p.Control)
	if v.infra != "" {
		return fmt.Errorf("mint probe control: infrastructure failure (%s): %s", v.infra, v.detail)
	}
	if v.outcome != mintProbeOutcomeMinted {
		return fmt.Errorf("mint probe control: the same-repository request through the approved host %s/%s was denied (%s); "+
			"check that the mint lists that host in WORKFLOW_HOST_REPOS and enrolls %s/%s", w.Org, p.ApprovedHostRepo, v.detail, w.Org, w.RepoName)
	}
	for _, check := range []error{
		checkMintProbeClaims(p.Control, w.Org, w.RepoName, p.ApprovedHostRepo),
		checkMintProbeSingleRepoScope(p.Control, w.Org, w.RepoName),
		checkMintProbePermissions(p.Control, p.Role, p.Level),
	} {
		if check != nil {
			return fmt.Errorf("mint probe control: %w", check)
		}
	}
	return nil
}

// thenMintProbeOutcome checks the subject's outcome. Policy denials are
// told apart from infrastructure failures, and only a policy answer
// confirmed by a minted control can satisfy the expected outcome.
func thenMintProbeOutcome(w *world.World, expected string) error {
	switch expected {
	case mintProbeOutcomeMinted, mintProbeOutcomeScopeDenied, mintProbeOutcomeHostDenied:
	default:
		return fmt.Errorf("mint probe: unknown outcome %q (want %q, %q or %q)", expected, mintProbeOutcomeMinted, mintProbeOutcomeScopeDenied, mintProbeOutcomeHostDenied)
	}
	p, err := requireMintProbeResults(w)
	if err != nil {
		return err
	}
	if err := verifyMintProbeControl(w, p); err != nil {
		return err
	}

	v := classifyMintProbeResult(p.Subject)
	if v.infra != "" {
		return fmt.Errorf("mint probe: infrastructure failure (%s): %s; it cannot satisfy expected outcome %q", v.infra, v.detail, expected)
	}
	if err := checkMintProbeClaims(p.Subject, w.Org, w.RepoName, p.SubjectHostRepo); err != nil {
		return fmt.Errorf("mint probe: %w", err)
	}
	if v.outcome != expected {
		return fmt.Errorf("mint probe: outcome is %q (%s), want %q", v.outcome, v.detail, expected)
	}
	if expected == mintProbeOutcomeMinted {
		if err := checkMintProbePermissions(p.Subject, p.Role, p.Level); err != nil {
			return fmt.Errorf("mint probe: %w", err)
		}
	}
	return nil
}

// thenMintProbeTargetRepo checks the repository the subject's token is
// scoped to: "shim repo" (the leased repository) or "none" (no token).
func thenMintProbeTargetRepo(w *world.World, target string) error {
	p, err := requireMintProbeResults(w)
	if err != nil {
		return err
	}
	switch target {
	case mintProbeNone:
		if p.Subject.Outcome == "minted" || len(p.Subject.GrantedRepos) > 0 {
			return fmt.Errorf("mint probe: a token was minted for %v, want none", p.Subject.GrantedRepos)
		}
		return nil
	case mintProbeReposShim:
		if p.Subject.Outcome != "minted" {
			return fmt.Errorf("mint probe: no token was minted (outcome %q), want one for %s/%s", p.Subject.Outcome, w.Org, w.RepoName)
		}
		want := w.Org + "/" + w.RepoName
		if len(p.Subject.GrantedRepos) != 1 || !strings.EqualFold(p.Subject.GrantedRepos[0], want) {
			return fmt.Errorf("mint probe: the token is scoped to %v, want [%s]", p.Subject.GrantedRepos, want)
		}
		return nil
	default:
		return fmt.Errorf("mint probe: unknown target repository %q (want %q or %q)", target, mintProbeReposShim, mintProbeNone)
	}
}

// thenMintProbeSelection checks the subject token's repository_selection,
// or that no token was minted for "none".
func thenMintProbeSelection(w *world.World, selection string) error {
	p, err := requireMintProbeResults(w)
	if err != nil {
		return err
	}
	if selection == mintProbeNone {
		if p.Subject.Outcome == "minted" || p.Subject.RepositorySelection != "" {
			return fmt.Errorf("mint probe: a token was minted with repository_selection %q, want none", p.Subject.RepositorySelection)
		}
		return nil
	}
	if p.Subject.Outcome != "minted" {
		return fmt.Errorf("mint probe: no token was minted (outcome %q), want repository_selection %q", p.Subject.Outcome, selection)
	}
	if p.Subject.RepositorySelection != selection {
		return fmt.Errorf("mint probe: repository_selection is %q, want %q", p.Subject.RepositorySelection, selection)
	}
	return nil
}
