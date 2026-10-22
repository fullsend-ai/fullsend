package repos

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// This file implements `fullsend repos set`: a schema-aware editor for any
// writable repos.yaml path. Paths are resolved against the Manifest struct
// definitions (and the managed config schema under `config`), so there is
// no separate list of settable keys. Edits are applied to the YAML node
// tree and spliced into the original bytes so unrelated values, comments,
// and ordering are preserved. The resulting manifest is fully validated
// before anything is written, so a failed edit leaves the file untouched.

// SetManifestValue sets the value at path in the manifest file at
// manifestPath. value is interpreted according to the target field's type:
// strings are taken literally, booleans and numbers are parsed, and lists,
// maps, and objects accept YAML or JSON. A missing manifest is created with
// version: 1. It reports whether the file changed.
func SetManifestValue(manifestPath, path, value string) (bool, error) {
	return editManifestValue(manifestPath, path, &value)
}

// UnsetManifestValue removes the key at path from the manifest file at
// manifestPath. Removing a key that is already absent is not an error and
// reports false.
func UnsetManifestValue(manifestPath, path string) (bool, error) {
	return editManifestValue(manifestPath, path, nil)
}

func editManifestValue(manifestPath, path string, value *string) (bool, error) {
	segs, err := parseManifestPath(path)
	if err != nil {
		return false, err
	}
	rp, err := resolveManifestPath(path, segs, value == nil)
	if err != nil {
		return false, err
	}
	// Resolve the destination once, so the read-only check, the read, the
	// validation context, and the write all name the same file.
	requested := manifestPath
	if ManifestReadOnly(&Manifest{}, requested) { // also rejects http(s) sources
		return false, readOnlyManifestError(requested)
	}
	manifestPath, err = resolveManifestTarget(requested)
	if err != nil {
		return false, err
	}
	if ManifestReadOnly(&Manifest{}, manifestPath) {
		return false, readOnlyManifestError(requested)
	}

	var newVal *yaml.Node
	if value != nil {
		last := rp.steps[len(rp.steps)-1]
		if last.kind == stepEntry {
			newVal, err = entryValueNode(*value, last.key, path)
		} else {
			newVal, err = valueNode(rp.typ, *value, path)
		}
		if err != nil {
			return false, err
		}
		if err := checkForgeFields(rp.forge, rp.typ, newVal, path); err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	}

	orig, exists, err := readManifestForEdit(manifestPath)
	if err != nil {
		return false, err
	}
	if !exists && value == nil {
		// Absent file: removing a whole entry or plain key is a no-op, but a
		// path through a repository entry that cannot exist is still a typo,
		// as it is when the file exists without that entry.
		for i, st := range rp.steps[:len(rp.steps)-1] {
			if st.kind == stepEntry {
				return false, missingEntryError(path, rp.steps[:i+1])
			}
		}
		return false, nil
	}

	ed, err := newManifestEditor(orig)
	if err != nil {
		return false, fmt.Errorf("parsing manifest: %w", err)
	}
	plan, err := ed.plan(path, rp.steps, newVal)
	if err != nil {
		return false, err
	}
	clearsLegacyTags := path == agentRunnerTagsPath
	if plan == nil && !clearsLegacyTags {
		return false, nil
	}
	out := orig
	if plan != nil {
		out, err = ed.apply(plan)
		if err != nil {
			return false, err
		}
	}
	if clearsLegacyTags {
		// The deprecated gitlab.runner_tags alias refills an empty
		// agent_runner_tags on load, so an edit of the new key must remove
		// it too, as the old set-default command did.
		out, err = dropLegacyRunnerTags(out)
		if err != nil {
			return false, err
		}
	}
	if exists && bytes.Equal(out, orig) {
		return false, nil
	}
	if int64(len(out)) > maxManifestBytes {
		return false, fmt.Errorf("%s: resulting manifest would exceed the maximum size of %d bytes", path, maxManifestBytes)
	}

	var m Manifest
	if err := parseManifestBytes(out, &m); err != nil {
		return false, fmt.Errorf("%s: resulting manifest is invalid: %w", path, err)
	}
	// Validate local config_base sources against the directory of the path the
	// caller passed in, as LoadManifest does, not the symlink target's
	// directory: consumers load the manifest through the requested path.
	if abs, absErr := filepath.Abs(requested); absErr == nil {
		m.sourceDir = filepath.Dir(abs)
	}
	if err := m.Validate(); err != nil {
		return false, fmt.Errorf("%s: resulting manifest is invalid: %w", path, err)
	}

	if err := writeManifestAtomic(manifestPath, out); err != nil {
		return false, fmt.Errorf("writing manifest: %w", err)
	}
	return true, nil
}

// agentRunnerTagsPath is the path whose edits must also clear the deprecated
// gitlab.runner_tags alias.
const agentRunnerTagsPath = "gitlab.agent_runner_tags"

// dropLegacyRunnerTags removes gitlab.runner_tags from the manifest bytes in
// data, leaving them unchanged when the key is absent.
func dropLegacyRunnerTags(data []byte) ([]byte, error) {
	const legacyPath = "gitlab.runner_tags"
	segs, err := parseManifestPath(legacyPath)
	if err != nil {
		return nil, err
	}
	rp, err := resolveManifestPath(legacyPath, segs, true)
	if err != nil {
		return nil, err
	}
	ed, err := newManifestEditor(data)
	if err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	plan, err := ed.plan(legacyPath, rp.steps, nil)
	if err != nil || plan == nil {
		return data, err
	}
	return ed.apply(plan)
}

