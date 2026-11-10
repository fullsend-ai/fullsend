package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// TraceSigningKeySecret is the only signing-key reference v1 accepts in
// trace_context.signing_key.secret. internal/forge declares the same
// literal as SecretTraceSigningKey; config must not import forge.
const TraceSigningKeySecret = "FULLSEND_TRACE_SIGNING_KEY"

// TraceContextConfig is the trace_context block (ADR 0154): the trace
// installation ID, the protected reference to the signing key and the
// public verification keys. Every value is public. Only a concrete
// repository entry in repos.yaml carries it; config.base.yaml never
// supplies it.
type TraceContextConfig struct {
	InstallationID   string                 `yaml:"installation_id,omitempty"`
	SigningKey       *TraceSigningKeyRef    `yaml:"signing_key,omitempty"`
	VerificationKeys *TraceVerificationKeys `yaml:"verification_keys,omitempty"`
}

// TraceSigningKeyRef names where the signing key lives. In v1 Secret must
// be TraceSigningKeySecret.
type TraceSigningKeyRef struct {
	Secret string `yaml:"secret,omitempty"`
}

// TraceVerificationKeys holds the active verification key and the retained
// keys that still verify envelopes signed before a rotation, each as
// standard base64 of a 32-byte Ed25519 public key.
type TraceVerificationKeys struct {
	Active   string   `yaml:"active,omitempty"`
	Retained []string `yaml:"retained,omitempty"`
}

// ValidateTraceContext checks a trace_context block; nil is valid. A
// non-nil block must be complete: installation_id (32 lowercase hex
// characters, not all zero), signing_key.secret (exactly
// TraceSigningKeySecret) and verification_keys.active. The active key and
// each retained key must be standard base64 of 32 bytes, and no two keys
// may share a key ID. Errors name the field path, for example
// trace_context.verification_keys.retained[1], and never contain the
// value. validateLocalFields calls it on the write path, and runAgent on
// the run path, because fullsend run never calls Validate.
func ValidateTraceContext(tc *TraceContextConfig) error {
	if tc == nil {
		return nil
	}
	if _, err := tc.Installation(); err != nil {
		return err
	}
	if tc.SigningKey == nil || tc.SigningKey.Secret != TraceSigningKeySecret {
		return fmt.Errorf("trace_context.signing_key.secret must be %s", TraceSigningKeySecret)
	}
	_, err := tc.KeySet()
	return err
}

// Installation returns the parsed installation ID of a validated block.
func (tc *TraceContextConfig) Installation() ([16]byte, error) {
	id, err := tracecontext.ParseInstallationID(tc.InstallationID)
	if err != nil {
		return [16]byte{}, errors.New("trace_context.installation_id must be 32 lowercase hex characters, not all zero")
	}
	return id, nil
}

// ActiveKey returns the parsed active verification key of a validated
// block.
func (tc *TraceContextConfig) ActiveKey() (ed25519.PublicKey, error) {
	var active string
	if tc.VerificationKeys != nil {
		active = tc.VerificationKeys.Active
	}
	return parseTraceVerificationKey("trace_context.verification_keys.active", active)
}

// KeySet returns the admission key map of a validated block: the active and
// retained keys, keyed by key ID (tracecontext.KeySet).
func (tc *TraceContextConfig) KeySet() (map[[8]byte]ed25519.PublicKey, error) {
	active, err := tc.ActiveKey()
	if err != nil {
		return nil, err
	}
	retained := make([]ed25519.PublicKey, len(tc.VerificationKeys.Retained))
	for i, s := range tc.VerificationKeys.Retained {
		path := fmt.Sprintf("trace_context.verification_keys.retained[%d]", i)
		if retained[i], err = parseTraceVerificationKey(path, s); err != nil {
			return nil, err
		}
	}
	keys, err := tracecontext.KeySet(active, retained)
	if err != nil {
		// Every key parsed, so what is left is two keys with one key ID.
		return nil, errors.New("trace_context.verification_keys must not list a key twice: the active and retained keys need distinct key IDs")
	}
	return keys, nil
}

// parseTraceVerificationKey parses one verification key. The error names
// path, never the value.
func parseTraceVerificationKey(path, s string) (ed25519.PublicKey, error) {
	pub, err := tracecontext.ParseVerificationKey(s)
	if err != nil {
		return nil, fmt.Errorf("%s must be standard base64 (with padding) of a 32-byte Ed25519 public key", path)
	}
	return pub, nil
}

// cloneTraceContext returns a deep copy of tc, or nil when tc is nil.
func cloneTraceContext(tc *TraceContextConfig) *TraceContextConfig {
	if tc == nil {
		return nil
	}
	out := *tc
	if tc.SigningKey != nil {
		ref := *tc.SigningKey
		out.SigningKey = &ref
	}
	if tc.VerificationKeys != nil {
		keys := *tc.VerificationKeys
		keys.Retained = cloneStringSlice(keys.Retained)
		out.VerificationKeys = &keys
	}
	return &out
}
