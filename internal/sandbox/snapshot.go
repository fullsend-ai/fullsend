package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// This file holds the trusted-state round trip of the download sanitizer: the
// repository snapshot captured on the host before upload and replayed after
// download. Parsing and writing use the host Git binary with includes
// disabled; there is no custom config, refspec, or URL parser. Only values
// captured from the trusted host checkout are replayed, and every downloaded
// config is rebuilt from its declared storage format alone. The untrusted-tree
// walk and publication live in safedownload.go.

// configEntry is one repository-local config value captured from the trusted
// host checkout and replayed verbatim after the round trip. Downloaded config
// never supplies an entry.
type configEntry struct {
	Key   string
	Value string
}

// trustedSingleKeys is the single-valued allowlist: host identity and the
// working-copy hints a checkout needs. Remote and branch keys are selected by
// shape instead, so every trusted remote and tracked branch survives.
var trustedSingleKeys = []string{
	"core.filemode",
	"core.ignorecase",
	"core.symlinks",
	"core.precomposeunicode",
	"user.name",
	"user.email",
	"remote.pushdefault",
	"push.default",
	"core.sparsecheckout",
	"core.sparsecheckoutcone",
}

// trustedBoolKeys are the working-copy hints Git reads as booleans, where a
// bare key means true and an empty value means false.
var trustedBoolKeys = map[string]bool{
	"core.filemode":           true,
	"core.ignorecase":         true,
	"core.symlinks":           true,
	"core.precomposeunicode":  true,
	"core.sparsecheckout":     true,
	"core.sparsecheckoutcone": true,
}

// gitConfigFormat is the storage format the downloaded repository must keep so
// Git can read its objects and refs.
type gitConfigFormat struct {
	version      string
	objectFormat string
	refStorage   string
}

// gitConfigDirs records the git dirs whose config the sanitizer rebuilds.
// The walk populates it; discovery stays in the single existing tree walk.
type gitConfigDirs map[string]bool

// storeSnapshot is the trusted state of one module store: its config and its
// shallow boundary.
type storeSnapshot struct {
	format  gitConfigFormat
	config  []configEntry
	shallow shallowState
}

// configRecord is one entry of a config file, read by native Git.
type configRecord struct {
	key      string
	value    string
	hasValue bool
}

// listGitConfig lists one config file with includes disabled. A missing file is
// an empty listing so callers can apply Git's defaults.
func listGitConfig(cwd, configPath string) ([]configRecord, error) {
	if _, err := os.Lstat(configPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out, err := runBoundedGit(cwd, "", nil, "config", "--no-includes", "--file", configPath, "--list", "-z")
	if err != nil {
		return nil, err
	}
	var records []configRecord
	for _, record := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if record == "" {
			continue
		}
		key, value, hasValue := strings.Cut(record, "\n")
		records = append(records, configRecord{key: key, value: value, hasValue: hasValue})
	}
	return records, nil
}

// trustedWorktreeConfigEnabled reports whether Git would read a per-worktree
// config for this checkout (extensions.worktreeConfig).
func trustedWorktreeConfigEnabled(cwd, configPath string) (bool, error) {
	out, err := runBoundedGit(cwd, "", nil, "config", "--no-includes", "--file", configPath, "--bool", "--get", "extensions.worktreeConfig")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// trustedConfigPaths returns the config files Git reads for the checkout whose
// git dir is gitDir: its config plus, only when the extension enables it, its
// per-worktree config. A missing worktree config is empty state.
func trustedConfigPaths(cwd, gitDir string) ([]string, error) {
	configPath := filepath.Join(gitDir, "config")
	paths := []string{configPath}
	enabled, err := trustedWorktreeConfigEnabled(cwd, configPath)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return paths, nil
	}
	worktreeConfig := filepath.Join(gitDir, "config.worktree")
	info, err := os.Lstat(worktreeConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return paths, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspecting trusted worktree config %s: %w", worktreeConfig, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("trusted worktree config %s is not a regular file", worktreeConfig)
	}
	return append(paths, worktreeConfig), nil
}

// normalizeGitConfigFormat validates the declared storage format and fills
// Git's defaults. An unsupported extension or format value fails closed: the
// sanitizer must reproduce it rather than silently drop it.
func normalizeGitConfigFormat(format gitConfigFormat) (gitConfigFormat, error) {
	if format.version == "" {
		format.version = "0"
	}
	if format.objectFormat == "" {
		format.objectFormat = "sha1"
	}
	if format.refStorage == "" {
		format.refStorage = "files"
	}
	format.objectFormat = strings.ToLower(format.objectFormat)
	format.refStorage = strings.ToLower(format.refStorage)
	switch format.version {
	case "0", "1":
	default:
		return gitConfigFormat{}, rejectRepository(fmt.Errorf("unsupported repositoryformatversion %q", format.version))
	}
	switch format.objectFormat {
	case "sha1", "sha256":
	default:
		return gitConfigFormat{}, rejectRepository(fmt.Errorf("unsupported object format %q", format.objectFormat))
	}
	switch format.refStorage {
	case "files", "reftable":
	default:
		return gitConfigFormat{}, rejectRepository(fmt.Errorf("unsupported ref storage %q", format.refStorage))
	}
	if format.version != "1" && (format.objectFormat != "sha1" || format.refStorage != "files") {
		return gitConfigFormat{}, rejectRepository(errors.New("repository extensions require repositoryformatversion 1"))
	}
	return format, nil
}

