package cli

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// writeTree creates files (slash paths relative to root) with the given
// content, all mode 0644 unless the path ends in ".sh" (0755).
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		mode := fs.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		require.NoError(t, os.WriteFile(p, []byte(content), mode))
		require.NoError(t, os.Chmod(p, mode))
	}
}

// snapshotTree reads every regular file under root keyed by slash path,
// with its mode, so a test can prove a tree was not modified.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		info, err := d.Info()
		require.NoError(t, err)
		out[filepath.ToSlash(rel)] = info.Mode().String() + ":" + string(data)
		return nil
	}))
	return out
}

func strPtr(s string) *string { return &s }

// realTempDir is t.TempDir with symlinks resolved (macOS /var is one), since
// override sources must not be reached through a symlink.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

// cacheStyleSkill lays out a URL skill the way the fetch cache does: a real
// tree directory and a named symlink to it (fetch.CacheNamedSymlink),
// returning the symlink path the resolver hands to the runner.
func cacheStyleSkill(t *testing.T) string {
	t.Helper()
	hashDir := filepath.Join(realTempDir(t), "sha256", "abc123")
	writeTree(t, filepath.Join(hashDir, "tree"), map[string]string{
		"SKILL.md":                      "---\nname: pr-review\n---\nbody\n",
		"sub-agents/security.md":        "---\nname: security\n---\nupstream security\n",
		"sub-agents/correctness.md":     "---\nname: correctness\n---\nupstream correctness\n",
		"sub-agents/user-experience.md": "---\nname: user-experience\n---\nux\n",
	})
	named, err := fetch.CacheNamedSymlink(filepath.Join(hashDir, "tree"), "pr-review")
	require.NoError(t, err)
	info, err := os.Lstat(named)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink, "the named path is a symlink")
	return named
}

func TestStageSkills_URLSkillCacheLayout(t *testing.T) {
	src := cacheStyleSkill(t)
	override := filepath.Join(realTempDir(t), "security.md")
	require.NoError(t, os.WriteFile(override, []byte("---\nname: security\n---\nlocal\n"), 0o644))
	stageRoot := filepath.Join(realTempDir(t), "skills")

	staged, err := stageSkills([]harness.SkillEntry{{Source: src, Overrides: map[string]*string{
		"sub-agents/security.md":        strPtr(override),
		"sub-agents/user-experience.md": nil,
	}}}, stageRoot, func(string) {})
	require.NoError(t, err)
	require.Len(t, staged, 1)
	assert.Equal(t, filepath.Join(stageRoot, "0", "pr-review"), staged[0].Dir, "named after the symlink, not \"tree\"")
	got := snapshotTree(t, staged[0].Dir)
	assert.Contains(t, got["sub-agents/security.md"], "local")
	assert.Contains(t, got["sub-agents/correctness.md"], "upstream correctness")
	assert.NotContains(t, got, "sub-agents/user-experience.md")
	assert.FileExists(t, filepath.Join(src, "sub-agents", "user-experience.md"), "the cache is not written")
}

// resolveFromLock hands the runner the same named symlink for a locked URL
// skill, so staging works on that path too.
func TestStageRunSkills_LockPathURLSkill(t *testing.T) {
	src := cacheStyleSkill(t)
	runDir := realTempDir(t)
	h := &harness.Harness{Skills: []harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{"sub-agents/user-experience.md": nil}},
	}}
	dirs, err := stageRunSkills(h, runDir, "", "review", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(runDir, "skills", "0", "pr-review")}, dirs)

	data, err := os.ReadFile(filepath.Join(runDir, personasFileName))
	require.NoError(t, err)
	var m personaManifest
	require.NoError(t, json.Unmarshal(data, &m))
	var names []string
	for _, p := range m.Personas {
		names = append(names, p.Skill+"/"+p.Name)
	}
	assert.Equal(t, []string{"pr-review/correctness", "pr-review/security"}, names)
	assert.Equal(t, []removedPersona{{Skill: "pr-review", Path: "sub-agents/user-experience.md"}}, m.Removed)
}

func TestStageSkills_InTreeSymlinkEscapeRefused(t *testing.T) {
	src := cacheStyleSkill(t)
	outside := filepath.Join(realTempDir(t), "secret")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "key"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(src, "sub-agents", "escape")))

	_, err := stageSkills([]harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{"sub-agents/user-experience.md": nil}},
	}, realTempDir(t), func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `skill tree contains a symlink at "sub-agents/escape"`)
}