// writeManifestAtomic replaces the manifest with data by writing a temp file
// in the same directory and renaming it into place, so a failed write leaves
// the original intact. A symlinked manifest is replaced at its target, and
// the existing permission bits are kept. Group ownership and ACLs are not
// preserved: the replacement belongs to the invoking user and the directory's
// group, as with any create-and-rename write.
func writeManifestAtomic(manifestPath string, data []byte) error {
	target, err := resolveManifestTarget(manifestPath)
	if err != nil {
		return err
	}
	// An existing manifest keeps its permission bits; a new one gets 0644
	// filtered by the process umask, like a plain os.WriteFile.
	var mode os.FileMode
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	} else if mode, err = newManifestMode(filepath.Dir(target)); err != nil {
		return err
	}

	// The temp file stays private (0600) while it is written; it is given
	// its final mode only afterwards, so a restrictive manifest is never
	// readable through a more permissive temp file.
	tmp, err := createManifestTemp(filepath.Dir(target), 0o600)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// newManifestMode returns the mode a newly created manifest in dir gets:
// 0644 filtered by the process umask. It creates and removes an empty probe
// file, since the umask cannot be read without changing it.
func newManifestMode(dir string) (os.FileMode, error) {
	f, err := createManifestTemp(dir, 0o644)
	if err != nil {
		return 0, err
	}
	name := f.Name()
	defer os.Remove(name)
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}

// createManifestTemp exclusively creates a temp file in dir with the given
// mode filtered by the process umask.
func createManifestTemp(dir string, mode os.FileMode) (*os.File, error) {
	for range 100 {
		name := filepath.Join(dir, fmt.Sprintf(".repos.yaml.tmp.%d", rand.Uint64()))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, errors.New("creating temporary manifest: too many name collisions")
}

// resolveManifestTarget returns the file a write should replace: the final
// destination of any symlink chain, including a dangling link whose target
// does not exist yet, so the link itself is never replaced. Only the final
// component may be missing; a missing or non-directory intermediate is an
// error.
func resolveManifestTarget(manifestPath string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(manifestPath); err == nil {
		return resolved, nil
	}
	// EvalSymlinks failed because something is missing. Resolve the path one
	// component at a time, like realpath(3), so ".." follows the symlinked
	// directory it comes after rather than being collapsed lexically; the
	// components from the first missing one on are kept as written.
	//
	// The path is made absolute by prefixing the working directory as-is:
	// filepath.Abs would Clean it and collapse ".." lexically first.
	abs := manifestPath
	if !filepath.IsAbs(abs) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		abs = wd + string(filepath.Separator) + abs
	}
	splitPath := func(p string) []string {
		return strings.FieldsFunc(p, func(r rune) bool { return r == filepath.Separator })
	}
	vol := filepath.VolumeName(abs)
	resolved := vol + string(filepath.Separator)
	queue := splitPath(abs[len(vol):])
	links := 0
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		switch c {
		case ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		info, err := os.Lstat(next)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			// Only the final destination may be missing. Resolving a later
			// ".." through a missing directory would collapse it lexically and
			// name a file the read never looked at.
			if len(queue) > 0 {
				return "", fmt.Errorf("manifest %s: %s does not exist", manifestPath, next)
			}
			resolved = next
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if len(queue) > 0 && !info.IsDir() {
				return "", fmt.Errorf("manifest %s: %s is not a directory", manifestPath, next)
			}
			resolved = next
			continue
		}
		if links++; links > 40 {
			return "", fmt.Errorf("manifest %s: too many levels of symbolic links", manifestPath)
		}
		link, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(link) {
			lvol := filepath.VolumeName(link)
			resolved = lvol + string(filepath.Separator)
			link = link[len(lvol):]
		}
		queue = append(splitPath(link), queue...)
	}
	return resolved, nil
}

func readOnlyManifestError(path string) error {
	return fmt.Errorf("manifest %s is read-only; apply the equivalent edit to its source, then re-run", path)
}

// readManifestForEdit reads a local manifest with the same size limit
// LoadManifest enforces. A missing file reports exists=false.
func readManifestForEdit(path string) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading manifest: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("reading manifest: %w", err)
	}
	if int64(len(data)) > maxManifestBytes {
		return nil, false, fmt.Errorf("manifest file %s exceeds maximum size of %d bytes", path, maxManifestBytes)
	}
	return data, true, nil
}

// ---------------------------------------------------------------------------
// Path grammar
//
//	path     = name { "." name | selector }
//	name     = 1*( ALPHA / DIGIT / "_" / "-" )
//	selector = "[" quoted "]"      ; Go/JSON double-quoted string
//
// A selector picks a repository entry by its exact name
// (github.repos["acme/api"]) or a key of a map-valued field. Because the
// selector is quoted, entry names may contain dots, globs, and brackets.

type pathSeg struct {
	name     string
	selector bool
}

