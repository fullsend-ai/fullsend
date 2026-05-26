package fetch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendFetchAudit_SingleEntry(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")

	entry := FetchAuditEntry{
		TraceID:   "trace-001",
		FetchTime: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		URL:       "https://example.com/resource",
		SHA256:    "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		FetchType: "http_get",
		AllowedBy: "allowlist-rule-1",
		CacheHit:  false,
	}

	err := AppendFetchAudit(logPath, entry)
	require.NoError(t, err)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var got FetchAuditEntry
	err = json.Unmarshal([]byte(strings.TrimSpace(string(data))), &got)
	require.NoError(t, err)

	assert.Equal(t, "trace-001", got.TraceID)
	assert.Equal(t, time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC), got.FetchTime)
	assert.Equal(t, "https://example.com/resource", got.URL)
	assert.Equal(t, "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", got.SHA256)
	assert.Equal(t, "http_get", got.FetchType)
	assert.Equal(t, "allowlist-rule-1", got.AllowedBy)
	assert.False(t, got.CacheHit)
}

func TestAppendFetchAudit_MultipleEntries(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")

	for i := range 3 {
		entry := FetchAuditEntry{
			TraceID:   fmt.Sprintf("trace-%03d", i),
			FetchTime: time.Now().UTC(),
			URL:       fmt.Sprintf("https://example.com/resource/%d", i),
			SHA256:    "deadbeef",
			FetchType: "http_get",
			AllowedBy: "allowlist",
			CacheHit:  i > 0,
		}
		err := AppendFetchAudit(logPath, entry)
		require.NoError(t, err)
	}

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 3)

	for _, line := range lines {
		var got FetchAuditEntry
		err := json.Unmarshal([]byte(line), &got)
		assert.NoError(t, err)
	}
}

func TestAppendFetchAudit_CreatesParentDirs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "a", "b", "c", "audit.jsonl")

	entry := FetchAuditEntry{
		TraceID:   "trace-nested",
		FetchTime: time.Now().UTC(),
		URL:       "https://example.com/nested",
		SHA256:    "cafebabe",
		FetchType: "http_get",
		AllowedBy: "allowlist",
		CacheHit:  true,
	}

	err := AppendFetchAudit(logPath, entry)
	require.NoError(t, err)

	_, err = os.Stat(logPath)
	assert.NoError(t, err)
}

func TestAppendFetchAudit_JSONFields(t *testing.T) {
	entry := FetchAuditEntry{
		TraceID:   "trace-fields",
		FetchTime: time.Now().UTC(),
		URL:       "https://example.com/fields",
		SHA256:    "fieldhash",
		FetchType: "http_get",
		AllowedBy: "allowlist",
		CacheHit:  false,
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)

	expectedKeys := []string{"trace_id", "fetch_time", "url", "sha256", "fetch_type", "allowed_by", "cache_hit"}
	for _, key := range expectedKeys {
		assert.Contains(t, m, key)
	}
	assert.Len(t, m, len(expectedKeys))
}