// A fullsend directory reached through a symlinked ancestor (macOS /tmp)
// still stages; a symlink below it that leaves the directory is refused.
func TestStageSkillsIn_TrustedRootBehindSymlink(t *testing.T) {
	src := upstreamSkill(t)
	realDir := realTempDir(t)
	linkParent := realTempDir(t)
	root := filepath.Join(linkParent, "fullsend")
	require.NoError(t, os.Symlink(realDir, root))
	require.NoError(t, os.MkdirAll(filepath.Join(realDir, "overrides"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "overrides", "security.md"), []byte("---\nname: security\n---\nmine\n"), 0o644))

	staged, err := stageSkillsIn([]harness.SkillEntry{{Source: src, Overrides: map[string]*string{
		"sub-agents/security.md": strPtr(filepath.Join(root, "overrides", "security.md")),
	}}}, realTempDir(t), root, func(string) {})
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(staged[0].Dir, "sub-agents", "security.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "mine")

	outside := realTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "x.md"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(realDir, "escape")))
	_, err = stageSkillsIn([]harness.SkillEntry{{Source: src, Overrides: map[string]*string{
		"sub-agents/security.md": strPtr(filepath.Join(root, "escape", "x.md")),
	}}}, realTempDir(t), root, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves through a symlink")

	_, err = stageSkillsIn([]harness.SkillEntry{{Source: src, Overrides: map[string]*string{
		"sub-agents/security.md": strPtr(filepath.Join(outside, "x.md")),
	}}}, realTempDir(t), root, func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is outside")
}

func TestStageSkills_OverrideSourceThroughSymlinkedParentRefused(t *testing.T) {
	src := upstreamSkill(t)
	outside := realTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(outside, "security.md"), []byte("---\nname: security\n---\n"), 0o644))
	harnessDir := realTempDir(t)
	require.NoError(t, os.Symlink(outside, filepath.Join(harnessDir, "overrides")))

	_, err := stageSkills([]harness.SkillEntry{{Source: src, Overrides: map[string]*string{
		"sub-agents/security.md": strPtr(filepath.Join(harnessDir, "overrides", "security.md")),
	}}}, realTempDir(t), func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves through a symlink to "+filepath.Join(outside, "security.md"))
}

func TestStageSkills_EmptySourceWithOverridesWarns(t *testing.T) {
	var warnings []string
	staged, err := stageSkills([]harness.SkillEntry{
		{Source: "", Overrides: map[string]*string{"x.md": nil}},
		{Source: ""},
	}, realTempDir(t), func(w string) { warnings = append(warnings, w) })
	require.NoError(t, err)
	assert.Empty(t, staged)
	assert.Equal(t, []string{"skills[0]: skill source is empty; its file overrides are not applied"}, warnings)
}

// An empty skill source with overrides passes harness validation, and
// neither the no-URL run path (which skips resolve.ResolveHarness) nor the
// lock-file replay drops it, so staging must warn rather than apply the
// overrides to nothing (#8974).
func TestStageSkills_EmptySourceReachesStagingThroughResolution(t *testing.T) {
	const want = "skills[0]: skill source is empty; its file overrides are not applied"
	writeHarness := func(t *testing.T, dir, agent string) *harness.Harness {
		t.Helper()
		p := filepath.Join(dir, "harness.yaml")
		content := "agent: " + agent + "\nrole: test\nskills:\n  - \"\":\n      sub-agents/x.md: null\n"
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		h, err := harness.LoadWithOpts(p, harness.LoadOpts{})
		require.NoError(t, err)
		require.NoError(t, h.ResolveRelativeTo(dir))
		return h
	}
	stageWarnings := func(t *testing.T, h *harness.Harness) []string {
		t.Helper()
		var warnings []string
		staged, err := stageSkills(h.Skills, realTempDir(t), func(w string) { warnings = append(warnings, w) })
		require.NoError(t, err)
		assert.Empty(t, staged)
		return warnings
	}

	t.Run("no URL references", func(t *testing.T) {
		h := writeHarness(t, realTempDir(t), "agents/code.md")
		require.False(t, h.HasURLReferences(), "the run path skips resolve.ResolveHarness")
		require.Empty(t, h.OpenShellProfiles())
		require.False(t, hasLocalProviders(h))
		assert.Equal(t, []string{want}, stageWarnings(t, h))
	})

	t.Run("lock-file replay", func(t *testing.T) {
		agentContent := []byte("You are a coding agent.")
		agentHash := fetch.ComputeSHA256(agentContent)
		dir := realTempDir(t)
		require.NoError(t, fetch.CachePut(dir, "https://example.com/agents/code.md", agentContent))

		h := writeHarness(t, dir, "\"https://example.com/agents/code.md#sha256="+agentHash+"\"")
		h.AllowedRemoteResources = []string{"https://example.com/"}
		entry := &lock.HarnessLock{Dependencies: []lock.DependencyEntry{
			{Field: "agent", URL: "https://example.com/agents/code.md", SHA256: agentHash},
		}}
		_, err := resolveFromLock(h, entry, dir, nil, ui.New(io.Discard))
		require.NoError(t, err)
		require.Len(t, h.Skills, 1, "the lock replay keeps the empty-source entry")
		assert.Equal(t, []string{want}, stageWarnings(t, h))
	})
}

