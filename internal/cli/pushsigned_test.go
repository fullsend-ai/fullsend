package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestSplitOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		{"org/repo", "org", "repo", true},
		{"my-org/my-repo", "my-org", "my-repo", true},
		{"no-slash", "", "", false},
		{"/repo", "", "", false},
		{"org/", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, repo, ok := splitOwnerRepo(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantOwner, owner)
				assert.Equal(t, tt.wantRepo, repo)
			}
		})
	}
}

func TestParseLocalCommits(t *testing.T) {
	t.Run("single commit", func(t *testing.T) {
		out := []byte("abc123\x00Test User <test@x>\x00fix: do a thing\n\nBody text.\x00")
		commits, err := parseLocalCommits(out)
		require.NoError(t, err)
		require.Len(t, commits, 1)
		assert.Equal(t, "abc123", commits[0].SHA)
		assert.Equal(t, "Test User <test@x>", commits[0].Author)
		assert.Equal(t, "fix: do a thing\n\nBody text.", commits[0].Message)
	})

	t.Run("multiple commits", func(t *testing.T) {
		out := []byte("sha1\x00A <a@x>\x00first commit\x00sha2\x00B <b@x>\x00second commit\n\nwith body\x00")
		commits, err := parseLocalCommits(out)
		require.NoError(t, err)
		require.Len(t, commits, 2)
		assert.Equal(t, "sha1", commits[0].SHA)
		assert.Equal(t, "A <a@x>", commits[0].Author)
		assert.Equal(t, "first commit", commits[0].Message)
		assert.Equal(t, "sha2", commits[1].SHA)
		assert.Equal(t, "B <b@x>", commits[1].Author)
		assert.Equal(t, "second commit\n\nwith body", commits[1].Message)
	})

	t.Run("empty output", func(t *testing.T) {
		commits, err := parseLocalCommits([]byte(""))
		require.NoError(t, err)
		assert.Empty(t, commits)
	})

	t.Run("trailing newline ignored", func(t *testing.T) {
		out := []byte("abc123\x00Test <t@x>\x00message\n\x00\n")
		commits, err := parseLocalCommits(out)
		require.NoError(t, err)
		require.Len(t, commits, 1)
		assert.Equal(t, "message", commits[0].Message)
	})
}