// captureGitConfigFormat reads the repository's declared storage format with
// native Git. An extension this sanitizer cannot reproduce (for example
// partialClone or preciousObjects) is rejected instead of being silently
// discarded, which would damage the returned repository.
func captureGitConfigFormat(cwd, configPath string) (gitConfigFormat, error) {
	records, err := listGitConfig(cwd, configPath)
	if err != nil {
		return gitConfigFormat{}, err
	}
	format := gitConfigFormat{}
	for _, record := range records {
		key := strings.ToLower(record.key)
		// A remote-form partial clone in a downloaded config would be
		// stripped by the rewrite, leaving missing objects the host cannot
		// retrieve; reject it the same way host capture does.
		if partialCloneRemoteKey(key) {
			return gitConfigFormat{}, rejectRepository(fmt.Errorf("git config declares a partial clone, which is not supported"))
		}
		if rest, ok := strings.CutPrefix(key, "extensions."); ok {
			switch rest {
			case "objectformat":
				format.objectFormat = strings.TrimSpace(record.value)
			case "refstorage":
				format.refStorage = strings.TrimSpace(record.value)
			case "worktreeconfig":
				// The per-worktree config file is removed with this
				// extension; the settings worth keeping are captured from it,
				// including the sparse-checkout booleans.
			default:
				return gitConfigFormat{}, rejectRepository(fmt.Errorf("unsupported repository extension %q", record.key))
			}
			continue
		}
		if key == "core.repositoryformatversion" {
			format.version = strings.TrimSpace(record.value)
		}
	}
	return normalizeGitConfigFormat(format)
}

// captureTrustedConfig selects the safe values from trusted config files in
// Git's precedence order: identity, working-copy hints, every remote
// destination/refspec/push setting, and branch tracking. worktreeRoot is the
// working tree Git resolves a relative local destination against; it is empty
// for a module store, whose relative destinations Git resolves against the
// submodule worktree and therefore fail closed instead of being rebased.
func captureTrustedConfig(cwd, worktreeRoot string, configPaths ...string) ([]configEntry, error) {
	single := make(map[string]string)
	var ordered []configEntry
	// finalValue holds single-valued settings at their effective final value:
	// the mirror and activation booleans and the single-valued tracking
	// destinations, where only the last entry in Git's precedence order takes
	// effect and a repeated earlier value must not survive as the last entry.
	finalValue := make(map[string]string)
	var finalOrder []string
	// urlLists holds the multi-valued remote destination lists, whose empty
	// entries reset the list in Git's readers.
	urlLists := make(map[string][]configEntry)
	var urlOrder []string
	// seen drops a tracking entry whose exact key and value an earlier config
	// file already contributed: a refspec present in both config and
	// config.worktree must replay once, or Git applies it twice.
	seen := make(map[string]bool)
	for _, configPath := range configPaths {
		records, err := listGitConfig(cwd, configPath)
		if err != nil {
			return nil, err
		}
		// Booleans are listed by native Git so a bare key stays true and an
		// empty value stays false instead of being value-filtered. Mirror and
		// activation keys are single-valued in Git, so only the last value in
		// precedence order takes effect and that final value is the one
		// restored; working-copy hints use the case-insensitive canonical key.
		bools := make(map[string]bool)
		for _, record := range records {
			key := strings.ToLower(record.key)
			switch {
			case partialCloneRemoteKey(key):
				return nil, fmt.Errorf("trusted git config %q declares a partial clone, which is not supported", record.key)
			case mirrorKey(key), submoduleActiveKey(key):
				out, err := runBoundedGit(cwd, "", nil, "config", "--no-includes", "--file", configPath, "--bool", "--get-all", record.key)
				if err != nil {
					return nil, fmt.Errorf("trusted git config %q: %w", record.key, err)
				}
				if values := strings.Fields(string(out)); len(values) > 0 {
					if _, ok := finalValue[record.key]; !ok {
						finalOrder = append(finalOrder, record.key)
					}
					finalValue[record.key] = values[len(values)-1]
				}
			case trustedBoolKeys[key]:
				if bools[key] {
					continue
				}
				bools[key] = true
				out, err := runBoundedGit(cwd, "", nil, "config", "--no-includes", "--file", configPath, "--bool", "--get-all", record.key)
				if err != nil {
					return nil, fmt.Errorf("trusted git config %q: %w", record.key, err)
				}
				if values := strings.Fields(string(out)); len(values) > 0 {
					single[key] = values[len(values)-1]
				}
			}
		}
		for _, record := range records {
			key := strings.ToLower(record.key)
			if mirrorKey(key) || submoduleActiveKey(key) || trustedBoolKeys[key] || !record.hasValue {
				continue
			}
			switch {
			case isTrustedSingleKey(key):
				// An explicit empty value is a real Git value with real
				// precedence: a later empty override clears an earlier
				// setting instead of being dropped for it.
				single[key] = record.value
			case remoteTrackingKey(key), branchTrackingKey(key), submoduleActivationKey(key), submoduleActivePathspecKey(key):
				value := record.value
				if value != "" && (strings.HasSuffix(key, ".url") || strings.HasSuffix(key, ".pushurl")) {
					if isExecutableTransport(value) {
						return nil, fmt.Errorf("trusted git config %q uses an executable transport helper, which is not supported", record.key)
					}
					resolved, err := resolveTrustedDestination(worktreeRoot, value)
					if err != nil {
						return nil, fmt.Errorf("trusted git config %q: %w", record.key, err)
					}
					value = resolved
				}
				if singleValuedTrackingKey(key) {
					if _, ok := finalValue[record.key]; !ok {
						finalOrder = append(finalOrder, record.key)
					}
					finalValue[record.key] = value
					continue
				}
				if urlResetKey(key) {
					// Git reads remote URL lists with reset semantics: an
					// empty entry clears everything before it, including
					// values inherited from global config. Replay the
					// sequence verbatim, empties included, so the restored
					// config carries the same reset the host's did.
					if _, ok := urlLists[record.key]; !ok {
						urlOrder = append(urlOrder, record.key)
					}
					urlLists[record.key] = append(urlLists[record.key], configEntry{Key: record.key, Value: value})
					continue
				}
				pair := record.key + "\x00" + value
				if seen[pair] {
					continue
				}
				seen[pair] = true
				ordered = append(ordered, configEntry{Key: record.key, Value: value})
			}
		}
	}
	for _, key := range urlOrder {
		ordered = append(ordered, urlLists[key]...)
	}
	for _, key := range finalOrder {
		ordered = append(ordered, configEntry{Key: key, Value: finalValue[key]})
	}
	entries := make([]configEntry, 0, len(trustedSingleKeys)+len(ordered))
	for _, key := range trustedSingleKeys {
		if value, ok := single[key]; ok {
			entries = append(entries, configEntry{Key: key, Value: value})
		}
	}
	return append(entries, ordered...), nil
}

