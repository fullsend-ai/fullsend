package steps

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// Outputs of `fullsend mint status --mint-url`, as runMintStatusAPI and
// cmd/fullsend print them (stdout, then "Error: ..." on stderr).
const (
	mintStatusTestBanner = "⚡ fullsend dev\n  Autonomous agentic development for Git-hosted organizations\n\n→ Mint Status (API)\n\n  • Authenticating to /v1/status\n"

	mintStatusTestOK = mintStatusTestBanner +
		"  ✓ Authenticated via GitHub\n\n    URL: https://mint.example.test\n    Version: v1.2.3\n    Commit: abc\n\n\n" +
		"→ Roles\n      coder\n      fullsend\n      prioritize\n      retro\n      review\n      triage\n\n" +
		"→ Workflow Host Repos\n      halfsend-03/test-repo-01\n      halfsend-03/test-repo-01-mint-host\n\n" +
		"╭────────────────╮\n│ Status         │\n│   Auth: GitHub │\n│   Roles: 6     │\n│                │\n╰────────────────╯\n"

	mintStatusTestOIDC = mintStatusTestBanner +
		"  ✓ Authenticated via OIDC\n\n    URL: https://mint.example.test\n\n" +
		"→ Caller Organization\n      halfsend-03\n\n" +
		"→ Roles\n      coder\n\n" +
		"╭──────────────────────╮\n│ Status               │\n│   Auth: OIDC         │\n│   Org: halfsend-03   │\n│   Roles: 1           │\n│                      │\n╰──────────────────────╯\n"

	mintStatusTestAuthFailed = mintStatusTestBanner + "  ✗ Authentication failed\n" +
		"Error: querying mint status: authentication failed for /v1/status; attempted: OIDC (env vars not set), GitHub token (rejected)\n"

	mintStatusTestNotFound = mintStatusTestBanner + "  ✗ Status query failed\n" +
		"Error: querying mint status: calling /v1/status with GitHub token: status endpoint returned HTTP 404: {\"error\":\"not found\"}\n"

	mintStatusTestRefused = mintStatusTestBanner + "  ✗ Status query failed\n" +
		"Error: querying mint status: calling /v1/status with GitHub token: giving up after 3 attempts: requesting status: Get \"https://127.0.0.1:1/v1/status\": dial tcp 127.0.0.1:1: connect: connection refused\n"
)

const mintStatusTestURL = "https://mint.example.test"

// mintURLDriver is an install.Driver that reports a mint URL.
type mintURLDriver struct {
	install.Driver
	url string
}

func (d mintURLDriver) MintURL() string { return d.url }

// noReporterDriver is a name-only Driver that reports no mint URL.
type noReporterDriver struct {
	install.Driver
}

// failingLeaseDriver is a Driver whose slot allocation always fails
// with a fixed error, so suiteMintURL's leasing path can be driven
// without a pool.
type failingLeaseDriver struct {
	install.Driver
	err error
}

func (d failingLeaseDriver) AllocateLease(context.Context) (install.RepoLease, error) {
	return install.RepoLease{}, d.err
}

func (d failingLeaseDriver) DeallocateLease(context.Context, string, string) error {
	return nil
}

var _ install.LeaseDriver = failingLeaseDriver{}

// TestSuiteMintURL_LeaseFailureKeepsClassification checks that a slot
// allocation failure surfaced through suiteMintURL keeps its error
// type for the After-hook classifier: an infrastructure-marked
// allocation failure classifies infrastructure and an unmarked one
// stays scenario, instead of both degrading to rendered message text
// (PR #8801 review).
func TestSuiteMintURL_LeaseFailureKeepsClassification(t *testing.T) {
	t.Run("marked allocation failure stays infrastructure", func(t *testing.T) {
		allocErr := behaviourfail.Mark(errors.New("slot pool exhausted"), "slot allocation")
		_, err := suiteMintURL(context.Background(), &world.World{Driver: failingLeaseDriver{err: allocErr}})
		require.Error(t, err)
		assert.ErrorIs(t, err, allocErr)
		assert.Equal(t, mintStatusClassDeployment, mintStatusClass(t, err))
		assert.Equal(t, behaviourfail.Infrastructure, failclass.Classify(err))
	})

	t.Run("unmarked allocation failure stays scenario", func(t *testing.T) {
		allocErr := errors.New("slot allocator not configured")
		_, err := suiteMintURL(context.Background(), &world.World{Driver: failingLeaseDriver{err: allocErr}})
		require.Error(t, err)
		assert.ErrorIs(t, err, allocErr)
		assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err))
	})
}

