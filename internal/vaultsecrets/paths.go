// Package vaultsecrets resolves Fullsend runner secrets from HashiCorp
// Vault KV v2 through the job's workload identity (ADR 0155). It knows
// nothing about Fullsend configuration files: callers translate the
// trusted connections and secrets mappings into Connection and Source
// values and supply the trusted target-repository context.
package vaultsecrets

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// RepoContext holds the trusted target-repository values Vault path
// templates may reference. Values come from the CI platform's own job
// context, never from PR-head files or event-supplied repository names.
type RepoContext struct {
	// Forge is the forge platform name (github, gitlab).
	Forge string
	// Host is the forge host name, such as github.com.
	Host string
	// Owner is the canonical owner or top-level namespace.
	Owner string
	// Name is the repository or project name.
	Name string
	// FullName is the canonical namespace/project path, including nested
	// GitLab groups.
	FullName string
	// Org is the organization or group; empty when the target does not
	// belong to one, which makes {{org}} unavailable.
	Org string
}

// templateVariables lists the variables a Vault path may reference.
var templateVariables = map[string]bool{
	"forge":          true,
	"repo.host":      true,
	"repo.owner":     true,
	"repo.name":      true,
	"repo.full_name": true,
	"org":            true,
}

// templatePart is a literal run or a variable reference of a template.
type templatePart struct {
	literal  string
	variable string
}

// parseTemplate splits tmpl into literal runs and {{variable}} references.
// An unterminated reference, an unknown variable, or a stray "}}" is an
// error.
func parseTemplate(tmpl string) ([]templatePart, error) {
	var parts []templatePart
	rest := tmpl
	for rest != "" {
		open := strings.Index(rest, "{{")
		if open < 0 {
			if strings.Contains(rest, "}}") {
				return nil, fmt.Errorf("unmatched \"}}\"")
			}
			parts = append(parts, templatePart{literal: rest})
			break
		}
		if strings.Contains(rest[:open], "}}") {
			return nil, fmt.Errorf("unmatched \"}}\"")
		}
		if open > 0 {
			parts = append(parts, templatePart{literal: rest[:open]})
		}
		rest = rest[open+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return nil, fmt.Errorf("unterminated \"{{\"")
		}
		name := rest[:end]
		if !templateVariables[name] {
			return nil, fmt.Errorf("unknown variable %q", name)
		}
		parts = append(parts, templatePart{variable: name})
		rest = rest[end+2:]
	}
	return parts, nil
}

// ValidateTemplate checks a Vault path template without a repository
// context: the variable syntax, the variable names, and the literal
// text. Configuration validation uses it; ExpandPath repeats every check
// on the expanded path.
func ValidateTemplate(tmpl string) error {
	parts, err := parseTemplate(tmpl)
	if err != nil {
		return err
	}
	// Substitute a placeholder component for each variable so the path
	// rules apply to the literal text around it.
	var b strings.Builder
	for _, p := range parts {
		if p.variable != "" {
			b.WriteString("x")
			continue
		}
		b.WriteString(p.literal)
	}
	return ValidatePath(b.String())
}

// ExpandPath expands the variables in tmpl from rc and validates the
// result as a relative KV v2 API path. Each variable value is validated
// and encoded with path-segment semantics; {{repo.full_name}} is split on
// "/" and each component is handled separately, preserving nested groups.
// Raw values are never inserted into the path.
func ExpandPath(tmpl string, rc RepoContext) (string, error) {
	parts, err := parseTemplate(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range parts {
		if p.variable == "" {
			b.WriteString(p.literal)
			continue
		}
		encoded, err := encodeVariable(p.variable, rc)
		if err != nil {
			return "", err
		}
		b.WriteString(encoded)
	}
	path := b.String()
	if err := ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

// encodeVariable returns the validated, path-segment-encoded value of the
// named variable.
func encodeVariable(name string, rc RepoContext) (string, error) {
	var value string
	switch name {
	case "forge":
		value = rc.Forge
	case "repo.host":
		value = rc.Host
	case "repo.owner":
		value = rc.Owner
	case "repo.name":
		value = rc.Name
	case "org":
		value = rc.Org
	case "repo.full_name":
		if rc.FullName == "" {
			return "", fmt.Errorf("variable %q is not available", name)
		}
		comps := strings.Split(rc.FullName, "/")
		for i, c := range comps {
			if err := validateComponent(c); err != nil {
				return "", fmt.Errorf("variable %q component %d: %w", name, i+1, err)
			}
			comps[i] = url.PathEscape(c)
		}
		return strings.Join(comps, "/"), nil
	}
	if value == "" {
		return "", fmt.Errorf("variable %q is not available", name)
	}
	if err := validateComponent(value); err != nil {
		return "", fmt.Errorf("variable %q: %w", name, err)
	}
	return url.PathEscape(value), nil
}

// validateComponent checks one path component value before encoding.
func validateComponent(c string) error {
	switch {
	case c == "":
		return fmt.Errorf("empty path component")
	case c == "." || c == "..":
		return fmt.Errorf("path component %q is not allowed", c)
	case strings.ContainsAny(c, "/\\"):
		return fmt.Errorf("path component contains a slash or backslash")
	case strings.IndexFunc(c, unicode.IsControl) >= 0:
		return fmt.Errorf("path component contains a control character")
	}
	return nil
}

// ValidatePath checks a complete relative KV v2 API path: no leading "/",
// scheme, authority, query, fragment, backslash, control character, empty
// component, or "." / ".." component, including in percent-encoded form.
func ValidatePath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("path is empty")
	case strings.HasPrefix(path, "/"):
		return fmt.Errorf("path must be relative to /v1/ (no leading \"/\")")
	case strings.Contains(path, "://") || strings.HasPrefix(path, "//"):
		return fmt.Errorf("path must not contain a scheme or authority")
	case strings.ContainsAny(path, "?#"):
		return fmt.Errorf("path must not contain a query or fragment")
	case strings.Contains(path, "\\"):
		return fmt.Errorf("path must not contain a backslash")
	case strings.IndexFunc(path, unicode.IsControl) >= 0:
		return fmt.Errorf("path must not contain control characters")
	}
	for _, comp := range strings.Split(path, "/") {
		if comp == "" {
			return fmt.Errorf("path has an empty component")
		}
		decoded, err := url.PathUnescape(comp)
		if err != nil {
			return fmt.Errorf("path has an invalid percent-encoding")
		}
		if decoded == "." || decoded == ".." {
			return fmt.Errorf("path must not contain a %q component", decoded)
		}
		if decoded != comp && (strings.ContainsAny(decoded, "/\\") || strings.IndexFunc(decoded, unicode.IsControl) >= 0) {
			// An encoded separator or control character is acceptable only
			// when it came from an encoded variable value, which never
			// carries one: validateComponent refuses them first.
			return fmt.Errorf("path must not contain an encoded separator or control character")
		}
	}
	return nil
}