func upstreamSkill(t *testing.T) string {
	t.Helper()
	src := filepath.Join(realTempDir(t), "cache", "pr-review")
	writeTree(t, src, map[string]string{
		"SKILL.md":                      "---\nname: pr-review\n---\nbody\n",
		"scripts/run.sh":                "#!/bin/sh\necho hi\n",
		"sub-agents/security.md":        "---\nname: security\n---\nupstream security\n",
		"sub-agents/correctness.md":     "---\nname: correctness\n---\nupstream correctness\n",
		"sub-agents/user-experience.md": "---\nname: user-experience\n---\nux\n",
	})
	return src
}

func TestStageSkills_ReplaceAddRemove(t *testing.T) {
	src := upstreamSkill(t)
	before := snapshotTree(t, src)

	overrideDir := realTempDir(t)
	writeTree(t, overrideDir, map[string]string{
		"security.md": "---\nname: security\n---\nlocal security\n",
		"style.md":    "---\nname: style\n---\nnew persona\n",
	})

	var warnings []string
	stageRoot := filepath.Join(realTempDir(t), "skills")
	staged, err := stageSkills([]harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{
			"sub-agents/security.md":        strPtr(filepath.Join(overrideDir, "security.md")),
			"sub-agents/style.md":           strPtr(filepath.Join(overrideDir, "style.md")),
			"sub-agents/user-experience.md": nil,
			"sub-agents/missing.md":         nil,
		}},
	}, stageRoot, func(w string) { warnings = append(warnings, w) })
	require.NoError(t, err)
	require.Len(t, staged, 1)

	s := staged[0]
	assert.Equal(t, filepath.Join(stageRoot, "0", "pr-review"), s.Dir)
	got := snapshotTree(t, s.Dir)
	assert.Equal(t, "-rw-r--r--:---\nname: security\n---\nlocal security\n", got["sub-agents/security.md"])
	assert.Equal(t, "-rw-r--r--:---\nname: style\n---\nnew persona\n", got["sub-agents/style.md"])
	assert.NotContains(t, got, "sub-agents/user-experience.md")
	assert.Equal(t, before["sub-agents/correctness.md"], got["sub-agents/correctness.md"])
	assert.Equal(t, before["scripts/run.sh"], got["scripts/run.sh"], "file modes are kept")
	assert.True(t, strings.HasPrefix(got["scripts/run.sh"], "-rwxr-xr-x"))

	assert.Equal(t, originOverride, s.origin("sub-agents/security.md"))
	assert.Equal(t, originAdded, s.origin("sub-agents/style.md"))
	assert.Equal(t, originUpstream, s.origin("sub-agents/correctness.md"))
	assert.Equal(t, []string{"sub-agents/user-experience.md"}, s.Removed)

	require.Len(t, warnings, 1)
	assert.Equal(t, `skills[0]: override "sub-agents/missing.md" is null but the skill has no such file; nothing removed`, warnings[0])

	assert.Equal(t, before, snapshotTree(t, src), "the resolved source tree must stay byte-identical")
}

func TestStageSkills_ReplaceKeepsUpstreamMode(t *testing.T) {
	src := upstreamSkill(t)
	override := filepath.Join(realTempDir(t), "run.sh")
	require.NoError(t, os.WriteFile(override, []byte("#!/bin/sh\necho override\n"), 0o644))

	staged, err := stageSkills([]harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{"scripts/run.sh": strPtr(override)}},
	}, realTempDir(t), func(string) {})
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(staged[0].Dir, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o755), info.Mode().Perm())
}