func parseManifestPath(p string) ([]pathSeg, error) {
	if p == "" {
		return nil, errors.New("path is required")
	}
	var segs []pathSeg
	expectName := true
	for i := 0; i < len(p); {
		switch c := p[i]; {
		case c == '[':
			if len(segs) == 0 {
				return nil, fmt.Errorf("invalid path %q: a [\"...\"] selector must follow a field name", p)
			}
			quoted, err := strconv.QuotedPrefix(p[i+1:])
			if err != nil || !strings.HasPrefix(quoted, `"`) {
				return nil, fmt.Errorf("invalid path %q: the selector at offset %d must be a double-quoted string, e.g. repos[\"owner/repo\"]", p, i)
			}
			name, err := strconv.Unquote(quoted)
			if err != nil {
				return nil, fmt.Errorf("invalid path %q: %w", p, err)
			}
			j := i + 1 + len(quoted)
			if j >= len(p) || p[j] != ']' {
				return nil, fmt.Errorf("invalid path %q: missing ] after selector %s", p, quoted)
			}
			if name == "" {
				return nil, fmt.Errorf("invalid path %q: selector must not be empty", p)
			}
			segs = append(segs, pathSeg{name: name, selector: true})
			i = j + 1
			expectName = false
		case c == '.':
			if expectName {
				return nil, fmt.Errorf("invalid path %q: empty field name at offset %d", p, i)
			}
			expectName = true
			i++
		default:
			if !expectName {
				return nil, fmt.Errorf("invalid path %q: expected '.' or '[' at offset %d", p, i)
			}
			j := i
			for j < len(p) && isPathNameChar(p[j]) {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("invalid path %q: unexpected character %q at offset %d", p, c, i)
			}
			segs = append(segs, pathSeg{name: p[i:j]})
			i = j
			expectName = false
		}
	}
	if expectName {
		return nil, fmt.Errorf("invalid path %q: path must not end with '.'", p)
	}
	return segs, nil
}

func isPathNameChar(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ---------------------------------------------------------------------------
// Schema resolution

type stepKind int

const (
	stepField stepKind = iota // mapping key (struct field or map key)
	stepEntry                 // repository entry selected by exact name
)

type pathStep struct {
	kind stepKind
	key  string
}

type resolvedPath struct {
	steps []pathStep
	typ   reflect.Type // Go type of the target value
	forge string       // "github" or "gitlab" under a forge section, else ""
}

var (
	manifestType        = reflect.TypeOf(Manifest{})
	defaultsType        = reflect.TypeOf(DefaultsConfig{})
	platformType        = reflect.TypeOf(PlatformConfig{})
	repoEntryType       = reflect.TypeOf(RepoEntry{})
	managedConfigType   = reflect.TypeOf(config.ManagedConfig{})
	yamlUnmarshalerType = reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()
)

// schemaType dereferences pointers and maps the opaque ManagedConfig
// wrapper onto the struct that defines its fields.
func schemaType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == managedConfigType {
		return config.ManagedConfigSchema()
	}
	return t
}

func resolveManifestPath(raw string, segs []pathSeg, unset bool) (*resolvedPath, error) {
	t := manifestType
	var steps []pathStep
	prefix := ""
	forge := ""
	for i, s := range segs {
		if i == 0 && !s.selector && (s.name == ForgeGitHub || s.name == ForgeGitLab) {
			forge = s.name
		}
		st := schemaType(t)
		if s.selector {
			switch {
			case st.Kind() == reflect.Slice && st.Elem() == repoEntryType:
				steps = append(steps, pathStep{kind: stepEntry, key: s.name})
				t = repoEntryType
			case st.Kind() == reflect.Map && st.Key().Kind() == reflect.String:
				steps = append(steps, pathStep{kind: stepField, key: s.name})
				t = st.Elem()
			default:
				return nil, fmt.Errorf("%s: %s does not accept a [\"...\"] selector; selectors pick repository entries (repos[\"owner/repo\"]) or map keys", raw, prefix)
			}
			prefix += "[" + strconv.Quote(s.name) + "]"
			continue
		}
		switch {
		case st.Kind() == reflect.Struct && !reflect.PointerTo(st).Implements(yamlUnmarshalerType):
			f, ok := yamlField(st, s.name)
			if !ok {
				return nil, unknownFieldError(raw, prefix, st, s.name)
			}
			if msg := nonWritableField(st, s.name, prefix, unset); msg != "" {
				return nil, fmt.Errorf("%s: %s", raw, msg)
			}
			if !unset {
				// --unset stays allowed so an invalid key already in the
				// file can still be removed.
				if err := forgeFieldError(forge, st, s.name, prefix); err != nil {
					return nil, fmt.Errorf("%s: %w", raw, err)
				}
			}
			steps = append(steps, pathStep{kind: stepField, key: s.name})
			t = f.Type
		case st.Kind() == reflect.Map && st.Key().Kind() == reflect.String:
			steps = append(steps, pathStep{kind: stepField, key: s.name})
			t = st.Elem()
		case st.Kind() == reflect.Slice && st.Elem() == repoEntryType:
			return nil, fmt.Errorf("%s: %s is a list of repository entries; select one with %s[\"owner/repo\"]", raw, prefix, prefix)
		case st.Kind() == reflect.Slice:
			return nil, fmt.Errorf("%s: %s is a list; set the whole list as a YAML or JSON value instead of addressing its elements", raw, prefix)
		default:
			return nil, fmt.Errorf("%s: %s is a %s value and has no field %q", raw, prefix, st.Kind(), s.name)
		}
		prefix = joinPath(prefix, s.name)
	}
	return &resolvedPath{steps: steps, typ: t, forge: forge}, nil
}

