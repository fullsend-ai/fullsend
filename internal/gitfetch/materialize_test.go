package gitfetch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// treeSpec describes a test tree: files maps slash paths to content and
// links maps slash paths to link text.
type treeSpec struct {
	files map[string]string
	links map[string]string
}

func writeTree(t *testing.T, dir string, spec treeSpec) {
	t.Helper()
	for p, content := range spec.files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, target := range spec.links {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
}

// createLinkRepo commits spec (symlinks included) to a new repository and
// returns its file:// URL and commit sha.
func createLinkRepo(t *testing.T, spec treeSpec) (string, string) {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, spec)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), testGitEnv()...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	run("config", "commit.gpgsign", "false")
	run("add", "-f", ".")
	run("commit", "-m", "initial")
	return "file://" + dir, run("rev-parse", "HEAD")
}

func fetchMaterialized(t *testing.T, spec treeSpec, subpath string) (map[string][]byte, error) {
	t.Helper()
	url, sha := createLinkRepo(t, spec)
	return FetchTreeWithOptions(context.Background(), url, subpath, sha, "", Options{MaterializeSymlinks: true})
}

func TestFetchTreeWithOptions_Materialize(t *testing.T) {
	base := map[string]string{
		".claude-plugin/plugin.json": `{"name":"sample"}`,
		"scripts/run.sh":             "#!/bin/sh\necho run\n",
		"scripts/lib/util.sh":        "util\n",
	}
	tests := []struct {
		name    string
		spec    treeSpec
		subpath string
		want    map[string]string
		wantErr string
	}{
		{
			name: "root with file and directory links",
			spec: treeSpec{files: base, links: map[string]string{
				"skills/x/run.sh": "../../scripts/run.sh",
				"skills/y/lib":    "../../scripts/lib",
			}},
			want: map[string]string{
				".claude-plugin/plugin.json": `{"name":"sample"}`,
				"scripts/run.sh":             "#!/bin/sh\necho run\n",
				"scripts/lib/util.sh":        "util\n",
				"skills/x/run.sh":            "#!/bin/sh\necho run\n",
				"skills/y/lib/util.sh":       "util\n",
			},
		},
		{
			name:    "subpath with an inner link",
			spec:    treeSpec{files: map[string]string{"pipelines/a/real.txt": "a", "other.txt": "o"}, links: map[string]string{"pipelines/a/alias.txt": "real.txt"}},
			subpath: "pipelines/a",
			want:    map[string]string{"real.txt": "a", "alias.txt": "a"},
		},
		{
			name:    "subpath link to a sibling directory leaves the fetched tree",
			spec:    treeSpec{files: map[string]string{"pipelines/a/real.txt": "a", "shared/s.txt": "s"}, links: map[string]string{"pipelines/a/s.txt": "../../shared/s.txt"}},
			subpath: "pipelines/a",
			wantErr: "s.txt is a symlink to ../../shared/s.txt, which leaves the fetched tree; point the link inside the repository or commit the file",
		},
		{
			name:    "subpath link to a repository-root file leaves the fetched tree",
			spec:    treeSpec{files: map[string]string{"pipelines/a/real.txt": "a", "root.txt": "r"}, links: map[string]string{"pipelines/a/r.txt": "../../root.txt"}},
			subpath: "pipelines/a",
			wantErr: "r.txt is a symlink to ../../root.txt, which leaves the fetched tree",
		},
		{
			name:    "escaping link",
			spec:    treeSpec{files: base, links: map[string]string{"skills/x/run.sh": "../../../etc/passwd"}},
			wantErr: "skills/x/run.sh is a symlink to ../../../etc/passwd, which leaves the fetched tree",
		},
		{
			name:    "dangling link",
			spec:    treeSpec{files: base, links: map[string]string{"skills/x/run.sh": "missing.sh"}},
			wantErr: "skills/x/run.sh is a symlink to missing.sh, which does not exist",
		},
		{
			name:    "directory cycle",
			spec:    treeSpec{files: base, links: map[string]string{"scripts/lib/up": ".."}},
			wantErr: "scripts/lib/up is a symlink to .., which is a directory that contains the link",
		},
		{
			name:    "link loop",
			spec:    treeSpec{files: base, links: map[string]string{"a": "b", "b": "a"}},
			wantErr: "a is a symlink to b, which cannot be resolved because the links form a loop",
		},
		{
			name: "cap exceeded through materialization",
			spec: func() treeSpec {
				files := map[string]string{}
				for i := 0; i < MaxFiles/2+1; i++ {
					files[fmt.Sprintf("data/f%04d.txt", i)] = "x"
				}
				return treeSpec{files: files, links: map[string]string{"copy": "data"}}
			}(),
			wantErr: fmt.Sprintf("exceeded maximum of %d files", MaxFiles),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := fetchMaterialized(t, tt.spec, tt.subpath)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got files %v", tt.wantErr, sortedKeys(files))
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != len(tt.want) {
				t.Fatalf("got files %v, want %v", sortedKeys(files), tt.want)
			}
			for p, want := range tt.want {
				if string(files[p]) != want {
					t.Errorf("%s = %q, want %q", p, files[p], want)
				}
			}
		})
	}
}

