package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hostURL = "https://git.example.com/forgejo"

const workflowHostManifest = `version: 1
forgejo:
  instances:
    - alias: home
      url: https://git.example.com/forgejo
      repos:
        - name: fullsend/workflows
          workflow_host: true
        - name: org/app
        - name: org/solo
          vendor: true
    - alias: lone
      url: https://lone.example.com
      repos:
        - name: org/standalone
          vendor: true
        - name: org/needs-host
`

func hostEnv() map[string]string {
	return map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t", "FULLSEND_FORGEJO_LONE_TOKEN": "l"}
}

func selectTargets(t *testing.T, lc *ForgejoLifecycle, sel ...string) []ForgejoTarget {
	t.Helper()
	s, err := lc.Manifest.SplitForgejoSelectors(sel)
	require.NoError(t, err)
	return s.Targets
}

func newHostLifecycle(t *testing.T) (*ForgejoLifecycle, *memForgejo, *memForgejo) {
	t.Helper()
	home := &memForgejo{files: map[string]string{}}
	lone := &memForgejo{files: map[string]string{}}
	lc, _ := newTestLifecycle(t, workflowHostManifest, map[string]*memForgejo{hostURL: home, "https://lone.example.com": lone}, hostEnv())
	return lc, home, lone
}

func testBundle(dispatch string) *ForgejoBundle {
	b := NewForgejoBundle(map[string][]byte{forgejoDispatchPath: []byte(dispatch)})
	return &b
}

func TestLoadForgejoBundle(t *testing.T) {
	b, err := LoadForgejoBundle()
	require.NoError(t, err)
	assert.Len(t, b.Version, 12)
	assert.Contains(t, string(b.Files[forgejoDispatchPath]), "workflow_call")
	assert.Equal(t, forgejoBundleTagPrefix+b.Version, b.Tag())
	for p := range b.Files {
		assert.True(t, forgejoManagedPath(p), p)
	}
	again, err := LoadForgejoBundle()
	require.NoError(t, err)
	assert.Equal(t, b.Version, again.Version, "the version is derived from content")
	assert.NotEqual(t, b.Version, testBundle("other").Version)
}

func TestForgejoManifest_WorkflowHostValidation(t *testing.T) {
	for name, tc := range map[string]struct{ repos, want string }{
		"two hosts":     {"        - name: a/one\n          workflow_host: true\n        - name: a/two\n          workflow_host: true\n", "more than one workflow host"},
		"vendored host": {"        - name: a/one\n          workflow_host: true\n          vendor: true\n", "cannot set vendor: true"},
	} {
		t.Run(name, func(t *testing.T) {
			var m Manifest
			require.NoError(t, parseManifestBytes([]byte("version: 1\nforgejo:\n  instances:\n    - alias: a\n      url: https://a.example\n      repos:\n"+tc.repos), &m))
			assert.ErrorContains(t, m.Validate(), tc.want)
		})
	}
}