// githubOnlyFields are the PlatformConfig and RepoEntry fields that only
// apply to GitHub; gitlabOnlyFields are the PlatformConfig fields that only
// apply to GitLab.
var (
	githubOnlyFields = []string{"mint_url", "mint_mode", "app_set"}
	gitlabOnlyFields = []string{"agent_runner_tags", "control_runner_tags", "runner_tags"}
)

// forgeFieldError reports a field that the forge section does not support,
// whatever its value, mirroring the wrong-forge checks in Manifest.Validate
// (which only see nonempty values). prefix is the path of the struct t.
func forgeFieldError(forge string, t reflect.Type, name, prefix string) error {
	full := joinPath(prefix, name)
	switch {
	case t == platformType && forge == ForgeGitLab && slices.Contains(githubOnlyFields, name):
		return fmt.Errorf("%s is not supported; %s is a GitHub-only field", full, name)
	case t == platformType && forge == ForgeGitHub && slices.Contains(gitlabOnlyFields, name):
		return fmt.Errorf("%s is not supported; %s is a GitLab-only field", full, name)
	case t == repoEntryType && forge == ForgeGitLab && slices.Contains(githubOnlyFields, name):
		return fmt.Errorf("%s is only supported for GitHub repos", full)
	}
	return nil
}

// checkForgeFields applies forgeFieldError to every key of a whole-object
// value n for a target of type t (a forge section, a repository list, or a
// repository entry), so objects cannot carry fields the forge rejects.
func checkForgeFields(forge string, t reflect.Type, n *yaml.Node, prefix string) error {
	if forge == "" {
		return nil
	}
	st := schemaType(t)
	switch {
	case st == platformType && n.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if err := forgeFieldError(forge, st, key, prefix); err != nil {
				return err
			}
			if msg := nonWritableField(st, key, prefix, false); msg != "" {
				return errors.New(msg)
			}
			if key == "repos" {
				if err := checkForgeFields(forge, reflect.TypeFor[[]RepoEntry](), n.Content[i+1], joinPath(prefix, "repos")); err != nil {
					return err
				}
			}
		}
	case st == repoEntryType && n.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if err := forgeFieldError(forge, st, n.Content[i].Value, prefix); err != nil {
				return err
			}
		}
	case st.Kind() == reflect.Slice && st.Elem() == repoEntryType && n.Kind == yaml.SequenceNode:
		for i, el := range n.Content {
			if err := checkForgeFields(forge, repoEntryType, el, fmt.Sprintf("%s[%d]", prefix, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func yamlFieldName(f reflect.StructField) string {
	if f.PkgPath != "" {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if name == "-" {
		return ""
	}
	return name
}

func yamlField(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if n := yamlFieldName(f); n != "" && n == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// nonWritableField explains why a schema field cannot be edited with
// repos set, or returns "" when it can.
func nonWritableField(t reflect.Type, name, prefix string, unset bool) string {
	switch {
	case t == manifestType && name == "version":
		return "version is fixed at 1 and cannot be changed"
	case t == platformType && name == "runner_tags" && !unset:
		return fmt.Sprintf("%s.runner_tags is a deprecated alias; use %s.agent_runner_tags (--unset %s.runner_tags removes the old key)", prefix, prefix, prefix)
	case t == repoEntryType && name == "name":
		if unset {
			return fmt.Sprintf("name is required and cannot be removed; unset %s to remove the whole entry", prefix)
		}
		return fmt.Sprintf("the entry name comes from the selector; set the whole entry to create one under another name (for example %s)", prefix)
	}
	return ""
}

func writableFields(t reflect.Type, prefix string) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		n := yamlFieldName(t.Field(i))
		if n != "" && nonWritableField(t, n, prefix, false) == "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func hasConfigField(t reflect.Type) bool {
	f, ok := yamlField(t, "config")
	return ok && f.Type == managedConfigType
}

func unknownFieldError(raw, prefix string, t reflect.Type, name string) error {
	if hasConfigField(t) && slices.Contains(removedConfigSiblingKeys, name) {
		return fmt.Errorf("%s: %s is no longer supported; use %s", raw, joinPath(prefix, name), joinPath(joinPath(prefix, "config"), name))
	}
	var hints []string
	if hasConfigField(t) {
		if _, ok := yamlField(config.ManagedConfigSchema(), name); ok {
			hints = append(hints, joinPath(joinPath(prefix, "config"), name))
		}
	}
	if t != defaultsType {
		if _, ok := yamlField(defaultsType, name); ok {
			hints = append(hints, "defaults."+name)
		}
	}
	if t != platformType {
		if _, ok := yamlField(platformType, name); ok && nonWritableField(platformType, name, "", false) == "" {
			hints = append(hints, ForgeGitHub+"."+name, ForgeGitLab+"."+name)
		}
	}
	if t != repoEntryType {
		if _, ok := yamlField(repoEntryType, name); ok && name != "name" {
			hints = append(hints, ForgeGitHub+`.repos["owner/repo"].`+name, ForgeGitLab+`.repos["group/project"].`+name)
		}
	}
	scope := prefix
	if scope == "" {
		scope = "the top level"
	}
	msg := fmt.Sprintf("%s: unknown field %q at %s; valid fields: %s", raw, name, scope, strings.Join(writableFields(t, prefix), ", "))
	if len(hints) > 0 {
		msg += fmt.Sprintf("; %q is valid at %s", name, strings.Join(hints, ", "))
	}
	return errors.New(msg)
}

// ---------------------------------------------------------------------------
// Value parsing

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// valueNode converts the command-line value into a YAML node for a field of
// type t. Strings are literal (so "none", "" and "true" stay strings);
// booleans and numbers must parse as their schema type; lists, maps, and
// objects accept YAML or JSON and are strictly decoded into the schema type.
func valueNode(t reflect.Type, raw, path string) (*yaml.Node, error) {
	bt := t
	for bt.Kind() == reflect.Pointer {
		bt = bt.Elem()
	}
	switch bt.Kind() {
	case reflect.String:
		return strNode(raw), nil
	case reflect.Bool:
		if raw != "true" && raw != "false" {
			return nil, fmt.Errorf("%s must be true or false, got %q", path, raw)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: raw}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// The node carries the canonical base-10 spelling: YAML reads a
		// leading-zero literal such as 010 as octal, so writing raw would
		// store a different number than the one validated here.
		n, err := strconv.ParseInt(raw, 10, bt.Bits())
		if err != nil {
			return nil, fmt.Errorf("%s must be an integer, got %q", path, raw)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(n, 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, bt.Bits())
		if err != nil {
			return nil, fmt.Errorf("%s must be a non-negative integer, got %q", path, raw)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatUint(n, 10)}, nil
	case reflect.Float32, reflect.Float64:
		if _, err := strconv.ParseFloat(raw, bt.Bits()); err != nil {
			return nil, fmt.Errorf("%s must be a number, got %q", path, raw)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: raw}, nil
	}

	// An interface-typed target (for example a value of a map[string]any)
	// accepts any YAML or JSON node kind; the whole-manifest Validate
	// enforces what the field allows.
	anyKind := bt.Kind() == reflect.Interface
	want, desc := yaml.MappingNode, "mapping (object)"
	if bt.Kind() == reflect.Slice || bt.Kind() == reflect.Array {
		want, desc = yaml.SequenceNode, "list"
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("%s: value is not valid YAML or JSON: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || (!anyKind && doc.Content[0].Kind != want) {
		if anyKind {
			return nil, fmt.Errorf("%s must be a YAML or JSON value, got %q (use --unset to remove the key)", path, raw)
		}
		return nil, fmt.Errorf("%s must be a YAML or JSON %s, got %q (use --unset to remove the key)", path, desc, raw)
	}
	n := doc.Content[0]
	if containsAlias(n) {
		return nil, fmt.Errorf("%s: a YAML alias is not a valid value", path)
	}
	if containsMergeKey(n) {
		// The forge and non-writable-field checks inspect literal keys only, so
		// a merge key would carry fields past them.
		return nil, fmt.Errorf("%s: a YAML merge key (<<) is not a valid value; list the fields explicitly", path)
	}
	// yaml.v3 silently drops a null element from a []string, so the strict
	// decode below would accept it and the file would keep it.
	if hasNullStringElement(t, n) {
		return nil, fmt.Errorf("%s: list elements must be strings, not null", path)
	}
	if err := decodeNodeStrict(n, reflect.New(t).Interface()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	normalizeValueStyle(n)
	return n, nil
}

// hasNullStringElement reports whether n, a value for schema type t, holds a
// null element in any []string, including lists nested inside objects, maps,
// and lists of objects.
func hasNullStringElement(t reflect.Type, n *yaml.Node) bool {
	st := schemaType(t)
	switch {
	case st.Kind() == reflect.Slice && n.Kind == yaml.SequenceNode:
		elem := schemaType(st.Elem())
		for _, el := range n.Content {
			if elem.Kind() == reflect.String {
				if el.ShortTag() == "!!null" {
					return true
				}
			} else if hasNullStringElement(elem, el) {
				return true
			}
		}
	case st.Kind() == reflect.Map && n.Kind == yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if hasNullStringElement(st.Elem(), n.Content[i]) {
				return true
			}
		}
	case st.Kind() == reflect.Struct && !reflect.PointerTo(st).Implements(yamlUnmarshalerType) && n.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if f, ok := yamlField(st, n.Content[i].Value); ok && hasNullStringElement(f.Type, n.Content[i+1]) {
				return true
			}
		}
	}
	return false
}

// entryValueNode parses a whole repository entry. The selector supplies the
// entry name; a name in the value must match it exactly.
func entryValueNode(raw, name, path string) (*yaml.Node, error) {
	n, err := valueNode(repoEntryType, raw, path)
	if err != nil {
		return nil, err
	}
	if ki := mappingKey(n, "name"); ki >= 0 {
		v := n.Content[ki+1]
		if v.Kind != yaml.ScalarNode || v.Value != name {
			return nil, fmt.Errorf("%s: entry name %q does not match the selector %q; omit name or make it match", path, v.Value, name)
		}
		return n, nil
	}
	n.Content = append([]*yaml.Node{strNode("name"), strNode(name)}, n.Content...)
	return n, nil
}

func decodeNodeStrict(n *yaml.Node, out any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(n); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// containsAlias reports whether n or any descendant is a YAML alias.
func containsAlias(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, c := range n.Content {
		if containsAlias(c) {
			return true
		}
	}
	return false
}

// containsMergeKey reports whether n or any descendant mapping has a YAML
// merge key (an unquoted <<).
func containsMergeKey(n *yaml.Node) bool {
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 && c.Kind == yaml.ScalarNode && c.Tag == "!!merge" {
			return true
		}
		if containsMergeKey(c) {
			return true
		}
	}
	return false
}

// normalizeValueStyle renders parsed JSON or flow YAML as block YAML that
// matches the rest of the manifest. Empty collections keep their flow
// form ([] and {}), and comments in the value are dropped.
func normalizeValueStyle(n *yaml.Node) {
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	// Anchors are dropped so a value cannot redefine an anchor that an alias
	// elsewhere in the manifest resolves to.
	n.Anchor = ""
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		if len(n.Content) > 0 {
			n.Style = 0
		}
		for _, c := range n.Content {
			normalizeValueStyle(c)
		}
	case yaml.ScalarNode:
		if n.Tag == "!!str" {
			n.Style &^= yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
		}
	}
}

// ---------------------------------------------------------------------------
// Node editing

type manifestEditor struct {
	doc    *yaml.Node
	lines  []string
	indent int
	fresh  bool // no existing document to splice into; render in full
}

func newManifestEditor(data []byte) (*manifestEditor, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			strNode("version"), {Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
		}}
		return &manifestEditor{
			doc:    &yaml.Node{Kind: yaml.DocumentNode, HeadComment: doc.HeadComment, Content: []*yaml.Node{root}},
			indent: 2,
			fresh:  true,
		}, nil
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("manifest root must be a YAML mapping")
	}
	return &manifestEditor{
		doc:    &doc,
		lines:  strings.Split(string(data), "\n"),
		indent: detectIndent(doc.Content[0]),
	}, nil
}

// detectIndent returns the indentation width the manifest uses for nested
// mappings, defaulting to 2.
func detectIndent(root *yaml.Node) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		v := root.Content[i+1]
		if v.Kind == yaml.MappingNode && len(v.Content) > 0 && v.Style&yaml.FlowStyle == 0 {
			if d := v.Content[0].Column - root.Content[i].Column; d > 0 {
				return d
			}
		}
	}
	return 2
}

