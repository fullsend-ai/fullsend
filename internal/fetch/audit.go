package fetch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FetchAuditEntry is a JSONL audit record for a remote resource fetch.
type FetchAuditEntry struct {
	TraceID   string    `json:"trace_id"`
	FetchTime time.Time `json:"fetch_time"`
	URL       string    `json:"url"`
	SHA256    string    `json:"sha256"`
	FetchType string    `json:"fetch_type"`
	AllowedBy string    `json:"allowed_by"`
	CacheHit  bool      `json:"cache_hit"`
}

// AppendFetchAudit writes a fetch audit entry as a JSON line to the given log path.
func AppendFetchAudit(logPath string, entry FetchAuditEntry) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("creating audit log directory: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling audit entry: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", data); err != nil {
		return fmt.Errorf("writing audit entry: %w", err)
	}
	return nil
}
