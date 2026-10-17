package sandbox

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReservedCredentialKeys pins the accessor that internal/cli merges into
// its own sandbox env blocklist (#3373). It must expose the complete reserved
// set — a silently narrowed list would reopen the drift this exists to close —
// in a stable order, and hand back a copy callers cannot use to mutate package
// state.
func TestReservedCredentialKeys(t *testing.T) {
	t.Parallel()

	keys := ReservedCredentialKeys()
	require.Len(t, keys, len(reservedCredentialKeys),
		"accessor must expose every reserved credential key")

	returned := make(map[string]bool, len(keys))
	for _, k := range keys {
		require.True(t, reservedCredentialKeys[k],
			"accessor returned %s, which is not a reserved credential key", k)
		returned[k] = true
	}
	for k := range reservedCredentialKeys {
		assert.True(t, returned[k], "accessor omitted reserved credential key %s", k)
	}

	assert.True(t, sort.StringsAreSorted(keys),
		"accessor must return a deterministic order; its contract is exported")

	// The slice is the caller's to corrupt — and a later caller must still get
	// a clean list, which is what fails if the accessor ever memoizes and
	// hands out one shared slice.
	want := append([]string(nil), keys...)
	require.NotEmpty(t, keys, "no reserved credential keys to probe; the blocklist would be empty")
	first := keys[0]
	keys[0] = "FULLSEND_ACCESSOR_MUTATION_PROBE"

	assert.False(t, reservedCredentialKeys["FULLSEND_ACCESSOR_MUTATION_PROBE"],
		"mutating the returned slice must not add to the blocklist")
	assert.True(t, reservedCredentialKeys[first],
		"mutating the returned slice must not remove from the blocklist")
	assert.Equal(t, want, ReservedCredentialKeys(),
		"a later caller must receive an untouched list")
}

// reservedCredentialKeysUnpinnedFloor holds the entries of
// reservedCredentialKeys that no other test pins against that map.
// TestReservedCredentialKeys proves the accessor mirrors the map but cannot
// notice the map shrinking, because then both sides shrink together. SHELL,
// ENV and LD_LIBRARY_PATH are listed here even though the cli blocklist names
// them too, because that pin is satisfied by cli's own map and would stay
// green if the credential map alone dropped the name. Dropping any entry here
// has to be a deliberate edit in two files, not a silent policy change
// (#3373).
var reservedCredentialKeysUnpinnedFloor = []string{
	"ALL_PROXY", "IFS", "CDPATH", "PYTHONPATH", "JAVA_TOOL_OPTIONS", "RUBYOPT",
	"PERL5OPT", "DYLD_INSERT_LIBRARIES", "GIT_TEMPLATE_DIR", "GIT_ASKPASS",
	"SHELL", "ENV", "LD_LIBRARY_PATH",
}

// TestReservedCredentialKeys_RetainUnpinnedNames keeps the credential
// deny-list from losing an entry that nothing else would notice.
func TestReservedCredentialKeys_RetainUnpinnedNames(t *testing.T) {
	t.Parallel()

	keys := ReservedCredentialKeys()
	for _, want := range reservedCredentialKeysUnpinnedFloor {
		assert.Contains(t, keys, want,
			"reservedCredentialKeys must retain %s; no other test pins it against this map", want)
	}
}