// hasMergeKey reports whether the mapping m has a literal YAML merge key (<<).
func hasMergeKey(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Tag == "!!merge" {
			return true
		}
	}
	return false
}

func mappingKey(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func isNullNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// frame records one step down the existing tree: the container and the
// index of the child that was followed (the key index for mappings).
type frame struct {
	node *yaml.Node
	idx  int
}

type editOp int

const (
	opReplace editOp = iota
	opInsert
	opRemove
)

// editPlan is a single change to one container node.
type editPlan struct {
	chain []frame
	node  *yaml.Node
	op    editOp
	idx   int
	key   *yaml.Node // new key for a mapping insert
	val   *yaml.Node // new value (mapping) or item (sequence)
}

func describeSteps(steps []pathStep) string {
	var b strings.Builder
	for _, st := range steps {
		if st.kind == stepEntry {
			b.WriteString("[")
			b.WriteString(strconv.Quote(st.key))
			b.WriteString("]")
			continue
		}
		if b.Len() > 0 {
			b.WriteString(".")
		}
		b.WriteString(st.key)
	}
	return b.String()
}

// findEntry returns the index of the repository entry named exactly name,
// or -1. Duplicate exact names and case-only mismatches are errors.
func findEntry(seq *yaml.Node, name, path string) (int, error) {
	found, variant := -1, ""
	for i, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		ki := mappingKey(item, "name")
		if ki < 0 {
			continue
		}
		got := item.Content[ki+1].Value
		switch {
		case got == name:
			if found >= 0 {
				return -1, fmt.Errorf("%s: the manifest has duplicate entries named %q; remove the duplicate first", path, name)
			}
			found = i
		case strings.EqualFold(got, name):
			variant = got
		}
	}
	if found < 0 && variant != "" {
		return -1, fmt.Errorf("%s: no entry is named exactly %q, but %q exists; selectors match entry names exactly", path, name, variant)
	}
	return found, nil
}

