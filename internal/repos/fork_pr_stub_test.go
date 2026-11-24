package repos

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// The GitHub fork pull request stub (.github/workflows/fullsend-fork-pr.yaml)
// is managed like the shim: shipped by install, fetched at a pinned ref,
// probed once the shim triggers on it, delivered by content drift, reported
// as an orphan when the selected release lacks it, and removed by uninstall.

const oldShimWithoutStub = "---\nname: fullsend\non:\n  pull_request:\n    types: [opened]\njobs:\n  dispatch:\n    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1.0.0\n"

func TestFetchRemoteScaffold_GitHub_ForkPRStub(t *testing.T) {
	const ref = "v0.42.0"
	const sha = "abcdef1234567890abcdef1234567890abcdef12"
	newFake := func() *forge.FakeClient {
		fc := forge.NewFakeClient()
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: __REUSABLE_DISPATCH__\n")
		return fc
	}

	t.Run("present at the ref", func(t *testing.T) {
		fc := newFake()
		raw, err := scaffold.FullsendRepoFile(scaffold.ForkPRStubTemplate)
		if err != nil {
			t.Fatal(err)
		}
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubForkPRStubPath+"@"+ref] = raw
		files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil, nil, false)
		if err != nil {
			t.Fatalf("FetchRemoteScaffold() error: %v", err)
		}
		i := slices.IndexFunc(files, func(f scaffold.InstallFile) bool { return f.Path == scaffold.ForkPRStubPath })
		if i < 0 {
			t.Fatalf("stub not fetched; files: %v", files)
		}
		if bytes.Contains(files[i].Content, []byte("__GH_RUNNER__")) || !bytes.HasPrefix(files[i].Content, []byte("# This file is managed by fullsend.")) {
			t.Errorf("stub not rendered with the managed header:\n%s", files[i].Content)
		}
	})

	t.Run("absent at an older ref", func(t *testing.T) {
		files, err := FetchRemoteScaffold(context.Background(), newFake(), ref, sha, ForgeGitHub, nil, nil, false)
		if err != nil {
			t.Fatalf("FetchRemoteScaffold() error: %v", err)
		}
		for _, f := range files {
			if f.Path == scaffold.ForkPRStubPath {
				t.Fatal("a release without the stub must not get one")
			}
		}
	})
}

func TestProbeComponents_ForkPRStubFollowsShim(t *testing.T) {
	newShim, err := scaffold.PerRepoShimTemplate()
	if err != nil {
		t.Fatal(err)
	}
	probe := func(shim []byte, withStub bool) map[string]ComponentStatus {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = shim
		if withStub {
			fc.FileContents["acme/api/"+scaffold.ForkPRStubPath] = []byte("name: fullsend-fork-pr\n")
		}
		components, err := ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
		if err != nil {
			t.Fatalf("ProbeComponents() error: %v", err)
		}
		out := map[string]ComponentStatus{}
		for _, c := range components {
			out[c.Name] = c
		}
		return out
	}
	name := "scaffold:" + scaffold.ForkPRStubPath
	if _, ok := probe([]byte(oldShimWithoutStub), false)[name]; ok {
		t.Error("a shim from before the stub must not require it")
	}
	if c, ok := probe(newShim, false)[name]; !ok || c.Present || c.Match {
		t.Errorf("a shim triggering on the stub must report it missing, got %+v (probed %v)", c, ok)
	}
	if c := probe(newShim, true)[name]; !c.Present || !c.Match {
		t.Errorf("an installed stub must match, got %+v", c)
	}
}

func TestCheckFileContentDrift_DeliversMissingForkPRStub(t *testing.T) {
	expected := []forge.TreeFile{{Path: scaffold.ForkPRStubPath, Content: []byte("stub")}}
	for _, tc := range []struct {
		forge string
		fc    ForgeConfig
		want  int
	}{
		{ForgeGitHub, GitHubForgeConfig(), 1},
		{ForgeGitLab, GitLabForgeConfig(), 0},
	} {
		fc := forge.NewFakeClient()
		drifted, err := CheckFileContentDrift(context.Background(), fc, "owner", "repo", tc.fc, tc.forge, expected)
		if err != nil {
			t.Fatalf("%s: %v", tc.forge, err)
		}
		if len(drifted) != tc.want {
			t.Errorf("%s: drifted = %v, want %d", tc.forge, drifted, tc.want)
		}
	}
}

