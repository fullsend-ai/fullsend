// runid.go defines the single per-run identity shared by the two
// behaviour-test subsystems that ADRs 0114 and 0115 introduce.
//
// A run generates one RunID at startup and uses it for everything:
//   - it prefixes every ephemeral repo the run creates, bt-{run-id}-{uuid8}
//     (ADR 0114), so all of a run's repos share a prefix;
//   - it is the owner token written into the lock repo's description
//     (ADR 0115), so a held lock maps back to exactly its run's repos via a
//     bt-{run-id}-* lookup, and release/stale-reclaim can verify ownership;
//   - it orders runs for oldest-first pruning (ADR 0114).
//
// The RunID is derived from a UUIDv7: unique (two runs never collide, even
// started in the same instant) yet time-ordered, so its string form sorts
// chronologically. The hyphens of the canonical UUID form are stripped so
// the id is a fixed-width 32-char token that embeds cleanly in a repo name
// (whose own segments are hyphen-separated) and is still lexicographically
// time-sortable, since dropping the fixed-position hyphens preserves order.
package install

import (
	"fmt"

	"github.com/google/uuid"
)

// RunIDLen is the fixed character length of a RunID: a UUIDv7 rendered as
// 32 lowercase hex digits with its hyphens removed.
const RunIDLen = 32

// RunID is the time-ordered identity of one behaviour-test run. See the
// package-level note in runid.go for how it is used.
type RunID string

// NewRunID returns a fresh time-ordered run id derived from a UUIDv7.
func NewRunID() (RunID, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generating run id: %w", err)
	}
	// uuid.UUID.String() is 8-4-4-4-12 hex with hyphens; strip them for a
	// fixed-width, hyphen-free token. The hyphens sit at fixed positions,
	// so removing them keeps the value time-sortable.
	hex := make([]byte, 0, RunIDLen)
	for i := 0; i < len(u); i++ {
		const digits = "0123456789abcdef"
		hex = append(hex, digits[u[i]>>4], digits[u[i]&0x0f])
	}
	return RunID(hex), nil
}

// String returns the run id as a string.
func (r RunID) String() string { return string(r) }
