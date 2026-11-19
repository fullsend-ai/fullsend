package repos

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// trackerMappingCommit is the fullsend-ai/fullsend merge commit of #9082,
// which replaced the legacy tracker scalar and create_issues keys with the
// tracker mapping (configuration schema version 2). A pinned ref that
// predates config.ManagedConfigSchemaDeclarationPath is legacy unless it
// contains this commit.
const trackerMappingCommit = "32c4e78cd9f982a528550baa818096b4446cdd92"

// Managed configuration compatibility values reported by `repos status`.
const (
	ConfigCompatible   = "compatible"
	ConfigIncompatible = "incompatible"
	ConfigUnknown      = "unknown"
)

// managedConfigSchema is the per-repo configuration schema the repository's
// effective fullsend_ref reads (#9292). The zero value is the running
// binary's schema, used when the repository renders the binary's own
// release (no explicit pin).
type managedConfigSchema struct {
	// Ref is the explicitly pinned fullsend_ref the schema was selected
	// for; empty when the repository follows the running release.
	Ref string
	// Version is the selected schema version; 0 means
	// config.ManagedConfigSchemaVersion.
	Version int
	// Unknown is the reason the schema could not be determined; when set,
	// managed configuration is neither rendered nor reported as current.
	Unknown string
}

// version returns the selected schema version.
func (s managedConfigSchema) version() int {
	if s.Version == 0 {
		return config.ManagedConfigSchemaVersion
	}
	return s.Version
}

// unknownError returns the error reported when the schema is unknown, or nil.
func (s managedConfigSchema) unknownError() error {
	if s.Unknown == "" {
		return nil
	}
	return fmt.Errorf("configuration schema of fullsend_ref %q is unknown: %s; %s was left unchanged", s.Ref, s.Unknown, preset.OverlayPath)
}

// selectManagedConfigSchema returns the configuration schema to render and
// assess resolved's managed configuration in. A pinned fullsend_ref is
// resolved only when the schema matters: when the desired managed
// configuration or existing, the installed overlay (nil when not read),
// declares a key whose shape differs between known schemas. Otherwise every
// known schema renders and reads the configuration identically, and the
// binary's schema is used without upstream lookups.
func selectManagedConfigSchema(ctx context.Context, resolved ResolvedConfig, existing []byte, upstreamRef, upstreamTag string, resolver *RefResolver) managedConfigSchema {
	if !resolved.ConfigManaged || !pinnedFullsendRef(resolved.FullsendRef, upstreamRef, upstreamTag) {
		return managedConfigSchema{}
	}
	body, err := marshalManagedConfig(resolved.Managed)
	if err == nil && !config.ManagedConfigSchemaDependent(body) && !config.ManagedConfigSchemaDependent(existing) {
		return managedConfigSchema{Ref: resolved.FullsendRef}
	}
	return resolveManagedConfigSchema(ctx, resolved.FullsendRef, upstreamRef, upstreamTag, resolver)
}

// pinnedFullsendRef reports whether fullsendRef is an explicit pin rather
// than the running release (see resolveTargetRef).
func pinnedFullsendRef(fullsendRef, upstreamRef, upstreamTag string) bool {
	return fullsendRef != "" && fullsendRef != upstreamRef && fullsendRef != upstreamTag
}