func TestParseDiffTreeNUL(t *testing.T) {
	t.Run("added file", func(t *testing.T) {
		out := []byte(":000000 100644 0000000 abcdef1 A\x00src/new.go\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, localFileChange{Status: "A", Mode: "100644", Path: "src/new.go"}, changes[0])
	})

	t.Run("modified file", func(t *testing.T) {
		out := []byte(":100644 100644 abcdef1 1234567 M\x00src/main.go\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, localFileChange{Status: "M", Mode: "100644", Path: "src/main.go"}, changes[0])
	})

	t.Run("deleted file", func(t *testing.T) {
		out := []byte(":100644 000000 abcdef1 0000000 D\x00old_file.txt\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, localFileChange{Status: "D", Mode: "000000", Path: "old_file.txt"}, changes[0])
	})

	t.Run("executable file added", func(t *testing.T) {
		out := []byte(":000000 100755 0000000 abcdef1 A\x00scripts/run.sh\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, localFileChange{Status: "A", Mode: "100755", Path: "scripts/run.sh"}, changes[0])
	})

	t.Run("rename", func(t *testing.T) {
		out := []byte(":100644 100644 abcdef1 abcdef1 R100\x00old/path.go\x00new/path.go\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 2)
		assert.Equal(t, localFileChange{Status: "D", Path: "old/path.go"}, changes[0])
		assert.Equal(t, localFileChange{Status: "A", Mode: "100644", Path: "new/path.go"}, changes[1])
	})

	t.Run("copy", func(t *testing.T) {
		out := []byte(":100644 100644 abcdef1 abcdef1 C100\x00src/orig.go\x00src/copy.go\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, localFileChange{Status: "A", Mode: "100644", Path: "src/copy.go"}, changes[0])
	})

	t.Run("type change", func(t *testing.T) {
		out := []byte(":100644 120000 abcdef1 1234567 T\x00link.txt\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, localFileChange{Status: "T", Mode: "120000", Path: "link.txt"}, changes[0])
	})

	t.Run("multiple files", func(t *testing.T) {
		out := []byte(":000000 100644 0000000 abcdef1 A\x00new.go\x00" +
			":100644 100644 abcdef1 1234567 M\x00existing.go\x00" +
			":100644 000000 abcdef1 0000000 D\x00removed.go\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 3)
		assert.Equal(t, "A", changes[0].Status)
		assert.Equal(t, "new.go", changes[0].Path)
		assert.Equal(t, "M", changes[1].Status)
		assert.Equal(t, "existing.go", changes[1].Path)
		assert.Equal(t, "D", changes[2].Status)
		assert.Equal(t, "removed.go", changes[2].Path)
	})

	t.Run("empty output", func(t *testing.T) {
		changes, err := parseDiffTreeNUL([]byte(""))
		require.NoError(t, err)
		assert.Empty(t, changes)
	})

	t.Run("path with special characters", func(t *testing.T) {
		out := []byte(":000000 100644 0000000 abcdef1 A\x00path with\ttab.go\x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, "path with\ttab.go", changes[0].Path)
	})

	t.Run("path with trailing space", func(t *testing.T) {
		out := []byte(":000000 100644 0000000 abcdef1 A\x00trailing space \x00")
		changes, err := parseDiffTreeNUL(out)
		require.NoError(t, err)
		require.Len(t, changes, 1)
		assert.Equal(t, "trailing space ", changes[0].Path)
	})

	t.Run("malformed metadata", func(t *testing.T) {
		out := []byte(":100644 100644 abc\x00file.go\x00")
		_, err := parseDiffTreeNUL(out)
		assert.Error(t, err)
	})
}

func TestBuildTreeFiles(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "added.go", "package main\n")
	gitAddCommit(t, repoDir, "initial")
	sha := gitHeadSHA(t, repoDir)

	changes := []localFileChange{
		{Status: "A", Mode: "100644", Path: "added.go"},
	}
	files, err := buildTreeFiles(repoDir, sha, changes)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "added.go", files[0].Path)
	assert.Equal(t, []byte("package main\n"), files[0].Content)
	assert.Equal(t, "100644", files[0].Mode)
	assert.False(t, files[0].Delete)
}

func TestBuildTreeFiles_deletion(t *testing.T) {
	changes := []localFileChange{
		{Status: "D", Path: "removed.go"},
	}
	files, err := buildTreeFiles(t.TempDir(), "abc123", changes)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "removed.go", files[0].Path)
	assert.True(t, files[0].Delete)
}

func TestBuildTreeFiles_defaultMode(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)
	writeFile(t, repoDir, "file.txt", "content\n")
	gitAddCommit(t, repoDir, "add file")
	sha := gitHeadSHA(t, repoDir)

	changes := []localFileChange{
		{Status: "A", Mode: "", Path: "file.txt"},
	}
	files, err := buildTreeFiles(repoDir, sha, changes)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "100644", files[0].Mode, "empty mode defaults to 100644")
}

func TestBuildTreeFiles_submoduleRejected(t *testing.T) {
	changes := []localFileChange{
		{Status: "M", Mode: "160000", Path: "vendor/dep"},
	}
	_, err := buildTreeFiles(t.TempDir(), "abc123456789", changes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "submodule")
}

func TestBuildTreeFiles_executableModeRejected(t *testing.T) {
	changes := []localFileChange{
		{Status: "A", Mode: "100755", Path: "scripts/run.sh"},
	}
	_, err := buildTreeFiles(t.TempDir(), "abc123456789", changes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode 100755")
	assert.Contains(t, err.Error(), "signed-commit")
}

func TestBuildTreeFiles_symlinkModeRejected(t *testing.T) {
	changes := []localFileChange{
		{Status: "A", Mode: "120000", Path: "link.txt"},
	}
	_, err := buildTreeFiles(t.TempDir(), "abc123456789", changes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode 120000")
}

func TestPushSignedCommits(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "base.txt", "base content\n")
	gitAddCommit(t, repoDir, "initial commit")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "feature-branch")
	writeFile(t, repoDir, "new_file.go", "package feature\n")
	gitAddCommit(t, repoDir, "feat: add new feature")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": baseSHA,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "feature-branch", "main", repoDir)
	require.NoError(t, err)

	require.Len(t, fc.CreatedBranchSHAs, 1)
	assert.Equal(t, "org", fc.CreatedBranchSHAs[0].Owner)
	assert.Equal(t, "repo", fc.CreatedBranchSHAs[0].Repo)
	assert.Equal(t, "feature-branch", fc.CreatedBranchSHAs[0].Branch)
	assert.Equal(t, baseSHA, fc.CreatedBranchSHAs[0].SHA)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	assert.Equal(t, "org", fc.CommittedFilesToBranch[0].Owner)
	assert.Equal(t, "repo", fc.CommittedFilesToBranch[0].Repo)
	assert.Equal(t, "feature-branch", fc.CommittedFilesToBranch[0].Branch)
	assert.Contains(t, fc.CommittedFilesToBranch[0].Message, "feat: add new feature")
	assert.Contains(t, fc.CommittedFilesToBranch[0].Message, "Authored-by: Test <test@test.local>")

	require.Len(t, fc.CommittedFilesToBranch[0].Files, 1)
	assert.Equal(t, "new_file.go", fc.CommittedFilesToBranch[0].Files[0].Path)
	assert.Equal(t, []byte("package feature\n"), fc.CommittedFilesToBranch[0].Files[0].Content)
}

func TestPushSignedCommits_multipleCommits(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "base.txt", "base\n")
	gitAddCommit(t, repoDir, "initial")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "multi-branch")
	writeFile(t, repoDir, "file1.go", "package one\n")
	gitAddCommit(t, repoDir, "first feature commit")
	writeFile(t, repoDir, "file2.go", "package two\n")
	gitAddCommit(t, repoDir, "second feature commit")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": baseSHA,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "multi-branch", "main", repoDir)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 2)
	assert.Contains(t, fc.CommittedFilesToBranch[0].Message, "first feature commit")
	assert.Contains(t, fc.CommittedFilesToBranch[1].Message, "second feature commit")
}

func TestPushSignedCommits_branchAlreadyExistsAtBase(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "base.txt", "base\n")
	gitAddCommit(t, repoDir, "initial")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "existing-branch")
	writeFile(t, repoDir, "new.go", "package new\n")
	gitAddCommit(t, repoDir, "new commit")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main":            baseSHA,
			"org/repo/existing-branch": baseSHA,
		},
		ExistingBranches: map[string]bool{
			"org/repo/existing-branch": true,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "existing-branch", "main", repoDir)
	require.NoError(t, err)
	require.Len(t, fc.CommittedFilesToBranch, 1)
}

