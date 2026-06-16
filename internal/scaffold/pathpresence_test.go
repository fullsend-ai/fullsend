package scaffold

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestComparePathPresence_AllPresent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"org/.fullsend/.defaults/action.yml":                  []byte("marker"),
			"org/.fullsend/.github/workflows/reusable-triage.yml": []byte("wf"),
			"org/.fullsend/bin/fullsend":                          []byte("binary"),
		},
	}

	missing, err := ComparePathPresence(context.Background(), client, "org", ".fullsend", []string{
		".defaults/action.yml",
		".github/workflows/reusable-triage.yml",
		"bin/fullsend",
	})
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestComparePathPresence_SomeMissing(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"org/.fullsend/.defaults/action.yml": []byte("marker"),
			"org/.fullsend/bin/fullsend":         []byte("binary"),
		},
	}

	missing, err := ComparePathPresence(context.Background(), client, "org", ".fullsend", []string{
		".defaults/action.yml",
		".github/workflows/reusable-triage.yml",
		".github/workflows/reusable-code.yml",
		"bin/fullsend",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		".github/workflows/reusable-code.yml",
		".github/workflows/reusable-triage.yml",
	}, missing)
}

func TestComparePathPresence_AllMissing(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}

	missing, err := ComparePathPresence(context.Background(), client, "org", ".fullsend", []string{
		".defaults/action.yml",
		"bin/fullsend",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{".defaults/action.yml", "bin/fullsend"}, missing)
}

func TestComparePathPresence_EmptyExpected(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"org/.fullsend/bin/fullsend": []byte("binary"),
		},
	}

	missing, err := ComparePathPresence(context.Background(), client, "org", ".fullsend", nil)
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestComparePathPresence_ForgeError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"ListRepositoryFiles": errors.New("network error"),
		},
	}

	_, err := ComparePathPresence(context.Background(), client, "org", ".fullsend", []string{
		".defaults/action.yml",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing repository files")
}

func TestComparePathPresence_UsesOneAPICall(t *testing.T) {
	// Verify that ComparePathPresence uses ListRepositoryFiles (batch)
	// rather than per-path GetFileContent. We inject an error on
	// GetFileContent to ensure it is never called.
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"org/repo/path-a": []byte("a"),
			"org/repo/path-b": []byte("b"),
		},
		Errors: map[string]error{
			"GetFileContent": errors.New("should not be called"),
		},
	}

	missing, err := ComparePathPresence(context.Background(), client, "org", "repo", []string{
		"path-a",
		"path-b",
		"path-c",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"path-c"}, missing)
}
