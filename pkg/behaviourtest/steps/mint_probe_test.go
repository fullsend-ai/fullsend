package steps

import (
	"bufio"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// --- fakes ---

// fakeMintProbeSCM implements the scm.Driver methods the mint probe steps
// use; the rest come from the embedded interface and panic when called.
type fakeMintProbeSCM struct {
	scm.Driver

	createdRepos []string
	publicRepos  []string
	commits      map[string][]byte // "repo:path" → content
	branches     []string
	branchFiles  map[string][]byte // "branch:path" → content
	createErr    error

	existing     map[string]bool // repos present before the scenario (stale leftovers)
	deletedRepos []string
	deleteErr    error

	lookupErr   error          // returned by GetDefaultBranch for a repo this fake created
	deleteLag   int            // lookups that still see a repo after it is deleted
	deleting    map[string]int // repo → remaining lagging lookups
	reservedFor int            // CreateRepo calls that report already-exists (nil) without creating
	createCalls int
}

func newFakeMintProbeSCM() *fakeMintProbeSCM {
	return &fakeMintProbeSCM{commits: map[string][]byte{}, branchFiles: map[string][]byte{}}
}

func (f *fakeMintProbeSCM) CreateRepo(_ context.Context, _, name, _ string) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	if f.reservedFor > 0 {
		// The GitHub driver swallows already-exists: success, nothing created.
		f.reservedFor--
		return nil
	}
	f.createdRepos = append(f.createdRepos, name)
	return nil
}

func (f *fakeMintProbeSCM) DeleteRepo(_ context.Context, _, repo string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedRepos = append(f.deletedRepos, repo)
	delete(f.existing, repo)
	if f.deleteLag > 0 {
		if f.deleting == nil {
			f.deleting = map[string]int{}
		}
		f.deleting[repo] = f.deleteLag
	}
	return nil
}

func (f *fakeMintProbeSCM) EnsureRepoPublic(_ context.Context, _, repo string) error {
	f.publicRepos = append(f.publicRepos, repo)
	return nil
}

func (f *fakeMintProbeSCM) CommitFile(_ context.Context, _, repo, path, _ string, content []byte) error {
	f.commits[repo+":"+path] = content
	return nil
}

func (f *fakeMintProbeSCM) GetFileContent(_ context.Context, _, repo, path string) ([]byte, error) {
	if c, ok := f.commits[repo+":"+path]; ok {
		return c, nil
	}
	return nil, forge.ErrNotFound
}

func (f *fakeMintProbeSCM) GetDefaultBranch(_ context.Context, _, repo string) (string, error) {
	if n := f.deleting[repo]; n > 0 {
		f.deleting[repo] = n - 1
		return "main", nil
	}
	if f.lookupErr != nil && slices.Contains(f.createdRepos, repo) {
		return "", f.lookupErr
	}
	if !f.existing[repo] && !slices.Contains(f.createdRepos, repo) {
		return "", fmt.Errorf("getting default branch: %w", forge.ErrNotFound)
	}
	return "main", nil
}

func (f *fakeMintProbeSCM) CreateBranch(_ context.Context, _, _, branch string) error {
	f.branches = append(f.branches, branch)
	return nil
}

func (f *fakeMintProbeSCM) CommitFileToBranch(_ context.Context, _, _, branch, path, _ string, content []byte) error {
	f.branchFiles[branch+":"+path] = content
	return nil
}

// fakeMintProbeCI serves one probe run through ci.RunLister and writes the
// configured results as artifacts.
type fakeMintProbeCI struct {
	ci.Driver

	run       *forge.WorkflowRun // Name is filled in from the pushed shim
	scm       *fakeMintProbeSCM
	results   map[string]*world.MintProbeResult // artifact → result
	listCalls int
	downloads map[string]int
}

func (f *fakeMintProbeCI) ListRecentRuns(context.Context, string, string, int) ([]forge.WorkflowRun, error) {
	f.listCalls++
	if f.run == nil {
		return nil, nil
	}
	run := *f.run
	for key := range f.scm.branchFiles {
		if strings.Contains(key, ".github/workflows/mint-probe-") {
			name := strings.TrimSuffix(filepath.Base(key), ".yml")
			run.Name = name
		}
	}
	return []forge.WorkflowRun{{ID: 1, Name: "fullsend", Status: "completed", Conclusion: "success"}, run}, nil
}

func (f *fakeMintProbeCI) DownloadNamedArtifactFromRun(_ context.Context, _, _ string, _ int, artifact, destDir string) error {
	if f.downloads == nil {
		f.downloads = map[string]int{}
	}
	f.downloads[artifact]++
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
	return os.WriteFile(filepath.Join(dir, mintProbeResultFile), data, 0o600)
}

// fastMintProbePolls makes the probe's waits instant for one test.
func fastMintProbePolls(t *testing.T) {
	t.Helper()
	appear, complete, poll, attempts, delay := mintProbeAppearTimeout, mintProbeCompleteTimeout, mintProbePollInterval, mintProbeArtifactAttempts, mintProbeArtifactDelay
	mintProbeAppearTimeout, mintProbeCompleteTimeout, mintProbePollInterval, mintProbeArtifactAttempts, mintProbeArtifactDelay = 50*time.Millisecond, 50*time.Millisecond, time.Millisecond, 2, time.Millisecond
	repoDelay := mintProbeRepoRetryDelay
	mintProbeRepoRetryDelay = time.Millisecond
	t.Cleanup(func() {
		mintProbeAppearTimeout, mintProbeCompleteTimeout, mintProbePollInterval, mintProbeArtifactAttempts, mintProbeArtifactDelay = appear, complete, poll, attempts, delay
		mintProbeRepoRetryDelay = repoDelay
	})
}

func mintProbeTestWorld(scmDriver scm.Driver, ciDriver ci.Driver) *world.World {
	return &world.World{
		Config:         env.RunnerConfig{SCM: "github"},
		SCM:            scmDriver,
		CI:             ciDriver,
		Org:            "halfsend-03",
		RepoOwner:      "halfsend-03",
		RepoName:       "test-repo-07",
		LeasedRepoName: "test-repo-07",
	}
}

