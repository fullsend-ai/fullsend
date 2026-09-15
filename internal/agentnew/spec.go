package agentnew

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// SpecVersion is the only accepted `version:` value in a spec file.
const SpecVersion = "1"

// AgentSpec is the document accepted by `fullsend agent new -f <file>`. Keys
// mirror the command-line flags one for one so a local coding agent can emit
// either form. Command-line flags override spec keys.
type AgentSpec struct {
	Version        string `yaml:"version"`
	Name           string `yaml:"name"`
	Role           string `yaml:"role,omitempty"`
	Description    string `yaml:"description,omitempty"`
	On             string `yaml:"on,omitempty"`
	Trigger        string `yaml:"trigger,omitempty"`
	Model          string `yaml:"model,omitempty"`
	Effort         string `yaml:"effort,omitempty"`
	Runtime        string `yaml:"runtime,omitempty"`
	Slug           string `yaml:"slug,omitempty"`
	Image          string `yaml:"image,omitempty"`
	TimeoutMinutes *int   `yaml:"timeout_minutes,omitempty"`
	ValidationLoop bool   `yaml:"validation_loop,omitempty"`
}

// LoadSpecFile reads and validates a spec file.
func LoadSpecFile(path string) (*AgentSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading spec file: %w", err)
	}
	return ParseSpec(data)
}

// ParseSpec decodes a spec document. Unknown keys are an error rather than a
// silent no-op: a typo in a spec file would otherwise generate a different
// agent than the author asked for, which is the failure mode this command
// exists to remove.
func ParseSpec(data []byte) (*AgentSpec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var spec AgentSpec
	if err := dec.Decode(&spec); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("spec file is empty")
		}
		return nil, fmt.Errorf("parsing spec file: %w", err)
	}
	// A second document would be silently ignored otherwise. Only a real EOF
	// means "there was nothing more": any other error is a malformed second
	// document, which must be reported rather than treated as absence.
	var extra AgentSpec
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, fmt.Errorf("spec file must contain exactly one YAML document")
	case errors.Is(err, io.EOF):
		// The expected case: one document, cleanly ended.
	default:
		return nil, fmt.Errorf("parsing spec file: %w", err)
	}

	if spec.Version != SpecVersion {
		return nil, fmt.Errorf("spec version must be %q, got %q", SpecVersion, spec.Version)
	}
	if spec.Name == "" {
		return nil, fmt.Errorf("spec field \"name\" is required")
	}
	if spec.On != "" && spec.Trigger != "" {
		return nil, fmt.Errorf("spec fields \"on\" and \"trigger\" are mutually exclusive")
	}
	if spec.TimeoutMinutes != nil && *spec.TimeoutMinutes < 0 {
		return nil, fmt.Errorf("spec field \"timeout_minutes\" must not be negative, got %d", *spec.TimeoutMinutes)
	}
	return &spec, nil
}
