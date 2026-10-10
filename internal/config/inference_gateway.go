package config

import (
	"fmt"
	"maps"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
)

// Inference gateway APIs a model may speak (ADR 0137). They match the
// pi-inference-gateway extension's per-model "api" values.
const (
	GatewayAPIOpenAIResponses   = "openai-responses"
	GatewayAPIAnthropicMessages = "anthropic-messages"
	GatewayAPIOpenAICompletions = "openai-completions"
)

// ValidGatewayAPIs returns the accepted per-model "api" values for an
// inference.gateway model list.
func ValidGatewayAPIs() []string {
	return []string{GatewayAPIOpenAIResponses, GatewayAPIAnthropicMessages, GatewayAPIOpenAICompletions}
}

// InferenceGatewayConfig is the inference.gateway block (ADR 0137): a
// self-hosted, OpenAI/Anthropic-compatible gateway that validates the job's
// forge OIDC token directly. URL and Audience are all-or-none (see
// Missing). The model list is optional at block level and takes exactly
// one of two forms: Models (inline) or ModelsFile (a repository path to a
// file in the pi-inference-gateway extension's own config format). A
// runtime that cannot discover models (pi under PI_OFFLINE) requires one
// when a gateway/ model is resolved.
//
// There is deliberately no runner-variable override: the committed config
// is the single source, and pull-request events read it from the base
// branch, so a pull request cannot redirect its own run.
type InferenceGatewayConfig struct {
	URL        string                           `yaml:"url,omitempty"`
	Audience   string                           `yaml:"audience,omitempty"`
	Models     map[string]InferenceGatewayModel `yaml:"models,omitempty"`
	ModelsFile string                           `yaml:"models_file,omitempty"`
}

// InferenceGatewayModel holds the per-model settings of an inline
// inference.gateway model list. Field names follow the
// pi-inference-gateway extension's config keys so they render verbatim.
type InferenceGatewayModel struct {
	API           string         `yaml:"api"`
	Compat        map[string]any `yaml:"compat,omitempty"`
	ContextWindow int            `yaml:"contextWindow,omitempty"`
	MaxTokens     int            `yaml:"maxTokens,omitempty"`
}

// Trimmed returns the block with surrounding whitespace removed from the
// scalar fields, so a whitespace-only YAML value counts as unset. Models
// is copied (shallowly per entry) so the result does not alias the
// receiver's map.
func (c InferenceGatewayConfig) Trimmed() InferenceGatewayConfig {
	out := InferenceGatewayConfig{
		URL:        strings.TrimSpace(c.URL),
		Audience:   strings.TrimSpace(c.Audience),
		ModelsFile: strings.TrimSpace(c.ModelsFile),
	}
	if c.Models != nil {
		out.Models = make(map[string]InferenceGatewayModel, len(c.Models))
		for id, m := range c.Models {
			m.API = strings.TrimSpace(m.API)
			out.Models[strings.TrimSpace(id)] = m
		}
	}
	return out
}

// IsZero reports whether no field is set.
func (c InferenceGatewayConfig) IsZero() bool {
	return c.URL == "" && c.Audience == "" && len(c.Models) == 0 && c.ModelsFile == ""
}

// HasModelList reports whether either model-list form is set.
func (c InferenceGatewayConfig) HasModelList() bool {
	return len(c.Models) > 0 || c.ModelsFile != ""
}

// Missing lists the required fields still unset (url, audience), in a
// fixed order, so a partial block can be reported precisely. The model
// list is not required at block level: it is checked when a runtime that
// needs it resolves a gateway/ model.
func (c InferenceGatewayConfig) Missing() []string {
	var missing []string
	if c.URL == "" {
		missing = append(missing, "url")
	}
	if c.Audience == "" {
		missing = append(missing, "audience")
	}
	return missing
}