func mintStatusClass(t *testing.T, err error) string {
	t.Helper()
	var msErr *mintStatusError
	require.True(t, errors.As(err, &msErr), "want a classified mint status error, got %v", err)
	return msErr.Class
}

func TestMintStatusFeature_StepsMatchPatterns(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "e2e", "behaviour", "features", "platform", "mint-status.feature"))
	require.NoError(t, err)
	defer f.Close()

	var plainSteps, outlineSteps, header []string
	var rows [][]string
	inOutline := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Scenario Outline:"):
			inOutline = true
		case strings.HasPrefix(line, "Scenario:"):
			inOutline = false
		case strings.HasPrefix(line, "Given ") || strings.HasPrefix(line, "When ") ||
			strings.HasPrefix(line, "Then ") || strings.HasPrefix(line, "And "):
			step := line[strings.Index(line, " ")+1:]
			if inOutline {
				outlineSteps = append(outlineSteps, step)
			} else {
				plainSteps = append(plainSteps, step)
			}
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
	require.Equal(t, []string{"mint address", "credential", "outcome"}, header)

	patterns := []*regexp.Regexp{
		regexp.MustCompile(mintStatusAddressStep),
		regexp.MustCompile(mintStatusRunStep),
		regexp.MustCompile(mintStatusOutcomeStep),
		regexp.MustCompile(mintStatusFactsStep),
		regexp.MustCompile(mintStatusAbsentStep),
	}
	matches := func(step string) bool {
		for _, p := range patterns {
			if p.MatchString(step) {
				return true
			}
		}
		return false
	}

	require.NotEmpty(t, plainSteps)
	for _, step := range plainSteps {
		assert.True(t, matches(step), "step %q matches no mint status step", step)
	}
	assert.Contains(t, plainSteps, "the operator runs mint status with the write actor credential")
	assert.Contains(t, plainSteps, `the mint status outcome is "ok"`)
	assert.Contains(t, plainSteps, "mint status reports the suite mint's status facts")

	outcomes := map[string]bool{}
	for _, row := range rows {
		require.Len(t, row, len(header))
		outcomes[row[2]] = true
		for _, step := range outlineSteps {
			expanded := step
			for i, h := range header {
				expanded = strings.ReplaceAll(expanded, "<"+h+">", row[i])
			}
			assert.NotContains(t, expanded, "<", "unexpanded placeholder in %q", expanded)
			assert.True(t, matches(expanded), "step %q matches no mint status step", expanded)
		}
	}
	assert.Equal(t, map[string]bool{
		mintStatusClassAuthentication: true,
		mintStatusClassTransport:      true,
		mintStatusClassDeployment:     true,
	}, outcomes, "each failure class needs a negative control")
}

func TestGivenMintStatusAddress(t *testing.T) {
	w := &world.World{Driver: mintURLDriver{url: mintStatusTestURL + "/"}}

	require.NoError(t, givenMintStatusAddress(context.Background(), w, mintStatusAddressSuite))
	assert.Equal(t, mintStatusTestURL+"/", w.MintStatus.MintURL)

	require.NoError(t, givenMintStatusAddress(context.Background(), w, mintStatusAddressNonMint))
	assert.Equal(t, mintStatusTestURL+mintStatusNonMintPath, w.MintStatus.MintURL)

	require.NoError(t, givenMintStatusAddress(context.Background(), w, mintStatusAddressUnreachable))
	assert.Equal(t, mintStatusUnreachableURL, w.MintStatus.MintURL)

	assert.Error(t, givenMintStatusAddress(context.Background(), w, "some other address"))
}