// resolveTrustedDestination turns a relative local destination into the
// absolute path it names on the trusted host. URLs, absolute paths, and
// scp-like remote addresses are returned unchanged; an unresolvable relative
// path fails closed instead of silently pointing elsewhere after download.
func resolveTrustedDestination(worktreeRoot, value string) (string, error) {
	if filepath.IsAbs(value) || strings.Contains(value, "://") {
		return value, nil
	}
	// Git's [user@]host:path syntax is already a remote address.
	if colon := strings.Index(value, ":"); colon > 0 && !strings.Contains(value[:colon], "/") {
		return value, nil
	}
	if worktreeRoot == "" {
		return "", fmt.Errorf("relative local destination %q cannot be resolved without its owning worktree", value)
	}
	// Resolve the raw path: filepath.Join would clean "link/.." lexically
	// before following the symlink, which names a different target than the
	// kernel path Git resolves.
	resolved, err := filepath.EvalSymlinks(worktreeRoot + string(filepath.Separator) + value)
	if err != nil {
		return "", fmt.Errorf("relative local destination %q cannot be resolved in the trusted checkout: %w", value, err)
	}
	return resolved, nil
}

func isTrustedSingleKey(key string) bool {
	for _, trusted := range trustedSingleKeys {
		if key == trusted {
			return true
		}
	}
	return false
}

// mirrorKey matches remote.<name>.mirror, the boolean that makes push mirror
// every ref.
func mirrorKey(key string) bool {
	return remoteTrackingKey(key) && strings.HasSuffix(key, ".mirror")
}

// remoteTrackingKey matches remote.<name>.url, .pushurl, .fetch, and .push; the
// length check rejects a section-only key such as "remote.pushurl".
func remoteTrackingKey(key string) bool {
	if !strings.HasPrefix(key, "remote.") {
		return false
	}
	for _, suffix := range []string{".url", ".pushurl", ".fetch", ".push", ".mirror"} {
		if strings.HasSuffix(key, suffix) && len(key) > len("remote.")+len(suffix) {
			return true
		}
	}
	return false
}

// branchTrackingKey matches branch.<name>.remote, .merge, and .pushRemote.
func branchTrackingKey(key string) bool {
	if !strings.HasPrefix(key, "branch.") {
		return false
	}
	for _, suffix := range []string{".remote", ".merge", ".pushremote"} {
		if strings.HasSuffix(key, suffix) && len(key) > len("branch.")+len(suffix) {
			return true
		}
	}
	return false
}

// submoduleActivePathspecKey matches the bare multi-valued submodule.active
// pathspec list, which Git consults after the per-name activation booleans
// and before URL presence, so it must round-trip for activation to hold.
func submoduleActivePathspecKey(key string) bool {
	return key == "submodule.active"
}