func TestForgejoWorkflows_PublishAndPin(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call # v1\n")
	targets := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")

	res, err := lc.Install(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateInstalled, res[0].State)
	assert.Equal(t, "workflow-host", res[0].Role)
	assert.Equal(t, ForgejoStateInstalled, res[1].State)

	tag := lc.Bundle.Tag()
	pin := home.tags["fullsend/workflows@"+tag]
	require.NotEmpty(t, pin, "the bundle version is tagged on the host")
	assert.Equal(t, "on: workflow_call # v1\n", home.files["fullsend/workflows/"+forgejoDispatchPath])
	assert.Equal(t, "on: workflow_call # v1\n", home.commits[pin]["fullsend/workflows/"+forgejoDispatchPath], "the tag points at the published bundle")
	assert.NotContains(t, home.files, "fullsend/workflows/.fullsend/config.yaml", "the host gets no consumer config")

	shim := home.files["org/app/"+forgejoShimPath]
	assert.Contains(t, shim, "uses: "+hostURL+"/fullsend/workflows/"+forgejoDispatchPath+"@"+pin)
	assert.NotContains(t, shim, "github.com", "no generated reference points at GitHub")
	assert.NotContains(t, shim, "__")
	assert.Contains(t, res[1].Detail, "@"+pin)
	assert.NotContains(t, home.files, "org/app/"+forgejoDispatchPath, "a consumer does not carry the bundle")
	assert.Contains(t, home.files["org/app/"+forgejoWorkflowIndexPath], "  - "+forgejoShimPath+"\n")

	// Rerunning converges without changes.
	writes := home.writes
	res, err = lc.Install(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUpToDate, res[0].State)
	assert.Equal(t, ForgejoStateUpToDate, res[1].State)
	assert.Equal(t, writes, home.writes)

	st, err := lc.Status(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUpToDate, st[0].State)
	assert.Contains(t, st[0].Detail, tag)
	assert.Equal(t, ForgejoStateUpToDate, st[1].State)

	// A new bundle version publishes a new tag and repins consumers.
	lc.Bundle = testBundle("on: workflow_call # v2\n")
	st, err = lc.Status(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateDrift, st[0].State)
	assert.Contains(t, st[0].Detail, "not tagged")
	assert.Equal(t, ForgejoStateDrift, st[1].State)
	assert.Contains(t, st[1].Detail, "is not published on forgejo:home/fullsend/workflows")
	assert.Equal(t, writes, home.writes, "status is read-only")

	res, err = lc.Install(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUpgraded, res[0].State)
	assert.Equal(t, ForgejoStateUpgraded, res[1].State)
	newPin := home.tags["fullsend/workflows@"+lc.Bundle.Tag()]
	require.NotEmpty(t, newPin)
	assert.NotEqual(t, pin, newPin)
	assert.Equal(t, pin, home.tags["fullsend/workflows@"+tag], "the old version tag is kept")
	assert.Contains(t, home.files["org/app/"+forgejoShimPath], "@"+newPin)
}

func TestForgejoWorkflows_StatusReportsDriftWithoutWriting(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	targets := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app", "forgejo:home/org/solo")

	st, err := lc.Status(ctx, targets)
	require.NoError(t, err)
	for _, r := range st {
		assert.Equal(t, ForgejoStateNotInstalled, r.State, r.Selector)
	}

	_, err = lc.Install(ctx, targets)
	require.NoError(t, err)
	assert.Contains(t, home.files["org/solo/"+forgejoShimPath], "uses: ./"+forgejoDispatchPath)
	assert.Equal(t, "on: workflow_call\n", home.files["org/solo/"+forgejoDispatchPath])

	home.files["org/app/"+forgejoShimPath] += "# edited\n"
	home.files["fullsend/workflows/"+forgejoDispatchPath] = "edited\n"
	home.files["org/solo/"+forgejoDispatchPath] = "edited\n"
	writes := home.writes
	st, err = lc.Status(ctx, targets)
	require.NoError(t, err)
	for _, r := range st {
		assert.Equal(t, ForgejoStateDrift, r.State, r.Selector)
		assert.Contains(t, r.Detail, "workflow files differ", r.Selector)
	}
	assert.Equal(t, writes, home.writes)

	res, err := lc.Install(ctx, targets)
	require.NoError(t, err)
	for _, r := range res {
		assert.Equal(t, ForgejoStateUpgraded, r.State, r.Selector)
	}
	assert.Equal(t, "on: workflow_call\n", home.files["fullsend/workflows/"+forgejoDispatchPath])
	assert.NotContains(t, home.files["org/app/"+forgejoShimPath], "# edited")
}

func TestForgejoWorkflows_MissingHostFailsBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	lc, home, lone := newHostLifecycle(t)
	var contacted int
	inner := lc.NewClient
	lc.NewClient = func(u, tok string) (ForgejoFileClient, error) { contacted++; return inner(u, tok) }

	_, err := lc.Install(ctx, selectTargets(t, lc, "forgejo:home/org/app", "forgejo:lone/org/standalone", "forgejo:lone/org/needs-host"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `Forgejo instance "lone" (needed by forgejo:lone/org/needs-host)`)
	assert.Contains(t, err.Error(), "workflow_host: true")
	assert.Contains(t, err.Error(), "vendor: true")
	assert.Zero(t, contacted, "no client is built")
	assert.Empty(t, home.files)
	assert.Empty(t, lone.files)

	st, err := lc.Status(ctx, selectTargets(t, lc, "forgejo:lone/org/needs-host"))
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateError, st[0].State)
	assert.Contains(t, st[0].Detail, `instance "lone"`)

	// Vendored mode needs no host.
	res, err := lc.Install(ctx, selectTargets(t, lc, "forgejo:lone/org/standalone"))
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateInstalled, res[0].State)
	assert.Equal(t, "vendored", res[0].Role)
	assert.Contains(t, lone.files, "org/standalone/"+forgejoDispatchPath)
	assert.Contains(t, lone.files, "org/standalone/.fullsend/config.yaml")
}