func (e *manifestEditor) plan(path string, steps []pathStep, newVal *yaml.Node) (*editPlan, error) {
	unset := newVal == nil
	cur := e.doc.Content[0]
	var chain []frame
	for i, st := range steps {
		last := i == len(steps)-1
		if st.kind == stepEntry {
			idx, err := findEntry(cur, st.key, path)
			if err != nil {
				return nil, err
			}
			switch {
			case idx < 0 && !last:
				return nil, missingEntryError(path, steps[:i+1])
			case idx < 0 && unset:
				return nil, nil
			case idx < 0:
				return &editPlan{chain: chain, node: cur, op: opInsert, val: newVal}, nil
			case last && unset:
				return &editPlan{chain: chain, node: cur, op: opRemove, idx: idx}, nil
			case last:
				return &editPlan{chain: chain, node: cur, op: opReplace, idx: idx, val: newVal}, nil
			}
			chain = append(chain, frame{node: cur, idx: idx})
			cur = cur.Content[idx]
			continue
		}

		ki := mappingKey(cur, st.key)
		if hasMergeKey(cur) && (ki < 0 && !unset || ki >= 0 && last && unset) {
			// Adding or removing a literal key next to a merge key changes
			// how the merged fields combine with it (an explicit mapping
			// replaces a merged one as a whole), and mappingKey sees literal
			// keys only, so inherited settings could be lost silently.
			return nil, fmt.Errorf("%s: %s passes through a YAML merge key (<<) in the manifest; list the merged fields explicitly before editing it", path, describeSteps(steps[:i+1]))
		}
		if last {
			switch {
			case ki < 0 && unset:
				return nil, nil
			case ki < 0:
				return &editPlan{chain: chain, node: cur, op: opInsert, key: strNode(st.key), val: newVal}, nil
			case unset:
				return &editPlan{chain: chain, node: cur, op: opRemove, idx: ki}, nil
			default:
				return &editPlan{chain: chain, node: cur, op: opReplace, idx: ki, val: newVal}, nil
			}
		}

		var child *yaml.Node
		if ki >= 0 {
			child = cur.Content[ki+1]
		}
		if child == nil || isNullNode(child) {
			if unset {
				// A missing parent is a no-op, except when the path goes
				// through a repository entry: report that entry as missing,
				// as an existing repos list without it does.
				for j := i + 1; j < len(steps)-1; j++ {
					if steps[j].kind == stepEntry {
						return nil, missingEntryError(path, steps[:j+1])
					}
				}
				return nil, nil
			}
			sub, err := buildSubtree(path, steps, i+1, newVal)
			if err != nil {
				return nil, err
			}
			if child == nil {
				return &editPlan{chain: chain, node: cur, op: opInsert, key: strNode(st.key), val: sub}, nil
			}
			return &editPlan{chain: chain, node: cur, op: opReplace, idx: ki, val: sub}, nil
		}
		want, word := yaml.MappingNode, "mapping"
		if steps[i+1].kind == stepEntry {
			want, word = yaml.SequenceNode, "list"
		}
		if child.Kind != want {
			return nil, fmt.Errorf("%s: %s is not a %s in the manifest", path, describeSteps(steps[:i+1]), word)
		}
		chain = append(chain, frame{node: cur, idx: ki})
		cur = child
	}
	return nil, nil
}