// urlResetKey matches the multi-valued destination lists whose empty entries
// newer Git versions read as list resets. The replay is verbatim either way,
// so the round trip preserves whatever the host's Git reads. Refspec-shaped
// lists keep their raw sequence instead.
func urlResetKey(key string) bool {
	if !strings.HasPrefix(key, "remote.") {
		return false
	}
	for _, suffix := range []string{".url", ".pushurl"} {
		if strings.HasSuffix(key, suffix) && len(key) > len("remote.")+len(suffix) {
			return true
		}
	}
	return false
}

// submoduleActiveKey matches submodule.<name>.active, a Git boolean where a
// bare key means true and an empty value means false.
func submoduleActiveKey(key string) bool {
	return strings.HasPrefix(key, "submodule.") &&
		strings.HasSuffix(key, ".active") &&
		len(key) > len("submodule.")+len(".active")
}

// partialCloneRemoteKey matches remote.<name>.promisor and
// .partialclonefilter, the remote-based form of a partial clone that may
// appear without the extensions.partialClone marker.
func partialCloneRemoteKey(key string) bool {
	if !strings.HasPrefix(key, "remote.") {
		return false
	}
	for _, suffix := range []string{".promisor", ".partialclonefilter"} {
		if strings.HasSuffix(key, suffix) && len(key) > len("remote.")+len(suffix) {
			return true
		}
	}
	return false
}

// singleValuedTrackingKey matches the tracking settings Git reads as one
// value, where only the final entry takes effect: branch.<name>.remote and
// .pushRemote, and submodule.<name>.url. Refspecs, remote URLs, and merge
// refs are multi-valued and keep their deduplicated sequence.
func singleValuedTrackingKey(key string) bool {
	if strings.HasPrefix(key, "submodule.") &&
		strings.HasSuffix(key, ".url") &&
		len(key) > len("submodule.")+len(".url") {
		return true
	}
	if !strings.HasPrefix(key, "branch.") {
		return false
	}
	for _, suffix := range []string{".remote", ".pushremote"} {
		if strings.HasSuffix(key, suffix) && len(key) > len("branch.")+len(suffix) {
			return true
		}
	}
	return false
}

// submoduleActivationKey matches submodule.<name>.url and .active, the keys
// that keep an initialised submodule initialised once its store and gitfile
// survive the round trip. A store's own config holds the activation keys of
// its nested submodules, so this applies to store capture too.
func submoduleActivationKey(key string) bool {
	if !strings.HasPrefix(key, "submodule.") {
		return false
	}
	for _, suffix := range []string{".url", ".active"} {
		if strings.HasSuffix(key, suffix) && len(key) > len("submodule.")+len(suffix) {
			return true
		}
	}
	return false
}

// isExecutableTransport reports whether a destination names the ext helper,
// whose address is an arbitrary shell command. Other helper names and
// unknown URL schemes dispatch to named binaries Git resolves from the host
// PATH, like any other host-chosen destination, so only ext is rejected.
func isExecutableTransport(value string) bool {
	helper := strings.Index(value, "::")
	if helper < 0 {
		return false
	}
	scheme := strings.Index(value, "://")
	if scheme >= 0 && helper > scheme {
		return false
	}
	return value[:helper] == "ext"
}