func TestGivenMintStatusAddress_LeasesForOrgMint(t *testing.T) {
	ld := &fakeLeaseDriver{lease: install.RepoLease{
		Org: "halfsend-04", Repo: "test-repo-02", Token: "tok-04",
		Client: forge.NewFakeClient(), MintURL: "https://mint.halfsend-04",
	}}
	w := &world.World{Driver: ld, Org: "halfsend-01", Logf: t.Logf}

	require.NoError(t, givenMintStatusAddress(context.Background(), w, mintStatusAddressSuite))
	assert.Equal(t, "https://mint.halfsend-04", w.MintStatus.MintURL, "the leased org's mint")
	assert.Equal(t, "halfsend-04", w.Org)
	assert.Equal(t, "halfsend-04", w.LeasedRepoOrg)
	assert.Equal(t, 1, ld.allocs)

	// A scenario that already leased reuses its lease's mint.
	require.NoError(t, givenMintStatusAddress(context.Background(), w, mintStatusAddressSuite))
	assert.Equal(t, 1, ld.allocs)
}

func TestGivenMintStatusAddress_LeaseFailureIsDeployment(t *testing.T) {
	w := &world.World{Driver: &fakeLeaseDriver{allocErr: errors.New("pool exhausted")}}
	err := givenMintStatusAddress(context.Background(), w, mintStatusAddressSuite)
	assert.Equal(t, mintStatusClassDeployment, mintStatusClass(t, err))
	assert.ErrorContains(t, err, "pool exhausted")
}

func TestGivenMintStatusAddress_UnknownSuiteMint(t *testing.T) {
	for name, w := range map[string]*world.World{
		"no driver":          {},
		"no mint URL":        {Driver: mintURLDriver{url: "  "}},
		"driver no reporter": {Driver: noReporterDriver{}},
	} {
		t.Run(name, func(t *testing.T) {
			err := givenMintStatusAddress(context.Background(), w, mintStatusAddressSuite)
			assert.Equal(t, mintStatusClassDeployment, mintStatusClass(t, err))
			assert.Nil(t, w.MintStatus)
		})
	}
	// The unreachable address does not need the suite's mint.
	w := &world.World{}
	require.NoError(t, givenMintStatusAddress(context.Background(), w, mintStatusAddressUnreachable))
}

func TestMintStatusCredential(t *testing.T) {
	_, err := mintStatusCredential(&world.World{}, mintStatusCredentialWrite)
	assert.ErrorContains(t, err, "TEST_ACTOR_WRITE_PAT")

	tok, err := mintStatusCredential(&world.World{WriteToken: "write-pat-value"}, mintStatusCredentialWrite)
	require.NoError(t, err)
	assert.Equal(t, "write-pat-value", tok)

	a, err := mintStatusCredential(&world.World{}, mintStatusCredentialInvalid)
	require.NoError(t, err)
	b, err := mintStatusCredential(&world.World{}, mintStatusCredentialInvalid)
	require.NoError(t, err)
	assert.NotEmpty(t, a)
	assert.NotEqual(t, a, b)

	_, err = mintStatusCredential(&world.World{}, "other credential")
	assert.Error(t, err)
}