func missingEntryError(path string, steps []pathStep) error {
	return fmt.Errorf("%s: repository entry %s does not exist; set %s to a whole entry to create it first", path, describeSteps(steps), describeSteps(steps))
}

// buildSubtree creates the missing optional parents for steps[from:] around
// leaf. A repository entry can only be created as a whole entry.
func buildSubtree(path string, steps []pathStep, from int, leaf *yaml.Node) (*yaml.Node, error) {
	if from == len(steps) {
		return leaf, nil
	}
	st := steps[from]
	if st.kind == stepEntry {
		if from != len(steps)-1 {
			return nil, missingEntryError(path, steps[:from+1])
		}
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{leaf}}, nil
	}
	sub, err := buildSubtree(path, steps, from+1, leaf)
	if err != nil {
		return nil, err
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{strNode(st.key), sub}}, nil
}

func (p *editPlan) mutate() {
	c := p.node
	isMap := c.Kind == yaml.MappingNode
	switch p.op {
	case opReplace:
		if !isMap {
			c.Content[p.idx] = p.val
			return
		}
		old := c.Content[p.idx+1]
		if old.Kind == yaml.ScalarNode && p.val.Kind == yaml.ScalarNode && p.val.LineComment == "" {
			p.val.LineComment = old.LineComment
		}
		c.Content[p.idx+1] = p.val
	case opInsert:
		if len(c.Content) == 0 {
			c.Style &^= yaml.FlowStyle
		}
		if isMap {
			c.Content = append(c.Content, p.key, p.val)
		} else {
			c.Content = append(c.Content, p.val)
		}
	case opRemove:
		if isMap {
			c.Content = slices.Delete(c.Content, p.idx, p.idx+2)
		} else {
			c.Content = slices.Delete(c.Content, p.idx, p.idx+1)
		}
	}
}

// apply performs the plan on the tree and returns the new file contents.
// It splices the change into the original lines when it can verify the
// result is equivalent, and otherwise re-renders the whole document.
func (e *manifestEditor) apply(p *editPlan) ([]byte, error) {
	var splice func() ([]string, error)
	if !e.fresh {
		splice = e.prepareSplice(p)
	}
	p.mutate()
	if splice != nil {
		if lines, err := splice(); err == nil {
			out := []byte(strings.Join(lines, "\n"))
			if e.equivalent(out) {
				return out, nil
			}
		}
	}
	return encodeYAML(e.doc, e.indent)
}

func encodeYAML(n *yaml.Node, indent int) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	if err := enc.Encode(n); err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// equivalent reports whether out decodes to the same data as the edited
// tree.
func (e *manifestEditor) equivalent(out []byte) bool {
	var got, want any
	if err := yaml.Unmarshal(out, &got); err != nil {
		return false
	}
	if err := e.doc.Decode(&want); err != nil {
		return false
	}
	return reflect.DeepEqual(got, want)
}