func TestForgejoWorkflows_UnselectedHostMustBePublished(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	app := selectTargets(t, lc, "forgejo:home/org/app")

	res, err := lc.Install(ctx, app)
	require.Error(t, err)
	assert.Equal(t, ForgejoStateError, res[0].State)
	assert.Contains(t, err.Error(), "run repos install forgejo:home/fullsend/workflows first")
	assert.Empty(t, home.files, "nothing is written for the consumer")

	_, err = lc.Install(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows"))
	require.NoError(t, err)
	res, err = lc.Install(ctx, app)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateInstalled, res[0].State)
	assert.NotContains(t, home.files, "org/solo/.fullsend/config.yaml", "unselected repositories are untouched")
}

func TestForgejoWorkflows_TamperedTagIsNotPinned(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	home.files["fullsend/workflows/README.md"] = "x"
	home.tags = map[string]string{"fullsend/workflows@" + lc.Bundle.Tag(): home.snapshot()}

	targets := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")
	res, err := lc.Install(ctx, targets)
	require.Error(t, err)
	assert.Contains(t, res[0].Detail, "lacks "+forgejoDispatchPath)
	assert.Contains(t, res[1].Detail, "workflow host forgejo:home/fullsend/workflows failed")
	assert.NotContains(t, home.files, "org/app/"+forgejoShimPath)

	st, err := lc.Status(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateError, st[0].State)
	assert.Equal(t, ForgejoStateError, st[1].State)

	// A tag over different content is refused too.
	home.files = map[string]string{"fullsend/workflows/" + forgejoDispatchPath: "evil\n"}
	home.tags["fullsend/workflows@"+lc.Bundle.Tag()] = home.snapshot()
	home.files = map[string]string{}
	_, err = lc.Install(ctx, targets[:1])
	assert.ErrorContains(t, err, "differs from the bundle")
}

func TestForgejoWorkflows_TagMissingForCurrentFiles(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	host := selectTargets(t, lc, "forgejo:home/fullsend/workflows")
	_, err := lc.Install(ctx, host)
	require.NoError(t, err)

	delete(home.tags, "fullsend/workflows@"+lc.Bundle.Tag())
	res, err := lc.Install(ctx, host)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUpgraded, res[0].State)
	pin := home.tags["fullsend/workflows@"+lc.Bundle.Tag()]
	assert.Equal(t, "on: workflow_call\n", home.commits[pin]["fullsend/workflows/"+forgejoDispatchPath])
}

func TestForgejoWorkflows_DryRunPublishesNothing(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.DryRun = true
	res, err := lc.Install(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app"))
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateInstalled, res[0].State)
	assert.Contains(t, res[0].Detail, "would publish")
	assert.Equal(t, ForgejoStateInstalled, res[1].State)
	assert.Contains(t, res[1].Detail, "<pending")
	assert.Zero(t, home.writes)
	assert.Empty(t, home.tags)
}

func TestForgejoWorkflows_UnownedFilesAreNeverChanged(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	home.files["org/solo/"+forgejoShimPath] = "hand written\n"
	home.files["fullsend/workflows/"+forgejoWorkflowIndexPath] = "role: mine\n"
	targets := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/solo")

	st, err := lc.Status(ctx, targets)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUnmanaged, st[0].State)
	assert.Contains(t, st[0].Detail, "ownership marker")
	assert.Equal(t, ForgejoStateUnmanaged, st[1].State)
	assert.Contains(t, st[1].Detail, "not listed in "+forgejoWorkflowIndexPath)

	_, err = lc.Install(ctx, targets)
	require.Error(t, err)
	assert.Zero(t, home.writes)

	// org/app is selected too, so the host's declared consumer is released.
	res, err := lc.Uninstall(ctx, append(targets, selectTargets(t, lc, "forgejo:home/org/app")...))
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUnmanaged, res[0].State)
	assert.Equal(t, ForgejoStateUnmanaged, res[1].State, "a workflow file without an ownership index is retained as unmanaged")
	assert.Contains(t, res[1].Detail, forgejoShimPath)
	assert.Equal(t, "hand written\n", home.files["org/solo/"+forgejoShimPath])
	assert.Zero(t, home.writes)
}