func TestMintStatusEnv_WithholdsAmbientCredentials(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"HTTPS_PROXY=http://proxy:3128",
		"SSL_CERT_FILE=/etc/ssl/cert.pem",
		"HOME=/home/runner",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://oidc.example.test",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=oidc-request-token",
		"GITHUB_TOKEN=installation-token",
		"GH_TOKEN=ambient-token",
		"FULLSEND_MINT_URL=https://other-mint.example.test",
		"GOOGLE_APPLICATION_CREDENTIALS=/tmp/gcp.json",
		"CLOUDSDK_CORE_PROJECT=gcp-project",
		"E2E_GCP_PROJECT_ID=gcp-project",
		"TEST_ACTOR_WRITE_PAT=write-pat-value",
	}
	env := mintStatusEnv(environ, "/tmp/cli-home", "the-token")

	assert.ElementsMatch(t, []string{
		"PATH=/usr/bin",
		"HTTPS_PROXY=http://proxy:3128",
		"SSL_CERT_FILE=/etc/ssl/cert.pem",
		"HOME=/tmp/cli-home",
		"XDG_CONFIG_HOME=/tmp/cli-home/.config",
		"GH_CONFIG_DIR=/tmp/cli-home/.config/gh",
		"NO_COLOR=1",
		"GH_TOKEN=the-token",
	}, env)
}

func TestMintStatusArgs_NoProject(t *testing.T) {
	args := mintStatusArgs(mintStatusTestURL)
	assert.Equal(t, []string{"mint", "status", "--mint-url", mintStatusTestURL}, args)
	assert.NotContains(t, args, "--project")
}

func TestClassifyMintStatus(t *testing.T) {
	cases := []struct {
		name string
		ms   world.MintStatus
		want string
	}{
		{"success", world.MintStatus{Output: mintStatusTestOK}, mintStatusOutcomeOK},
		{"rejected token", world.MintStatus{ExitCode: 1, Output: mintStatusTestAuthFailed}, mintStatusClassAuthentication},
		{"connection refused", world.MintStatus{ExitCode: 1, Output: mintStatusTestRefused}, mintStatusClassTransport},
		{"timed out", world.MintStatus{ExitCode: -1, TimedOut: true}, mintStatusClassTransport},
		{"not a mint", world.MintStatus{ExitCode: 1, Output: mintStatusTestNotFound}, mintStatusClassDeployment},
		{"server error", world.MintStatus{ExitCode: 1, Output: "Error: querying mint status: calling /v1/status with GitHub token: giving up after 3 attempts: status endpoint returned HTTP 503: unavailable\n"}, mintStatusClassDeployment},
		{"not https", world.MintStatus{ExitCode: 1, Output: "Error: querying mint status: mint URL must use HTTPS\n"}, mintStatusClassDeployment},
		{"unknown failure", world.MintStatus{ExitCode: 2, Output: "Error: unknown command\n"}, mintStatusOutcomeUnclassifiable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyMintStatus(&tc.ms))
		})
	}
}

func TestThenMintStatusOutcome(t *testing.T) {
	ran := func(exit int, output string) *world.World {
		return &world.World{Org: "halfsend-03", MintStatusGroup: "halfsend-07/mint-status", MintStatus: &world.MintStatus{
			MintURL: mintStatusTestURL, Credential: mintStatusCredentialWrite,
			Ran: true, ExitCode: exit, Output: output,
		}}
	}

	assert.NoError(t, thenMintStatusOutcome(ran(0, mintStatusTestOK), "ok"))
	assert.NoError(t, thenMintStatusOutcome(ran(1, mintStatusTestAuthFailed), "authentication"))
	assert.NoError(t, thenMintStatusOutcome(ran(1, mintStatusTestRefused), "transport"))
	assert.NoError(t, thenMintStatusOutcome(ran(1, mintStatusTestNotFound), "deployment"))

	// A failure where success was expected keeps its own class.
	err := thenMintStatusOutcome(ran(1, mintStatusTestAuthFailed), "ok")
	assert.Equal(t, mintStatusClassAuthentication, mintStatusClass(t, err))
	assert.ErrorContains(t, err, "halfsend-07/mint-status", "a rejected write actor names the stored status group")
	assert.NotContains(t, err.Error(), "halfsend-03/mint-status", "the status group is not derived from the scenario's org")
	noGroup := ran(1, mintStatusTestAuthFailed)
	noGroup.MintStatusGroup = ""
	err = thenMintStatusOutcome(noGroup, "ok")
	assert.ErrorContains(t, err, "the mint's status group")
	assert.NotContains(t, err.Error(), "halfsend-03/mint-status")
	err = thenMintStatusOutcome(ran(1, mintStatusTestRefused), "ok")
	assert.Equal(t, mintStatusClassTransport, mintStatusClass(t, err))
	err = thenMintStatusOutcome(ran(1, mintStatusTestNotFound), "ok")
	assert.Equal(t, mintStatusClassDeployment, mintStatusClass(t, err))

	// No expected failure passes on a success or a failure of another class.
	err = thenMintStatusOutcome(ran(0, mintStatusTestOK), "authentication")
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))
	err = thenMintStatusOutcome(ran(1, mintStatusTestRefused), "deployment")
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))
	err = thenMintStatusOutcome(ran(2, "Error: unknown command\n"), "ok")
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))

	assert.Error(t, thenMintStatusOutcome(ran(0, mintStatusTestOK), "assertion"), "assertion is not an expected outcome")
	assert.Error(t, thenMintStatusOutcome(&world.World{}, "ok"), "mint status not run")
}

