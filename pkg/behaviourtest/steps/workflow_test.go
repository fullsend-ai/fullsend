package steps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	scmdriver "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// treeSCM is a leased repository held in memory: it records every file
// committed, by path, reads them back, and deletes them. failAfter > 0
// makes the commit after that many succeeded fail.
type treeSCM struct {
	fakeCleanupSCM
	files     map[string][]byte
	commits   int
	failAfter int
}

func (s *treeSCM) CommitFile(_ context.Context, _, _, path, _ string, content []byte) error {
	if s.failAfter > 0 && s.commits >= s.failAfter {
		return errors.New("commit refused")
	}
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	s.files[path] = content
	s.commits++
	return nil
}

func (s *treeSCM) GetFileContent(_ context.Context, _, _, path string) ([]byte, error) {
	if c, ok := s.files[path]; ok {
		return c, nil
	}
	return nil, forge.ErrNotFound
}

func (s *treeSCM) DeleteFile(_ context.Context, _, _, path, _ string) error {
	if _, ok := s.files[path]; !ok {
		return forge.ErrNotFound
	}
	delete(s.files, path)
	s.deletedFiles = append(s.deletedFiles, path)
	return nil
}

func TestWorkflowDefinitionRepoDir(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		dest, want, err string
	}{
		{dest: "pipelines/sample-pipeline", want: "pipelines/sample-pipeline"},
		{dest: "pipelines/./sample/", want: "pipelines/sample"},
		{dest: ".", err: "repository root"},
		{dest: "", err: "relative path"},
		{dest: "/abs", err: "relative path"},
		{dest: `a\b`, err: "relative path"},
		{dest: "../out", err: "leaves the repository"},
		{dest: ".fullsend/pipeline", err: "inside .fullsend/"},
		{dest: ".fullsend", err: "inside .fullsend/"},
	} {
		got, err := workflowDefinitionRepoDir(tc.dest)
		if tc.err != "" {
			require.ErrorContains(t, err, tc.err, tc.dest)
			continue
		}
		require.NoError(t, err, tc.dest)
		assert.Equal(t, tc.want, got)
	}
}

func TestGivenWorkflowDefinitionCommitted(t *testing.T) {
	t.Parallel()
	scm := &treeSCM{}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scm, FixturesRoot: "e2e/behaviour"}

	require.NoError(t, givenWorkflowDefinitionCommitted(w, "sample-pipeline", "pipelines/sample-pipeline"))
	paths := make([]string, 0, len(scm.files))
	for p := range scm.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	assert.Equal(t, []string{
		"pipelines/sample-pipeline/.claude-plugin/plugin.json",
		"pipelines/sample-pipeline/scripts/checklist.sh",
		"pipelines/sample-pipeline/skills/read-issue/SKILL.md",
		"pipelines/sample-pipeline/workflows/triage-fanout.js",
	}, paths)

	require.ErrorContains(t, givenWorkflowDefinitionCommitted(w, "../workflow", "x"), "directory name")
	require.ErrorContains(t, givenWorkflowDefinitionCommitted(w, "sample-pipeline", "."), "repository root")
	require.ErrorContains(t, givenWorkflowDefinitionCommitted(&world.World{}, "sample-pipeline", "x"), "no repo configured")
	require.Error(t, givenWorkflowDefinitionCommitted(w, "missing", "x"))
}

var sampleDefinitionFiles = []string{
	"pipelines/sample-pipeline/.claude-plugin/plugin.json",
	"pipelines/sample-pipeline/scripts/checklist.sh",
	"pipelines/sample-pipeline/skills/read-issue/SKILL.md",
	"pipelines/sample-pipeline/workflows/triage-fanout.js",
}