func TestStageSkills_ReplaceReadOnlyUpstreamFile(t *testing.T) {
	src := upstreamSkill(t)
	readOnly := filepath.Join(src, "sub-agents", "security.md")
	require.NoError(t, os.Chmod(readOnly, 0o444))
	before := snapshotTree(t, src)

	override := filepath.Join(realTempDir(t), "security.md")
	require.NoError(t, os.WriteFile(override, []byte("---\nname: security\n---\nlocal security\n"), 0o644))

	staged, err := stageSkills([]harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{"sub-agents/security.md": strPtr(override)}},
	}, realTempDir(t), func(string) {})
	require.NoError(t, err)
	require.Len(t, staged, 1)

	got := snapshotTree(t, staged[0].Dir)
	assert.Equal(t, "-r--r--r--:---\nname: security\n---\nlocal security\n", got["sub-agents/security.md"],
		"the override's content with the upstream read-only mode")
	assert.Equal(t, originOverride, staged[0].origin("sub-agents/security.md"))
	assert.Equal(t, before, snapshotTree(t, src), "the resolved source tree must stay byte-identical")
}

func TestStageSkills_NoOverridesKeepsSource(t *testing.T) {
	a := filepath.Join(realTempDir(t), "a")
	stageRoot := filepath.Join(realTempDir(t), "skills")
	staged, err := stageSkills([]harness.SkillEntry{{Source: a}, {Source: ""}}, stageRoot, func(string) {})
	require.NoError(t, err)
	assert.Equal(t, []string{a}, stagedSkillDirs(staged))
	_, statErr := os.Stat(stageRoot)
	assert.True(t, errors.Is(statErr, fs.ErrNotExist), "nothing is staged without overrides")
	assert.Nil(t, stagedSkillDirs(nil))
}

func TestStageSkills_SymlinkRefused(t *testing.T) {
	src := upstreamSkill(t)
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(src, "sub-agents", "link.md")))

	_, err := stageSkills([]harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{"sub-agents/user-experience.md": nil}},
	}, realTempDir(t), func(string) {})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `skills[0]: skill tree contains a symlink at "sub-agents/link.md"`)
}

// A URL base harness's relative override is a cache named symlink to a
// sibling file (content.md → content); it stages.
func TestStageSkills_CacheNamedOverrideLinkAccepted(t *testing.T) {
	src := upstreamSkill(t)
	dir := realTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "content"), []byte("---\nname: security\n---\ncached\n"), 0o644))
	require.NoError(t, os.Symlink("content", filepath.Join(dir, "content.md")))

	staged, err := stageSkills([]harness.SkillEntry{{Source: src, Overrides: map[string]*string{
		"sub-agents/security.md": strPtr(filepath.Join(dir, "content.md")),
	}}}, realTempDir(t), func(string) {})
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(staged[0].Dir, "sub-agents", "security.md"))
	require.NoError(t, err)
	assert.Contains(t, string(got), "cached")
}

func TestStageSkills_BadKeysRefusedAtApplyTime(t *testing.T) {
	src := upstreamSkill(t)
	for key, want := range map[string]string{
		"../escape.md":             "must not contain path traversal segments",
		"/etc/passwd":              "must be relative (no leading /)",
		".":                        "must name a file in the skill",
		"./sub-agents/security.md": "must be in clean form",
		"sub-agents":               "names a directory; only files can be overridden",
		"SKILL.md/x.md":            "skills[0]: override \"SKILL.md/x.md\"",
		"a\x00b":                   "must not contain null bytes",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := stageSkills([]harness.SkillEntry{
				{Source: src, Overrides: map[string]*string{key: nil}},
			}, realTempDir(t), func(string) {})
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

func TestStageSkills_OverrideSourceErrors(t *testing.T) {
	src := upstreamSkill(t)
	dir := realTempDir(t)
	link := filepath.Join(dir, "link.md")
	require.NoError(t, os.Symlink(filepath.Join(src, "SKILL.md"), link))

	for name, val := range map[string]string{
		"missing": filepath.Join(dir, "nope.md"),
		"symlink": link,
		"dir":     dir,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stageSkills([]harness.SkillEntry{
				{Source: src, Overrides: map[string]*string{"sub-agents/security.md": strPtr(val)}},
			}, realTempDir(t), func(string) {})
			require.Error(t, err)
			assert.Contains(t, err.Error(), `skills[0]: override "sub-agents/security.md": reading override source`)
		})
	}
}