func TestFetchTreeWithOptions_SourceRootLinks(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, treeSpec{files: map[string]string{"plugin.json": "{}", "secret.txt": "s"}})
	spec := treeSpec{
		files: map[string]string{"real/def/plugin.json": "{}", "real/def/workflows/probe.js": "x"},
		links: map[string]string{
			"rel":   "../../../../../../../../../../../../.." + outside,
			"abs":   outside,
			"inner": "real/def",
			"mid":   "real",
		},
	}
	url, sha := createLinkRepo(t, spec)
	for _, tc := range []struct{ subpath, want string }{
		{"rel", `"rel" is a symlink in the repository; point source at the real directory`},
		{"abs", `"abs" is a symlink in the repository; point source at the real directory`},
		{"inner", `"inner" is a symlink in the repository; point source at the real directory`},
		{"mid/def", `"mid" is a symlink in the repository; point source at the real directory`},
		{".git", `".git" is inside .git/`},
		{"missing", `path "missing" not found in repository`},
		{"real/def/plugin.json", `"real/def/plugin.json" is not a directory in the repository`},
	} {
		t.Run(tc.subpath, func(t *testing.T) {
			files, err := FetchTreeWithOptions(context.Background(), url, tc.subpath, sha, "", Options{MaterializeSymlinks: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("subpath %s: got files %v, err %v; want %q", tc.subpath, sortedKeys(files), err, tc.want)
			}
		})
	}
	files, err := FetchTreeWithOptions(context.Background(), url, "real/def", sha, "", Options{MaterializeSymlinks: true})
	if err != nil || strings.Join(sortedKeys(files), ",") != "plugin.json,workflows/probe.js" {
		t.Fatalf("real path: files %v, err %v", sortedKeys(files), err)
	}
}

func TestFetchTreeWithOptions_LinkErrorIsTyped(t *testing.T) {
	_, err := fetchMaterialized(t, treeSpec{files: map[string]string{"a.txt": "a"}, links: map[string]string{"b.txt": "nope.txt"}}, "")
	var le *LinkError
	if !errors.As(err, &le) {
		t.Fatalf("expected a *LinkError, got %v", err)
	}
	if le.Path != "b.txt" || le.Target != "nope.txt" {
		t.Fatalf("unexpected LinkError %+v", le)
	}
}