func TestForgejoWorkflows_IndexCannotClaimUnrelatedFiles(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	solo := selectTargets(t, lc, "forgejo:home/org/solo")
	_, err := lc.Install(ctx, solo)
	require.NoError(t, err)

	home.files["org/solo/README.md"] = "keep me"
	home.files["org/solo/"+forgejoWorkflowIndexPath] += "  - README.md\n  - .fullsend/../README.md\n"
	res, err := lc.Uninstall(ctx, solo)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateRemoved, res[0].State)
	assert.Equal(t, map[string]string{"org/solo/README.md": "keep me"}, home.files)
}

func TestForgejoWorkflows_UninstallProtectsDeclaredConsumers(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	all := selectTargets(t, lc, "forgejo:home/*/*")
	_, err := lc.Install(ctx, all)
	require.NoError(t, err)

	host := selectTargets(t, lc, "forgejo:home/fullsend/workflows")
	writes := home.writes
	res, err := lc.Uninstall(ctx, host)
	require.Error(t, err)
	assert.Equal(t, ForgejoStateError, res[0].State)
	assert.Contains(t, err.Error(), "still serves 1 consumer(s) declared in repos.yaml: forgejo:home/org/app")
	assert.NotContains(t, err.Error(), "org/solo", "a vendored repository is not a consumer")
	assert.Contains(t, err.Error(), "Only consumers declared in repos.yaml")
	assert.Equal(t, writes, home.writes)
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)

	// A consumer whose teardown fails still depends on the host.
	home.failOn = "delete"
	_, err = lc.Uninstall(ctx, all)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still serves")
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)

	// Removing the consumers in the same run releases the host.
	home.failOn = ""
	res, err = lc.Uninstall(ctx, all)
	require.NoError(t, err)
	for _, r := range res {
		assert.Equal(t, ForgejoStateRemoved, r.State, r.Selector)
	}
	tagged := home.tags
	for k := range home.files {
		assert.Fail(t, "file left behind", k)
	}
	assert.NotEmpty(t, tagged, "version tags are kept for pinned consumers")

	// The lifecycle does not edit repos.yaml, so the consumer is still
	// declared and a host-only uninstall stays refused until it is not.
	_, err = lc.Uninstall(ctx, host)
	assert.ErrorContains(t, err, "still serves")
	lc.Manifest.RemoveForgejoRepos(selectTargets(t, lc, "forgejo:home/org/app"))
	res, err = lc.Uninstall(ctx, host)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateNotInstalled, res[0].State)
}

