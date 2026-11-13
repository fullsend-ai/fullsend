package tracecontext

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Errors for invalid installation IDs, keys and key IDs. Their text is
// fixed: they never wrap or quote the input, which may be key material.
var (
	ErrInvalidInstallationID  = errors.New("tracecontext: invalid installation ID")
	ErrInvalidVerificationKey = errors.New("tracecontext: invalid verification key")
	ErrInvalidSigningSeed     = errors.New("tracecontext: invalid signing seed")
	ErrInvalidKeyID           = errors.New("tracecontext: invalid key ID")
	ErrDuplicateKeyID         = errors.New("tracecontext: duplicate verification key ID")
)

// redacted is what String, GoString, Format, MarshalJSON, MarshalText and
// LogValue return for a SigningKey.
const redacted = "[redacted]"

// ParseInstallationID accepts exactly 32 lowercase hex characters that are
// not all zero, and returns ErrInvalidInstallationID otherwise.
func ParseInstallationID(s string) ([16]byte, error) {
	var id [16]byte
	if !decodeLowerHex(id[:], s) || id == ([16]byte{}) {
		return [16]byte{}, ErrInvalidInstallationID
	}
	return id, nil
}

// FormatInstallationID returns id as 32 lowercase hex characters.
func FormatInstallationID(id [16]byte) string {
	return hex.EncodeToString(id[:])
}

// NewInstallationID reads 16 bytes from r (crypto/rand.Reader when r is
// nil). An all-zero result is an error.
func NewInstallationID(r io.Reader) ([16]byte, error) {
	var id [16]byte
	if err := readRandom(r, id[:]); err != nil {
		return [16]byte{}, fmt.Errorf("tracecontext: generate installation ID: %w", err)
	}
	if id == ([16]byte{}) {
		return [16]byte{}, errors.New("tracecontext: generate installation ID: the random source returned all zeros")
	}
	return id, nil
}

// ParseVerificationKey accepts strict standard base64 (with padding) of
// exactly 32 bytes, and returns ErrInvalidVerificationKey otherwise.
func ParseVerificationKey(s string) (ed25519.PublicKey, error) {
	b, ok := decodeStrictBase64(s, ed25519.PublicKeySize)
	if !ok {
		return nil, ErrInvalidVerificationKey
	}
	return ed25519.PublicKey(b), nil
}

// FormatVerificationKey returns pub as standard base64 with padding.
func FormatVerificationKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ParseKeyID accepts exactly 16 lowercase hex characters, and returns
// ErrInvalidKeyID otherwise. FormatKeyID is its inverse.
func ParseKeyID(s string) ([8]byte, error) {
	var id [8]byte
	if !decodeLowerHex(id[:], s) {
		return [8]byte{}, ErrInvalidKeyID
	}
	return id, nil
}

// FormatKeyID returns id as 16 lowercase hex characters.
func FormatKeyID(id [8]byte) string {
	return hex.EncodeToString(id[:])
}

// SigningKey holds an Ed25519 private key expanded from a 32-byte seed.
// EncodeSeed is the only way the key material leaves the type: String,
// GoString, Format (every verb), MarshalJSON, MarshalText and LogValue all
// return "[redacted]". The zero value holds no key.
type SigningKey struct {
	key *signingKey
}

// signingKey keeps the key material two pointers away from SigningKey. fmt
// never calls Format on a SigningKey reached through an unexported field;
// it prints the field by reflection instead, and for a verb a pointer does
// not take (%s, %q) it also prints the pointee. With every field here a
// pointer, fmt shows only addresses.
type signingKey struct {
	priv *ed25519.PrivateKey
}

// ParseSigningSeed trims surrounding ASCII whitespace (not Unicode spaces
// such as U+00A0), then accepts strict standard base64 (no CR or LF
// anywhere) of exactly 32 bytes and expands it with ed25519.NewKeyFromSeed.
// Anything else returns ErrInvalidSigningSeed.
func ParseSigningSeed(s string) (SigningKey, error) {
	seed, ok := decodeStrictBase64(strings.Trim(s, " \t\n\v\f\r"), ed25519.SeedSize)
	if !ok {
		return SigningKey{}, ErrInvalidSigningSeed
	}
	return newSigningKey(seed), nil
}