func TestCheckOrphanFiles_ForkPRStubAbsentFromRelease(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["owner/repo/.github/workflows/fullsend.yaml"] = []byte("content")
	fc.FileContents["owner/repo/"+scaffold.ForkPRStubPath] = []byte("stub")
	expected := []forge.TreeFile{{Path: ".github/workflows/fullsend.yaml", Content: []byte("content")}}
	orphans, err := CheckOrphanFiles(context.Background(), fc, "owner", "repo", GitHubForgeConfig(), ForgeGitHub, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Path != scaffold.ForkPRStubPath {
		t.Errorf("orphans = %v, want the stub", orphans)
	}
}

func TestUninstall_RemovesForkPRStub(t *testing.T) {
	client := newInstalledFakeClient("acme/api")
	client.FileContents["acme/api/"+scaffold.ForkPRStubPath] = []byte("name: fullsend-fork-pr\n")
	_, err := Uninstall(context.Background(), UninstallConfig{
		Manifest: testManifest("acme/api"), Repos: []string{"acme/api"}, Direct: true, MaxConcurrency: 1,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if !slices.Contains(collectDeletedPaths(client), scaffold.ForkPRStubPath) {
		t.Errorf("uninstall did not delete %s", scaffold.ForkPRStubPath)
	}
}

// An install from a release before the stub upgrades in one converge pass:
// the shim is rewritten and the stub delivered together; the dry run plans
// the same files, and afterwards status and converge both report current.
func TestConverge_GitHub_UpgradeDeliversForkPRStubInOnePass(t *testing.T) {
	fc, m := installedWithDeclaredRegion(t, "us-central1")
	shimPath := "acme/api/.github/workflows/fullsend.yaml"
	fc.FileContents[shimPath] = []byte(strings.Replace(string(fc.FileContents[shimPath]),
		"  workflow_run:\n    workflows: [fullsend-fork-pr]\n    types: [completed]\n", "", 1))
	if scaffold.ShimReferencesForkPRStub(fc.FileContents[shimPath]) {
		t.Fatal("test setup: the old shim must not reference the stub")
	}
	delete(fc.FileContents, "acme/api/"+scaffold.ForkPRStubPath)

	convergeOnce := func(dryRun bool) ([]string, *ConvergeBatchResult) {
		cfg := withoutInferenceInputs(convergeCfgWithDefaults(m))
		cfg.DryRun = dryRun
		sc := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		if err != nil {
			t.Fatalf("Converge(dryRun=%v) error: %v", dryRun, err)
		}
		if len(result.Failed()) != 0 {
			t.Fatalf("Converge(dryRun=%v) failed: %v", dryRun, result.Failed()[0].Error)
		}
		var paths []string
		for _, f := range sc.files {
			paths = append(paths, f.Path)
			if !dryRun {
				fc.FileContents["acme/api/"+f.Path] = f.Content
			}
		}
		var planned []string
		for _, a := range result.Results[0].Actions {
			if a.Action == "update" || a.Action == "add" {
				planned = append(planned, a.Component)
			}
		}
		if dryRun {
			return planned, result
		}
		return paths, result
	}

	planned, _ := convergeOnce(true)
	if !slices.Contains(planned, scaffold.ForkPRStubPath) {
		t.Errorf("dry run does not plan the stub; planned: %v", planned)
	}
	committed, _ := convergeOnce(false)
	for _, p := range []string{".github/workflows/fullsend.yaml", scaffold.ForkPRStubPath} {
		if !slices.Contains(committed, p) {
			t.Errorf("first converge did not deliver %s; committed: %v", p, committed)
		}
	}

	_, again := convergeOnce(false)
	if len(again.AlreadyCurrent()) != 1 {
		t.Errorf("second converge is not current: %+v", again.Results[0].Actions)
	}
	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Repos[0].Drifts) != 0 {
		t.Errorf("status after converge reports drift: %+v", status.Repos[0].Drifts)
	}
}

func TestProbeComponents_ForkPRStubReadError(t *testing.T) {
	shim, err := scaffold.PerRepoShimTemplate()
	if err != nil {
		t.Fatal(err)
	}
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = shim
	fc.GetFileContentErrors = map[string]error{"acme/api/" + scaffold.ForkPRStubPath: errors.New("boom")}
	_, err = ProbeComponents(context.Background(), fc, "acme", "api", ForgeGitHub, defaultForgeConfig, nil)
	if err == nil || !strings.Contains(err.Error(), "checking fork pull request stub") {
		t.Fatalf("ProbeComponents() error = %v, want the stub read error", err)
	}
}