// writeGitConfig atomically replaces path with the declared format plus
// entries, written by native Git into a fresh sibling so values are quoted by
// Git and no downloaded key survives.
func writeGitConfig(cwd, path string, format gitConfigFormat, entries []configEntry) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return rejectRepository(fmt.Errorf("git config %s is not a regular file", path))
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading git config %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".gitconfig-*")
	if err != nil {
		return fmt.Errorf("writing git config %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best effort after a successful rename
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing git config %s: %w", path, err)
	}

	values := []configEntry{{Key: "core.repositoryformatversion", Value: format.version}}
	if format.objectFormat != "sha1" {
		values = append(values, configEntry{Key: "extensions.objectFormat", Value: format.objectFormat})
	}
	if format.refStorage != "files" {
		values = append(values, configEntry{Key: "extensions.refStorage", Value: format.refStorage})
	}
	values = append(values, entries...)
	for _, entry := range values {
		if _, err := runBoundedGit(cwd, "", nil, "config", "--file", tmpName, "--add", entry.Key, entry.Value); err != nil {
			return fmt.Errorf("writing git config %s: %w", path, err)
		}
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("writing git config %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("writing git config %s: %w", path, err)
	}
	return nil
}

// rebuildGitConfigs rewrites every collected git config from its declared
// storage format alone, so no downloaded include, hook path, fsmonitor
// command, filter, credential helper or other executable setting reaches a
// native Git invocation on the host. The parent of the download, not the
// untrusted tree, is the command's working directory.
func rebuildGitConfigs(absLocal string, dirs gitConfigDirs, trusted map[string]gitConfigFormat) error {
	cwd := filepath.Dir(absLocal)
	sortedDirs := make([]string, 0, len(dirs))
	for gitDir := range dirs {
		sortedDirs = append(sortedDirs, gitDir)
	}
	sort.Strings(sortedDirs)
	for _, gitDir := range sortedDirs {
		configPath := filepath.Join(gitDir, "config")
		format, err := captureGitConfigFormat(cwd, configPath)
		if err != nil {
			return fmt.Errorf("reading git config format in %s: %w", gitDir, err)
		}
		if trusted != nil {
			// The root and every initialized module store format is host state:
			// a download that declares another one is rejected before any
			// native reftable processing can trust the agent's choice.
			if key, relErr := filepath.Rel(absLocal, gitDir); relErr == nil {
				key := filepath.ToSlash(key)
				want, ok := trusted[key]
				if !ok {
					// A case-insensitive filesystem may name the same trusted
					// git dir with different casing. Match by identity, not
					// lowercased keys: case-sensitive names must stay distinct.
					if dirInfo, dirErr := os.Stat(gitDir); dirErr == nil {
						for trustedKey, candidate := range trusted {
							info, statErr := os.Stat(filepath.Join(absLocal, filepath.FromSlash(trustedKey)))
							if statErr == nil && os.SameFile(dirInfo, info) {
								want, ok = candidate, true
								break
							}
						}
					}
				}
				if ok {
					if _, statErr := os.Lstat(configPath); statErr == nil && format != want {
						return rejectRepository(fmt.Errorf("git config %s declares repository format %s/%s/%s but the trusted checkout is %s/%s/%s",
							configPath, format.version, format.objectFormat, format.refStorage,
							want.version, want.objectFormat, want.refStorage))
					}
					format = want
				}
			}
		}
		if err := writeGitConfig(cwd, configPath, format, nil); err != nil {
			return err
		}
	}
	return nil
}

// rejectWorktreeRegistry fails closed when a git dir holds a registered
// worktree registry, matching the download walk's rejection: any entry named
// worktrees, case-insensitively. Capture uses it to fail the run before
// upload instead of after the agent has run.
func rejectWorktreeRegistry(gitDir string) error {
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		return fmt.Errorf("inspecting git metadata %s: %w", gitDir, err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), "worktrees") {
			return fmt.Errorf("unsupported git worktree metadata %s", filepath.Join(gitDir, "worktrees"))
		}
	}
	return nil
}

// captureStoreConfigs captures the trusted config of every module store of a
// checkout, keyed by the store path relative to the checkout root.
func captureStoreConfigs(repoDir string) (map[string]storeSnapshot, error) {
	modules := filepath.Join(repoDir, ".git", "modules")
	info, err := os.Lstat(modules)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("module store path %s is a symlink", modules)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("module store path %s is not a directory", modules)
	}
	cwd := filepath.Dir(repoDir)
	stores := make(map[string]storeSnapshot)
	err = filepath.WalkDir(modules, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// A symlinked entry under modules/ points Git at metadata this
		// capture never reads, and the download walk rejects it; fail
		// before upload with the same rule.
		if path != modules && d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("module store path %s is a symlink", path)
		}
		// A directory under modules/ that holds a HEAD entry but not Git's
		// store layout is a store the download walk rejects as
		// unrecognized. Metadata directories inside a recognized store
		// (its logs hold a HEAD too) sit under a supported parent and are
		// excluded by that guard, matching the walk's own skip rule.
		if d.IsDir() && path != modules && !isSupportedModuleGitDir(filepath.Dir(path)) &&
			hasDirectEntry(path, "HEAD") && !isSupportedModuleGitDir(path) {
			return fmt.Errorf("unsupported module store layout %s", path)
		}
		if !d.IsDir() || !isSupportedModuleGitDir(path) {
			// Store metadata directories cannot hold a nested store, so their
			// contents (every loose object among them) need no walk. A
			// directory named like one may still be a store itself
			// (.git/modules/objects), so skip only inside a recognized store.
			if d.IsDir() && isSupportedModuleGitDir(filepath.Dir(path)) {
				switch strings.ToLower(d.Name()) {
				case "objects", "refs", "logs", "info", "hooks", "worktrees", "reftable":
					return filepath.SkipDir
				}
			}
			return nil
		}
		key, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			return relErr
		}
		if err := rejectWorktreeRegistry(path); err != nil {
			return fmt.Errorf("capturing trusted config of %s: %w", key, err)
		}
		configPath := filepath.Join(path, "config")
		format, formatErr := captureGitConfigFormat(cwd, configPath)
		if formatErr != nil {
			return fmt.Errorf("capturing trusted format of %s: %w", key, formatErr)
		}
		configPaths, pathsErr := trustedConfigPaths(cwd, path)
		if pathsErr != nil {
			return fmt.Errorf("capturing trusted config of %s: %w", key, pathsErr)
		}
		// A module store's relative destination is resolved by Git against the
		// submodule worktree, not the superproject root, so an empty base fails
		// closed rather than rebasing it to the wrong path.
		entries, configErr := captureTrustedConfig(cwd, "", configPaths...)
		if configErr != nil {
			return fmt.Errorf("capturing trusted config of %s: %w", key, configErr)
		}
		shallow, shallowErr := captureShallowState(path)
		if shallowErr != nil {
			return fmt.Errorf("capturing trusted shallow boundary of %s: %w", key, shallowErr)
		}
		stores[filepath.ToSlash(key)] = storeSnapshot{format: format, config: entries, shallow: shallow}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stores, nil
}

