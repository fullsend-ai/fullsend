package repos

import (
	"fmt"
	"strings"
)

// SetAdoptOverlays records the repositories (owner/repo) whose existing
// .fullsend/config.yaml without the ownership marker the operator
// acknowledged for adoption in this run (`repos install --adopt`, #8512).
// Configuration resolved from m afterwards carries AdoptOverlay for those
// repositories, so preflight and convergence replace the markerless file
// with the managed one when the ADR-0122 safety gate passes, instead of
// stopping the repository as adoption required. The names are matched
// case-insensitively and are never written to the manifest file. Callers
// validate them first with ValidateAdoptSelections.
func (m *Manifest) SetAdoptOverlays(names []string) {
	if len(names) == 0 {
		m.adoptOverlays = nil
		return
	}
	m.adoptOverlays = make(map[string]bool, len(names))
	for _, name := range names {
		m.adoptOverlays[strings.ToLower(strings.TrimSpace(name))] = true
	}
}

// adoptsOverlay reports whether the operator acknowledged adoption of the
// repository's markerless overlay in this run.
func (m *Manifest) adoptsOverlay(owner, repo string) bool {
	return m.adoptOverlays[strings.ToLower(owner+"/"+repo)]
}

// ValidateAdoptSelections checks the `repos install --adopt` values before
// anything is read from or written to the forge. ADR 0122 requires an
// explicit acknowledgement per repository, so each value must be an exact
// owner/repo name, not a glob. The repository must be declared in the
// manifest (an entry or a covering glob entry) and, when positional
// selectors are given, be among the repositories they select.
func ValidateAdoptSelections(m *Manifest, filter, adopt []string) error {
	var errs []string
	for _, raw := range adopt {
		name := strings.TrimSpace(raw)
		if name == "" || isGlob(name) || !strings.Contains(name, "/") ||
			strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
			errs = append(errs, fmt.Sprintf("--adopt %q: must name one repository as owner/repo; adoption is acknowledged per repository (ADR-0122), so patterns are not accepted", raw))
			continue
		}
		undeclared, err := m.UndeclaredSelectors([]string{name})
		if err != nil {
			return fmt.Errorf("matching --adopt %q: %w", raw, err)
		}
		if len(undeclared) > 0 {
			errs = append(errs, fmt.Sprintf("--adopt %q: not declared in repos.yaml; declare the repository and its settings first", raw))
			continue
		}
		if len(filter) == 0 {
			continue
		}
		selected := false
		for _, pattern := range filter {
			ok, err := matchesPattern(pattern, name)
			if err != nil {
				return fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if ok {
				selected = true
				break
			}
		}
		if !selected {
			errs = append(errs, fmt.Sprintf("--adopt %q: not among the repositories selected by %s", raw, strings.Join(filter, ", ")))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s; nothing was changed", strings.Join(errs, "; "))
	}
	return nil
}

// adoptionBlocks reports whether an existing .fullsend/config.yaml stops
// the repository as adoption required: the file lacks the ownership marker
// (see overlayNeedsAdoption) and the operator did not acknowledge its
// adoption with --adopt. An acknowledged markerless file is still subject
// to the ADR-0122 safety gate before it is replaced.
func (r ResolvedConfig) adoptionBlocks(found bool, content []byte) bool {
	return !r.AdoptOverlay && overlayNeedsAdoption(found, content)
}