func TestForgejoWorkflows_ForgeErrors(t *testing.T) {
	ctx := context.Background()
	for _, failOn := range []string{"tag", "change", "createtag", "get"} {
		t.Run(failOn, func(t *testing.T) {
			lc, home, _ := newHostLifecycle(t)
			lc.Bundle = testBundle("on: workflow_call\n")
			home.failOn = failOn
			targets := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")
			res, err := lc.Install(ctx, targets)
			require.Error(t, err)
			assert.Equal(t, ForgejoStateError, res[0].State)
			assert.Equal(t, ForgejoStateError, res[1].State)
			if failOn == "tag" || failOn == "get" {
				st, err := lc.Status(ctx, targets)
				require.NoError(t, err)
				assert.Equal(t, ForgejoStateError, st[0].State)
				assert.Equal(t, ForgejoStateError, st[1].State)
			}
		})
	}

	t.Run("verify read", func(t *testing.T) {
		lc, home, _ := newHostLifecycle(t)
		lc.Bundle = testBundle("on: workflow_call\n")
		_, err := lc.Install(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows"))
		require.NoError(t, err)
		home.failOn = "getat"
		_, err = lc.Install(ctx, selectTargets(t, lc, "forgejo:home/org/app"))
		assert.ErrorContains(t, err, "verifying")
	})

	t.Run("consumer workflow write", func(t *testing.T) {
		lc, home, _ := newHostLifecycle(t)
		lc.Bundle = testBundle("on: workflow_call\n")
		_, err := lc.Install(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows"))
		require.NoError(t, err)
		home.failOn = "change"
		_, err = lc.Install(ctx, selectTargets(t, lc, "forgejo:home/org/solo"))
		assert.ErrorContains(t, err, "writing workflow files")
		_, err = lc.Uninstall(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows"))
		assert.ErrorContains(t, err, "still serves")
	})

	t.Run("removal write", func(t *testing.T) {
		lc, home, _ := newHostLifecycle(t)
		lc.Bundle = testBundle("on: workflow_call\n")
		solo := selectTargets(t, lc, "forgejo:home/org/solo")
		_, err := lc.Install(ctx, solo)
		require.NoError(t, err)
		home.failOn = "change"
		_, err = lc.Uninstall(ctx, solo)
		assert.ErrorContains(t, err, "removing workflow files")
	})
}

func TestCombineForgejoStates(t *testing.T) {
	assert.Equal(t, ForgejoStateError, combineForgejoStatus(ForgejoStateUpToDate, ForgejoStateError))
	assert.Equal(t, ForgejoStateUnmanaged, combineForgejoStatus(ForgejoStateDrift, ForgejoStateUnmanaged))
	assert.Equal(t, ForgejoStateUpToDate, combineForgejoStatus(ForgejoStateUpToDate, ForgejoStateUpToDate))
	assert.Equal(t, ForgejoStateNotInstalled, combineForgejoStatus(ForgejoStateNotInstalled, ForgejoStateNotInstalled))
	assert.Equal(t, ForgejoStateDrift, combineForgejoStatus(ForgejoStateNotInstalled, ForgejoStateUpToDate))

	assert.Equal(t, ForgejoStateInstalled, combineForgejoInstall(ForgejoStateInstalled, ForgejoStateInstalled))
	assert.Equal(t, ForgejoStateUpgraded, combineForgejoInstall(ForgejoStateUpToDate, ForgejoStateInstalled))
	assert.Equal(t, ForgejoStateUpToDate, combineForgejoInstall(ForgejoStateUpToDate))

	assert.False(t, forgejoManagedPath("/etc/passwd"))
	assert.False(t, forgejoManagedPath(".fullsend/../x"))
	assert.True(t, forgejoManagedPath(".forgejo/workflows/fullsend-x.yml"))
	assert.Equal(t, "a; b", joinDetails("a", "", "b"))
	assert.True(t, strings.HasPrefix(forgejoConflictError(forgejoShimPath).Error(), forgejoShimPath))
}

func TestForgejoWorkflows_UnmanagedConsumerIndexKeepsHostProtected(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	all := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")
	_, err := lc.Install(ctx, all)
	require.NoError(t, err)

	// The consumer's index loses its ownership marker, so its shim stays.
	idx := "org/app/" + forgejoWorkflowIndexPath
	home.files[idx] = strings.TrimPrefix(home.files[idx], forgejoWorkflowIndexMarker)
	res, err := lc.Uninstall(ctx, all)
	require.Error(t, err)
	assert.Contains(t, res[1].Detail, "workflow files are left unchanged")
	assert.Equal(t, ForgejoStateError, res[0].State, "the host is still called by the kept shim")
	assert.Contains(t, home.files, "org/app/"+forgejoShimPath)
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)
}

func TestForgejoWorkflows_MissingConsumerIndexKeepsHostProtected(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	all := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")
	_, err := lc.Install(ctx, all)
	require.NoError(t, err)

	// The consumer's index is deleted by hand, so its shim is unlisted.
	delete(home.files, "org/app/"+forgejoWorkflowIndexPath)

	// A combined uninstall keeps the host protected.
	res, err := lc.Uninstall(ctx, all)
	require.Error(t, err)
	assert.True(t, res[1].WorkflowsKept)
	assert.Contains(t, res[1].Detail, "left unchanged")
	assert.Equal(t, ForgejoStateError, res[0].State, "the host is still called by the kept shim")
	assert.Contains(t, home.files, "org/app/"+forgejoShimPath)
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)

	// A consumer-only uninstall reports the kept shim, so callers keep the
	// consumer declared and a later host-only uninstall stays refused.
	res, err = lc.Uninstall(ctx, selectTargets(t, lc, "forgejo:home/org/app"))
	require.NoError(t, err)
	assert.True(t, res[0].WorkflowsKept)
	host := selectTargets(t, lc, "forgejo:home/fullsend/workflows")
	_, err = lc.Uninstall(ctx, host)
	assert.ErrorContains(t, err, "still serves")
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)

	// Once the shim is gone, the consumer is not kept.
	delete(home.files, "org/app/"+forgejoShimPath)
	res, err = lc.Uninstall(ctx, selectTargets(t, lc, "forgejo:home/org/app"))
	require.NoError(t, err)
	assert.False(t, res[0].WorkflowsKept)
	assert.Equal(t, ForgejoStateNotInstalled, res[0].State)
}