func TestParseMintStatusOutput(t *testing.T) {
	r := parseMintStatusOutput(mintStatusTestOK)
	assert.Equal(t, "GitHub", r.AuthMethod)
	assert.Equal(t, mintStatusTestURL, r.URL)
	assert.False(t, r.OrgReported)
	assert.Equal(t, []string{"coder", "fullsend", "prioritize", "retro", "review", "triage"}, r.Roles)
	assert.Equal(t, []string{"halfsend-03/test-repo-01", "halfsend-03/test-repo-01-mint-host"}, r.WorkflowHostRepos)
	assert.Equal(t, "GitHub", r.SummaryAuth)
	assert.True(t, r.SummaryRolesSet)
	assert.Equal(t, 6, r.SummaryRoles)

	r = parseMintStatusOutput(mintStatusTestOIDC)
	assert.Equal(t, "OIDC", r.AuthMethod)
	assert.True(t, r.OrgReported)
	assert.Equal(t, "halfsend-03", r.Org)
	assert.Equal(t, []string{"coder"}, r.Roles)
	assert.Empty(t, r.WorkflowHostRepos)

	r = parseMintStatusOutput(strings.Replace(mintStatusTestOK,
		"→ Roles\n      coder\n      fullsend\n      prioritize\n      retro\n      review\n      triage\n",
		"→ Roles\n      (none)\n", 1))
	assert.Empty(t, r.Roles)
}

func TestMintStatusFacts(t *testing.T) {
	assert.Empty(t, mintStatusFacts(parseMintStatusOutput(mintStatusTestOK), mintStatusTestURL, "halfsend-03/test-repo-01", nil))
	assert.Empty(t, mintStatusFacts(parseMintStatusOutput(mintStatusTestOK), mintStatusTestURL, "Halfsend-03/test-repo-01", nil),
		"org comparison is case-insensitive")

	problems := mintStatusFacts(parseMintStatusOutput(mintStatusTestOIDC), mintStatusTestURL, "halfsend-03/test-repo-01", nil)
	joined := strings.Join(problems, "\n")
	assert.Contains(t, joined, `authenticated via "OIDC"`)
	assert.Contains(t, joined, `summary auth "OIDC"`)
	assert.Contains(t, joined, "caller organization")
	assert.Contains(t, joined, `role "triage" not reported`)
	assert.Contains(t, joined, "do not include halfsend-03/test-repo-01")

	problems = mintStatusFacts(parseMintStatusOutput(mintStatusTestOK), "https://other.example.test", "halfsend-01/test-repo-01", nil)
	joined = strings.Join(problems, "\n")
	assert.Contains(t, joined, `URL "https://mint.example.test"`)
	assert.Contains(t, joined, "outside org halfsend-01")

	noRoles := strings.Replace(mintStatusTestOK,
		"→ Roles\n      coder\n      fullsend\n      prioritize\n      retro\n      review\n      triage\n",
		"→ Roles\n      (none)\n", 1)
	joined = strings.Join(mintStatusFacts(parseMintStatusOutput(noRoles), mintStatusTestURL, "halfsend-03/test-repo-01", nil), "\n")
	assert.Contains(t, joined, "no roles reported")
	assert.Contains(t, joined, "summary role count")
}