// A slot whose repository already has the destination, or any file the
// definition would write, is refused before anything is committed, so
// cleanup never deletes a file the scenario did not write.
func TestGivenWorkflowDefinitionCommitted_ExistingDestinationRefused(t *testing.T) {
	t.Parallel()
	for _, existing := range []string{"pipelines/sample-pipeline", "pipelines/sample-pipeline/workflows/triage-fanout.js"} {
		scm := &treeSCM{files: map[string][]byte{existing: []byte("theirs")}}
		w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scm, FixturesRoot: "e2e/behaviour"}
		err := givenWorkflowDefinitionCommitted(w, "sample-pipeline", "pipelines/sample-pipeline")
		require.ErrorContains(t, err, existing+" already exists in the leased repository")
		assert.Equal(t, 0, scm.commits, existing)
		assert.Empty(t, w.WorkflowDefinitionFiles, existing)
	}

	scm := &treeSCM{}
	scm.getFileErr = errors.New("decode file content: json: cannot unmarshal array")
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: &unreadableSCM{scm}, FixturesRoot: "e2e/behaviour"}
	err := givenWorkflowDefinitionCommitted(w, "sample-pipeline", "pipelines/sample-pipeline")
	require.ErrorContains(t, err, "cannot tell whether pipelines/sample-pipeline exists")
	assert.Equal(t, 0, scm.commits)
}

// noDeleteSCM exposes only the scm.Driver methods of the wrapped driver,
// like an external driver written before scm.FileDeleter existed.
type noDeleteSCM struct{ scmdriver.Driver }

// A driver that cannot delete files is refused before anything is
// committed or recorded, since cleanup could not remove the definition.
func TestGivenWorkflowDefinitionCommitted_DriverWithoutFileDeleter(t *testing.T) {
	t.Parallel()
	inner := &treeSCM{}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: noDeleteSCM{inner}, FixturesRoot: "e2e/behaviour"}
	err := givenWorkflowDefinitionCommitted(w, "sample-pipeline", "pipelines/sample-pipeline")
	require.EqualError(t, err, "this SCM driver cannot delete files, so the definition could not be cleaned up; implement scm.FileDeleter")
	assert.Equal(t, 0, inner.commits)
	assert.Empty(t, w.WorkflowDefinitionFiles)

	// Cleanup on such a driver logs the files it leaves and clears the list.
	var logged []string
	w.WorkflowDefinitionFiles = []string{"pipelines/x/a"}
	w.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	RemoveWorkflowDefinitionFiles(w)
	assert.Empty(t, w.WorkflowDefinitionFiles)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "[pipelines/x/a] left in place")
	assert.Contains(t, logged[0], "implement scm.FileDeleter")
}

// unreadableSCM fails every read with the embedded fake's getFileErr.
type unreadableSCM struct{ *treeSCM }

func (s *unreadableSCM) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, s.getFileErr
}

// CleanupScenario deletes every definition file the step committed.
func TestCleanupScenario_RemovesWorkflowDefinition(t *testing.T) {
	t.Parallel()
	scm := &treeSCM{}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scm, FixturesRoot: "e2e/behaviour"}
	require.NoError(t, givenWorkflowDefinitionCommitted(w, "sample-pipeline", "pipelines/sample-pipeline"))
	assert.ElementsMatch(t, sampleDefinitionFiles, w.WorkflowDefinitionFiles)

	CleanupScenario(w)
	assert.Empty(t, scm.files, "every committed definition file is deleted")
	assert.ElementsMatch(t, sampleDefinitionFiles, scm.deletedFiles)
	assert.Empty(t, w.WorkflowDefinitionFiles)
}

// A commit that fails partway leaves the files written so far, and the
// one that failed, recorded; cleanup deletes the written ones and skips
// the missing one.
func TestCleanupScenario_RemovesPartialWorkflowDefinition(t *testing.T) {
	t.Parallel()
	scm := &treeSCM{failAfter: 2}
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scm, FixturesRoot: "e2e/behaviour"}
	err := givenWorkflowDefinitionCommitted(w, "sample-pipeline", "pipelines/sample-pipeline")
	require.ErrorContains(t, err, "commit refused")
	require.Len(t, scm.files, 2)
	require.Len(t, w.WorkflowDefinitionFiles, 3, "the failed file is recorded before its write")

	CleanupScenario(w)
	assert.Empty(t, scm.files)
	assert.Len(t, scm.deletedFiles, 2)
}