func TestForgejoWorkflows_MarkedIndexWithoutShimKeepsHostProtected(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	all := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")
	_, err := lc.Install(ctx, all)
	require.NoError(t, err)

	// The consumer's marked index is edited by hand to omit the shim.
	idx := "org/app/" + forgejoWorkflowIndexPath
	home.files[idx] = forgejoWorkflowIndexMarker + "version: 1\nrole: consumer\nbundle: x\nfiles:\n  - " + forgejoWorkflowIndexPath + "\n"

	consumer := selectTargets(t, lc, "forgejo:home/org/app")
	res, err := lc.Uninstall(ctx, consumer)
	require.NoError(t, err)
	assert.True(t, res[0].WorkflowsKept)
	assert.Contains(t, res[0].Detail, "still calls the workflow host")
	assert.Contains(t, home.files, "org/app/"+forgejoShimPath)

	// The kept consumer keeps the host protected.
	_, err = lc.Uninstall(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows"))
	assert.ErrorContains(t, err, "still serves")
}

func TestForgejoWorkflows_MissingIndexReportsLeftoverFilesUnmanaged(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	all := selectTargets(t, lc, "forgejo:home/fullsend/workflows", "forgejo:home/org/app")
	_, err := lc.Install(ctx, all)
	require.NoError(t, err)

	// The host's index is deleted by hand, leaving the dispatch workflow.
	delete(home.files, "fullsend/workflows/"+forgejoWorkflowIndexPath)
	res, err := lc.Uninstall(ctx, selectTargets(t, lc, "forgejo:home/fullsend/workflows"))
	require.Error(t, err, "the declared consumer still depends on the host")
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)

	res, err = lc.Uninstall(ctx, all)
	require.NoError(t, err)
	assert.Equal(t, ForgejoStateUnmanaged, res[0].State)
	assert.Contains(t, res[0].Detail, forgejoDispatchPath)
	assert.Contains(t, res[0].Detail, "left unchanged")
	assert.Contains(t, home.files, "fullsend/workflows/"+forgejoDispatchPath)
}

func TestForgejoWorkflows_IndexCannotClaimManagedConfig(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	solo := selectTargets(t, lc, "forgejo:home/org/solo")
	_, err := lc.Install(ctx, solo)
	require.NoError(t, err)

	cfg := "org/solo/" + forgejoManagedConfigPath
	require.Contains(t, home.files, cfg)
	home.files["org/solo/"+forgejoWorkflowIndexPath] += "  - " + forgejoManagedConfigPath + "\n  - .fullsend/config.base.yaml\n"
	home.files["org/solo/.fullsend/config.base.yaml"] = "keep me"
	_, err = lc.Install(ctx, solo)
	require.NoError(t, err)
	assert.Contains(t, home.files, cfg)
	assert.Contains(t, home.files, "org/solo/.fullsend/config.base.yaml")

	assert.False(t, forgejoManagedPath(forgejoManagedConfigPath))
	assert.True(t, forgejoManagedPath(forgejoWorkflowIndexPath))
}

