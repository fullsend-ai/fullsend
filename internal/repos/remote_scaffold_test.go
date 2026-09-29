package repos

import (
	"context"
	"slices"
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
		content := "---\n__AGENT_RUNNER_TAGS__\n__CONTROL_RUNNER_TAGS__\nVERSION=\"__FULLSEND_VERSION__\"\n"
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+sp.repoPath+"@"+ref] = []byte(content)
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitLab, []string{"docker"}, []string{"api"}, false)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}
	if len(files) != len(scaffoldGitLabPaths) {
		t.Fatalf("expected %d files, got %d", len(scaffoldGitLabPaths), len(files))
	}

	for _, f := range files {
		s := string(f.Content)
		if strings.Contains(s, "__AGENT_RUNNER_TAGS__") {
			t.Errorf("%s: __AGENT_RUNNER_TAGS__ was not substituted", f.Path)
		}
		if strings.Contains(s, "__CONTROL_RUNNER_TAGS__") {
			t.Errorf("%s: __CONTROL_RUNNER_TAGS__ was not substituted", f.Path)
		}
		if strings.Contains(s, "__RUNNER_TAGS__") {
			t.Errorf("%s: __RUNNER_TAGS__ was not substituted", f.Path)
		}
		if !strings.Contains(s, `["docker"]`) {
			t.Errorf("%s: agent tags not rendered", f.Path)
		}
		if !strings.Contains(s, `["api"]`) {
			t.Errorf("%s: control tags not rendered", f.Path)
		}
		if strings.Contains(s, "__FULLSEND_VERSION__") {
			t.Errorf("%s: __FULLSEND_VERSION__ was not substituted", f.Path)
		}
		if f.Path == fullsendPipelineInclude {
			if !strings.Contains(s, "# fullsend-ref: "+sha) {
				t.Errorf("pipeline wrapper should contain version marker with SHA")
			}
		}
		if f.Path == fullsendDispatchInclude {
			t.Errorf("remote GitLab scaffold must not include obsolete %s", f.Path)
		}
		if f.Path == gitlabInstallCLIScriptPath {
			if !strings.Contains(s, `VERSION="`+ref+`"`) {
				t.Errorf("%s: should contain rendered version %q", f.Path, ref)
			}
		}
	}
}

func TestGitLabScaffoldListsIncludeTrustScript(t *testing.T) {
	files, err := scaffold.CollectGitLabPerRepoInstallFiles(nil, nil, "", "")
	if err != nil {
		t.Fatalf("CollectGitLabPerRepoInstallFiles() error: %v", err)
	}

	installPaths := make(map[string]bool, len(files))
	for _, f := range files {
		installPaths[f.Path] = true
	}
	remoteOut := make(map[string]bool, len(scaffoldGitLabPaths))
	for _, sp := range scaffoldGitLabPaths {
		remoteOut[sp.outPath] = true
	}

	for _, yamlPath := range []string{
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/fullsend-agent.yml",
	} {
		if !installPaths[yamlPath] {
			t.Fatalf("embedded GitLab install files missing %s", yamlPath)
		}
		if !remoteOut[yamlPath] {
			t.Errorf("scaffoldGitLabPaths missing %s", yamlPath)
		}
		if !slices.Contains(gitlabScaffoldPaths, yamlPath) {
			t.Errorf("gitlabScaffoldPaths missing %s", yamlPath)
		}
	}

	if !installPaths[gitlabTrustScriptPath] {
		t.Errorf("embedded GitLab install files missing %s", gitlabTrustScriptPath)
	}
	if !remoteOut[gitlabTrustScriptPath] {
		t.Errorf("scaffoldGitLabPaths missing %s (poll/agent templates source it)", gitlabTrustScriptPath)
	}
	if !slices.Contains(gitlabScaffoldPaths, gitlabTrustScriptPath) {
		t.Errorf("gitlabScaffoldPaths missing %s", gitlabTrustScriptPath)
	}

	for path := range installPaths {
		if !remoteOut[path] {
			t.Errorf("scaffoldGitLabPaths missing embedded install file %s", path)
		}
		if !slices.Contains(gitlabScaffoldPaths, path) {
			t.Errorf("gitlabScaffoldPaths missing embedded install file %s", path)
		}
	}

	if installPaths[fullsendDispatchInclude] {
		t.Error("embedded GitLab install files must not include obsolete fullsend-dispatch.yml")
	}
	if remoteOut[fullsendDispatchInclude] {
		t.Error("remote GitLab scaffold paths must not fetch obsolete fullsend-dispatch.yml")
	}
	if !slices.Contains(gitlabScaffoldPaths, fullsendDispatchInclude) {
		t.Error("gitlabScaffoldPaths must still list fullsend-dispatch.yml so uninstall/converge can remove it from legacy repos")
	}
	if !slices.Contains(gitlabRetiredScaffoldPaths, fullsendDispatchInclude) {
		t.Error("gitlabRetiredScaffoldPaths must list fullsend-dispatch.yml so converge deletes it")
	}
}

func TestFetchRemoteScaffold_GitLab_IncludesTrustScript(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.35.0"
	sha := "deadbeef1234567890abcdef1234567890abcdef"
	trustContent := []byte("#!/usr/bin/env bash\n# CI_SERVER_TLS_CA_FILE\n")

	for _, sp := range scaffoldGitLabPaths {
		content := []byte("---\n__RUNNER_TAGS__\nVERSION=\"__FULLSEND_VERSION__\"\n")
		if sp.outPath == gitlabTrustScriptPath {
			content = trustContent
		}
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+sp.repoPath+"@"+ref] = content
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitLab, []string{"docker"}, nil, false)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	var found bool
	for _, f := range files {
		if f.Path != gitlabTrustScriptPath {
			continue
		}
		found = true
		if string(f.Content) != string(trustContent) {
			t.Errorf("trust script content = %q, want %q", f.Content, trustContent)
		}
		wantMode := scaffold.FileMode(gitlabTrustScriptPath)
		if f.Mode != wantMode {
			t.Errorf("trust script mode = %q, want %q", f.Mode, wantMode)
		}
	}
	if !found {
		t.Fatal("FetchRemoteScaffold GitLab files missing trust-ci-server-ca.sh")
	}
}