func TestFetchTree_RefusesFileLinkInTree(t *testing.T) {
	url, sha := createLinkRepo(t, treeSpec{files: map[string]string{"a.txt": "a"}, links: map[string]string{"b.txt": "a.txt"}})
	_, err := FetchTree(context.Background(), url, "", sha, "")
	if err == nil || !strings.Contains(err.Error(), "symlinks are not supported") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

// walkLocal materializes a plain directory with the local skip list, for
// the walker rules that do not depend on git.
func walkLocal(dir string) (map[string][]byte, error) {
	return materializeTree(dir, "the source directory", []string{".git", localCacheDirName})
}

// gitRepo initializes dir as a git repository and stages paths (all of
// dir when none are given) without committing: ReadLocalTree reads the
// index, so staged is enough.
func gitRepo(t *testing.T, dir string, paths ...string) {
	t.Helper()
	gitIn(t, dir, "init", "-q", "-b", "main")
	if len(paths) == 0 {
		paths = []string{"."}
	}
	gitIn(t, dir, append([]string{"add", "-f", "--"}, paths...)...)
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), testGitEnv()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func readLocal(t *testing.T, root, source string, exclude ...string) (map[string][]byte, error) {
	t.Helper()
	return ReadLocalTree(context.Background(), root, source, exclude)
}

func TestReadLocalTree(t *testing.T) {
	t.Run("matches the fetched tree", func(t *testing.T) {
		spec := treeSpec{
			files: map[string]string{"plugin.json": "{}", "scripts/run.sh": "run"},
			links: map[string]string{"skills/x/run.sh": "../../scripts/run.sh"},
		}
		url, sha := createLinkRepo(t, spec)
		dir := strings.TrimPrefix(url, "file://")
		local, err := readLocal(t, dir, ".")
		if err != nil {
			t.Fatal(err)
		}
		remote, err := FetchTreeWithOptions(context.Background(), url, "", sha, "", Options{MaterializeSymlinks: true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(sortedKeys(local), ",") != strings.Join(sortedKeys(remote), ",") {
			t.Fatalf("local %v != remote %v", sortedKeys(local), sortedKeys(remote))
		}
		for p := range remote {
			if string(local[p]) != string(remote[p]) {
				t.Errorf("%s differs", p)
			}
		}
	})
	t.Run("only tracked files are read", func(t *testing.T) {
		// A GITHUB_WORKSPACE-like checkout: the configuration repository
		// at the top, with runner files beside the tracked definition.
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{
			"def/plugin.json":         "{}",
			"def/workflows/probe.js":  "x",
			".gitignore":              "def/ignored.txt\n",
			".fullsend/config.yaml":   "version: \"1\"\n",
			".fullsend/harness/a.yml": "agent: a\n",
		}})
		gitRepo(t, root)
		writeTree(t, root, treeSpec{files: map[string]string{
			"def/untracked.txt":       "u",
			"def/ignored.txt":         "i",
			"gha-creds-0123.json":     `{"secret":true}`,
			".defaults/config.yaml":   "d",
			"output/run.log":          "o",
			".fullsend/bin/fullsend":  "binary",
			"def/target-repo/main.go": "package main",
		}})
		gitRepo(t, filepath.Join(root, "def", "target-repo"))
		// A .fullsend-cache that someone committed is still skipped.
		writeTree(t, root, treeSpec{files: map[string]string{"def/.fullsend-cache/x": "c"}})
		gitIn(t, root, "add", "-f", "def/.fullsend-cache/x")

		files, err := readLocal(t, root, "def")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(sortedKeys(files), ","); got != "plugin.json,workflows/probe.js" {
			t.Fatalf("got %s", got)
		}

		files, err = readLocal(t, root, ".", ".fullsend")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(sortedKeys(files), ","); got != ".gitignore,def/plugin.json,def/workflows/probe.js" {
			t.Fatalf("source . with .fullsend excluded: got %s", got)
		}
	})
	t.Run("submodule contents are skipped", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"plugin.json": "{}", "sub/inner.txt": "i"}})
		gitRepo(t, filepath.Join(root, "sub"))
		gitIn(t, filepath.Join(root, "sub"), "commit", "-q", "-m", "inner")
		gitRepo(t, root)
		files, err := readLocal(t, root, ".")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(sortedKeys(files), ","); got != "plugin.json" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("tracked link to an untracked file dangles", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"a.txt": "a", "secret.txt": "s"}, links: map[string]string{"s": "secret.txt"}})
		gitRepo(t, root, "a.txt", "s")
		_, err := readLocal(t, root, ".")
		if err == nil || !strings.Contains(err.Error(), "s is a symlink to secret.txt, which does not exist; commit the target") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("tracked directory link carries only tracked files", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"real/t.txt": "t"}, links: map[string]string{"lib": "real"}})
		gitRepo(t, root)
		writeTree(t, root, treeSpec{files: map[string]string{"real/untracked.txt": "u"}})
		files, err := readLocal(t, root, ".")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(sortedKeys(files), ","); got != "lib/t.txt,real/t.txt" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("link into .git is refused", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"a.txt": "a"}, links: map[string]string{"cfg": ".git/config"}})
		gitRepo(t, root)
		_, err := readLocal(t, root, ".")
		if err == nil || !strings.Contains(err.Error(), "cfg is a symlink to .git/config, which points into .git/") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("tracked file deleted from the working tree", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"def/a.txt": "a", "def/gone.txt": "g"}})
		gitRepo(t, root)
		if err := os.Remove(filepath.Join(root, "def", "gone.txt")); err != nil {
			t.Fatal(err)
		}
		_, err := readLocal(t, root, "def")
		if err == nil || !strings.Contains(err.Error(), `"gone.txt" is tracked by git but missing from the working tree`) {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("working-tree directory swapped for a link", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "repo")
		writeTree(t, root, treeSpec{files: map[string]string{"def/sub/a.txt": "a", "def/b.txt": "b"}})
		gitRepo(t, root)
		writeTree(t, filepath.Join(parent, "outside"), treeSpec{files: map[string]string{"a.txt": "secret"}})
		if err := os.RemoveAll(filepath.Join(root, "def", "sub")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "def", "sub")); err != nil {
			t.Fatal(err)
		}
		_, err := readLocal(t, root, "def")
		if err == nil || !strings.Contains(err.Error(), `"sub/a.txt" is tracked by git, but a directory on its path is a symlink in the working tree`) {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("tracked file replaced by a directory", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"a.txt": "a", "b": "b"}})
		gitRepo(t, root)
		if err := os.Remove(filepath.Join(root, "b")); err != nil {
			t.Fatal(err)
		}
		writeTree(t, root, treeSpec{files: map[string]string{"b/c.txt": "c"}})
		_, err := readLocal(t, root, ".")
		if err == nil || !strings.Contains(err.Error(), `"b" is tracked by git as a file but is a directory`) {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("source root symlinks are refused", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "repo")
		writeTree(t, filepath.Join(parent, "outside"), treeSpec{files: map[string]string{"plugin.json": "{}"}})
		writeTree(t, root, treeSpec{
			files: map[string]string{"real/def/plugin.json": "{}"},
			links: map[string]string{"rel": "../outside", "mid": "real"},
		})
		if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(root, "abs")); err != nil {
			t.Fatal(err)
		}
		gitRepo(t, root)
		for source, want := range map[string]string{
			"rel":     `"rel" is a symlink in the repository; point source at the real directory`,
			"abs":     `"abs" is a symlink in the repository; point source at the real directory`,
			"mid/def": `"mid" is a symlink in the repository; point source at the real directory`,
		} {
			_, err := readLocal(t, root, source)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("source %s: unexpected error %v", source, err)
			}
		}
	})
	t.Run("source problems", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"a.txt": "a", "empty/.keep": ""}})
		gitRepo(t, root, "a.txt")
		for source, want := range map[string]string{
			"a.txt":   `"a.txt" is not a directory in the repository`,
			"missing": `"missing" does not exist in`,
			"empty":   "\"empty\" holds no files git tracks; commit or `git add`",
			".git":    `".git" is inside .git/`,
		} {
			_, err := readLocal(t, root, source)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("source %s: unexpected error %v", source, err)
			}
		}
	})
	t.Run("not a git repository", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"a.txt": "a"}})
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
		if _, err := readLocal(t, root, "."); err == nil || !strings.Contains(err.Error(), "git ls-files") {
			t.Fatalf("unexpected error %v", err)
		}
		if _, err := RepoToplevel(context.Background(), root); err == nil || !strings.Contains(err.Error(), "git rev-parse") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("repository-location variables from a hook are ignored", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"a.txt": "a"}})
		gitRepo(t, root)
		t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
		t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
		top, err := RepoToplevel(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		real, _ := filepath.EvalSymlinks(root)
		if top != real {
			t.Fatalf("toplevel %s, want %s", top, real)
		}
		if _, err := readLocal(t, root, "."); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("file-count cap", func(t *testing.T) {
		root := t.TempDir()
		files := map[string]string{}
		for i := 0; i <= MaxFiles; i++ {
			files[fmt.Sprintf("d/f%04d", i)] = ""
		}
		writeTree(t, root, treeSpec{files: files})
		gitRepo(t, root)
		_, err := readLocal(t, root, "d")
		if err == nil || !strings.Contains(err.Error(), "exceeded maximum of 1000 files; point source at a sub-directory that holds only the plugin") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("size cap on a sparse file", func(t *testing.T) {
		root := t.TempDir()
		writeTree(t, root, treeSpec{files: map[string]string{"big.bin": ""}})
		gitRepo(t, root)
		if err := os.Truncate(filepath.Join(root, "big.bin"), maxTotal+1); err != nil {
			t.Fatal(err)
		}
		_, err := readLocal(t, root, ".")
		if err == nil || !strings.Contains(err.Error(), "MiB limit; point source at a sub-directory") {
			t.Fatalf("unexpected error %v", err)
		}
	})
}

func TestReadBounded(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sparse")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file: its size is past the budget though nothing is written.
	if err := f.Truncate(maxTotal + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := readBounded(p, maxTotal); !errors.Is(err, errTooLarge) {
		t.Fatalf("expected errTooLarge, got %v", err)
	}
	if data, err := readBounded(p, maxTotal+1); err != nil || int64(len(data)) != maxTotal+1 {
		t.Fatalf("read %d bytes, err %v", len(data), err)
	}
	if _, err := readBounded(filepath.Join(dir, "missing"), 1); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected not-exist, got %v", err)
	}

	w := &treeWalker{files: map[string][]byte{}}
	if err := w.addFile("sparse", p); err == nil || !strings.Contains(err.Error(), "MiB limit") {
		t.Fatalf("addFile: unexpected error %v", err)
	}
	if len(w.files) != 0 || w.total != 0 {
		t.Fatalf("addFile stored data after the size error: %d files, %d bytes", len(w.files), w.total)
	}
}

func TestReadTrackedFile(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "src")
	writeTree(t, dir, treeSpec{
		files: map[string]string{"a.txt": "a", "b.txt": "b"},
		links: map[string]string{"out": "../outside.txt"},
	})
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lstat := func(rel string) os.FileInfo {
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return info
	}

	t.Run("regular file at check time", func(t *testing.T) {
		data, err := readTrackedFile(root, "a.txt", lstat("a.txt"), maxTotal)
		if err != nil || string(data) != "a" {
			t.Fatalf("got %q, %v", data, err)
		}
	})
	t.Run("a path that leaves the root is refused by the open", func(t *testing.T) {
		for _, rel := range []string{"../outside.txt", "out"} {
			if _, err := readTrackedFile(root, rel, lstat("a.txt"), maxTotal); err == nil || !strings.Contains(err.Error(), "escapes from parent") {
				t.Fatalf("%s: expected an escape error, got %v", rel, err)
			}
		}
	})
	t.Run("a file replaced after the check is refused", func(t *testing.T) {
		checked := lstat("a.txt")
		if _, err := readTrackedFile(root, "b.txt", checked, maxTotal); err == nil || !strings.Contains(err.Error(), `"b.txt" changed while it was read`) {
			t.Fatalf("expected a changed-file error, got %v", err)
		}
	})
	t.Run("the read is bounded", func(t *testing.T) {
		if _, err := readTrackedFile(root, "a.txt", lstat("a.txt"), 0); !errors.Is(err, errTooLarge) {
			t.Fatalf("expected errTooLarge, got %v", err)
		}
	})
}

func TestIsLinkLoop(t *testing.T) {
	if !isLinkLoop(syscall.ELOOP) || !isLinkLoop(errors.New("EvalSymlinks: too many links")) {
		t.Fatal("loop errors not recognized")
	}
	if isLinkLoop(fs.ErrPermission) {
		t.Fatal("permission error taken for a loop")
	}
}

func TestMaterializeTree_UnresolvableLinkCarriesTheCause(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, treeSpec{files: map[string]string{"locked/f.txt": "f"}, links: map[string]string{"l": "locked/f.txt"}})
	if err := os.Chmod(filepath.Join(dir, "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	_, err := walkLocal(dir)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "loop") {
		t.Fatalf("a permission error was reported as a loop: %v", err)
	}
}

func TestMaterializeTree_Local(t *testing.T) {
	t.Run("link into .git is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{files: map[string]string{"a.txt": "a", ".git/config": "secret"}, links: map[string]string{"cfg": ".git/config"}})
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "cfg is a symlink to .git/config, which points into .git/") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("escaping link names the source directory", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{files: map[string]string{"a.txt": "a"}, links: map[string]string{"x": "../outside.txt"}})
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "x is a symlink to ../outside.txt, which leaves the source directory") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("absolute target is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{files: map[string]string{"a.txt": "a"}, links: map[string]string{"x": "/etc/passwd"}})
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "x is a symlink to /etc/passwd, which is an absolute path") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("absolute target inside the tree is still refused", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{files: map[string]string{"a.txt": "a"}})
		if err := os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
			t.Fatal(err)
		}
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "which is an absolute path") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("sibling directory sharing the root prefix is outside", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "a")
		writeTree(t, root, treeSpec{files: map[string]string{"f.txt": "f"}, links: map[string]string{"x": "../ab/secret.txt"}})
		writeTree(t, filepath.Join(parent, "ab"), treeSpec{files: map[string]string{"secret.txt": "s"}})
		_, err := walkLocal(root)
		if err == nil || !strings.Contains(err.Error(), "x is a symlink to ../ab/secret.txt, which leaves the source directory") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("every hop of a directory link is checked", func(t *testing.T) {
		// lib is a valid link to an inner directory, but that directory
		// holds a link that leaves the tree; the nested link is refused
		// under the path it would take in the output.
		parent := t.TempDir()
		root := filepath.Join(parent, "root")
		writeTree(t, filepath.Join(parent, "outside"), treeSpec{files: map[string]string{"secret.txt": "s"}})
		writeTree(t, root, treeSpec{
			files: map[string]string{"real/ok.txt": "ok"},
			links: map[string]string{"lib": "real", "real/esc": "../../outside"},
		})
		_, err := walkLocal(root)
		if err == nil || !strings.Contains(err.Error(), "is a symlink to ../../outside, which leaves the source directory") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("in-tree link chain that ends outside is refused by its real path", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "root")
		writeTree(t, filepath.Join(parent, "outside"), treeSpec{files: map[string]string{"secret.txt": "s"}})
		writeTree(t, root, treeSpec{
			files: map[string]string{"f.txt": "f"},
			links: map[string]string{"hop": "../outside/secret.txt", "a": "hop"},
		})
		_, err := walkLocal(root)
		if err == nil || !strings.Contains(err.Error(), "leaves the source directory") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("output keys are clean relative slash paths", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{
			files: map[string]string{"a/b.txt": "b", "c.txt": "c"},
			links: map[string]string{"d/e": "../a"},
		})
		files, err := walkLocal(dir)
		if err != nil {
			t.Fatal(err)
		}
		for k := range files {
			if !validOutputPath(k) {
				t.Errorf("output key %q is not a clean relative path", k)
			}
		}
		if _, ok := files["d/e/b.txt"]; !ok {
			t.Errorf("expected d/e/b.txt in %v", sortedKeys(files))
		}
	})
	t.Run("backslash in a file name is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, `a\b.txt`), []byte("x"), 0o644); err != nil {
			t.Skipf("cannot create a backslash name: %v", err)
		}
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "is not a path the tree can hold") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("nested directory links past the depth limit", func(t *testing.T) {
		dir := t.TempDir()
		spec := treeSpec{files: map[string]string{}, links: map[string]string{}}
		for i := 0; i <= maxLinkDepth+1; i++ {
			spec.files[fmt.Sprintf("d%d/f.txt", i)] = "f"
			spec.links[fmt.Sprintf("d%d/next", i)] = fmt.Sprintf("../d%d", i+1)
		}
		spec.files[fmt.Sprintf("d%d/f.txt", maxLinkDepth+2)] = "end"
		writeTree(t, dir, spec)
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "symlinked directories deep") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("fan-out of directory links is bounded", func(t *testing.T) {
		// Four levels of ten directory links each reach an empty directory
		// 10^4 times without storing a file, so only the visit cap stops it.
		dir := t.TempDir()
		spec := treeSpec{files: map[string]string{"top.txt": "t"}, links: map[string]string{}}
		levels := []string{"a", "b", "c", "d", "e"}
		for li := 0; li < len(levels)-1; li++ {
			for k := 0; k < 10; k++ {
				spec.links[fmt.Sprintf("%s/l%d", levels[li], k)] = "../" + levels[li+1]
			}
		}
		writeTree(t, dir, spec)
		if err := os.MkdirAll(filepath.Join(dir, "e"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "walked more than") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("special file is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{files: map[string]string{"a.txt": "a"}})
		if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), `unsupported file type at "pipe"`) {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("link to a special file is refused", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, treeSpec{files: map[string]string{"a.txt": "a"}, links: map[string]string{"p": "sub/pipe"}})
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(filepath.Join(dir, "sub", "pipe"), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		_, err := walkLocal(dir)
		if err == nil || !strings.Contains(err.Error(), "unsupported file type at") {
			t.Fatalf("unexpected error %v", err)
		}
	})
	t.Run("size limit", func(t *testing.T) {
		dir := t.TempDir()
		big := make([]byte, maxTotal/2+1)
		if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("big.bin", filepath.Join(dir, "copy.bin")); err != nil {
			t.Fatal(err)
		}
		if _, err := walkLocal(dir); err == nil || !strings.Contains(err.Error(), "MiB limit") {
			t.Fatalf("unexpected error %v", err)
		}
	})
}

func TestValidOutputPathAndContains(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"a/b.txt", true}, {"a", true}, {"", false}, {"/a", false}, {"../a", false},
		{"a/../b", false}, {"a/./b", false}, {"a//b", false}, {`a\b`, false}, {"a\x00b", false}, {"a/", false},
	} {
		if got := validOutputPath(tc.in); got != tc.want {
			t.Errorf("validOutputPath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		root, p string
		want    bool
	}{
		{"/tmp/a", "/tmp/a", true}, {"/tmp/a", "/tmp/a/b", true}, {"/tmp/a", "/tmp/ab", false},
		{"/tmp/a", "/tmp", false}, {"/tmp/a", "/tmp/a/../b", false}, {"/tmp/a", "/etc/passwd", false},
		{"/tmp/a", "/tmp/a/..b", true},
	} {
		if got := containsPath(tc.root, tc.p); got != tc.want {
			t.Errorf("containsPath(%q, %q) = %v, want %v", tc.root, tc.p, got, tc.want)
		}
	}
}

func sortedKeys(m map[string][]byte) []string {
	out := keys(m)
	sort.Strings(out)
	return out
}

// TestCopyTrackedSourceSwappedForLink covers a source directory replaced
// by a symlink out of the checkout after it was checked: the copy opens
// the source through the checkout's root, which refuses the escape.
func TestCopyTrackedSourceSwappedForLink(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "def")); err != nil {
		t.Fatal(err)
	}
	err := copyTracked(repo, filepath.Join(repo, "def"), t.TempDir(), []string{"a.txt"})
	if err == nil {
		t.Fatal("copyTracked read through a source that leaves the checkout")
	}
	if !strings.Contains(err.Error(), "opening the source inside the checkout") {
		t.Errorf("unexpected error: %v", err)
	}
}