// mintStatusGroupOutput is mintStatusTestOK for a mint whose group
// serves halfsend-02 and halfsend-03, listing hosts.
func mintStatusGroupOutput(hosts ...string) string {
	return strings.Replace(mintStatusTestOK,
		"→ Workflow Host Repos\n      halfsend-03/test-repo-01\n      halfsend-03/test-repo-01-mint-host\n",
		"→ Workflow Host Repos\n      "+strings.Join(hosts, "\n      ")+"\n", 1)
}

func TestMintStatusFacts_MintGroup(t *testing.T) {
	group := []string{
		"halfsend-02/test-repo-01", "halfsend-02/test-repo-01-mint-host",
		"halfsend-03/test-repo-01", "halfsend-03/test-repo-01-mint-host",
	}

	// Hosts of other orgs in the scenario's mint group are expected.
	r := parseMintStatusOutput(mintStatusGroupOutput(group...))
	assert.Empty(t, mintStatusFacts(r, mintStatusTestURL, "halfsend-03/test-repo-01", group))
	assert.Empty(t, mintStatusFacts(r, mintStatusTestURL, "HALFSEND-02/test-repo-01", group),
		"host comparison is case-insensitive")

	// A host outside the group is rejected.
	r = parseMintStatusOutput(mintStatusGroupOutput(append(group[:4:4], "halfsend-09/test-repo-01")...))
	joined := strings.Join(mintStatusFacts(r, mintStatusTestURL, "halfsend-03/test-repo-01", group), "\n")
	assert.Contains(t, joined, `"halfsend-09/test-repo-01" is not a workflow host of the mint's group`)

	// A group host the mint does not report is a mismatch.
	r = parseMintStatusOutput(mintStatusGroupOutput(group[2:]...))
	joined = strings.Join(mintStatusFacts(r, mintStatusTestURL, "halfsend-03/test-repo-01", group), "\n")
	assert.Contains(t, joined, `"halfsend-02/test-repo-01" of the mint's group not reported`)

	// The scenario's own host is required even when the group list
	// lacks it.
	joined = strings.Join(mintStatusFacts(parseMintStatusOutput(mintStatusGroupOutput(group[:2]...)),
		mintStatusTestURL, "halfsend-03/test-repo-01", group[:2]), "\n")
	assert.Contains(t, joined, "do not include halfsend-03/test-repo-01")
}

func TestThenMintStatusReportsFacts(t *testing.T) {
	w := &world.World{Org: "halfsend-03", MintStatus: &world.MintStatus{
		MintURL: mintStatusTestURL, Ran: true, Output: mintStatusTestOK,
	}}
	assert.NoError(t, thenMintStatusReportsFacts(w))

	// The leased repo is the scenario's own host, and the stored mint
	// group hosts are what the mint must report.
	group := []string{"halfsend-02/test-repo-05", "halfsend-03/test-repo-05"}
	grouped := &world.World{Org: "halfsend-03", LeasedRepoName: "test-repo-05", MintWorkflowHostRepos: group,
		MintStatus: &world.MintStatus{MintURL: mintStatusTestURL, Ran: true, Output: mintStatusGroupOutput(group...)}}
	assert.NoError(t, thenMintStatusReportsFacts(grouped))
	grouped.LeasedRepoName = "test-repo-06"
	err := thenMintStatusReportsFacts(grouped)
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))
	assert.ErrorContains(t, err, "do not include halfsend-03/test-repo-06")

	w.MintStatus.Output = strings.Replace(mintStatusTestOK, "Authenticated via GitHub", "Authenticated via OIDC", 1)
	err = thenMintStatusReportsFacts(w)
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))

	w.MintStatus.ExitCode = 1
	w.MintStatus.Output = mintStatusTestRefused
	err = thenMintStatusReportsFacts(w)
	assert.Equal(t, mintStatusClassTransport, mintStatusClass(t, err))

	assert.Error(t, thenMintStatusReportsFacts(&world.World{}))
}