func TestPushSignedCommits_branchAlreadyExistsDiverged(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "base.txt", "base\n")
	gitAddCommit(t, repoDir, "initial")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "diverged-branch")
	writeFile(t, repoDir, "new.go", "package new\n")
	gitAddCommit(t, repoDir, "new commit")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main":            baseSHA,
			"org/repo/diverged-branch": "divergedSHA6789012345678901234567890",
		},
		ExistingBranches: map[string]bool{
			"org/repo/diverged-branch": true,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "diverged-branch", "main", repoDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists with commits beyond")
	assert.Empty(t, fc.CommittedFilesToBranch)
}

func TestPushSignedCommits_noCommits(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)
	writeFile(t, repoDir, "base.txt", "base\n")
	gitAddCommit(t, repoDir, "initial")
	baseSHA := gitHeadSHA(t, repoDir)

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": baseSHA,
		},
	}

	// HEAD is main, base is main — no commits between them. Should succeed silently.
	err := pushSignedCommits(context.Background(), fc, "org", "repo", "branch", "main", repoDir)
	assert.NoError(t, err)
	assert.Empty(t, fc.CommittedFilesToBranch)
	assert.Empty(t, fc.CreatedBranchSHAs)
}

func TestPushSignedCommits_baseSHAMismatch(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)
	writeFile(t, repoDir, "base.txt", "base\n")
	gitAddCommit(t, repoDir, "initial")

	gitCheckoutBranch(t, repoDir, "feature")
	writeFile(t, repoDir, "new.go", "package new\n")
	gitAddCommit(t, repoDir, "feature commit")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": "remoteSHA_different_from_local_12345678",
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "feature", "main", repoDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match remote base")
}