func TestStageSkills_SourceErrors(t *testing.T) {
	file := filepath.Join(realTempDir(t), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	for name, src := range map[string]string{
		"missing": filepath.Join(realTempDir(t), "nope"),
		"not dir": file,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := stageSkills([]harness.SkillEntry{
				{Source: src, Overrides: map[string]*string{"x.md": nil}},
			}, realTempDir(t), func(string) {})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "skills[0]")
		})
	}
}

func TestStageRunSkills_WritesManifestAndStagesOverrides(t *testing.T) {
	src := upstreamSkill(t)
	plain := filepath.Join(realTempDir(t), "lint")
	writeTree(t, plain, map[string]string{"SKILL.md": "---\nname: lint\n---\n"})
	runDir := realTempDir(t)
	h := &harness.Harness{Skills: []harness.SkillEntry{
		{Source: plain},
		{Source: src, Overrides: map[string]*string{"sub-agents/user-experience.md": nil}},
	}}

	dirs, err := stageRunSkills(h, runDir, "", "review", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, []string{plain, filepath.Join(runDir, "skills", "1", "pr-review")}, dirs)
	assert.FileExists(t, filepath.Join(runDir, personasFileName))
	assert.FileExists(t, filepath.Join(src, "sub-agents", "user-experience.md"))
}

func TestStageRunSkills_Errors(t *testing.T) {
	src := upstreamSkill(t)
	h := &harness.Harness{Skills: []harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{"../x": nil}},
	}}
	_, err := stageRunSkills(h, realTempDir(t), "", "review", ui.New(io.Discard))
	require.Error(t, err)

	// Frontmatter never fails the run; only I/O does. A directory where
	// personas.json goes makes the write fail.
	bad := filepath.Join(realTempDir(t), "bad")
	writeTree(t, bad, map[string]string{"sub-agents/a.md": "---\nalways: yes please\n---\n"})
	runDir := realTempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(runDir, personasFileName), 0o755))
	_, err = stageRunSkills(&harness.Harness{Skills: []harness.SkillEntry{{Source: bad}}}, runDir, "", "review", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing personas.json")
}

func TestNewHarnessBootstrap_UsesStagedSkillDirs(t *testing.T) {
	h := &harness.Harness{Skills: []harness.SkillEntry{{Source: "/src/a"}}}
	boot, err := newHarnessBootstrap(h, []string{"/run/skills/0/a"}, "sb", "agent", "", nil, nil, "", "/repo", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"/run/skills/0/a"}, boot.SkillDirs())
}

func TestUploadPersonaManifest(t *testing.T) {
	runDir := realTempDir(t)
	var gotCmd, gotLocal, gotRemote string
	exec := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		gotCmd = cmd
		return "", "", 0, nil
	}
	upload := func(_ string, local, remote string) error {
		gotLocal, gotRemote = local, remote
		return nil
	}
	require.NoError(t, uploadPersonaManifest(exec, upload, "sb", runDir))
	assert.Equal(t, "mkdir -p /sandbox/workspace/.fullsend", gotCmd)
	assert.Equal(t, filepath.Join(runDir, "personas.json"), gotLocal)
	assert.Equal(t, "/sandbox/workspace/.fullsend/personas.json", gotRemote)
	assert.Equal(t, "export FULLSEND_PERSONAS_FILE=/sandbox/workspace/.fullsend/personas.json", personasEnvLine())

	failExec := func(string, string, time.Duration) (string, string, int, error) {
		return "", "", 0, errors.New("exec boom")
	}
	assert.ErrorContains(t, uploadPersonaManifest(failExec, upload, "sb", runDir), "exec boom")
	codeExec := func(string, string, time.Duration) (string, string, int, error) {
		return "", "denied", 1, nil
	}
	assert.ErrorContains(t, uploadPersonaManifest(codeExec, upload, "sb", runDir), "denied")
	failUpload := func(string, string, string) error { return errors.New("upload boom") }
	assert.ErrorContains(t, uploadPersonaManifest(exec, failUpload, "sb", runDir), "upload boom")
}

func TestPersonasFile_IsReservedSandboxKey(t *testing.T) {
	assert.True(t, isReservedSandboxKey("FULLSEND_PERSONAS_FILE"))
}