// resolveManagedConfigSchema selects the configuration schema for a
// repository's effective fullsend_ref. A repository that follows the running
// release (no pin, or a pin equal to the release ref or tag) uses the
// binary's schema. A pinned ref is resolved to a commit on
// fullsend-ai/fullsend where possible (the forge resolves a tag or branch
// the resolver could not); the schema it declares in
// config.ManagedConfigSchemaDeclarationPath is used, and a ref without that
// file is legacy unless it contains trackerMappingCommit. Anything that
// cannot be determined, including a ref that does not exist, is reported as
// unknown, never guessed.
func resolveManagedConfigSchema(ctx context.Context, fullsendRef, upstreamRef, upstreamTag string, resolver *RefResolver) managedConfigSchema {
	if !pinnedFullsendRef(fullsendRef, upstreamRef, upstreamTag) {
		return managedConfigSchema{}
	}
	s := managedConfigSchema{Ref: fullsendRef}
	if resolver == nil {
		s.Unknown = "resolving the ref requires an upstream GitHub client, and none is available"
		return s
	}
	commit := resolver.Resolve(ctx, fullsendRef)
	data, err := resolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, config.ManagedConfigSchemaDeclarationPath, commit)
	switch {
	case err == nil:
		v, parseErr := config.ParseManagedConfigSchemaDeclaration(data)
		if parseErr != nil {
			s.Unknown = fmt.Sprintf("reading %s: %v", config.ManagedConfigSchemaDeclarationPath, parseErr)
			return s
		}
		if v < config.ManagedConfigSchemaLegacy || v > config.ManagedConfigSchemaVersion {
			s.Unknown = fmt.Sprintf("the ref declares schema version %d, which this fullsend does not know (known: %d to %d); upgrade fullsend",
				v, config.ManagedConfigSchemaLegacy, config.ManagedConfigSchemaVersion)
			return s
		}
		s.Version = v
		return s
	case !forge.IsNotFound(err):
		s.Unknown = fmt.Sprintf("reading %s: %v", config.ManagedConfigSchemaDeclarationPath, err)
		return s
	}
	// No declaration: the ref predates explicit schema metadata, or does
	// not exist, which the comparison reports as an error.
	hasMapping, err := resolver.IsAncestor(ctx, trackerMappingCommit, commit)
	if err != nil {
		s.Unknown = fmt.Sprintf("the ref cannot be resolved or compared on %s/%s: %v", shimOwner, shimRepo, err)
		return s
	}
	if hasMapping {
		s.Version = config.ManagedConfigSchemaTrackerMapping
	} else {
		s.Version = config.ManagedConfigSchemaLegacy
	}
	return s
}

// checkManagedConfigCompatibility records in status whether the installed
// overlay is readable at the repository's effective fullsend_ref, whose
// schema is known. found reports whether the overlay exists; an absent
// overlay is not judged.
func checkManagedConfigCompatibility(cfg ResolvedConfig, existing []byte, found bool, status *RepoStatus) {
	if !found {
		return
	}
	status.ConfigSchemaRef = cfg.ConfigSchema.Ref
	v := cfg.ConfigSchema.version()
	if err := config.CheckManagedConfigSchemaCompat(existing, v); err != nil {
		status.ConfigCompatibility = ConfigIncompatible
		status.ConfigCompatibilityReason = fmt.Sprintf("%s %v (schema version %d)", preset.OverlayPath, err, v)
		return
	}
	status.ConfigCompatibility = ConfigCompatible
}

// reportUnknownManagedConfigSchema reports a repository whose effective
// fullsend_ref schema cannot be determined: compatibility is unknown, with
// the ref and reason, and the overlay is reported as drift instead of being
// compared with a rendering in a guessed schema. A repository with no
// Fullsend components and no overlay has nothing to report yet.
func reportUnknownManagedConfigSchema(ctx context.Context, cfg ResolvedConfig, status *RepoStatus) {
	if !status.Installed {
		_, found, err := readExistingFile(ctx, cfg.ForgeConfig.Client, cfg.Owner, cfg.Repo, preset.OverlayPath)
		if err != nil {
			status.Error = fmt.Sprintf("reading %s for %s/%s: %v", preset.OverlayPath, cfg.Owner, cfg.Repo, err)
			return
		}
		if !found {
			return
		}
	}
	status.ConfigSchemaRef = cfg.ConfigSchema.Ref
	status.ConfigCompatibility = ConfigUnknown
	status.ConfigCompatibilityReason = cfg.ConfigSchema.Unknown
	status.Drifts = append(status.Drifts, Drift{
		Field:    preset.OverlayPath,
		Expected: fmt.Sprintf("managed configuration in the schema of fullsend_ref %q", cfg.ConfigSchema.Ref),
		Actual:   "compatibility unknown: " + cfg.ConfigSchema.Unknown,
	})
}
