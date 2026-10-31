package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/skill"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// File origins recorded for each staged file (personas.json "origin").
const (
	originUpstream = "upstream"
	originOverride = "override"
	originAdded    = "added"
)

// stagedSkill is one skill directory as the sandbox will see it: either
// the resolved source (no overrides) or a per-run merged copy.
type stagedSkill struct {
	// Dir is the host directory handed to the runtime.
	Dir string
	// Origins maps a slash-separated path relative to Dir to the origin of
	// a file an override wrote (override or added). Files not in the map
	// came from the skill source unchanged.
	Origins map[string]string
	// Removed lists the slash-separated paths a null override removed
	// from the upstream tree, sorted.
	Removed []string
}

// origin reports where the staged file at rel came from.
func (s stagedSkill) origin(rel string) string {
	if o, ok := s.Origins[rel]; ok {
		return o
	}
	return originUpstream
}

// stageSkills returns the skill directories the runtime must upload. An
// entry with file overrides is copied into stageRoot/<index>/<sandbox
// name> and the overrides are applied to the copy; the resolved source
// (shared fetch cache or a local .fullsend directory) is never written.
// Entries without overrides keep their resolved path, so a harness with
// no overrides stages exactly the trees it did before (#6158).
func stageSkills(skills []harness.SkillEntry, stageRoot string, warn func(string)) ([]stagedSkill, error) {
	return stageSkillsIn(skills, stageRoot, "", warn)
}

// stageSkillsIn is stageSkills with the directory override sources must
// stay inside (the fullsend directory, which also holds the fetch cache).
// Symlinks above that directory are allowed; with no directory, any
// symlink on the path is refused.
func stageSkillsIn(skills []harness.SkillEntry, stageRoot, trustedRoot string, warn func(string)) ([]stagedSkill, error) {
	staged := make([]stagedSkill, 0, len(skills))
	for i, entry := range skills {
		if entry.Source == "" {
			// Resolution clears Source for a skill already pulled in as a
			// dependency; its overrides then have no tree to apply to.
			if entry.HasOverrides() {
				warn(fmt.Sprintf("skills[%d]: skill has no resolved source; its file overrides are not applied", i))
			}
			continue
		}
		if !entry.HasOverrides() {
			staged = append(staged, stagedSkill{Dir: entry.Source})
			continue
		}
		field := fmt.Sprintf("skills[%d]", i)
		dest := filepath.Join(stageRoot, strconv.Itoa(i), skill.SandboxName(entry.Source))
		s, err := stageOverriddenSkill(field, entry, dest, trustedRoot, warn)
		if err != nil {
			return nil, err
		}
		staged = append(staged, s)
	}
	return staged, nil
}

// stagedSkillDirs returns the Dir of each staged skill, in order.
func stagedSkillDirs(staged []stagedSkill) []string {
	if len(staged) == 0 {
		return nil
	}
	dirs := make([]string, len(staged))
	for i, s := range staged {
		dirs[i] = s.Dir
	}
	return dirs
}

func stageOverriddenSkill(field string, entry harness.SkillEntry, dest, trustedRoot string, warn func(string)) (stagedSkill, error) {
	// A URL skill's source is a named symlink into the fetch cache
	// (fetch.CacheNamedSymlink); resolve the root only. Symlinks inside
	// the tree are still refused by copySkillTree.
	root, err := filepath.EvalSymlinks(entry.Source)
	if err != nil {
		return stagedSkill{}, fmt.Errorf("%s: reading skill source: %w", field, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return stagedSkill{}, fmt.Errorf("%s: reading skill source: %w", field, err)
	}
	if !info.IsDir() {
		return stagedSkill{}, fmt.Errorf("%s: skill source %q is not a directory", field, entry.Source)
	}
	if err := os.RemoveAll(dest); err != nil {
		return stagedSkill{}, fmt.Errorf("%s: clearing staging directory: %w", field, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return stagedSkill{}, fmt.Errorf("%s: creating staging directory: %w", field, err)
	}
	if err := copySkillTree(field, root, dest); err != nil {
		return stagedSkill{}, err
	}

	s := stagedSkill{Dir: dest, Origins: map[string]string{}}
	keys := make([]string, 0, len(entry.Overrides))
	for k := range entry.Overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := harness.ValidateOverrideKey(field, key); err != nil {
			return stagedSkill{}, err
		}
		rel := path.Clean(filepath.ToSlash(key))
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if r, err := filepath.Rel(dest, target); err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return stagedSkill{}, fmt.Errorf("%s: override file path %q resolves outside the skill directory", field, key)
		}
		existing, statErr := os.Lstat(target)
		exists := statErr == nil
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			// A parent that is a regular file lands here (ENOTDIR).
			return stagedSkill{}, fmt.Errorf("%s: override %q: %w", field, key, statErr)
		}
		if exists && existing.IsDir() {
			return stagedSkill{}, fmt.Errorf("%s: override %q names a directory; only files can be overridden", field, key)
		}

		val := entry.Overrides[key]
		if val == nil {
			if !exists {
				warn(fmt.Sprintf("%s: override %q is null but the skill has no such file; nothing removed", field, key))
				continue
			}
			if err := os.Remove(target); err != nil {
				return stagedSkill{}, fmt.Errorf("%s: override %q: removing file: %w", field, key, err)
			}
			delete(s.Origins, rel)
			s.Removed = append(s.Removed, rel)
			continue
		}

		data, err := readOverrideSource(*val, trustedRoot)
		if err != nil {
			return stagedSkill{}, fmt.Errorf("%s: override %q: reading override source: %w", field, key, err)
		}
		mode := fs.FileMode(0o644)
		origin := originAdded
		if exists {
			mode = existing.Mode().Perm()
			origin = originOverride
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return stagedSkill{}, fmt.Errorf("%s: override %q: creating parent directory: %w", field, key, err)
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return stagedSkill{}, fmt.Errorf("%s: override %q: writing file: %w", field, key, err)
		}
		if err := os.Chmod(target, mode); err != nil {
			return stagedSkill{}, fmt.Errorf("%s: override %q: setting file mode: %w", field, key, err)
		}
		s.Origins[rel] = origin
	}
	sort.Strings(s.Removed)
	return s, nil
}

