package scaffold

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHarnessBaseURL(t *testing.T) {
	sha := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	t.Run("valid inputs", func(t *testing.T) {
		url, err := HarnessBaseURL("triage", sha)
		require.NoError(t, err)
		assert.Equal(t,
			"https://raw.githubusercontent.com/fullsend-ai/fullsend/"+sha+"/internal/scaffold/fullsend-repo/harness/triage.yaml",
			url)
	})

	t.Run("hyphenated name", func(t *testing.T) {
		url, err := HarnessBaseURL("my-agent", sha)
		require.NoError(t, err)
		assert.Contains(t, url, "/my-agent.yaml")
	})

	t.Run("invalid harness name uppercase", func(t *testing.T) {
		_, err := HarnessBaseURL("Triage", sha)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid harness name")
	})

	t.Run("invalid harness name empty", func(t *testing.T) {
		_, err := HarnessBaseURL("", sha)
		assert.Error(t, err)
	})

	t.Run("invalid harness name starts with digit", func(t *testing.T) {
		_, err := HarnessBaseURL("1agent", sha)
		assert.Error(t, err)
	})

	t.Run("invalid harness name special chars", func(t *testing.T) {
		_, err := HarnessBaseURL("agent.name", sha)
		assert.Error(t, err)
	})

	t.Run("invalid commit SHA too short", func(t *testing.T) {
		_, err := HarnessBaseURL("triage", "abc123")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid commit SHA")
	})

	t.Run("invalid commit SHA uppercase", func(t *testing.T) {
		_, err := HarnessBaseURL("triage", "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2")
		assert.Error(t, err)
	})

	t.Run("invalid commit SHA wrong length", func(t *testing.T) {
		_, err := HarnessBaseURL("triage", strings.Repeat("a", 39))
		assert.Error(t, err)
	})
}

func TestHarnessContentHash(t *testing.T) {
	t.Run("known harness returns 64-char hex", func(t *testing.T) {
		hash, err := HarnessContentHash("triage")
		require.NoError(t, err)
		assert.Len(t, hash, 64)
		assert.Regexp(t, `^[0-9a-f]{64}$`, hash)
	})

	t.Run("unknown harness errors", func(t *testing.T) {
		_, err := HarnessContentHash("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown harness")
	})

	t.Run("invalid harness name errors", func(t *testing.T) {
		_, err := HarnessContentHash("INVALID")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid harness name")
	})

	t.Run("hash matches manual computation", func(t *testing.T) {
		data, err := content.ReadFile("fullsend-repo/harness/triage.yaml")
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		expected := hex.EncodeToString(sum[:])

		hash, err := HarnessContentHash("triage")
		require.NoError(t, err)
		assert.Equal(t, expected, hash)
	})

	t.Run("each harness has unique hash", func(t *testing.T) {
		names, err := HarnessNames()
		require.NoError(t, err)
		hashes := make(map[string]string)
		for _, name := range names {
			hash, err := HarnessContentHash(name)
			require.NoError(t, err, "hashing %s", name)
			if prev, dup := hashes[hash]; dup {
				t.Errorf("harness %q has same hash as %q", name, prev)
			}
			hashes[hash] = name
		}
	})
}

func TestHarnessBaseURLWithHash(t *testing.T) {
	sha := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	t.Run("produces URL with hash fragment", func(t *testing.T) {
		url, err := HarnessBaseURLWithHash("triage", sha)
		require.NoError(t, err)
		assert.Contains(t, url, "#sha256=")

		parts := strings.SplitN(url, "#sha256=", 2)
		require.Len(t, parts, 2)
		assert.Len(t, parts[1], 64, "hash fragment should be 64 hex chars")
		assert.Regexp(t, `^[0-9a-f]{64}$`, parts[1])
	})

	t.Run("hash fragment matches content hash", func(t *testing.T) {
		url, err := HarnessBaseURLWithHash("code", sha)
		require.NoError(t, err)

		hash, err := HarnessContentHash("code")
		require.NoError(t, err)

		assert.True(t, strings.HasSuffix(url, "#sha256="+hash))
	})

	t.Run("invalid harness name errors", func(t *testing.T) {
		_, err := HarnessBaseURLWithHash("INVALID", sha)
		assert.Error(t, err)
	})

	t.Run("unknown harness errors", func(t *testing.T) {
		_, err := HarnessBaseURLWithHash("nonexistent", sha)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown harness")
	})

	t.Run("invalid commit SHA errors", func(t *testing.T) {
		_, err := HarnessBaseURLWithHash("triage", "bad")
		assert.Error(t, err)
	})
}

func TestHarnessNames(t *testing.T) {
	names, err := HarnessNames()
	require.NoError(t, err)

	t.Run("returns expected harnesses", func(t *testing.T) {
		expected := []string{"code", "fix", "prioritize", "retro", "review", "triage"}
		assert.Equal(t, expected, names)
	})

	t.Run("is sorted", func(t *testing.T) {
		for i := 1; i < len(names); i++ {
			assert.True(t, names[i-1] < names[i],
				"names should be sorted: %q >= %q", names[i-1], names[i])
		}
	})
}

func TestHarnessBaseURLWithHashAllHarnesses(t *testing.T) {
	sha := "abcdef0123456789abcdef0123456789abcdef01"

	names, err := HarnessNames()
	require.NoError(t, err)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			url, err := HarnessBaseURLWithHash(name, sha)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(url, "https://"))
			assert.Contains(t, url, "/"+name+".yaml#sha256=")
		})
	}
}