func TestPushSignedCommits_deletedFile(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "keep.txt", "keep\n")
	writeFile(t, repoDir, "remove.txt", "remove\n")
	gitAddCommit(t, repoDir, "initial")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "del-branch")
	gitRm(t, repoDir, "remove.txt")
	gitCommit(t, repoDir, "remove file")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": baseSHA,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "del-branch", "main", repoDir)
	require.NoError(t, err)

	require.Len(t, fc.CommittedFilesToBranch, 1)
	files := fc.CommittedFilesToBranch[0].Files
	require.Len(t, files, 1)
	assert.Equal(t, "remove.txt", files[0].Path)
	assert.True(t, files[0].Delete)
}

func TestPushSignedCommits_mergeCommit(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "base.txt", "base\n")
	gitAddCommit(t, repoDir, "initial")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "side")
	writeFile(t, repoDir, "side.txt", "side content\n")
	gitAddCommit(t, repoDir, "side commit")

	run(t, repoDir, "git", "checkout", "main")
	writeFile(t, repoDir, "main2.txt", "main content\n")
	gitAddCommit(t, repoDir, "main commit")

	gitCheckoutBranch(t, repoDir, "feature")
	run(t, repoDir, "git", "merge", "--no-edit", "side")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": baseSHA,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "feature", "main", repoDir)
	require.NoError(t, err)

	// With --first-parent and --diff-merges=first-parent, we should get:
	// 1. "main commit" (adds main2.txt)
	// 2. merge commit (adds side.txt from first-parent perspective only, no duplicates)
	require.Len(t, fc.CommittedFilesToBranch, 2, "expect main commit + merge commit")

	// First commit: main2.txt
	assert.Contains(t, fc.CommittedFilesToBranch[0].Message, "main commit")
	require.Len(t, fc.CommittedFilesToBranch[0].Files, 1)
	assert.Equal(t, "main2.txt", fc.CommittedFilesToBranch[0].Files[0].Path)

	// Merge commit: only side.txt (first-parent diff), no duplicates
	mergeFiles := fc.CommittedFilesToBranch[1].Files
	require.Len(t, mergeFiles, 1, "merge commit should only include side.txt from first-parent diff")
	assert.Equal(t, "side.txt", mergeFiles[0].Path)
}

func TestPushSignedCommits_rootCommit(t *testing.T) {
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	writeFile(t, repoDir, "root.txt", "root content\n")
	gitAddCommit(t, repoDir, "root commit")
	baseSHA := gitHeadSHA(t, repoDir)

	gitCheckoutBranch(t, repoDir, "feature")
	writeFile(t, repoDir, "feature.txt", "feature content\n")
	gitAddCommit(t, repoDir, "feature commit")

	fc := &forge.FakeClient{
		BranchRefs: map[string]string{
			"org/repo/main": baseSHA,
		},
	}

	err := pushSignedCommits(context.Background(), fc, "org", "repo", "feature", "main", repoDir)
	require.NoError(t, err)
	require.Len(t, fc.CommittedFilesToBranch, 1)
	assert.Contains(t, fc.CommittedFilesToBranch[0].Message, "feature commit")
}