// mintedResult is a well-formed minted probe result for the leased repo.
func mintedResult(role, level, host string) *world.MintProbeResult {
	perms := maps.Clone(mintProbeRolePermissions[role][level])
	return &world.MintProbeResult{
		Outcome:             "minted",
		HTTPStatus:          200,
		Role:                role,
		Level:               level,
		Repos:               []string{"test-repo-07"},
		Repository:          "halfsend-03/test-repo-07",
		JobWorkflowRef:      "halfsend-03/" + host + "/.github/workflows/mint-probe.yml@refs/heads/main",
		RepositorySelection: "selected",
		GrantedRepos:        []string{"halfsend-03/test-repo-07"},
		GrantedPermissions:  perms,
	}
}

func deniedResult(status int, message, host string) *world.MintProbeResult {
	return &world.MintProbeResult{
		Outcome:        "denied",
		HTTPStatus:     status,
		Message:        message,
		OIDCVerified:   true,
		Repository:     "halfsend-03/test-repo-07",
		JobWorkflowRef: "halfsend-03/" + host + "/.github/workflows/mint-probe.yml@refs/heads/main",
	}
}

// probeWorld returns a world whose probe already completed with the given
// control and subject results.
func probeWorld(arrangement string, control, subject *world.MintProbeResult) *world.World {
	w := mintProbeTestWorld(nil, nil)
	p := &world.MintProbe{
		Forge:            "github",
		HostArrangement:  arrangement,
		ApprovedHostRepo: "test-repo-07-mint-host",
		SubjectHostRepo:  "test-repo-07-mint-host",
		Role:             "triage",
		Level:            "write",
		Run:              &forge.WorkflowRun{ID: 9, Status: "completed", Conclusion: "success"},
		Control:          control,
		Subject:          subject,
	}
	if arrangement == mintProbeHostUnapproved {
		p.SubjectHostRepo = "test-repo-07-mint-unapproved"
	}
	w.MintProbe = p
	return w
}

// --- feature file ---

// TestMintScopeFeature_RowsMatchSteps expands every Examples row of the
// mint scope feature and checks that each step matches a registered mint
// probe step pattern and that every row value is accepted, so a typo in
// the table fails here rather than after a pool lease.
func TestMintScopeFeature_RowsMatchSteps(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "e2e", "behaviour", "features", "platform", "mint-scope.feature"))
	require.NoError(t, err)
	defer f.Close()

	var steps, header []string
	var rows [][]string
	inOutline := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
	require.NotEmpty(t, steps)
	require.Equal(t, []string{"forge", "role", "repos", "level", "workflow host", "outcome", "target repo", "selection"}, header)
	require.NotEmpty(t, rows)

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^the enrolled test repository$`),
		regexp.MustCompile(`^a "([^"]*)" mint probe through the (approved|unapproved) workflow host$`),
		regexp.MustCompile(`^the mint probe requests role "([^"]*)" at level "([^"]*)" for repos "([^"]*)"$`),
		regexp.MustCompile(`^the mint probe outcome is "([^"]*)"$`),
		regexp.MustCompile(`^the mint probe target repository is "([^"]*)"$`),
		regexp.MustCompile(`^the mint probe repository selection is "([^"]*)"$`),
	}

	minted := map[string]bool{}
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
		_, err := mintProbeExpectedPermissions(values["role"], values["level"])
		assert.NoError(t, err, "row %v", row)
		assert.Contains(t, []string{mintProbeReposShim, mintProbeReposOther, mintProbeReposAll}, values["repos"])
		assert.Contains(t, []string{mintProbeHostApproved, mintProbeHostUnapproved}, values["workflow host"])
		outcomes[values["outcome"]] = true
		switch values["outcome"] {
		case mintProbeOutcomeMinted:
			minted[values["role"]+"/"+values["level"]] = true
			assert.Equal(t, mintProbeReposShim, values["target repo"], "row %v", row)
			assert.Equal(t, "selected", values["selection"], "row %v", row)
		case mintProbeOutcomeScopeDenied, mintProbeOutcomeHostDenied:
			assert.Equal(t, mintProbeNone, values["target repo"], "row %v", row)
			assert.Equal(t, mintProbeNone, values["selection"], "row %v", row)
		default:
			t.Errorf("row %v: unknown outcome %q", row, values["outcome"])
		}
	}

	// Every fullsend-test role mints at both levels, and every policy
	// answer has a row.
	for role, levels := range mintProbeRolePermissions {
		for level := range levels {
			assert.True(t, minted[role+"/"+level], "no minted row for %s at %s", role, level)
		}
	}
	for _, o := range []string{mintProbeOutcomeMinted, mintProbeOutcomeScopeDenied, mintProbeOutcomeHostDenied} {
		assert.True(t, outcomes[o], "no row expects %q", o)
	}
}

// --- workflows ---