func TestFetchRemoteScaffold_GitLab_LegacyRunnerTagsPlaceholder(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.34.0"
	sha := "deadbeef1234567890abcdef1234567890abcdef"

	for _, sp := range scaffoldGitLabPaths {
		content := []byte("---\n__RUNNER_TAGS__\nVERSION=\"__FULLSEND_VERSION__\"\n")
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+sp.repoPath+"@"+ref] = content
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitLab, []string{"docker"}, []string{"api"}, false)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	var sawAgentYAML, sawPollYAML bool
	for _, f := range files {
		s := string(f.Content)
		if strings.Contains(s, "__RUNNER_TAGS__") {
			t.Errorf("%s: legacy __RUNNER_TAGS__ placeholder was not substituted", f.Path)
		}
		switch f.Path {
		case ".gitlab/ci/fullsend-agent.yml":
			sawAgentYAML = true
			if !strings.Contains(s, `["docker"]`) {
				t.Errorf("%s: legacy placeholder should render agent tags, got %q", f.Path, s)
			}
			if strings.Contains(s, `["api"]`) {
				t.Errorf("%s: legacy placeholder should not render control tags, got %q", f.Path, s)
			}
		case ".gitlab/ci/fullsend-poll.yml":
			sawPollYAML = true
			// The legacy placeholder predates the agent/control split, so
			// fetch always stamps it with the agent tag list, even in the
			// poll template.
			if !strings.Contains(s, `["docker"]`) {
				t.Errorf("%s: legacy placeholder should render agent tags, got %q", f.Path, s)
			}
		}
	}
	if !sawAgentYAML {
		t.Fatal("FetchRemoteScaffold GitLab files missing fullsend-agent.yml")
	}
	if !sawPollYAML {
		t.Fatal("FetchRemoteScaffold GitLab files missing fullsend-poll.yml")
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

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil, nil, false)
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

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil, nil, false)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file (shim only, thin callers not found), got %d", len(files))
	}
}

func TestFetchRemoteScaffold_GitHub_VendoredRendersLocalRefs(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.42.0"
	sha := "abcdef1234567890abcdef1234567890abcdef12"

	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: __REUSABLE_DISPATCH__\nref: __FULLSEND_AI_REF__\n")

	for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
		remotePath := "internal/scaffold/fullsend-repo/" + tcPath
		fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+remotePath+"@"+ref] = []byte("---\n# fullsend-stage: prioritize\nname: thin-caller\nuses: __REUSABLE_WORKFLOW__\ninstall_mode: per-org\nrunner_image: __GH_RUNNER__\n")
	}

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil, nil, true)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	// Verify shim uses local ref for reusable-dispatch
	shimContent := string(files[0].Content)
	if !strings.Contains(shimContent, "./.github/workflows/reusable-dispatch.yml") {
		t.Errorf("vendored shim should use local ref, got:\n%s", shimContent)
	}
	if strings.Contains(shimContent, "fullsend-ai/fullsend/.github/workflows/") {
		t.Errorf("vendored shim should not contain cross-repo ref, got:\n%s", shimContent)
	}

	// Verify thin callers use local refs for reusable workflows
	for i, tcPath := range scaffold.PerRepoThinCallerPaths() {
		content := string(files[i+1].Content)
		if !strings.Contains(content, "./.github/workflows/reusable-prioritize.yml") {
			t.Errorf("vendored thin caller %s should use local ref, got:\n%s", tcPath, content)
		}
		if strings.Contains(content, "fullsend-ai/fullsend/.github/workflows/") {
			t.Errorf("vendored thin caller %s should not contain cross-repo ref", tcPath)
		}
	}
}

func TestFetchRemoteScaffold_GitHub_NonVendoredRendersCrossRepoRefs(t *testing.T) {
	fc := forge.NewFakeClient()
	ref := "v0.42.0"
	sha := "abcdef1234567890abcdef1234567890abcdef12"

	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+scaffoldGitHubShimPath+"@"+ref] = []byte("---\nname: fullsend\nuses: __REUSABLE_DISPATCH__\nref: __FULLSEND_AI_REF__\n")

	files, err := FetchRemoteScaffold(context.Background(), fc, ref, sha, ForgeGitHub, nil, nil, false)
	if err != nil {
		t.Fatalf("FetchRemoteScaffold() error: %v", err)
	}

	shimContent := string(files[0].Content)
	if strings.Contains(shimContent, "./.github/workflows/reusable-dispatch.yml") {
		t.Errorf("non-vendored shim should not use local ref, got:\n%s", shimContent)
	}
	if !strings.Contains(shimContent, "fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@") {
		t.Errorf("non-vendored shim should use cross-repo ref, got:\n%s", shimContent)
	}
}

func TestFetchRemoteScaffold_UnsupportedForge(t *testing.T) {
	fc := forge.NewFakeClient()
	_, err := FetchRemoteScaffold(context.Background(), fc, "v1.0.0", "sha", "unsupported", nil, nil, false)
	if err == nil {
		t.Fatal("expected error for unsupported forge")
	}
}
