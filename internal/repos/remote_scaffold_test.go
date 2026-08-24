package repos

import (
	"context"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestFetchRemoteScaffold_GitLab(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.35.0"
	sha := "deadbeef1234567890abcdef1234567890abcdef"

	for _, sp := range scaffoldGitLabPaths {
		content := "---\n__RUNNER_TAGS__\nVERSION=\"__FULLSEND_VERSION__\"\n"
		if sp.outPath == ".gitlab/ci/fullsend-dispatch.yml" {
			content = "---\n# fullsend-stage: dispatch\ntags: __RUNNER_TAGS__\n"
		}
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+sp.repoPath+"@"+ref] = []byte(content)
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitLab, []string{"docker"})
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}
	if len(files) != len(scaffoldGitLabPaths) {
		t.Fatalf("expected %d files, got %d", len(scaffoldGitLabPaths), len(files))
	}

	for _, f := range files {
		s := string(f.Content)
		if strings.Contains(s, "__RUNNER_TAGS__") {
			t.Errorf("%s: __RUNNER_TAGS__ was not substituted", f.Path)
		}
		if strings.Contains(s, "__FULLSEND_VERSION__") {
			t.Errorf("%s: __FULLSEND_VERSION__ was not substituted", f.Path)
		}
		if f.Path == ".gitlab/ci/fullsend-dispatch.yml" {
			if !strings.Contains(s, "# fullsend-ref: "+sha) {
				t.Errorf("dispatch file should contain version marker with SHA")
			}
		}
		if f.Path == ".gitlab/ci/fullsend-agent.yml" || f.Path == ".gitlab/ci/fullsend-poll.yml" {
			if !strings.Contains(s, `VERSION="`+ref+`"`) {
				t.Errorf("%s: should contain rendered version %q", f.Path, ref)
			}
		}
	}
}

func TestFetchRemoteScaffold_GitHub_IncludesThinCallers(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.42.0"
	sha := "abcdef1234567890abcdef1234567890abcdef12"

	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@__FULLSEND_AI_REF__\n")

	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		remotePath := "internal/scaffold/fullsend-repo/" + tcPath
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+remotePath+"@"+ref] = []byte("---\n# fullsend-stage: prioritize\nname: thin-caller\nuses: __REUSABLE_WORKFLOW__\ninstall_mode: per-org\nrunner_image: __GH_RUNNER__\n")
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	expectedCount := 1 + len(scaffold.PerRepoThinCallerPaths())
	if len(files) != expectedCount {
		t.Fatalf("expected %d files (shim + thin callers), got %d", expectedCount, len(files))
	}

	if files[0].Path != ".github/workflows/fullsend.yaml" {
		t.Errorf("first file should be shim, got %s", files[0].Path)
	}

	for i, tcPath := range scaffold.PerRepoThinCallerPaths() {
		if files[i+1].Path != tcPath {
			t.Errorf("expected thin caller %s at index %d, got %s", tcPath, i+1, files[i+1].Path)
		}
		content := string(files[i+1].Content)
		if !strings.Contains(content, "install_mode: per-repo") {
			t.Errorf("thin caller %s should have install_mode: per-repo, got:\n%s", tcPath, content)
		}
		if strings.Contains(content, "install_mode: per-org") {
			t.Errorf("thin caller %s should not have install_mode: per-org", tcPath)
		}
	}
}

func TestFetchRemoteScaffold_GitHub_ThinCallerNotFound(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.42.0"
	sha := "abcdef1234567890abcdef1234567890abcdef12"

	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@__FULLSEND_AI_REF__\n")

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file (shim only, thin callers not found), got %d", len(files))
	}
}

func TestFetchRemoteScaffold_UnsupportedForge(t *testing.T) {
	fc := forge.NewFakeClient()
	_, err := FetchRemoteScaffold(context.Background(), fc, "v1.0.0", "sha", "unsupported", nil)
	if err == nil {
		t.Fatal("expected error for unsupported forge")
	}
}