func TestMintProbeWorkflow_InputsMatchShim(t *testing.T) {
	var reusable struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]any `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(mintProbeWorkflow, &reusable))
	assert.Equal(t, "write", reusable.Jobs["probe"].Permissions["id-token"])

	shim, err := renderMintProbeShim(mintProbeShimSpec{
		Name: "mint-probe-0a1b2c3d", Branch: "behaviour/mint-probe-0a1b2c3d", Org: "halfsend-03",
		Role: "coder", Level: "read",
		ControlHost: "test-repo-07-mint-host", ControlRef: "main",
		SubjectHost: "test-repo-07-mint-unapproved", SubjectRef: "main",
		ShimRepo: "test-repo-07", Repos: []string{"*"},
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
	assert.Equal(t, "mint-probe-0a1b2c3d", parsed.Name)
	assert.Equal(t, []string{"behaviour/mint-probe-0a1b2c3d"}, parsed.On.Push.Branches)
	assert.Empty(t, parsed.Permissions)
	require.Len(t, parsed.Jobs, 2)

	control, subject := parsed.Jobs["control"], parsed.Jobs["subject"]
	assert.Equal(t, "halfsend-03/test-repo-07-mint-host/.github/workflows/mint-probe.yml@main", control.Uses)
	assert.Equal(t, "halfsend-03/test-repo-07-mint-unapproved/.github/workflows/mint-probe.yml@main", subject.Uses)
	assert.Equal(t, `["test-repo-07"]`, control.With["repos_json"])
	assert.Equal(t, `["*"]`, subject.With["repos_json"])
	assert.Equal(t, mintProbeControlArtifact, control.With["artifact_name"])
	assert.Equal(t, mintProbeSubjectArtifact, subject.With["artifact_name"])
	for name, job := range parsed.Jobs {
		assert.Equal(t, "write", job.Permissions["id-token"], name)
		assert.Equal(t, "${{ vars.FULLSEND_MINT_URL }}", job.With["mint_url"], name)
		assert.Equal(t, "coder", job.With["role"], name)
		assert.Equal(t, "read", job.With["level"], name)
		for input := range job.With {
			assert.Contains(t, reusable.On.WorkflowCall.Inputs, input, "shim passes input %q the probe does not declare", input)
		}
		for input := range reusable.On.WorkflowCall.Inputs {
			assert.Contains(t, job.With, input, "shim omits probe input %q", input)
		}
	}
}

func TestRenderMintProbeShim_RejectsUnsafeValues(t *testing.T) {
	base := mintProbeShimSpec{
		Name: "mint-probe-1", Branch: "behaviour/mint-probe-1", Org: "org",
		Role: "triage", Level: "write",
		ControlHost: "h", ControlRef: "main", SubjectHost: "h", SubjectRef: "main",
		ShimRepo: "r", Repos: []string{"r"},
	}
	for name, mutate := range map[string]func(*mintProbeShimSpec){
		"role":   func(s *mintProbeShimSpec) { s.Role = "triage'\nx: y" },
		"level":  func(s *mintProbeShimSpec) { s.Level = "WRITE" },
		"org":    func(s *mintProbeShimSpec) { s.Org = "org/evil" },
		"host":   func(s *mintProbeShimSpec) { s.SubjectHost = "h x" },
		"ref":    func(s *mintProbeShimSpec) { s.ControlRef = "main'" },
		"branch": func(s *mintProbeShimSpec) { s.Branch = "" },
		"repos":  func(s *mintProbeShimSpec) { s.Repos = []string{"a'b"} },
	} {
		t.Run(name, func(t *testing.T) {
			spec := base
			spec.Repos = append([]string(nil), base.Repos...)
			mutate(&spec)
			_, err := renderMintProbeShim(spec)
			assert.Error(t, err)
		})
	}
}

// --- classification and checks ---

func TestClassifyMintProbeResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     *world.MintProbeResult
		outcome string
		infra   string
	}{
		{"nil", nil, "", "result-artifact"},
		{"minted", &world.MintProbeResult{Outcome: "minted"}, mintProbeOutcomeMinted, ""},
		{"wildcard", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 403, Message: "same-org mint requires non-empty repos"}, mintProbeOutcomeScopeDenied, ""},
		{"other repo", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 403, Message: "per-repo mint requires repos to be exactly the requesting repository"}, mintProbeOutcomeScopeDenied, ""},
		{"role not allowed", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 403, Message: "role not allowed"}, "other-denied", ""},
		{"host", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 401, Message: "authentication failed", OIDCVerified: true}, mintProbeOutcomeHostDenied, ""},
		{"401 for an unverified OIDC token", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 401, Message: "authentication failed"}, "", "oidc-unverified"},
		{"empty 401", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 401, OIDCVerified: true}, "", "unexpected-401"},
		{"non-mint 401", &world.MintProbeResult{Outcome: "denied", HTTPStatus: 401, Message: "Bad credentials"}, "", "unexpected-401"},
		{"revoke", &world.MintProbeResult{Outcome: "error", Error: "revoke", HTTPStatus: 200}, "", "revoke"},
		{"oidc", &world.MintProbeResult{Outcome: "error", Error: "oidc"}, "", "oidc"},
		{"transport", &world.MintProbeResult{Outcome: "error", Error: "transport"}, "", "transport"},
		{"config", &world.MintProbeResult{Outcome: "error", Error: "config"}, "", "config"},
		{"response", &world.MintProbeResult{Outcome: "error", Error: "response", HTTPStatus: 200}, "", "response"},
		{"server", &world.MintProbeResult{Outcome: "error", Error: "http", HTTPStatus: 502, Message: "mint failed"}, "", "server"},
		{"unprocessable", &world.MintProbeResult{Outcome: "error", Error: "http", HTTPStatus: 422}, "", "server"},
		{"unknown error", &world.MintProbeResult{Outcome: "error", Error: "weird"}, "", "result-artifact"},
		{"unknown outcome", &world.MintProbeResult{Outcome: "maybe"}, "", "result-artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyMintProbeResult(tc.res)
			assert.Equal(t, tc.outcome, v.outcome)
			assert.Equal(t, tc.infra, v.infra)
			assert.True(t, (v.outcome == "") != (v.infra == ""), "exactly one of outcome and infra is set")
		})
	}
}

func TestParseMintProbeResult(t *testing.T) {
	res, err := parseMintProbeResult([]byte(`{"outcome":"denied","http_status":401,"message":"authentication failed","repository":"o/r","job_workflow_ref":"o/h/.github/workflows/mint-probe.yml@refs/heads/main"}`))
	require.NoError(t, err)
	assert.Equal(t, 401, res.HTTPStatus)
	assert.Equal(t, "o/r", res.Repository)

	_, err = parseMintProbeResult([]byte(`{"outcome":"ok"}`))
	assert.ErrorContains(t, err, "unknown outcome")
	_, err = parseMintProbeResult([]byte(`not json`))
	assert.Error(t, err)
}

func TestCheckMintProbePermissions(t *testing.T) {
	ok := mintedResult("coder", "write", "h")
	assert.NoError(t, checkMintProbePermissions(ok, "coder", "write"))

	// packages:read is optional for coder while installations catch up.
	optional := mintedResult("coder", "write", "h")
	delete(optional.GrantedPermissions, "packages")
	assert.NoError(t, checkMintProbePermissions(optional, "coder", "write"))

	missing := mintedResult("coder", "write", "h")
	delete(missing.GrantedPermissions, "contents")
	assert.ErrorContains(t, checkMintProbePermissions(missing, "coder", "write"), "missing contents:write")

	// A read request answered with write permissions is not downscoped.
	upscoped := mintedResult("triage", "write", "h")
	assert.ErrorContains(t, checkMintProbePermissions(upscoped, "triage", "read"), "issues is write, want read")

	extra := mintedResult("triage", "read", "h")
	extra.GrantedPermissions["pull_requests"] = "write"
	assert.ErrorContains(t, checkMintProbePermissions(extra, "triage", "read"), "unexpected pull_requests:write")

	assert.Error(t, checkMintProbePermissions(ok, "e2e", "write"), "the infrastructure e2e role is out of scope")
}

func TestCheckMintProbeClaims(t *testing.T) {
	res := mintedResult("triage", "read", "test-repo-07-mint-host")
	assert.NoError(t, checkMintProbeClaims(res, "halfsend-03", "test-repo-07", "test-repo-07-mint-host"))
	assert.ErrorContains(t, checkMintProbeClaims(res, "halfsend-03", "test-repo-07", "test-repo-07-mint-unapproved"), "job_workflow_ref")
	assert.ErrorContains(t, checkMintProbeClaims(res, "halfsend-03", "test-repo-08", "test-repo-07-mint-host"), "repository claim")

	// The shim repo hosting the workflow itself is not a distinct host.
	self := mintedResult("triage", "read", "test-repo-07")
	assert.Error(t, checkMintProbeClaims(self, "halfsend-03", "test-repo-07", "test-repo-07-mint-host"))
}

// --- Then steps ---

func TestThenMintProbeOutcome(t *testing.T) {
	const approved, unapproved = "test-repo-07-mint-host", "test-repo-07-mint-unapproved"
	control := func() *world.MintProbeResult { return mintedResult("triage", "write", approved) }

	for _, tc := range []struct {
		name        string
		arrangement string
		control     *world.MintProbeResult
		subject     *world.MintProbeResult
		expected    string
		wantErr     string
	}{
		{name: "minted", arrangement: "approved", control: control(), subject: mintedResult("triage", "write", approved), expected: "minted"},
		{name: "scope denied", arrangement: "approved", control: control(),
			subject: deniedResult(403, "same-org mint requires non-empty repos", approved), expected: "scope-denied"},
		{name: "host denied", arrangement: "unapproved", control: control(),
			subject: deniedResult(401, "authentication failed", unapproved), expected: "host-denied"},
		{name: "oidc failure cannot satisfy a denial", arrangement: "approved", control: control(),
			subject:  &world.MintProbeResult{Outcome: "error", Error: "oidc", Message: "requesting the GitHub Actions OIDC token failed"},
			expected: "scope-denied", wantErr: "infrastructure failure (oidc)"},
		{name: "server failure cannot satisfy a denial", arrangement: "approved", control: control(),
			subject: &world.MintProbeResult{Outcome: "error", Error: "http", HTTPStatus: 503, Repository: "halfsend-03/test-repo-07",
				JobWorkflowRef: "halfsend-03/" + approved + "/.github/workflows/mint-probe.yml@refs/heads/main"},
			expected: "scope-denied", wantErr: "infrastructure failure (server)"},
		{name: "a subject-only OIDC verification failure cannot satisfy a host denial", arrangement: "unapproved", control: control(),
			subject: &world.MintProbeResult{Outcome: "error", Error: "oidc", Message: "the OIDC token failed verification: signature does not verify", Repository: "halfsend-03/test-repo-07",
				JobWorkflowRef: "halfsend-03/" + unapproved + "/.github/workflows/mint-probe.yml@refs/heads/main"},
			expected: "host-denied", wantErr: "infrastructure failure (oidc)"},
		{name: "a 401 for a token the probe did not verify cannot satisfy a host denial", arrangement: "unapproved", control: control(),
			subject: func() *world.MintProbeResult {
				r := deniedResult(401, "authentication failed", unapproved)
				r.OIDCVerified = false
				return r
			}(), expected: "host-denied", wantErr: "infrastructure failure (oidc-unverified)"},
		{name: "an empty-message 401 cannot satisfy a host denial", arrangement: "unapproved", control: control(),
			subject: deniedResult(401, "", unapproved), expected: "host-denied", wantErr: "infrastructure failure (unexpected-401)"},
		{name: "a non-mint 401 cannot satisfy a host denial", arrangement: "unapproved", control: control(),
			subject: deniedResult(401, "Bad credentials", unapproved), expected: "host-denied", wantErr: "infrastructure failure (unexpected-401)"},
		{name: "a failed revocation cannot satisfy minted", arrangement: "approved", control: control(),
			subject:  &world.MintProbeResult{Outcome: "error", Error: "revoke", HTTPStatus: 200, Message: "revoking the minted installation token failed"},
			expected: "minted", wantErr: "infrastructure failure (revoke)"},
		{name: "a 401 without a minted control is not a host denial", arrangement: "unapproved",
			control: deniedResult(401, "authentication failed", approved),
			subject: deniedResult(401, "authentication failed", unapproved), expected: "host-denied", wantErr: "mint probe control"},
		{name: "control infrastructure failure", arrangement: "approved",
			control: &world.MintProbeResult{Outcome: "error", Error: "transport"},
			subject: deniedResult(403, "same-org mint requires non-empty repos", approved), expected: "scope-denied", wantErr: "control: infrastructure failure (transport)"},
		{name: "control with the wrong scope", arrangement: "approved",
			control: func() *world.MintProbeResult {
				r := control()
				r.RepositorySelection = "all"
				return r
			}(),
			subject: mintedResult("triage", "write", approved), expected: "minted", wantErr: "repository_selection"},
		{name: "other 403 is not a scope denial", arrangement: "approved", control: control(),
			subject: deniedResult(403, "role not allowed", approved), expected: "scope-denied", wantErr: `outcome is "other-denied"`},
		{name: "minted when denial expected", arrangement: "approved", control: control(),
			subject: mintedResult("triage", "write", approved), expected: "scope-denied", wantErr: `outcome is "minted"`},
		{name: "host denial when scope denial expected", arrangement: "approved", control: control(),
			subject: deniedResult(401, "authentication failed", approved), expected: "scope-denied", wantErr: `outcome is "host-denied"`},
		{name: "subject not hosted by the expected host", arrangement: "unapproved", control: control(),
			subject: deniedResult(401, "authentication failed", approved), expected: "host-denied", wantErr: "job_workflow_ref"},
		{name: "minted with wrong permissions", arrangement: "approved", control: control(),
			subject: func() *world.MintProbeResult {
				r := mintedResult("triage", "write", approved)
				r.GrantedPermissions["contents"] = "write"
				return r
			}(), expected: "minted", wantErr: "contents is write, want read"},
		{name: "unknown expected outcome", arrangement: "approved", control: control(), subject: control(), expected: "maybe", wantErr: "unknown outcome"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := probeWorld(tc.arrangement, tc.control, tc.subject)
			err := thenMintProbeOutcome(w, tc.expected)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}

	assert.ErrorContains(t, thenMintProbeOutcome(mintProbeTestWorld(nil, nil), "minted"), "no completed request")
}

func TestThenMintProbeTargetRepoAndSelection(t *testing.T) {
	const host = "test-repo-07-mint-host"
	minted := probeWorld("approved", mintedResult("triage", "write", host), mintedResult("triage", "write", host))
	assert.NoError(t, thenMintProbeTargetRepo(minted, "shim repo"))
	assert.NoError(t, thenMintProbeSelection(minted, "selected"))
	assert.ErrorContains(t, thenMintProbeTargetRepo(minted, "none"), "want none")
	assert.ErrorContains(t, thenMintProbeSelection(minted, "none"), "want none")
	assert.ErrorContains(t, thenMintProbeSelection(minted, "all"), `want "all"`)
	assert.ErrorContains(t, thenMintProbeTargetRepo(minted, "halfsend-03/test-repo-07"), "unknown target repository")

	wrongRepo := probeWorld("approved", mintedResult("triage", "write", host), mintedResult("triage", "write", host))
	wrongRepo.MintProbe.Subject.GrantedRepos = []string{"halfsend-03/test-repo-07", "halfsend-03/test-repo-08"}
	assert.ErrorContains(t, thenMintProbeTargetRepo(wrongRepo, "shim repo"), "scoped to")

	denied := probeWorld("approved", mintedResult("triage", "write", host), deniedResult(403, "same-org mint requires non-empty repos", host))
	assert.NoError(t, thenMintProbeTargetRepo(denied, "none"))
	assert.NoError(t, thenMintProbeSelection(denied, "none"))
	assert.ErrorContains(t, thenMintProbeTargetRepo(denied, "shim repo"), "no token was minted")
	assert.ErrorContains(t, thenMintProbeSelection(denied, "selected"), "no token was minted")
}

// --- Given / When steps ---

func TestGivenMintProbe_CreatesHosts(t *testing.T) {
	scmDriver := newFakeMintProbeSCM()
	w := mintProbeTestWorld(scmDriver, nil)
	require.NoError(t, givenMintProbe(context.Background(), w, "github", "approved"))
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.createdRepos)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.publicRepos)
	assert.Equal(t, mintProbeWorkflow, scmDriver.commits["test-repo-07-mint-host:.github/workflows/mint-probe.yml"])
	assert.Equal(t, "test-repo-07-mint-host", w.MintProbe.SubjectHostRepo)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, w.MintProbe.CreatedRepos)

	scmDriver = newFakeMintProbeSCM()
	w = mintProbeTestWorld(scmDriver, nil)
	require.NoError(t, givenMintProbe(context.Background(), w, "github", "unapproved"))
	assert.Equal(t, []string{"test-repo-07-mint-host", "test-repo-07-mint-unapproved"}, scmDriver.createdRepos)
	assert.Equal(t, "test-repo-07-mint-unapproved", w.MintProbe.SubjectHostRepo)
	assert.Contains(t, scmDriver.commits, "test-repo-07-mint-unapproved:.github/workflows/mint-probe.yml")
}

func TestGivenMintProbe_StaleHostRepo(t *testing.T) {
	// A leftover host repository is deleted and recreated, so the scenario
	// owns only a repository this run created.
	scmDriver := newFakeMintProbeSCM()
	scmDriver.existing = map[string]bool{"test-repo-07-mint-host": true}
	w := mintProbeTestWorld(scmDriver, nil)
	require.NoError(t, givenMintProbe(context.Background(), w, "github", "approved"))
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.deletedRepos)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.createdRepos)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, w.MintProbe.CreatedRepos)

	// A leftover that cannot be deleted fails before it is made public,
	// modified or recorded for cleanup.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.existing = map[string]bool{"test-repo-07-mint-host": true}
	scmDriver.deleteErr = errors.New("forbidden")
	w = mintProbeTestWorld(scmDriver, nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "deleting stale host repository")
	assert.Empty(t, scmDriver.publicRepos)
	assert.Empty(t, scmDriver.createdRepos)
	assert.Empty(t, w.MintProbe.CreatedRepos)
}

func TestGivenMintProbe_SlowStaleDeletion(t *testing.T) {
	fastMintProbePolls(t)

	// Deletion takes a few lookups to propagate and the name stays
	// reserved for the first creates: the step waits for the 404, then
	// retries the create until the new repository is visible.
	scmDriver := newFakeMintProbeSCM()
	scmDriver.existing = map[string]bool{"test-repo-07-mint-host": true}
	scmDriver.deleteLag = 2
	scmDriver.reservedFor = 2
	w := mintProbeTestWorld(scmDriver, nil)
	require.NoError(t, givenMintProbe(context.Background(), w, "github", "approved"))
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.deletedRepos)
	assert.Equal(t, 3, scmDriver.createCalls, "create is retried while the name is reserved")
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.createdRepos)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, w.MintProbe.CreatedRepos)
	assert.Contains(t, scmDriver.commits, "test-repo-07-mint-host:.github/workflows/mint-probe.yml")

	// A repository that never stops being visible fails before any create.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.existing = map[string]bool{"test-repo-07-mint-host": true}
	scmDriver.deleteLag = 1000
	w = mintProbeTestWorld(scmDriver, nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "still visible")
	assert.Zero(t, scmDriver.createCalls)
	assert.Empty(t, scmDriver.publicRepos)

	// A name that stays reserved never becomes visible: bounded failure.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.reservedFor = 1000
	w = mintProbeTestWorld(scmDriver, nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "not visible")
	assert.Equal(t, mintProbeRepoMaxAttempts, scmDriver.createCalls)
	assert.Empty(t, scmDriver.publicRepos)
	// CreateRepo reported success, so the host is recorded once for
	// cleanup (which tolerates a repo that never appeared).
	assert.Equal(t, []string{"test-repo-07-mint-host"}, w.MintProbe.CreatedRepos)

	// Cancellation interrupts the wait.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.existing = map[string]bool{"test-repo-07-mint-host": true}
	scmDriver.deleteLag = 1000
	w = mintProbeTestWorld(scmDriver, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, givenMintProbe(ctx, w, "github", "approved"), context.Canceled)
}

func TestGivenMintProbe_RecordsHostOnceCreated(t *testing.T) {
	fastMintProbePolls(t)

	// A create that succeeds and is then followed by a lookup error still
	// leaves the host recorded, so cleanup deletes it.
	scmDriver := newFakeMintProbeSCM()
	scmDriver.lookupErr = errors.New("rate limited")
	w := mintProbeTestWorld(scmDriver, nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "checking new host repository")
	assert.Equal(t, []string{"test-repo-07-mint-host"}, scmDriver.createdRepos)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, w.MintProbe.CreatedRepos)
	assert.Empty(t, scmDriver.publicRepos)

	// Cancellation during the visibility backoff after a successful create
	// also leaves the host recorded.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.reservedFor = 1000
	w = mintProbeTestWorld(scmDriver, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, givenMintProbe(ctx, w, "github", "approved"), context.Canceled)
	assert.Equal(t, []string{"test-repo-07-mint-host"}, w.MintProbe.CreatedRepos)

	// A create that itself fails records nothing.
	scmDriver = newFakeMintProbeSCM()
	scmDriver.createErr = errors.New("boom")
	w = mintProbeTestWorld(scmDriver, nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "boom")
	assert.Empty(t, w.MintProbe.CreatedRepos)
}

func TestGivenMintProbe_Errors(t *testing.T) {
	assert.ErrorContains(t, givenMintProbe(context.Background(), &world.World{}, "github", "approved"), "no repo configured")

	w := mintProbeTestWorld(newFakeMintProbeSCM(), nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "gitlab", "approved"), "not supported")

	w = mintProbeTestWorld(newFakeMintProbeSCM(), nil)
	w.Config.SCM = "gitlab"
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "BEHAVIOUR_SCM")

	w = mintProbeTestWorld(newFakeMintProbeSCM(), nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "sideways"), "unknown workflow host arrangement")

	// A failure after the probe is attached still leaves it for cleanup.
	failing := newFakeMintProbeSCM()
	failing.createErr = errors.New("boom")
	w = mintProbeTestWorld(failing, nil)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "boom")
	require.NotNil(t, w.MintProbe)
	assert.ErrorContains(t, givenMintProbe(context.Background(), w, "github", "approved"), "already set up")
}

func TestWhenMintProbeRequests_HappyPath(t *testing.T) {
	fastMintProbePolls(t)
	scmDriver := newFakeMintProbeSCM()
	ciDriver := &fakeMintProbeCI{
		scm: scmDriver,
		run: &forge.WorkflowRun{ID: 42, Status: "completed", Conclusion: "success", HTMLURL: "https://example.test/run/42"},
		results: map[string]*world.MintProbeResult{
			mintProbeControlArtifact: mintedResult("review", "read", "test-repo-07-mint-host"),
			mintProbeSubjectArtifact: deniedResult(403, "per-repo mint requires repos to be exactly the requesting repository", "test-repo-07-mint-host"),
		},
	}
	w := mintProbeTestWorld(scmDriver, ciDriver)
	require.NoError(t, givenMintProbe(context.Background(), w, "github", "approved"))
	require.NoError(t, whenMintProbeRequests(context.Background(), w, "review", "read", "other repo"))

	p := w.MintProbe
	assert.Equal(t, []string{"test-repo-07-mint-host"}, p.Repos, `"other repo" is the approved host`)
	require.Len(t, scmDriver.branches, 1)
	assert.Equal(t, p.Branch, scmDriver.branches[0])
	assert.Equal(t, []string{p.Branch}, w.CreatedBranches, "the shim branch is cleaned up with the other branches")
	shim := scmDriver.branchFiles[p.Branch+":.github/workflows/"+p.WorkflowName+".yml"]
	require.NotEmpty(t, shim)
	assert.Contains(t, string(shim), "repos_json: '[\"test-repo-07-mint-host\"]'")
	assert.Equal(t, 42, p.Run.ID)
	require.NotNil(t, p.Control)
	require.NotNil(t, p.Subject)

	require.NoError(t, thenMintProbeOutcome(w, "scope-denied"))
	require.NoError(t, thenMintProbeTargetRepo(w, "none"))
	require.NoError(t, thenMintProbeSelection(w, "none"))
	assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "review", "read", "other repo"), "already made")
}

func TestWhenMintProbeRequests_Failures(t *testing.T) {
	fastMintProbePolls(t)
	setup := func(t *testing.T, run *forge.WorkflowRun, results map[string]*world.MintProbeResult) *world.World {
		t.Helper()
		scmDriver := newFakeMintProbeSCM()
		w := mintProbeTestWorld(scmDriver, &fakeMintProbeCI{scm: scmDriver, run: run, results: results})
		require.NoError(t, givenMintProbe(context.Background(), w, "github", "approved"))
		return w
	}
	both := map[string]*world.MintProbeResult{
		mintProbeControlArtifact: mintedResult("triage", "read", "test-repo-07-mint-host"),
		mintProbeSubjectArtifact: mintedResult("triage", "read", "test-repo-07-mint-host"),
	}

	t.Run("run never starts", func(t *testing.T) {
		w := setup(t, nil, both)
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "triage", "read", "shim repo"), "workflow failure")
	})
	t.Run("run never completes", func(t *testing.T) {
		w := setup(t, &forge.WorkflowRun{ID: 5, Status: "in_progress"}, both)
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "triage", "read", "shim repo"), "did not complete")
	})
	t.Run("run fails", func(t *testing.T) {
		w := setup(t, &forge.WorkflowRun{ID: 5, Status: "completed", Conclusion: "startup_failure"}, both)
		err := whenMintProbeRequests(context.Background(), w, "triage", "read", "shim repo")
		assert.ErrorContains(t, err, "workflow failure")
		assert.ErrorContains(t, err, "startup_failure")
	})
	t.Run("missing artifact", func(t *testing.T) {
		ciResults := map[string]*world.MintProbeResult{mintProbeControlArtifact: both[mintProbeControlArtifact]}
		w := setup(t, &forge.WorkflowRun{ID: 5, Status: "completed", Conclusion: "success"}, ciResults)
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "triage", "read", "shim repo"), "result-artifact failure")
		assert.Equal(t, mintProbeArtifactAttempts, w.CI.(*fakeMintProbeCI).downloads[mintProbeSubjectArtifact], "the download is retried")
	})
	t.Run("bad inputs", func(t *testing.T) {
		w := setup(t, nil, both)
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "e2e", "write", "shim repo"), "not a fullsend-test role")
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "triage", "admin", "shim repo"), "no level")
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "triage", "read", "every repo"), "unknown repos value")
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), mintProbeTestWorld(nil, nil), "triage", "read", "shim repo"), "no probe set up")
	})
	t.Run("CI driver cannot list runs", func(t *testing.T) {
		scmDriver := newFakeMintProbeSCM()
		w := mintProbeTestWorld(scmDriver, &fakeBranchCI{})
		require.NoError(t, givenMintProbe(context.Background(), w, "github", "approved"))
		assert.ErrorContains(t, whenMintProbeRequests(context.Background(), w, "triage", "read", "shim repo"), "ci.RunLister")
	})
}

// --- cleanup ---

func TestCleanupScenario_DeletesMintProbeHosts(t *testing.T) {
	t.Parallel()

	scmDriver := &fakeCleanupSCM{}
	w := &world.World{
		Org:             "org",
		RepoOwner:       "org",
		RepoName:        "test-repo-07",
		SCM:             scmDriver,
		CreatedBranches: []string{"behaviour/mint-probe-0a1b2c3d"},
		MintProbe: &world.MintProbe{
			CreatedRepos: []string{"test-repo-07-mint-host", "test-repo-07-mint-unapproved", "test-repo-07", ""},
		},
	}
	CleanupScenario(w)

	require.Len(t, scmDriver.deletedRepos, 2, "the leased repo and empty names are never deleted")
	assert.Equal(t, deletedRepoRecord{owner: "org", repo: "test-repo-07-mint-host"}, scmDriver.deletedRepos[0])
	assert.Equal(t, deletedRepoRecord{owner: "org", repo: "test-repo-07-mint-unapproved"}, scmDriver.deletedRepos[1])
	require.Len(t, scmDriver.deletedBranches, 1)
	assert.Equal(t, "behaviour/mint-probe-0a1b2c3d", scmDriver.deletedBranches[0].branch)
}

// --- probe workflow script ---

// fakeCurlScript stands in for curl in the probe workflow script: it answers
// the OIDC, mint and revocation requests from FAKE_* variables and logs each
// revocation attempt to FAKE_LOG.
const fakeCurlScript = `#!/usr/bin/env bash
url="${@: -1}"
out=''
prev=''
for a in "$@"; do
  if [[ "$prev" == -o ]]; then out="$a"; fi
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
  */v1/token)
    echo mint >>"${FAKE_LOG}"
    printf '%s' "${FAKE_MINT_BODY}" >"$out"
    printf '%s' "${FAKE_MINT_STATUS}"
    ;;
  */installation/token)
    echo revoke >>"${FAKE_LOG}"
    case "${FAKE_REVOKE}" in
      ok) printf 204 ;;
      http-error) printf 403 ;;
      transport) exit 7 ;;
    esac
    ;;