// restoreTrustedStoreConfigs replays each trusted module store config into the
// downloaded store, so submodule remotes and branch tracking survive. A store
// the download no longer has is skipped.
func restoreTrustedStoreConfigs(localDir string, stores map[string]storeSnapshot) error {
	keys := make([]string, 0, len(stores))
	for key := range stores {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cwd := filepath.Dir(localDir)
	for _, key := range keys {
		dir := filepath.Join(localDir, filepath.FromSlash(key))
		if dir == localDir || !inDir(dir, localDir) {
			return rejectRepository(fmt.Errorf("restoring trusted module store: %s is outside the download", key))
		}
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("restoring trusted module store %s: %w", key, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return rejectRepository(fmt.Errorf("restoring trusted module store: %s is not a real directory", key))
		}
		store := stores[key]
		if err := writeGitConfig(cwd, filepath.Join(dir, "config"), store.format, store.config); err != nil {
			return fmt.Errorf("restoring trusted module store %s: %w", key, err)
		}
		if err := restoreTrustedShallowAt(dir, store.shallow); err != nil {
			return fmt.Errorf("restoring trusted module store %s: %w", key, err)
		}
	}
	return nil
}

// restoreTrustedConfig rewrites the downloaded root config with the trusted
// storage format plus the captured safe values. Nothing is copied from the
// download, and a plain input has no trusted values so its config is left as
// the sanitizer rebuilt it.
func restoreTrustedConfig(localDir string, snapshot RepositorySnapshot) error {
	if len(snapshot.config) == 0 && snapshot.format == (gitConfigFormat{}) {
		return nil
	}
	gitDir := filepath.Join(localDir, ".git")
	info, err := os.Lstat(gitDir)
	if errors.Is(err, fs.ErrNotExist) && snapshot.isCheckout {
		return rejectRepository(fmt.Errorf("downloaded .git directory %s is missing but the input was a git checkout; a checkout input must remain a git checkout", gitDir))
	}
	if err != nil || !info.IsDir() {
		return rejectRepository(fmt.Errorf("restoring trusted git config: %s is not a real git directory", gitDir))
	}
	if err := writeGitConfig(filepath.Dir(localDir), filepath.Join(gitDir, "config"), snapshot.format, snapshot.config); err != nil {
		return fmt.Errorf("restoring trusted git config: %w", err)
	}
	return nil
}

// shallowState is a repository's root git shallow state: whether a shallow
// file exists and its exact bytes. Git treats the commits listed there as
// having no parents, so the file bounds the visible history.
type shallowState struct {
	Present bool
	Data    []byte
}

// RepositorySnapshot captures selected host repository state before upload for
// validating or restoring downloaded output. It is not a copy of repository
// contents. isCheckout records that the host input was a real .git checkout;
// config and format hold the narrow allowlisted local values and the declared
// storage format restored after sanitization.
type RepositorySnapshot struct {
	shallow    shallowState
	isCheckout bool
	config     []configEntry
	format     gitConfigFormat
	stores     map[string]storeSnapshot
}

// restoreTrustedShallow replaces the downloaded repository's root .git/shallow
// with the trusted snapshot. The sandbox may remove the boundary (unshallow),
// but any boundary entry it added fails closed because only the host's
// pre-upload cutoffs may survive. Non-regular entries are rejected rather than
// written through, and a trusted marker without a real .git directory fails
// closed rather than creating git metadata in a non-repository.
func restoreTrustedShallow(localDir string, trusted shallowState) error {
	return restoreTrustedShallowAt(filepath.Join(localDir, ".git"), trusted)
}

// restoreTrustedShallowAt is restoreTrustedShallow for one explicit git dir:
// the root .git or any module store.
func restoreTrustedShallowAt(gitDir string, trusted shallowState) error {
	path := filepath.Join(gitDir, "shallow")

	info, err := os.Lstat(path)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return rejectRepository(fmt.Errorf("git shallow metadata %s is not a regular file", path))
	case err == nil:
		returned, oversized, err := readBoundedFile(path, maxShallowBytes)
		if err != nil {
			return fmt.Errorf("reading downloaded git shallow metadata %s: %w", path, err)
		}
		if oversized {
			return rejectRepository(fmt.Errorf("downloaded git shallow metadata %s is larger than %d bytes", path, maxShallowBytes))
		}
		// A sandbox fetch --depth may add cutoffs the host never trusted, so
		// every returned boundary entry must already be in the trusted set.
		allowed := make(map[string]bool)
		if trusted.Present {
			for _, entry := range strings.Fields(string(trusted.Data)) {
				allowed[entry] = true
			}
		}
		for _, entry := range strings.Fields(string(returned)) {
			if !allowed[entry] {
				return rejectRepository(fmt.Errorf("unsupported sandbox shallow boundary change in %s: returned shallow cutoffs were not in the trusted snapshot; sandbox fetch --depth or --deepen must not introduce new boundaries", path))
			}
		}
		if !trusted.Present {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("removing untrusted git shallow metadata %s: %w", path, err)
			}
			return nil
		}
		return replaceFileContents(path, trusted.Data)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("inspecting git shallow metadata %s: %w", path, err)
	}

	if !trusted.Present {
		return nil
	}
	gitInfo, err := os.Lstat(gitDir)
	if err != nil {
		return fmt.Errorf("restoring trusted git shallow metadata: %w", err)
	}
	if !gitInfo.IsDir() {
		return fmt.Errorf("restoring trusted git shallow metadata: %s is not a directory", gitDir)
	}
	if err := os.WriteFile(path, trusted.Data, 0o644); err != nil {
		return fmt.Errorf("writing trusted git shallow metadata %s: %w", path, err)
	}
	return nil
}