func childCount(c *yaml.Node) int {
	if c.Kind == yaml.MappingNode {
		return len(c.Content) / 2
	}
	return len(c.Content)
}

func childStep(c *yaml.Node) int {
	if c.Kind == yaml.MappingNode {
		return 2
	}
	return 1
}

func isBlankOrComment(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// childSpan returns the 1-based inclusive line range of child idx of c,
// excluding trailing blank and comment lines (which belong to the next
// node or stay in place). cEnd is the last line of c.
func (e *manifestEditor) childSpan(c *yaml.Node, idx, cEnd int) (int, int) {
	start := c.Content[idx].Line
	next := cEnd + 1
	if n := idx + childStep(c); n < len(c.Content) {
		next = c.Content[n].Line
	}
	end := next - 1
	for end > start && isBlankOrComment(e.lines[end-1]) {
		end--
	}
	return start, end
}

// containerEnd returns the last line of the container reached by chain.
func (e *manifestEditor) containerEnd(chain []frame) int {
	end := len(e.lines)
	for _, f := range chain {
		_, end = e.childSpan(f.node, f.idx, end)
	}
	return end
}

// childColumn returns the column at which child idx of c starts: the key
// column for mappings, the dash column for sequences.
func (e *manifestEditor) childColumn(c *yaml.Node, idx int) (int, bool) {
	n := c.Content[idx]
	if n.Line < 1 || n.Line > len(e.lines) || n.Column < 1 {
		return 0, false
	}
	if c.Kind == yaml.MappingNode {
		return n.Column, true
	}
	line := e.lines[n.Line-1]
	for i := min(n.Column-2, len(line)-1); i >= 0; i-- {
		if line[i] == '-' {
			return i + 1, true
		}
	}
	return 0, false
}

// renderChild renders child idx of c as block YAML starting at column col.
func (e *manifestEditor) renderChild(c *yaml.Node, idx, col int) ([]string, error) {
	var wrapper *yaml.Node
	if c.Kind == yaml.MappingNode {
		k, v := *c.Content[idx], *c.Content[idx+1]
		k.HeadComment, k.FootComment = "", ""
		v.HeadComment, v.FootComment = "", ""
		wrapper = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{&k, &v}}
	} else {
		v := *c.Content[idx]
		v.HeadComment, v.FootComment = "", ""
		wrapper = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{&v}}
	}
	out, err := encodeYAML(wrapper, e.indent)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	pad := strings.Repeat(" ", col-1)
	for i := range lines {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return lines, nil
}

func spliceLines(lines []string, start, end int, repl []string) []string {
	out := make([]string, 0, len(lines)+len(repl))
	out = append(out, lines[:start-1]...)
	out = append(out, repl...)
	return append(out, lines[end:]...)
}

// prepareSplice computes, before the tree is mutated, where the change
// lands in the original lines, and returns a function that renders the
// mutated node there. It returns nil when no splice is possible.
func (e *manifestEditor) prepareSplice(p *editPlan) func() ([]string, error) {
	c := p.node
	n := childCount(c)
	if c.Style&yaml.FlowStyle == 0 && n > 0 && (p.op != opRemove || n > 1) {
		cEnd := e.containerEnd(p.chain)
		switch p.op {
		case opReplace:
			start, end := e.childSpan(c, p.idx, cEnd)
			if col, ok := e.childColumn(c, p.idx); ok {
				return func() ([]string, error) {
					r, err := e.renderChild(c, p.idx, col)
					if err != nil {
						return nil, err
					}
					return spliceLines(e.lines, start, end, r), nil
				}
			}
		case opInsert:
			lastIdx := len(c.Content) - childStep(c)
			_, end := e.childSpan(c, lastIdx, cEnd)
			if col, ok := e.childColumn(c, lastIdx); ok {
				return func() ([]string, error) {
					r, err := e.renderChild(c, len(c.Content)-childStep(c), col)
					if err != nil {
						return nil, err
					}
					return spliceLines(e.lines, end+1, end, r), nil
				}
			}
		case opRemove:
			start, end := e.childSpan(c, p.idx, cEnd)
			if col, ok := e.childColumn(c, p.idx); ok {
				// Drop the removed node's own head comment with it.
				pad := strings.Repeat(" ", col-1) + "#"
				for start > 1 && strings.HasPrefix(e.lines[start-2], pad) {
					start--
				}
				return func() ([]string, error) {
					return spliceLines(e.lines, start, end, nil), nil
				}
			}
		}
	}
	// Re-render the nearest enclosing block-style pair or item instead.
	for k := len(p.chain) - 1; k >= 0; k-- {
		f := p.chain[k]
		if f.node.Style&yaml.FlowStyle != 0 {
			continue
		}
		start, end := e.childSpan(f.node, f.idx, e.containerEnd(p.chain[:k]))
		col, ok := e.childColumn(f.node, f.idx)
		if !ok {
			return nil
		}
		return func() ([]string, error) {
			r, err := e.renderChild(f.node, f.idx, col)
			if err != nil {
				return nil, err
			}
			return spliceLines(e.lines, start, end, r), nil
		}
	}
	return nil
}