esac
`

type probeScriptRun struct {
	oidcToken  string // value of the OIDC response; "" uses a signed, valid JWT
	oidc       probeOIDC
	mintStatus string
	mintBody   string
	revoke     string // ok, http-error or transport
}

// probeOIDC shapes the signed OIDC token and the signing keys the fake
// GitHub serves to the probe workflow script.
type probeOIDC struct {
	claims   map[string]any // overrides of the valid claims; a nil value removes the claim
	header   map[string]any // overrides of the valid header; a nil value removes the field
	wrongKey bool           // sign with a key GitHub does not publish
	noJWKS   bool           // the signing-key endpoint fails
}

type probeKeys struct {
	published, other *rsa.PrivateKey
	cert             []byte // DER certificate of the published key
}

var probeTestKeys = sync.OnceValue(func() *probeKeys {
	gen := func() *rsa.PrivateKey {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		return k
	}
	k := &probeKeys{published: gen(), other: gen()}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "probe-test-signing-key"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.published.PublicKey, k.published)
	if err != nil {
		panic(err)
	}
	k.cert = der
	return k
})

// signedProbeOIDC returns a GitHub-style RS256 OIDC token and the JWKS that
// publishes its signing key.
func signedProbeOIDC(t *testing.T, o probeOIDC) (token, jwks string) {
	t.Helper()
	keys := probeTestKeys()
	now := time.Now().Unix()
	claims := map[string]any{
		"iss":              "https://token.actions.githubusercontent.com",
		"aud":              "fullsend-mint",
		"exp":              now + 300,
		"nbf":              now - 10,
		"iat":              now - 10,
		"repository":       "o/r",
		"job_workflow_ref": "o/h/.github/workflows/mint-probe.yml@refs/heads/main",
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "probe-test-kid"}
	for _, m := range []struct{ dst, src map[string]any }{{claims, o.claims}, {header, o.header}} {
		for k, v := range m.src {
			if v == nil {
				delete(m.dst, k)
			} else {
				m.dst[k] = v
			}
		}
	}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(header) + "." + enc(claims)
	key := keys.published
	if o.wrongKey {
		key = keys.other
	}
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)

	if !o.noJWKS {
		b, err := json.Marshal(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "probe-test-kid",
			"x5c": []string{base64.StdEncoding.EncodeToString(keys.cert)},
		}}})
		require.NoError(t, err)
		jwks = string(b)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), jwks
}

// runMintProbeScript runs the probe workflow's script with fake curl and
// sleep, and returns the parsed result, the number of revocation attempts
// and the script's output.
func runMintProbeScript(t *testing.T, tc probeScriptRun) (*world.MintProbeResult, int, string) {
	t.Helper()
	var reusable struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(mintProbeWorkflow, &reusable))
	script := reusable.Jobs["probe"].Steps[0].Run
	require.NotEmpty(t, script)

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeCurlScript), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "sleep"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	scriptPath := filepath.Join(dir, "step.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o600))

	oidc, jwks := signedProbeOIDC(t, tc.oidc)
	if tc.oidcToken != "" {
		oidc = tc.oidcToken
	}
	oidcResponse, err := json.Marshal(map[string]string{"value": oidc})
	require.NoError(t, err)

	logPath := filepath.Join(dir, "calls.log")
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", scriptPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"MINT_URL=https://mint.test",
		"ROLE=triage", "LEVEL=read", `REPOS_JSON=["r"]`,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=request-token",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://tokens.test/?api=1",
		"FAKE_OIDC_RESPONSE="+string(oidcResponse),
		"FAKE_JWKS="+jwks,
		"FAKE_MINT_STATUS="+tc.mintStatus,
		"FAKE_MINT_BODY="+tc.mintBody,
		"FAKE_REVOKE="+tc.revoke,
		"FAKE_LOG="+logPath,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	data, err := os.ReadFile(filepath.Join(dir, mintProbeResultFile))
	require.NoError(t, err, string(output))
	res, err := parseMintProbeResult(data)
	require.NoError(t, err)

	revokes := 0
	if log, err := os.ReadFile(logPath); err == nil {
		revokes = strings.Count(string(log), "revoke")
	}
	return res, revokes, string(output)
}

const probeMintedBody = `{"token":"ghs_abc123","repository_selection":"selected","granted_repos":["o/r"],"granted_permissions":{"contents":"read"}}`

func TestMintProbeWorkflowScript_Revocation(t *testing.T) {
	for _, tool := range []string{"jq", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	t.Run("minted and revoked", func(t *testing.T) {
		res, revokes, out := runMintProbeScript(t, probeScriptRun{mintStatus: "200", mintBody: probeMintedBody, revoke: "ok"})
		assert.Equal(t, "minted", res.Outcome)
		assert.Equal(t, []string{"o/r"}, res.GrantedRepos)
		assert.Equal(t, 1, revokes)
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "::add-mask::") {
				assert.NotContains(t, line, "ghs_abc123", "the minted token is only ever printed to be masked")
			}
		}
	})
	for name, mode := range map[string]string{"HTTP error": "http-error", "transport failure": "transport"} {
		t.Run("revocation "+name+" is not a minted result", func(t *testing.T) {
			res, revokes, _ := runMintProbeScript(t, probeScriptRun{mintStatus: "200", mintBody: probeMintedBody, revoke: mode})
			assert.Equal(t, "error", res.Outcome)
			assert.Equal(t, "revoke", res.Error)
			assert.Empty(t, res.GrantedRepos, "an unrevoked token's scope is not reported")
			assert.Equal(t, 6, revokes, "three bounded attempts, then three more from the exit trap")
			assert.Equal(t, "revoke", classifyMintProbeResult(res).infra)
		})
	}
	t.Run("denial holds no token to revoke", func(t *testing.T) {
		res, revokes, _ := runMintProbeScript(t, probeScriptRun{mintStatus: "401", mintBody: `{"error":"authentication failed"}`, revoke: "http-error"})
		assert.Equal(t, "denied", res.Outcome)
		assert.Equal(t, 401, res.HTTPStatus)
		assert.Equal(t, mintProbeAuthFailedMessage, res.Message)
		assert.True(t, res.OIDCVerified, "the probe verified the OIDC token before the mint request")
		assert.Equal(t, mintProbeOutcomeHostDenied, classifyMintProbeResult(res).outcome)
		assert.Zero(t, revokes)
	})
}

// TestMintProbeWorkflowScript_VerifiesOIDCToken covers a subject-only OIDC
// failure: the probe must not call the mint with a token it cannot verify,
// so the mint's identical 401 never reads as a workflow-host denial.
func TestMintProbeWorkflowScript_VerifiesOIDCToken(t *testing.T) {
	for _, tool := range []string{"jq", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	now := time.Now().Unix()
	for name, o := range map[string]probeOIDC{
		"bad signature":      {wrongKey: true},
		"expired":            {claims: map[string]any{"exp": now - 60}},
		"missing exp":        {claims: map[string]any{"exp": nil}},
		"not yet valid":      {claims: map[string]any{"nbf": now + 3600}},
		"wrong audience":     {claims: map[string]any{"aud": "someone-else"}},
		"wrong issuer":       {claims: map[string]any{"iss": "https://issuer.example"}},
		"unknown kid":        {header: map[string]any{"kid": "other-kid"}},
		"missing kid":        {header: map[string]any{"kid": nil}},
		"unexpected alg":     {header: map[string]any{"alg": "none"}},
		"signing keys fetch": {noJWKS: true},
	} {
		t.Run(name, func(t *testing.T) {
			res, revokes, out := runMintProbeScript(t, probeScriptRun{oidc: o, mintStatus: "401", mintBody: `{"error":"authentication failed"}`, revoke: "ok"})
			assert.Equal(t, "error", res.Outcome)
			assert.Equal(t, "oidc", res.Error)
			assert.Zero(t, res.HTTPStatus, "the mint is not called with an unverified token")
			assert.False(t, res.OIDCVerified)
			assert.Zero(t, revokes)
			assert.Equal(t, "oidc", classifyMintProbeResult(res).infra)
			assert.NotContains(t, out, `"message"`)
		})
	}

	t.Run("audience list", func(t *testing.T) {
		res, _, _ := runMintProbeScript(t, probeScriptRun{
			oidc:       probeOIDC{claims: map[string]any{"aud": []string{"other", "fullsend-mint"}}},
			mintStatus: "401", mintBody: `{"error":"authentication failed"}`, revoke: "ok"})
		assert.Equal(t, "denied", res.Outcome)
		assert.True(t, res.OIDCVerified)
	})
}

// TestMintProbeWorkflowScript_DoesNotEchoResult keeps the mint's response
// text out of the step log; only fixed-vocabulary fields are printed.
func TestMintProbeWorkflowScript_DoesNotEchoResult(t *testing.T) {
	for _, tool := range []string{"jq", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	const hostile = "::error::injected ##[error]injected"
	res, _, out := runMintProbeScript(t, probeScriptRun{mintStatus: "403", mintBody: `{"error":"` + hostile + `"}`, revoke: "ok"})
	assert.Equal(t, hostile, res.Message, "the artifact keeps the mint's message")
	assert.NotContains(t, out, "injected")
	assert.Contains(t, out, "mint probe result: outcome=denied")
}

func TestMintProbeWorkflowScript_RejectsUnsafeTokens(t *testing.T) {
	for _, tool := range []string{"jq", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	for name, token := range map[string]string{
		"percent-encoded newline": "abc%0Adef",
		"percent-encoded return":  "abc%0Ddef",
		"escape character":        "abc\u001bdef",
		"workflow command":        "abc::set-output",
		"whitespace":              "abc def",
	} {
		t.Run("oidc token with "+name, func(t *testing.T) {
			res, revokes, _ := runMintProbeScript(t, probeScriptRun{oidcToken: token, mintStatus: "200", mintBody: probeMintedBody, revoke: "ok"})
			assert.Equal(t, "error", res.Outcome)
			assert.Equal(t, "oidc", res.Error)
			assert.Zero(t, revokes)
		})
		t.Run("minted token with "+name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"token": token, "repository_selection": "selected"})
			require.NoError(t, err)
			res, revokes, out := runMintProbeScript(t, probeScriptRun{mintStatus: "200", mintBody: string(body), revoke: "ok"})
			assert.Equal(t, "error", res.Outcome)
			assert.Equal(t, "response", res.Error)
			assert.Zero(t, revokes, "an unusable token is never sent to the revocation endpoint")
			assert.NotContains(t, out, "::add-mask::abc")
		})
	}
}