func TestThenMintStatusCredentialAbsent(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)
	w := &world.World{WriteToken: "write-pat-value-for-scan", MintStatus: &world.MintStatus{
		Ran: true, Credential: mintStatusCredentialWrite,
	}}
	require.NoError(t, os.WriteFile(filepath.Join(artifactDir, "clean.txt"), []byte("nothing here"), 0o600))
	assert.NoError(t, thenMintStatusCredentialAbsent(w))

	w.MintStatus.CredentialInOutput = true
	err := thenMintStatusCredentialAbsent(w)
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))
	w.MintStatus.CredentialInOutput = false

	sub := filepath.Join(artifactDir, "debug-x")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "leak.txt"), []byte("token=write-pat-value-for-scan"), 0o600))
	err = thenMintStatusCredentialAbsent(w)
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err))
	assert.ErrorContains(t, err, filepath.Join("debug-x", "leak.txt"))
	assert.NotContains(t, err.Error(), "write-pat-value-for-scan")

	assert.Error(t, thenMintStatusCredentialAbsent(&world.World{}))
}

func TestFilesContaining_MissingRoot(t *testing.T) {
	found, err := filesContaining(filepath.Join(t.TempDir(), "missing"), "x")
	require.NoError(t, err)
	assert.Empty(t, found)
}

func TestFilesContaining_SkipsInProgressTempFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".artifact-123"), []byte("needle"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "done.txt"), []byte("needle"), 0o600))
	found, err := filesContaining(root, "needle")
	require.NoError(t, err)
	assert.Equal(t, []string{"done.txt"}, found)
}

// writeFakeCLI writes a shell script standing in for the fullsend CLI.
// It records its arguments and environment under dir and runs body.
func writeFakeCLI(t *testing.T, dir, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	path := filepath.Join(dir, "fullsend")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + filepath.Join(dir, "args.txt") + "'\n" +
		"env > '" + filepath.Join(dir, "env.txt") + "'\n" +
		body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

func TestWhenOperatorRunsMintStatus_RunsCLIAsOperator(t *testing.T) {
	dir := t.TempDir()
	artifactDir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", artifactDir)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example.test")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-request-token")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/tmp/gcp.json")
	t.Setenv("FULLSEND_MINT_URL", "https://other-mint.example.test")

	const token = "write-pat-value-for-run"
	cli := writeFakeCLI(t, dir, `echo "Authenticated via GitHub"; echo "token was $GH_TOKEN"`)
	var logs []string
	w := &world.World{
		CLIBinary:    cli,
		WriteToken:   token,
		ScenarioName: "mint status run",
		Logf:         func(f string, a ...any) { logs = append(logs, f) },
		MintStatus:   &world.MintStatus{MintURL: mintStatusTestURL},
	}

	require.NoError(t, whenOperatorRunsMintStatus(context.Background(), w, mintStatusCredentialWrite))

	ms := w.MintStatus
	assert.True(t, ms.Ran)
	assert.Equal(t, 0, ms.ExitCode)
	assert.Equal(t, mintStatusCredentialWrite, ms.Credential)
	assert.True(t, ms.CredentialInOutput)
	assert.NotContains(t, ms.Output, token)
	assert.Contains(t, ms.Output, "Authenticated via GitHub")
	assert.NotEmpty(t, logs)

	args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	require.NoError(t, err)
	assert.Equal(t, "mint\nstatus\n--mint-url\n"+mintStatusTestURL+"\n", string(args))

	envData, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	require.NoError(t, err)
	env := string(envData)
	assert.Contains(t, env, "GH_TOKEN="+token+"\n")
	for _, withheld := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL=", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=", "GOOGLE_APPLICATION_CREDENTIALS=", "FULLSEND_MINT_URL=", "GITHUB_TOKEN="} {
		assert.NotContains(t, env, withheld)
	}

	// The redacted transcript is saved, and holds no token.
	found, err := filesContaining(artifactDir, "Authenticated via GitHub")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.True(t, strings.HasPrefix(found[0], "mint-status-"), "artifact %s", found[0])
	leaked, err := filesContaining(artifactDir, token)
	require.NoError(t, err)
	assert.Empty(t, leaked)

	err = thenMintStatusCredentialAbsent(w)
	assert.Equal(t, mintStatusClassAssertion, mintStatusClass(t, err), "the CLI printed the credential")
}