func TestAppendAuthoredByTrailer(t *testing.T) {
	t.Run("appends trailer", func(t *testing.T) {
		msg := appendAuthoredByTrailer("feat: add feature", "fullsend-code <bot@x>")
		assert.Equal(t, "feat: add feature\n\nAuthored-by: fullsend-code <bot@x>", msg)
	})

	t.Run("appends to message with body", func(t *testing.T) {
		msg := appendAuthoredByTrailer("feat: add feature\n\nBody text.", "fullsend-code <bot@x>")
		assert.Equal(t, "feat: add feature\n\nBody text.\n\nAuthored-by: fullsend-code <bot@x>", msg)
	})

	t.Run("does not duplicate existing trailer", func(t *testing.T) {
		original := "feat: thing\n\nAuthored-by: fullsend-code <bot@x>"
		msg := appendAuthoredByTrailer(original, "fullsend-code <bot@x>")
		assert.Equal(t, original, msg)
	})

	t.Run("empty author is no-op", func(t *testing.T) {
		msg := appendAuthoredByTrailer("feat: thing", "")
		assert.Equal(t, "feat: thing", msg)
	})

	t.Run("strips trailing newlines before appending", func(t *testing.T) {
		msg := appendAuthoredByTrailer("feat: thing\n\n", "fullsend-code <bot@x>")
		assert.Equal(t, "feat: thing\n\nAuthored-by: fullsend-code <bot@x>", msg)
	})
}

func TestNewPushSignedCmd_requiredFlags(t *testing.T) {
	t.Run("missing repo", func(t *testing.T) {
		t.Setenv("GITHUB_REPOSITORY", "")
		t.Setenv("REPO_DIR", "")
		cmd := newPushSignedCmd()
		cmd.SetArgs([]string{"--branch", "b", "--base", "main"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--repo is required")
	})

	t.Run("invalid repo format", func(t *testing.T) {
		cmd := newPushSignedCmd()
		cmd.SetArgs([]string{"--repo", "noslash", "--branch", "b", "--base", "main"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "owner/repo format")
	})

	t.Run("missing branch", func(t *testing.T) {
		cmd := newPushSignedCmd()
		cmd.SetArgs([]string{"--repo", "org/repo", "--base", "main"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--branch is required")
	})

	t.Run("missing base", func(t *testing.T) {
		cmd := newPushSignedCmd()
		cmd.SetArgs([]string{"--repo", "org/repo", "--branch", "feature"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--base is required")
	})

	t.Run("branch equals base", func(t *testing.T) {
		cmd := newPushSignedCmd()
		cmd.SetArgs([]string{"--repo", "org/repo", "--branch", "main", "--base", "main"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--branch and --base must differ")
	})

	t.Run("base starting with dash rejected", func(t *testing.T) {
		cmd := newPushSignedCmd()
		cmd.SetArgs([]string{"--repo", "org/repo", "--branch", "feature", "--base", "--evil"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a branch name, not a flag")
	})
}

// Test helpers for local git operations.

func gitInit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@test.local")
	run(t, dir, "git", "config", "user.name", "Test")
}

func gitAddCommit(t *testing.T, dir, message string) {
	t.Helper()
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "commit", "-m", message)
}

func gitCommit(t *testing.T, dir, message string) {
	t.Helper()
	run(t, dir, "git", "commit", "-m", message)
}

func gitCheckoutBranch(t *testing.T, dir, branch string) {
	t.Helper()
	run(t, dir, "git", "checkout", "-b", branch)
}

func gitHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out[:len(out)-1])
}

func gitRm(t *testing.T, dir, file string) {
	t.Helper()
	run(t, dir, "git", "rm", file)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.local",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.local",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "command %s %v failed: %s", name, args, string(out))
}
