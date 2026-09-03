package agentnew

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// Result describes what Generate did, so the caller can print next steps and
// tests can assert on it without reparsing output.
type Result struct {
	// Written lists the files created, in generation order. On a dry run it
	// lists what would have been created.
	Written []string
	// Rendered carries the same files with their bodies, for --dry-run.
	Rendered []File
	// SkippedShared lists shared scaffold assets that were already present
	// and therefore left alone.
	SkippedShared []string
	// Diagnostics are non-fatal lint warnings from the generated harness.
	Diagnostics []harness.Diagnostic
	// HarnessPath is the generated harness, relative to the fullsend dir.
	HarnessPath string
}

// Generate renders an agent, validates it, and writes it into fullsendDir.
//
// Ordering matters and is the reason this is not a straight loop of writes:
// everything is rendered and validated against a temporary tree first, so a
// harness that would fail validation never leaves a half-written .fullsend
// directory behind. Options must already have passed Validate.
func Generate(opts Options, fullsendDir string, force, dryRun bool) (*Result, error) {
	absDir, err := filepath.Abs(fullsendDir)
	if err != nil {
		return nil, fmt.Errorf("resolving fullsend dir: %w", err)
	}
	if info, statErr := os.Stat(absDir); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("fullsend dir %s does not exist; run `fullsend github setup` first", fullsendDir)
	}

	files, err := Render(opts)
	if err != nil {
		return nil, err
	}

	// Collisions are checked before anything is written. --force overwrites
	// the four files this agent owns, but never a shared scaffold asset:
	// those belong to every agent in the directory.
	var collisions []string
	writable := make([]File, 0, len(files))
	result := &Result{HarnessPath: filepath.Join("harness", opts.Name+".yaml")}
	for _, f := range files {
		_, statErr := os.Stat(filepath.Join(absDir, f.Path))
		exists := statErr == nil
		switch {
		case exists && f.Shared:
			result.SkippedShared = append(result.SkippedShared, f.Path)
		case exists && !force:
			collisions = append(collisions, f.Path)
		default:
			writable = append(writable, f)
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		return nil, fmt.Errorf("these files already exist:\n  %s\n\nPass --force to overwrite them, or pick another agent name",
			strings.Join(collisions, "\n  "))
	}

	if err := validateInTempTree(absDir, opts, files, result); err != nil {
		return nil, err
	}

	// Planned before the write loop so --dry-run can report exactly what a
	// real run would create, including the rendered bodies.
	for _, f := range writable {
		result.Written = append(result.Written, f.Path)
		result.Rendered = append(result.Rendered, f)
	}
	if dryRun {
		return result, nil
	}

	for _, f := range writable {
		path := filepath.Join(absDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", filepath.Dir(f.Path), err)
		}
		// Written directly rather than renamed in from the temp tree: a
		// rename across filesystems fails, and the bytes are already here.
		if err := os.WriteFile(path, f.Data, os.FileMode(f.Mode)); err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}
	return result, nil
}

// validateInTempTree writes the rendered files to a scratch directory and
// runs the generated harness through the real loader there.
//
// Where a shared asset already exists in the target, the target's copy is
// validated rather than the embedded one, so the check reflects what the
// agent will actually run against — a hand-edited policies/base.yaml is
// common and its content matters.
func validateInTempTree(absDir string, opts Options, files []File, result *Result) error {
	tmp, err := os.MkdirTemp("", "fullsend-agent-new-")
	if err != nil {
		return fmt.Errorf("creating scratch directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	for _, f := range files {
		data := f.Data
		if f.Shared {
			if existing, readErr := os.ReadFile(filepath.Join(absDir, f.Path)); readErr == nil {
				data = existing
			}
		}
		path := filepath.Join(tmp, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("preparing scratch directory: %w", err)
		}
		if err := os.WriteFile(path, data, os.FileMode(f.Mode)); err != nil {
			return fmt.Errorf("preparing scratch directory: %w", err)
		}
	}

	h, err := harness.Load(filepath.Join(tmp, "harness", opts.Name+".yaml"))
	if err != nil {
		return fmt.Errorf("the generated harness is not valid — this is a bug in `fullsend agent new`: %w", err)
	}
	diags, err := harness.CheckGenerated(h, tmp)
	result.Diagnostics = diags
	if err != nil {
		return fmt.Errorf("the generated agent did not validate — this is a bug in `fullsend agent new`: %w", err)
	}
	return nil
}

// DeriveSlug builds the default harness slug from the repository owner.
// The slug is install-time App discovery only — the mint never reads it —
// so falling back to a constant is safe, but the fallback is reported so the
// user can pass --slug if they care.
func DeriveSlug(name string, owner string) string {
	if owner == "" {
		owner = "fullsend"
	}
	return owner + "-" + name
}

// RepoOwner returns the owner segment of the origin remote in gitDir's
// repository, or "" when it cannot be determined.
type RepoOwner func(dir string) string

// GitConfigOwner reads .git/config for the origin remote and extracts the
// owner. It understands the two URL forms git writes.
func GitConfigOwner(dir string) string {
	for depth := 0; depth < 20; depth++ {
		data, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
		if err == nil {
			return ownerFromGitConfig(string(data))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

func ownerFromGitConfig(cfg string) string {
	inOrigin := false
	for _, line := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[remote ") {
			inOrigin = strings.Contains(trimmed, `"origin"`)
			continue
		}
		if !inOrigin || !strings.HasPrefix(trimmed, "url") {
			continue
		}
		_, url, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		return ownerFromRemoteURL(strings.TrimSpace(url))
	}
	return ""
}

func ownerFromRemoteURL(url string) string {
	url = strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")

	// scp-like form, git@host:owner/repo. Checked by the absence of a
	// scheme rather than by the presence of a colon: ssh://git@host/o/r
	// has both a colon and a scheme, and belongs to the path form below.
	if !strings.Contains(url, "://") {
		if _, rest, found := strings.Cut(url, ":"); found {
			owner, _, _ := strings.Cut(rest, "/")
			return sanitiseSlugSegment(owner)
		}
		return ""
	}

	// scheme://host/owner/repo — the owner is the second-to-last segment.
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return ""
	}
	return sanitiseSlugSegment(parts[len(parts)-2])
}

// sanitiseSlugSegment drops anything that would fail harness.ValidSlug, so a
// remote with an unusual owner degrades to the fallback rather than
// producing a harness that fails validation.
func sanitiseSlugSegment(s string) string {
	if s == "" || !harness.ValidSlug(s) {
		return ""
	}
	return s
}