func TestPlanForgejoWorkflows_UnchangedIndexGuardsWorkflowUpdate(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	solo := selectTargets(t, lc, "forgejo:home/org/solo")
	_, err := lc.Install(ctx, solo)
	require.NoError(t, err)

	// Hand-edit a listed workflow file; the index itself is unchanged.
	home.files["org/solo/"+forgejoShimPath] = "edited\n"
	want, err := desiredForgejoWorkflows(forgejoRoleVendored, *lc.Bundle, ForgejoTarget{}, "")
	require.NoError(t, err)
	plan, err := planForgejoWorkflows(ctx, home, solo[0], want)
	require.NoError(t, err)
	var guard *ForgejoFileChange
	for i, ch := range plan.Changes {
		if ch.Path == forgejoWorkflowIndexPath {
			guard = &plan.Changes[i]
		}
	}
	require.NotNil(t, guard, "the commit is conditional on the index revision")
	assert.Equal(t, ForgejoFileUpdate, guard.Operation)
	assert.Equal(t, memSHA(home.files["org/solo/"+forgejoWorkflowIndexPath]), guard.SHA)

	// An index withdrawn between the read and the commit rejects the commit.
	home.files["org/solo/"+forgejoWorkflowIndexPath] = "withdrawn\n"
	_, err = home.ChangeFiles(ctx, "org", "solo", "m", plan.Changes)
	assert.ErrorContains(t, err, "sha conflict")
	assert.Equal(t, "edited\n", home.files["org/solo/"+forgejoShimPath])
}

// tamperingForgejo edits a bundle file in the commit ChangeFiles records,
// as a concurrent push to a file outside the change would.
type tamperingForgejo struct {
	*memForgejo
	path string
}

func (c *tamperingForgejo) ChangeFiles(ctx context.Context, owner, repo, msg string, changes []ForgejoFileChange) (string, error) {
	sha, err := c.memForgejo.ChangeFiles(ctx, owner, repo, msg, changes)
	if err == nil {
		c.commits[sha][owner+"/"+repo+"/"+c.path] = "tampered\n"
	}
	return sha, err
}

func TestForgejoWorkflows_NewTagVerifiesWholeBundle(t *testing.T) {
	ctx := context.Background()
	lc, home, _ := newHostLifecycle(t)
	other := ".forgejo/workflows/fullsend-helper.yml"
	b := NewForgejoBundle(map[string][]byte{
		forgejoDispatchPath: []byte("on: workflow_call\n"),
		other:               []byte("helper\n"),
	})
	lc.Bundle = &b
	lc.NewClient = func(string, string) (ForgejoFileClient, error) {
		return &tamperingForgejo{memForgejo: home, path: other}, nil
	}
	host := selectTargets(t, lc, "forgejo:home/fullsend/workflows")
	res, err := lc.Install(ctx, host)
	require.Error(t, err)
	assert.Equal(t, ForgejoStateError, res[0].State)
	assert.Contains(t, err.Error(), "differs from the bundle")
}

func TestRenderForgejoShim_RejectsLineBreaks(t *testing.T) {
	_, err := renderForgejoShim("https://h.example/o/r/"+forgejoDispatchPath+"@"+strings.Repeat("a", 40), "src")
	require.NoError(t, err)
	for _, bad := range []string{"https://h.example/a\u0085b", "https://h.example/a b", "https://h.example/a\nb"} {
		_, err := renderForgejoShim(bad, "src")
		assert.Error(t, err, bad)
	}
	_, err = renderForgejoShim("https://h.example/x", "src\nuses: evil")
	assert.Error(t, err)

	_, err = desiredForgejoWorkflows(forgejoRoleConsumer, *testBundle("x"), ForgejoTarget{BaseURL: "https://h.example", Owner: "o", Repo: "r"}, "not-a-sha")
	assert.ErrorContains(t, err, "not a commit SHA")
}

func TestVerifyForgejoBundleAt_RejectsNonCommitRef(t *testing.T) {
	lc, home, _ := newHostLifecycle(t)
	lc.Bundle = testBundle("on: workflow_call\n")
	host := selectTargets(t, lc, "forgejo:home/fullsend/workflows")[0]
	for _, bad := range []string{"", "abc123", "main", strings.Repeat("a", 40) + "\nforged", "\x1b[31m" + strings.Repeat("a", 40)} {
		err := verifyForgejoBundleAt(context.Background(), home, host, bad, *lc.Bundle)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "not a full commit SHA")
		assert.NotContains(t, err.Error(), "\n")
		assert.NotContains(t, err.Error(), "\x1b")
	}
}