func TestWhenOperatorRunsMintStatus_RecordsFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")
	cli := writeFakeCLI(t, dir, `echo "Error: querying mint status: authentication failed for /v1/status" >&2; exit 1`)
	w := &world.World{CLIBinary: cli, MintStatus: &world.MintStatus{MintURL: mintStatusTestURL}}

	require.NoError(t, whenOperatorRunsMintStatus(context.Background(), w, mintStatusCredentialInvalid))
	assert.Equal(t, 1, w.MintStatus.ExitCode)
	assert.False(t, w.MintStatus.TimedOut)
	assert.False(t, w.MintStatus.CredentialInOutput)
	assert.NoError(t, thenMintStatusOutcome(w, mintStatusClassAuthentication))
	assert.NoError(t, thenMintStatusCredentialAbsent(w))
}

func TestWhenOperatorRunsMintStatus_TimesOut(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEHAVIOUR_ARTIFACT_DIR", "")
	orig := mintStatusTimeout
	mintStatusTimeout = 200 * time.Millisecond
	t.Cleanup(func() { mintStatusTimeout = orig })
	cli := writeFakeCLI(t, dir, `exec sleep 30`)
	w := &world.World{CLIBinary: cli, MintStatus: &world.MintStatus{MintURL: mintStatusTestURL}}

	require.NoError(t, whenOperatorRunsMintStatus(context.Background(), w, mintStatusCredentialInvalid))
	assert.True(t, w.MintStatus.TimedOut)
	assert.NotEqual(t, 0, w.MintStatus.ExitCode)
	assert.Equal(t, mintStatusClassTransport, classifyMintStatus(w.MintStatus))
}

func TestWhenOperatorRunsMintStatus_Preconditions(t *testing.T) {
	cli := writeFakeCLI(t, t.TempDir(), "exit 0")

	err := whenOperatorRunsMintStatus(context.Background(), &world.World{CLIBinary: cli}, mintStatusCredentialWrite)
	assert.ErrorContains(t, err, "no mint address")

	err = whenOperatorRunsMintStatus(context.Background(), &world.World{
		MintStatus: &world.MintStatus{MintURL: mintStatusTestURL},
	}, mintStatusCredentialWrite)
	assert.ErrorContains(t, err, "no fullsend CLI binary")

	w := &world.World{CLIBinary: cli, MintStatus: &world.MintStatus{MintURL: mintStatusTestURL}}
	err = whenOperatorRunsMintStatus(context.Background(), w, mintStatusCredentialWrite)
	assert.ErrorContains(t, err, "TEST_ACTOR_WRITE_PAT")
	assert.False(t, w.MintStatus.Ran)

	w = &world.World{CLIBinary: filepath.Join(t.TempDir(), "missing"), WriteToken: "tok-value",
		MintStatus: &world.MintStatus{MintURL: mintStatusTestURL}}
	err = whenOperatorRunsMintStatus(context.Background(), w, mintStatusCredentialWrite)
	assert.ErrorContains(t, err, "running fullsend mint status")
}