// TestSampleWorkflowFixtureResolves runs the fixture through the runner's
// own resolution, as a path workflow.source in a git checkout laid out
// like the leased repo, so a fixture the runner would refuse fails here
// rather than only on a live run.
func TestSampleWorkflowFixtureResolves(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root, err := moduleRootDir()
	require.NoError(t, err)
	src := filepath.Join(root, "e2e", "behaviour", "fixtures", "workflow", "sample-pipeline")

	repo := t.TempDir()
	def := filepath.Join(repo, "pipelines", "sample-pipeline")
	require.NoError(t, filepath.WalkDir(src, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dest := filepath.Join(def, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	}))
	fullsendDir := filepath.Join(repo, ".fullsend")
	require.NoError(t, os.MkdirAll(filepath.Join(fullsendDir, "harness"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte("version: \"1\"\n"), 0o644))
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	}

	h := &harness.Harness{Workflow: &harness.WorkflowSpec{Source: "pipelines/sample-pipeline", Name: "triage-fanout", Args: "issue 1"}}
	require.NoError(t, harness.ValidateWorkflowSpec(h.Workflow), "the feature's workflow: block must pass harness validation")
	loc := resolve.WorkflowLocation{HarnessDir: filepath.Join(fullsendDir, "harness"), FullsendDir: fullsendDir}
	rw, dep, err := resolve.ResolveWorkflowDefinition(context.Background(), h, loc, resolve.ResolveOpts{WorkspaceRoot: fullsendDir})
	require.NoError(t, err)
	assert.Nil(t, dep)
	assert.Equal(t, pluginformat.KindClaude, rw.Kind)
	assert.Equal(t, "/sample-pipeline:triage-fanout issue 1", rw.Command())
	assert.Regexp(t, treeHashPattern, rw.TreeHash)
}

func TestAssertRunStartedWorkflow(t *testing.T) {
	t.Parallel()
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	root := t.TempDir()
	writeArtifact(t, root, "agent-workflow-triage-1/metrics.json",
		`{"runtime":"dummy","workflow":{"source":"pipelines/sample-pipeline","pin_sha256":"`+hash+`","kind":"claude-plugin","command":"/sample-pipeline:triage-fanout issue 1"}}`)
	w := &world.World{ArtifactDir: root}

	require.NoError(t, assertRunStartedWorkflow(w, "/sample-pipeline:triage-fanout issue 1", "pipelines/sample-pipeline"))
	require.ErrorContains(t, assertRunStartedWorkflow(w, "/sample-pipeline:other", "pipelines/sample-pipeline"), "workflow.command")
	require.ErrorContains(t, assertRunStartedWorkflow(w, "/sample-pipeline:triage-fanout issue 1", "."), "workflow.source")

	short := t.TempDir()
	writeArtifact(t, short, "metrics.json", `{"workflow":{"source":"s","pin_sha256":"abc","kind":"claude-plugin","command":"/d:n"}}`)
	require.ErrorContains(t, assertRunStartedWorkflow(&world.World{ArtifactDir: short}, "/d:n", "s"), "pin_sha256")

	pi := t.TempDir()
	writeArtifact(t, pi, "metrics.json", `{"workflow":{"source":"s","pin_sha256":"`+hash+`","kind":"pi-extension"}}`)
	require.ErrorContains(t, assertRunStartedWorkflow(&world.World{ArtifactDir: pi}, "/d:n", "s"), `workflow.kind = "pi-extension"`)

	none := t.TempDir()
	writeArtifact(t, none, "metrics.json", `{"runtime":"dummy"}`)
	require.ErrorContains(t, assertRunStartedWorkflow(&world.World{ArtifactDir: none}, "/d:n", "s"), "no workflow object")
}

// A delete that fails for another reason than not found is logged, and
// the recorded list is cleared either way.
func TestRemoveWorkflowDefinitionFiles_DeleteError(t *testing.T) {
	t.Parallel()
	scm := &fakeCleanupSCM{deleteFileErr: errors.New("delete refused")}
	var logged []string
	w := &world.World{Org: "org", RepoOwner: "org", RepoName: "repo", SCM: scm,
		WorkflowDefinitionFiles: []string{"pipelines/x/a", "pipelines/x/b"},
		Logf:                    func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }}
	RemoveWorkflowDefinitionFiles(w)
	assert.Equal(t, []string{"pipelines/x/a", "pipelines/x/b"}, scm.deletedFiles)
	assert.Empty(t, w.WorkflowDefinitionFiles)
	require.Len(t, logged, 2)
	assert.Contains(t, logged[0], "delete workflow definition file pipelines/x/a: delete refused")
}

func TestAssertRunStartedWorkflow_NoMetrics(t *testing.T) {
	t.Parallel()
	require.Error(t, assertRunStartedWorkflow(&world.World{ArtifactDir: t.TempDir()}, "/a:b", "x"))
}
