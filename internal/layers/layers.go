package layers

import (
	"context"
	"fmt"
)

// LayerStatus represents the current state of a layer.
type LayerStatus int

const (
	StatusNotInstalled LayerStatus = iota
	StatusInstalled
	StatusDegraded // partially installed or misconfigured
	StatusUnknown  // cannot determine
)

// String returns a human-readable description of the status.
func (s LayerStatus) String() string {
	switch s {
	case StatusNotInstalled:
		return "not installed"
	case StatusInstalled:
		return "installed"
	case StatusDegraded:
		return "degraded"
	case StatusUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("LayerStatus(%d)", int(s))
	}
}

// LayerReport is the result of analyzing a single layer.
type LayerReport struct {
	Name         string
	Status       LayerStatus
	Details      []string // human-readable detail lines
	WouldInstall []string // what install would create
	WouldFix     []string // what install would fix (for degraded state)
}

// Operation identifies which action is being performed on a layer.
type Operation int

const (
	OpInstall Operation = iota
	OpUninstall
	OpAnalyze
)

func (o Operation) String() string {
	switch o {
	case OpInstall:
		return "install"
	case OpUninstall:
		return "uninstall"
	case OpAnalyze:
		return "analyze"
	default:
		return fmt.Sprintf("Operation(%d)", int(o))
	}
}

// Layer is the interface each installation concern implements.
// Layers are processed in order for install, reverse order for uninstall.
type Layer interface {
	// Name returns a human-readable name for this layer.
	Name() string

	// RequiredScopes returns the OAuth scopes this layer needs for the
	// given operation. Scopes are GitHub-flavored strings like "repo",
	// "delete_repo", "workflow". Used by Preflight to fail early when
	// the token is missing required scopes.
	RequiredScopes(op Operation) []string

	// Install creates or configures this layer's concern.
	Install(ctx context.Context) error

	// Uninstall tears down this layer's concern.
	Uninstall(ctx context.Context) error

	// Analyze assesses the current state and reports what would change.
	Analyze(ctx context.Context) (*LayerReport, error)
}

// Stack is an ordered collection of layers.
type Stack struct {
	layers []Layer
}

// NewStack creates a new Stack with the given layers in order.
func NewStack(layers ...Layer) *Stack {
	return &Stack{layers: layers}
}

// Layers returns the layers in order.
func (s *Stack) Layers() []Layer {
	return s.layers
}

// InstallAll runs Install on each layer in order.
// Stops on first error, returning the error and the name of the failed layer.
func (s *Stack) InstallAll(ctx context.Context) error {
	for _, l := range s.layers {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("cancelled before layer %s: %w", l.Name(), err)
		}
		if err := l.Install(ctx); err != nil {
			return fmt.Errorf("layer %s: %w", l.Name(), err)
		}
	}
	return nil
}

// UninstallAll runs Uninstall on each layer in reverse order.
// Collects all errors rather than stopping on first.
func (s *Stack) UninstallAll(ctx context.Context) []error {
	var errs []error
	for i := len(s.layers) - 1; i >= 0; i-- {
		l := s.layers[i]
		if err := l.Uninstall(ctx); err != nil {
			errs = append(errs, fmt.Errorf("layer %s: %w", l.Name(), err))
		}
	}
	return errs
}

// CollectRequiredScopes returns the deduplicated set of scopes needed
// by all layers for the given operation.
func (s *Stack) CollectRequiredScopes(op Operation) []string {
	seen := make(map[string]bool)
	var scopes []string
	for _, l := range s.layers {
		for _, scope := range l.RequiredScopes(op) {
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}
	return scopes
}

// AnalyzeAll runs Analyze on each layer and returns reports.
func (s *Stack) AnalyzeAll(ctx context.Context) ([]*LayerReport, error) {
	var reports []*LayerReport
	for _, l := range s.layers {
		if err := ctx.Err(); err != nil {
			return reports, fmt.Errorf("cancelled before analyzing %s: %w", l.Name(), err)
		}
		report, err := l.Analyze(ctx)
		if err != nil {
			return reports, fmt.Errorf("analyzing layer %s: %w", l.Name(), err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}