// captureShallowState reads one git dir's shallow boundary: whether the file
// exists and its exact bytes, bounded and regular like the root's.
func captureShallowState(gitDir string) (shallowState, error) {
	path := filepath.Join(gitDir, "shallow")
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return shallowState{}, nil
		}
		return shallowState{}, fmt.Errorf("inspecting git shallow metadata %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return shallowState{}, fmt.Errorf("git shallow metadata %s is not a regular file", path)
	}
	data, oversized, err := readBoundedFile(path, maxShallowBytes)
	if err != nil {
		return shallowState{}, fmt.Errorf("reading git shallow metadata %s: %w", path, err)
	}
	if oversized {
		return shallowState{}, fmt.Errorf("git shallow metadata %s is larger than %d bytes", path, maxShallowBytes)
	}
	return shallowState{Present: true, Data: data}, nil
}

// resolvedExcludes joins each upload exclude to the walk root, the form the
// walks compare against.
func resolvedExcludes(root string, excludes []string) []string {
	dirs := make([]string, 0, len(excludes))
	for _, rel := range excludes {
		dirs = append(dirs, filepath.Join(root, rel))
	}
	return dirs
}

// checkNestedLayouts walks a target worktree once for repository layouts the
// download walk rejects only after the agent has run: a Git-shaped directory
// under a name other than .git, such as a committed bare fixture; a nested
// .git entry — a directory (a vendored clone), or an entry that is neither a
// real directory nor a gitfile (a symlink); and, for a checkout, a gitfile
// whose target lies outside the target's own module store. With
// anyGitEntry, the plain-target rule, every .git entry counts as nested,
// including gitfiles. skipDirs names directories not to enter: the
// checkout's own .git and upload-excluded output directories. modulesDir is
// empty for a plain target, which rejects every .git entry outright. The
// nested finding is returned for the caller to word; gitfile validation
// runs after the walk.
func checkNestedLayouts(root string, skipDirs []string, anyGitEntry bool, modulesDir string) (nested string, err error) {
	var gitfiles []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		for _, skip := range skipDirs {
			if path == skip {
				return filepath.SkipDir
			}
		}
		if path != root && strings.EqualFold(d.Name(), "HEAD") {
			shaped, shapeErr := headExposesGitDir(path, func(dir string) bool {
				return dir == root
			})
			if shapeErr != nil {
				return shapeErr
			}
			if shaped {
				return fmt.Errorf("unsupported bare-style Git directory %s", filepath.Dir(path))
			}
		}
		if path == root || !strings.EqualFold(d.Name(), ".git") {
			return nil
		}
		// A case variant is nested Git metadata only when Git itself would
		// find it, so require the lowercase name to resolve to this entry.
		lowerInfo, lowerErr := os.Lstat(filepath.Join(filepath.Dir(path), ".git"))
		if lowerErr != nil {
			if errors.Is(lowerErr, fs.ErrNotExist) {
				return nil
			}
			return lowerErr
		}
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			return infoErr
		}
		if !os.SameFile(lowerInfo, info) {
			return nil
		}
		// A nested .git that is neither a real directory nor a gitfile — a
		// symlink or another special entry — redirects Git at metadata this
		// walk never inspects, and the download walk rejects it.
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("repository git metadata %s is a symlink", path)
		}
		if info.IsDir() || anyGitEntry {
			nested = path
			return fs.SkipAll
		}
		// A checkout's gitfile is the supported submodule layout; validate
		// its target after the walk.
		if modulesDir != "" {
			gitfiles = append(gitfiles, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, gitfile := range gitfiles {
		if err := checkGitfileTarget(filepath.Dir(root), gitfile, modulesDir); err != nil {
			return "", err
		}
	}
	return nested, nil
}

// hasDirectEntry reports whether dir holds a direct entry matching name
// case-insensitively.
func hasDirectEntry(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return true
		}
	}
	return false
}

