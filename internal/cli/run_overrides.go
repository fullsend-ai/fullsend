package cli

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// Runtime-neutral override environment variables. They are overrides of the
// same values the config file and the harness carry; the CLI resolves them
// once (flag > env > config/harness > default), prints the source, records it
// in metrics.json, and hands the result to the runtime — runtimes never read
// these themselves (#6526).
const (
	envRuntime        = "FULLSEND_RUNTIME"
	envModel          = "FULLSEND_MODEL"
	envEffort         = "FULLSEND_EFFORT"
	envFallbackModels = "FULLSEND_FALLBACK_MODELS"
	// envPiModel is the pre-#6526 pi-only model override, kept as an alias of
	// FULLSEND_MODEL for pi runs. FULLSEND_PI_PROVIDER stays pi-only (it is
	// the provider prefix for bare ids, not a model choice).
	envPiModel = "FULLSEND_PI_MODEL"

	sourceFlagRuntime = "--runtime flag"
	sourceFlagModel   = "--model flag"
	sourceFlagEffort  = "--effort flag"
	sourceHarness     = "harness"
	sourceDefault     = "default"
)

// runOverrideFlags are the per-run override flags of `fullsend run`.
type runOverrideFlags struct {
	runtime string
	model   string
	effort  string
}

// runOverrides is the resolved per-run override set. Empty *Source fields
// mean "not overridden" — the config file (runtime) or the composed harness
// (model, effort) stays in charge.
type runOverrides struct {
	runtime       string
	runtimeSource string

	model       string
	modelSource string

	effort       string
	effortSource string

	fallbackModels []string
	fallbackSource string
}

// resolveRunOverrides applies the precedence flag > env for each override.
// runtimeName is the runtime that will run (after the runtime override, if
// any) and only gates the pi-specific FULLSEND_PI_MODEL alias.
func resolveRunOverrides(flags runOverrideFlags, getenv func(string) string, runtimeName string) (runOverrides, error) {
	var o runOverrides
	env := func(name string) string { return strings.TrimSpace(getenv(name)) }

	switch {
	case strings.TrimSpace(flags.runtime) != "":
		o.runtime, o.runtimeSource = strings.TrimSpace(flags.runtime), sourceFlagRuntime
	case env(envRuntime) != "":
		o.runtime, o.runtimeSource = env(envRuntime), envRuntime
	}
	if o.runtime != "" {
		if err := validateRuntimeName(o.runtime); err != nil {
			return runOverrides{}, fmt.Errorf("%s: %w", o.runtimeSource, err)
		}
		runtimeName = o.runtime
	}

	switch {
	case strings.TrimSpace(flags.model) != "":
		o.model, o.modelSource = strings.TrimSpace(flags.model), sourceFlagModel
	case env(envModel) != "":
		o.model, o.modelSource = env(envModel), envModel
	case runtimeName == "pi" && env(envPiModel) != "":
		o.model, o.modelSource = env(envPiModel), envPiModel
	}

	switch {
	case strings.TrimSpace(flags.effort) != "":
		o.effort, o.effortSource = strings.TrimSpace(flags.effort), sourceFlagEffort
	case env(envEffort) != "":
		o.effort, o.effortSource = env(envEffort), envEffort
	}

	if v := env(envFallbackModels); v != "" {
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				o.fallbackModels = append(o.fallbackModels, m)
			}
		}
		if len(o.fallbackModels) > 0 {
			o.fallbackSource = envFallbackModels
		}
	}
	return o, nil
}

// validateRuntimeName mirrors the config validation so a flag/env override
// cannot select a runtime the config file could not.
func validateRuntimeName(name string) error {
	for _, v := range config.ValidRuntimes() {
		if name == v {
			return nil
		}
	}
	return fmt.Errorf("invalid runtime %q: must be one of %s", name, strings.Join(config.ValidRuntimes(), ", "))
}

// resolveBackend returns the runtime backend for the run and a human-readable
// source: the override (flag/env) when set, else the config file path (or the
// built-in default when no config exists). When agentName is non-empty and
// no flag/env override is set, the agents: entry's runtime from the
// config file takes precedence over the repo-wide runtime: key.
func resolveBackend(o runOverrides, configPath, agentName string) (agentruntime.Backend, string, error) {
	rc, err := loadRunConfig(configPath)
	if err != nil {
		return agentruntime.Backend{}, rc.source, err
	}
	return resolveBackendFrom(o, rc, agentName)
}

// resolveBackendFrom is resolveBackend over an already loaded config, so a
// run loads config.yaml once for runtime selection and agent settings.
func resolveBackendFrom(o runOverrides, rc runConfig, agentName string) (agentruntime.Backend, string, error) {
	if o.runtime == "" {
		return rc.backend(agentName)
	}
	backend, err := agentruntime.Resolve(o.runtime)
	if err != nil {
		return agentruntime.Backend{}, o.runtimeSource, fmt.Errorf("%w: %w", errResolvingRuntime, err)
	}
	return backend, o.runtimeSource, nil
}

// modelOverrideSource is the metrics.json override_source value for the
// effective model: the override source when one applied, "harness" when the
// composed harness/agent supplied the model, "default" otherwise.
func modelOverrideSource(o runOverrides, effectiveModel string) string {
	if o.modelSource != "" {
		return o.modelSource
	}
	if effectiveModel != "" {
		return sourceHarness
	}
	return sourceDefault
}