// ModelIDs returns the inline model ids in sorted order.
func (c InferenceGatewayConfig) ModelIDs() []string {
	ids := make([]string, 0, len(c.Models))
	for id := range c.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Validate checks the block's set fields. It does not enforce all-or-none
// on url + audience, because the block layers field by field (an org
// preset may carry url and audience while a repository adds its models);
// callers that resolved the effective block check Missing.
func (c InferenceGatewayConfig) Validate() error {
	if c.URL != "" {
		if err := ValidateGatewayURL(c.URL); err != nil {
			return err
		}
	}
	if len(c.Models) > 0 && c.ModelsFile != "" {
		return fmt.Errorf("inference.gateway: models and models_file are mutually exclusive")
	}
	if c.ModelsFile != "" {
		if err := ValidateGatewayModelsFilePath(c.ModelsFile); err != nil {
			return err
		}
	}
	for _, id := range c.ModelIDs() {
		if err := validateGatewayModel(id, c.Models[id]); err != nil {
			return err
		}
	}
	return nil
}

// ValidateGatewayURL checks that raw is an absolute https URL with a host
// and no embedded credentials, query or fragment.
func ValidateGatewayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("inference.gateway.url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("inference.gateway.url must use https (got scheme %q)", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("inference.gateway.url %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("inference.gateway.url must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("inference.gateway.url must not carry a query or fragment")
	}
	return nil
}

// ValidateGatewayModelsFilePath checks that p is a clean, relative
// repository path that stays inside the repository.
func ValidateGatewayModelsFilePath(p string) error {
	if strings.Contains(p, `\`) {
		return fmt.Errorf("inference.gateway.models_file %q must use forward slashes", p)
	}
	if path.IsAbs(p) {
		return fmt.Errorf("inference.gateway.models_file %q must be a repository-relative path", p)
	}
	if path.Clean(p) != p || p == "." || strings.HasPrefix(p, "../") || p == ".." {
		return fmt.Errorf("inference.gateway.models_file %q must be a clean path inside the repository", p)
	}
	return nil
}

// MaxGatewayModelIDLength is the longest model id the pi-inference-gateway
// extension accepts (v0.1.1 src/config.ts MAX_MODEL_ID_LENGTH), counted
// in UTF-16 code units as JavaScript counts string length.
const MaxGatewayModelIDLength = 256

// ValidateGatewayModelID applies the extension's model-id rule (v0.1.1
// src/config.ts isValidModelId): 1-256 characters, no whitespace and no
// control characters. The extension skips any other id with a warning, so
// a run would otherwise fail later with "model not found".
func ValidateGatewayModelID(id string) error {
	if id == "" {
		return fmt.Errorf("empty model id")
	}
	if n := len(utf16.Encode([]rune(id))); n > MaxGatewayModelIDLength {
		return fmt.Errorf("model id %q is %d characters long (at most %d)", id, n, MaxGatewayModelIDLength)
	}
	for _, r := range id {
		// JavaScript's \s also matches U+FEFF, which unicode.IsSpace does not.
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\ufeff' {
			return fmt.Errorf("model id %q must not contain whitespace or control characters", id)
		}
	}
	return nil
}

func validateGatewayModel(id string, m InferenceGatewayModel) error {
	if err := ValidateGatewayModelID(id); err != nil {
		return fmt.Errorf("inference.gateway.models: %w", err)
	}
	if !slices.Contains(ValidGatewayAPIs(), m.API) {
		return fmt.Errorf("inference.gateway.models[%q]: invalid api %q: must be one of %s", id, m.API, strings.Join(ValidGatewayAPIs(), ", "))
	}
	if m.ContextWindow < 0 {
		return fmt.Errorf("inference.gateway.models[%q]: contextWindow must not be negative", id)
	}
	if m.MaxTokens < 0 {
		return fmt.Errorf("inference.gateway.models[%q]: maxTokens must not be negative", id)
	}
	return nil
}

// mergeGateway layers child over parent field by field: url and audience
// resolve independently, and the model list (either form) is one unit — a
// layer that states a list replaces the parent's list, so the two forms
// never combine across layers.
func mergeGateway(parent InferenceGatewayConfig, child *InferenceGatewayConfig) InferenceGatewayConfig {
	out := parent
	if child == nil {
		return out
	}
	if child.URL != "" {
		out.URL = child.URL
	}
	if child.Audience != "" {
		out.Audience = child.Audience
	}
	if child.HasModelList() {
		out.Models = cloneGatewayModels(child.Models)
		out.ModelsFile = child.ModelsFile
	}
	return out
}

func cloneGatewayModels(src map[string]InferenceGatewayModel) map[string]InferenceGatewayModel {
	if src == nil {
		return nil
	}
	out := make(map[string]InferenceGatewayModel, len(src))
	for id, m := range src {
		if m.Compat != nil {
			c := make(map[string]any, len(m.Compat))
			maps.Copy(c, m.Compat)
			m.Compat = c
		}
		out[id] = m
	}
	return out
}

func cloneGateway(src *InferenceGatewayConfig) *InferenceGatewayConfig {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Models = cloneGatewayModels(src.Models)
	return &cp
}