// checkGitfileTarget fails closed when a worktree gitfile's target lies
// outside the target's own module store, the layout the download walk's
// gitfile validation rejects after the agent has run. Resolution is shared
// with the download walk, so both agree on where the gitfile points.
func checkGitfileTarget(cwd, gitfile, modulesDir string) error {
	resolvedModules, err := filepath.EvalSymlinks(modulesDir)
	if err != nil {
		return fmt.Errorf("inspecting module store %s: %w", modulesDir, err)
	}
	resolved, err := resolveGitfileDir(cwd, gitfile)
	if err != nil {
		return err
	}
	if !inDir(resolved, resolvedModules) {
		return fmt.Errorf("unsupported gitfile %s: target %s is outside the target's module store", gitfile, resolved)
	}
	return nil
}

// CaptureRepositorySnapshot validates repoDir and captures the trusted host
// state that SafeDownload reconciles against: the root shallow boundary, the
// allowlisted local config values, and the declared storage format. A plain
// input returns a zero snapshot without Git discovery, so a parent checkout is
// never captured. A plain input containing a nested checkout is rejected
// instead of being snapshotted as plain. Includes are not followed and no
// repository hook or helper is executed. excludes names top-level directories
// the upload omits, matching UploadDir; their layouts are not walked.
func CaptureRepositorySnapshot(repoDir string, excludes ...string) (RepositorySnapshot, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return RepositorySnapshot{}, err
	}
	plain, err := ValidateRepositoryRoot(absRepo)
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf("validating target repository: %w", err)
	}
	// WalkDir does not follow a symlinked root, but the upload path does, so
	// resolve the target first: nested layouts cannot hide behind a symlinked
	// target path. Upload-excluded directories never round-trip, so their
	// layouts do not matter; skip them like the upload does.
	resolvedRoot, err := filepath.EvalSymlinks(absRepo)
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf("checking target repository %s: %w", absRepo, err)
	}
	skipDirs := resolvedExcludes(resolvedRoot, excludes)
	if plain {
		nested, walkErr := checkNestedLayouts(resolvedRoot, skipDirs, true, "")
		if walkErr != nil {
			return RepositorySnapshot{}, rejectRepository(fmt.Errorf("checking plain repository %s: %w", absRepo, walkErr))
		}
		if nested != "" {
			return RepositorySnapshot{}, rejectRepository(fmt.Errorf("plain repository %s contains nested Git metadata %s; use a normal .git checkout or a plain directory without nested checkouts", absRepo, nested))
		}
		return RepositorySnapshot{}, nil
	}
	snapshot := RepositorySnapshot{isCheckout: true}

	// Fail before upload on registered worktrees the download walk would
	// reject after the whole run: their private directories carry hooks,
	// per-worktree config, and commondir redirects the walk does not
	// sanitize. The walk still rejects any worktree the agent plants.
	if err := rejectWorktreeRegistry(filepath.Join(absRepo, ".git")); err != nil {
		return RepositorySnapshot{}, err
	}

	// A nested .git directory the host already carries — a vendored clone,
	// an unabsorbed embedded repository — is not a supported submodule
	// layout, and the download round trip would strip its config and corrupt
	// its shallow state, so fail before upload like a plain target does.
	// Gitfile submodules are the supported layout and are skipped here. The
	// walk also skips the checkout's own .git.
	skipDirs = append(skipDirs, filepath.Join(resolvedRoot, ".git"))
	nested, nestedErr := checkNestedLayouts(resolvedRoot, skipDirs, false, filepath.Join(resolvedRoot, ".git", "modules"))
	if nestedErr != nil {
		return RepositorySnapshot{}, rejectRepository(fmt.Errorf("checking checkout %s: %w", absRepo, nestedErr))
	}
	if nested != "" {
		return RepositorySnapshot{}, rejectRepository(fmt.Errorf("checkout %s contains nested Git directory %s; a checkout target supports submodules through .git/modules, not nested checkouts", absRepo, nested))
	}

	// Validation proved .git is a real directory that resolves as itself, so
	// the boundary lives directly there. Resolving it with a Git query would
	// honour inherited selectors such as GIT_DIR or GIT_COMMON_DIR.
	shallow, shallowErr := captureShallowState(filepath.Join(absRepo, ".git"))
	if shallowErr != nil {
		return RepositorySnapshot{}, shallowErr
	}
	snapshot.shallow = shallow

	cwd := filepath.Dir(absRepo)
	configPath := filepath.Join(absRepo, ".git", "config")
	// A per-worktree config is read only when Git itself would read it, and its
	// values are merged in Git's precedence order so push destinations it sets
	// are not silently lost.
	configPaths, err := trustedConfigPaths(cwd, filepath.Join(absRepo, ".git"))
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf("capturing trusted worktree config: %w", err)
	}
	if snapshot.config, err = captureTrustedConfig(cwd, absRepo, configPaths...); err != nil {
		return RepositorySnapshot{}, fmt.Errorf("capturing trusted repository config: %w", err)
	}
	if snapshot.format, err = captureGitConfigFormat(cwd, configPath); err != nil {
		return RepositorySnapshot{}, fmt.Errorf("capturing trusted repository format: %w", err)
	}
	if snapshot.stores, err = captureStoreConfigs(absRepo); err != nil {
		return RepositorySnapshot{}, fmt.Errorf("capturing trusted module store config: %w", err)
	}
	return snapshot, nil
}