// readOverrideSource reads an override value that resolution has already
// turned into a local path (a harness file or a cached URL file). Only a
// regular file is accepted, and a path that reaches it through a symlink
// (the file itself or a parent directory) is refused. With a trustedRoot,
// p must sit inside it and only symlinks above the root are allowed (for
// example macOS /tmp → /private/tmp).
func readOverrideSource(p, trustedRoot string) ([]byte, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	want := filepath.Clean(p)
	if trustedRoot != "" {
		rel, err := filepath.Rel(filepath.Clean(trustedRoot), want)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside %s", p, trustedRoot)
		}
		realRoot, err := filepath.EvalSymlinks(trustedRoot)
		if err != nil {
			return nil, err
		}
		want = filepath.Join(realRoot, rel)
	}
	// A link to a sibling in the same directory is allowed: a URL base
	// harness's relative override is a fetch.CacheNamedSymlink
	// (content.md → content).
	if real != want && filepath.Dir(real) != filepath.Dir(want) {
		return nil, fmt.Errorf("%s resolves through a symlink to %s", p, real)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	return os.ReadFile(real)
}

// copySkillTree copies src into dst, keeping file modes. Only regular
// files and directories are copied: a symlink (or any other file type)
// fails the copy with an error naming its path, and is never followed.
func copySkillTree(field, src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("%s: copying skill tree: %w", field, walkErr)
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return fmt.Errorf("%s: copying skill tree: %w", field, err)
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("%s: copying skill tree: %w", field, err)
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: skill tree contains a symlink at %q; overridden skills must contain only regular files and directories", field, filepath.ToSlash(rel))
		case info.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("%s: copying skill tree: %w", field, err)
			}
			return os.Chmod(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyRegularFile(field, p, target, info.Mode().Perm())
		default:
			return fmt.Errorf("%s: skill tree contains a non-regular file at %q; overridden skills must contain only regular files and directories", field, filepath.ToSlash(rel))
		}
	})
}

func copyRegularFile(field, src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("%s: copying skill tree: %w", field, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("%s: copying skill tree: %w", field, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("%s: copying skill tree: %w", field, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("%s: copying skill tree: %w", field, err)
	}
	// OpenFile applies the umask; set the source mode exactly.
	return os.Chmod(dst, mode)
}

// stageRunSkills materializes the run's skill trees under runDir/skills
// and writes runDir/personas.json. It runs right after harness resolution
// (the lock-file path included), so pi child routing, the bootstrap and
// the pre-script all see the merged trees. It returns the directories the
// runtime uploads in place of the resolved sources.
func stageRunSkills(h *harness.Harness, runDir, trustedRoot, agentName string, printer *ui.Printer) ([]string, error) {
	staged, err := stageSkillsIn(h.Skills, filepath.Join(runDir, "skills"), trustedRoot, printer.StepWarn)
	if err != nil {
		printer.StepFail("Skill staging failed")
		return nil, err
	}
	if _, err := writePersonaManifest(staged, runDir, agentName, printer.StepWarn); err != nil {
		printer.StepFail("Persona manifest failed")
		return nil, err
	}
	return stagedSkillDirs(staged), nil
}

// uploadPersonaManifest copies runDir/personas.json to its sandbox path
// next to output-schema.json.
func uploadPersonaManifest(exec sandboxExecFunc, upload func(sandboxName, localPath, remotePath string) error, sandboxName, runDir string) error {
	mkdirCmd := fmt.Sprintf("mkdir -p %s", path.Dir(remotePersonasPath))
	if _, stderr, code, err := exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("creating .fullsend dir for %s: %w", personasFileName, err)
	} else if code != 0 {
		return fmt.Errorf("creating .fullsend dir for %s: %s", personasFileName, strings.TrimSpace(stderr))
	}
	if err := upload(sandboxName, filepath.Join(runDir, personasFileName), remotePersonasPath); err != nil {
		return fmt.Errorf("uploading %s: %w", personasFileName, err)
	}
	return nil
}

// personasEnvLine is the sandbox .env export for the uploaded manifest.
func personasEnvLine() string {
	// Safe: remotePersonasPath is built from the SandboxWorkspace constant.
	return fmt.Sprintf("export %s=%s", personasEnvVar, remotePersonasPath)
}
