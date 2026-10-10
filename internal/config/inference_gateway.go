package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net"
	"net/url"
	"path"
	"regexp"
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

// ValidateGatewayURL checks that raw is the gateway origin: an absolute
// https URL with a host, no path beyond "/", no port other than 443, and
// no embedded credentials, query or fragment. The runner appends the
// /v1/... model API paths itself, and the OpenShell egress profile binds
// the host on port 443 only. Plain http, and any port, is accepted only
// for a loopback host (test servers), as openaiwif.requireSecureURL does.
func ValidateGatewayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("inference.gateway.url: %w", err)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return fmt.Errorf("inference.gateway.url must use https (got scheme %q; plain http is allowed only for a loopback host)", u.Scheme)
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
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("inference.gateway.url %q must be the gateway origin (for example https://gateway.example.com): the runner adds the /v1/... paths itself", raw)
	}
	if port := u.Port(); port != "" && port != "443" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("inference.gateway.url %q must be the gateway origin (for example https://gateway.example.com): the egress profile allows port 443 only", raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// gatewayCompatKeyRe is the extension's compat flag-name rule (v0.1.1
// src/config.ts COMPAT_KEY_RE).
var gatewayCompatKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// gatewayCompatCheck is a compat flag's value type in extension v0.1.1
// (src/compat.ts): a boolean, a finite number, or one of a fixed set of
// strings. A nil check marks a structured pi field (a list or record),
// which a JSON-primitive config flag cannot express.
type gatewayCompatCheck func(v any) bool

func gatewayCompatBool(v any) bool { _, ok := v.(bool); return ok }

func gatewayCompatFinite(v any) bool { return isGatewayCompatNumber(v) }

func gatewayCompatOneOf(values ...string) gatewayCompatCheck {
	return func(v any) bool {
		s, ok := v.(string)
		return ok && slices.Contains(values, s)
	}
}

var (
	gatewayOpenAISessionAffinity = gatewayCompatOneOf("openai", "openai-nosession", "openrouter")
	gatewayThinkingFormats       = gatewayCompatOneOf("openai", "openrouter", "deepseek", "together", "baseten", "zai", "qwen", "chat-template", "qwen-chat-template", "string-thinking", "ant-ling")
)

// gatewayCompatFields are the compat flags extension v0.1.1 knows per
// transport (ANTHROPIC_FIELDS, RESPONSES_FIELDS and COMPLETIONS_FIELDS in
// src/compat.ts) with their value types. Re-check them when the extension
// pin moves.
var gatewayCompatFields = map[string]map[string]gatewayCompatCheck{
	GatewayAPIAnthropicMessages: {
		"supportsEagerToolInputStreaming": gatewayCompatBool,
		"supportsLongCacheRetention":      gatewayCompatBool,
		"sendSessionAffinityHeaders":      gatewayCompatBool,
		"sessionAffinityFormat":           gatewayCompatOneOf("openrouter"),
		"supportsCacheControlOnTools":     gatewayCompatBool,
		"supportsTemperature":             gatewayCompatBool,
		"forceAdaptiveThinking":           gatewayCompatBool,
		"allowEmptySignature":             gatewayCompatBool,
		"supportsStrictTools":             gatewayCompatBool,
		"supportsMidConvoEffort":          gatewayCompatBool,
		"supportsMidConvoSystemMessages":  gatewayCompatBool,
		"supportsMidConvoToolChanges":     gatewayCompatBool,
		"allowedFallbackModels":           nil,
	},
	GatewayAPIOpenAIResponses: {
		"supportsDeveloperRole":           gatewayCompatBool,
		"supportsMidConvoSystemMessages":  gatewayCompatBool,
		"sessionAffinityFormat":           gatewayOpenAISessionAffinity,
		"supportsLongCacheRetention":      gatewayCompatBool,
		"supportsStrictMode":              gatewayCompatBool,
		"supportsOpenAIGrammarTools":      gatewayCompatBool,
		"supportsAdditionalTools":         gatewayCompatBool,
		"supportsToolSearch":              gatewayCompatBool,
		"supportsExplicitPromptCacheMode": gatewayCompatBool,
		"supportsMaxOutputTokens":         gatewayCompatBool,
	},
	GatewayAPIOpenAICompletions: {
		"supportsStore":                               gatewayCompatBool,
		"supportsDeveloperRole":                       gatewayCompatBool,
		"supportsReasoningEffort":                     gatewayCompatBool,
		"supportsUsageInStreaming":                    gatewayCompatBool,
		"supportsFinishReason":                        gatewayCompatBool,
		"maxTokensField":                              gatewayCompatOneOf("max_completion_tokens", "max_tokens"),
		"requiresToolResultName":                      gatewayCompatBool,
		"requiresAssistantAfterToolResult":            gatewayCompatBool,
		"requiresThinkingAsText":                      gatewayCompatBool,
		"requiresReasoningContentOnAssistantMessages": gatewayCompatBool,
		"thinkingFormat":                              gatewayThinkingFormats,
		"chatTemplateKwargs":                          nil,
		"chatTemplateArgs":                            nil,
		"openRouterRouting":                           nil,
		"vercelGatewayRouting":                        nil,
		"zaiToolStream":                               gatewayCompatBool,
		"thinkingTokenBudgetField":                    gatewayCompatOneOf("thinking_token_budget", "thinking_budget", "thinking_budget_tokens"),
		"supportsThinkingTokenBudget":                 gatewayCompatBool,
		"supportsOpenAIGrammarTools":                  gatewayCompatBool,
		"supportsMidConvoSystemMessages":              gatewayCompatBool,
		"supportsMidConvoToolAdditions":               gatewayCompatBool,
		"supportsStrictMode":                          gatewayCompatBool,
		"cacheControlFormat":                          gatewayCompatOneOf("anthropic"),
		"sendSessionAffinityHeaders":                  gatewayCompatBool,
		"sessionAffinityFormat":                       gatewayOpenAISessionAffinity,
		"supportsLongCacheRetention":                  gatewayCompatBool,
		"vllmPriority":                                gatewayCompatFinite,
	},
}

// isKnownGatewayCompatFlag reports whether any transport declares k.
func isKnownGatewayCompatFlag(k string) bool {
	for _, fields := range gatewayCompatFields {
		if _, ok := fields[k]; ok {
			return true
		}
	}
	return false
}

// gatewayCredentialWords mark an unknown compat flag name as credential-
// or header-shaped. compat holds request-feature flags, and a value under
// such a name would be committed to the repository.
var gatewayCredentialWords = []string{"key", "token", "secret", "password", "passwd", "pwd", "auth", "credential", "cookie", "header", "bearer", "user", "login"}

// ValidateGatewayCompat checks a model's compat map the way extension
// v0.1.1 reads it (src/config.ts and validateCompat in src/compat.ts), but
// refuses what the extension would drop with a warning:
//   - flag names match COMPAT_KEY_RE;
//   - a flag the model's api declares must have that transport's type
//     (with api empty, the type of any transport that declares it);
//     structured fields such as allowedFallbackModels are refused;
//   - any other value is a boolean, a string or a finite number, under a
//     name that does not look like a credential or header.
//
// Holding values to these scalar types also keeps nested data (including
// the map[interface{}]interface{} yaml.v3 builds for a non-string-keyed
// mapping) out of the rendered file, and makes the one-level copy in
// cloneGatewayModels a full copy. Free-text string values cannot be
// scanned for secrets; the name filter and the typed known flags narrow
// where one could go.
func ValidateGatewayCompat(compat map[string]any, api string) error {
	for _, k := range slices.Sorted(maps.Keys(compat)) {
		v := compat[k]
		if !gatewayCompatKeyRe.MatchString(k) {
			return fmt.Errorf("flag name %q must match %s", k, gatewayCompatKeyRe)
		}
		if !isGatewayCompatValue(v) {
			return fmt.Errorf("flag %q must be a boolean, string or number", k)
		}
		// The extension refuses this one on every transport (validateCompat):
		// it is a list of model ids, and the gateway routes models itself.
		if k == "allowedFallbackModels" {
			return fmt.Errorf("flag %q is not supported: the gateway routes models itself", k)
		}
		checks, structured := gatewayCompatChecks(k, api)
		if len(checks) == 0 && !structured && api != "" {
			// A flag only another transport declares: the extension keeps
			// it untyped, but fullsend still holds it to the declared type
			// so the name cannot carry free text past the credential filter.
			checks, structured = gatewayCompatChecks(k, "")
		}
		switch {
		case structured && len(checks) == 0:
			return fmt.Errorf("flag %q is a list or record in pi, which a compat flag cannot carry", k)
		case len(checks) > 0:
			if !slices.ContainsFunc(checks, func(c gatewayCompatCheck) bool { return c(v) }) {
				return fmt.Errorf("flag %q does not match pi's type for this field%s", k, gatewayCompatAPISuffix(api))
			}
		case !isKnownGatewayCompatFlag(k):
			lower := strings.ToLower(k)
			for _, w := range gatewayCredentialWords {
				if strings.Contains(lower, w) {
					return fmt.Errorf("flag %q looks like a credential or header, which compat must not carry", k)
				}
			}
		}
	}
	return nil
}

// gatewayCompatChecks returns the value checks the transports declare for
// flag k (only api's when api is set) and whether any declares it as a
// structured field.
func gatewayCompatChecks(k, api string) ([]gatewayCompatCheck, bool) {
	var checks []gatewayCompatCheck
	structured := false
	for _, target := range ValidGatewayAPIs() {
		if api != "" && target != api {
			continue
		}
		if check, ok := gatewayCompatFields[target][k]; ok {
			if check == nil {
				structured = true
				continue
			}
			checks = append(checks, check)
		}
	}
	return checks, structured
}

func gatewayCompatAPISuffix(api string) string {
	if api == "" {
		return ""
	}
	return " on " + api
}

func isGatewayCompatNumber(v any) bool {
	switch x := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return !math.IsInf(float64(x), 0) && !math.IsNaN(float64(x))
	case float64:
		return !math.IsInf(x, 0) && !math.IsNaN(x)
	case json.Number:
		f, err := x.Float64()
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	}
	return false
}

func isGatewayCompatValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return true
	case string:
		return len(utf16.Encode([]rune(x))) <= 256 && !strings.ContainsFunc(x, unicode.IsControl)
	}
	return isGatewayCompatNumber(v)
}

func validateGatewayModel(id string, m InferenceGatewayModel) error {
	if err := ValidateGatewayModelID(id); err != nil {
		return fmt.Errorf("inference.gateway.models: %w", err)
	}
	if !slices.Contains(ValidGatewayAPIs(), m.API) {
		return fmt.Errorf("inference.gateway.models[%q]: invalid api %q: must be one of %s", id, m.API, strings.Join(ValidGatewayAPIs(), ", "))
	}
	if err := ValidateGatewayCompat(m.Compat, m.API); err != nil {
		return fmt.Errorf("inference.gateway.models[%q].compat: %w", id, err)
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