// NewSigningKey generates a key from r (crypto/rand.Reader when r is nil).
func NewSigningKey(r io.Reader) (SigningKey, error) {
	seed := make([]byte, ed25519.SeedSize)
	if err := readRandom(r, seed); err != nil {
		return SigningKey{}, fmt.Errorf("tracecontext: generate signing key: %w", err)
	}
	return newSigningKey(seed), nil
}

func newSigningKey(seed []byte) SigningKey {
	priv := ed25519.NewKeyFromSeed(seed)
	return SigningKey{key: &signingKey{priv: &priv}}
}

// IsZero reports whether k holds no key.
func (k SigningKey) IsZero() bool {
	return k.key == nil
}

// Public returns the verification key of k, or nil for the zero value.
func (k SigningKey) Public() ed25519.PublicKey {
	if k.IsZero() {
		return nil
	}
	return k.key.priv.Public().(ed25519.PublicKey)
}

// KeyID returns KeyID(k.Public()).
func (k SigningKey) KeyID() [8]byte {
	return KeyID(k.Public())
}

// EncodeSeed returns the 32-byte seed as standard base64, for the secret
// write, or "" for the zero value. It is the only way the key material
// leaves the type.
func (k SigningKey) EncodeSeed() string {
	if k.IsZero() {
		return ""
	}
	return base64.StdEncoding.EncodeToString(k.key.priv.Seed())
}

// SignEnvelope signs e in place with the expanded key (Envelope.Sign). The
// zero value returns an error and leaves e unchanged.
func (k SigningKey) SignEnvelope(e *Envelope) error {
	if k.IsZero() {
		return errors.New("tracecontext: no signing key")
	}
	return e.Sign(*k.key.priv)
}

// String returns "[redacted]".
func (k SigningKey) String() string {
	return redacted
}

// GoString returns "[redacted]".
func (k SigningKey) GoString() string {
	return redacted
}

// Format writes "[redacted]" for every verb, ignoring width and flags.
func (k SigningKey) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, redacted)
}

// MarshalJSON returns the JSON string "[redacted]".
func (k SigningKey) MarshalJSON() ([]byte, error) {
	return []byte(`"` + redacted + `"`), nil
}

// MarshalText returns "[redacted]".
func (k SigningKey) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// LogValue returns "[redacted]" as a string value.
func (k SigningKey) LogValue() slog.Value {
	return slog.StringValue(redacted)
}

// KeySet builds the admission key map from the active and retained keys,
// keyed by KeyID. It rejects a key that is not 32 bytes
// (ErrInvalidVerificationKey) and duplicate key IDs (ErrDuplicateKeyID);
// a key that is both active and retained is a duplicate. The map holds
// copies, so later changes to the arguments cannot put a key under another
// key's ID.
func KeySet(active ed25519.PublicKey, retained []ed25519.PublicKey) (map[[8]byte]ed25519.PublicKey, error) {
	keys := make(map[[8]byte]ed25519.PublicKey, 1+len(retained))
	for _, pub := range append([]ed25519.PublicKey{active}, retained...) {
		if len(pub) != ed25519.PublicKeySize {
			return nil, ErrInvalidVerificationKey
		}
		id := KeyID(pub)
		if _, dup := keys[id]; dup {
			return nil, ErrDuplicateKeyID
		}
		keys[id] = bytes.Clone(pub)
	}
	return keys, nil
}

// decodeLowerHex fills dst from s when s is exactly 2*len(dst) lowercase
// hex characters. hex.Decode alone would also accept uppercase.
func decodeLowerHex(dst []byte, s string) bool {
	if len(s) != hex.EncodedLen(len(dst)) || strings.Trim(s, "0123456789abcdef") != "" {
		return false
	}
	_, err := hex.Decode(dst, []byte(s))
	return err == nil
}

// decodeStrictBase64 decodes strict standard base64 (padding and canonical
// trailing bits required) that decodes to exactly n bytes. Strict mode
// still skips CR and LF, so they are rejected first.
func decodeStrictBase64(s string, n int) ([]byte, bool) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != n {
		return nil, false
	}
	return b, true
}

// readRandom fills b from r, or from crypto/rand.Reader when r is nil.
func readRandom(r io.Reader, b []byte) error {
	if r == nil {
		r = rand.Reader
	}
	_, err := io.ReadFull(r, b)
	return err
}
