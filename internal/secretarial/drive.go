package secretarial

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// DriveClient provides read-only access to Google Drive.
// The service account must be invited to the meeting (or the notes folder)
// so that Gemini-generated notes are automatically shared with it.
// The Drive API readonly scope prevents any writes.
type DriveClient struct {
	svc *drive.Service
}

// NewDriveClient creates a client from a service-account JSON key blob.
func NewDriveClient(ctx context.Context, credentialsJSON []byte) (*DriveClient, error) {
	scope := drive.DriveReadonlyScope

	creds, err := google.CredentialsFromJSON(ctx, credentialsJSON, scope)
	if err != nil {
		return nil, fmt.Errorf("parsing service account credentials: %w", err)
	}

	svc, err := drive.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("creating drive service: %w", err)
	}
	return &DriveClient{svc: svc}, nil
}

// DocMeta is lightweight metadata for a Google Doc.
type DocMeta struct {
	ID           string
	Name         string
	CreatedTime  time.Time
	ModifiedTime time.Time
	WebViewLink  string
}

// SearchRecentDocs finds Google Docs whose name contains nameQuery, created
// after cutoff. Uses createdTime so that viewing or editing old docs doesn't
// cause reprocessing.
func (c *DriveClient) SearchRecentDocs(ctx context.Context, nameQuery string, cutoff time.Time) ([]DocMeta, error) {
	q := fmt.Sprintf(
		"name contains '%s' and mimeType = 'application/vnd.google-apps.document' and trashed = false and createdTime > '%s'",
		escapeQueryString(nameQuery),
		cutoff.UTC().Format(time.RFC3339),
	)
	return c.query(ctx, q)
}

func (c *DriveClient) query(ctx context.Context, q string) ([]DocMeta, error) {
	resp, err := c.svc.Files.List().
		Context(ctx).
		Q(q).
		Fields("files(id, name, createdTime, modifiedTime, webViewLink)").
		OrderBy("createdTime desc").
		PageSize(20).
		IncludeItemsFromAllDrives(true).
		SupportsAllDrives(true).
		Do()
	if err != nil {
		return nil, fmt.Errorf("listing drive files: %w", err)
	}

	docs := make([]DocMeta, 0, len(resp.Files))
	for _, f := range resp.Files {
		ct, _ := time.Parse(time.RFC3339, f.CreatedTime)
		mt, _ := time.Parse(time.RFC3339, f.ModifiedTime)
		docs = append(docs, DocMeta{
			ID:           f.Id,
			Name:         f.Name,
			CreatedTime:  ct,
			ModifiedTime: mt,
			WebViewLink:  f.WebViewLink,
		})
	}
	return docs, nil
}

// DownloadDocText exports a Google Doc as plain text.
// Retries up to 3 times on transient server errors (5xx).
func (c *DriveClient) DownloadDocText(ctx context.Context, docID string) (string, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := range maxAttempts {
		resp, err := c.svc.Files.Export(docID, "text/plain").Context(ctx).Download()
		if err != nil {
			lastErr = fmt.Errorf("exporting doc: %w", err)
			if attempt < maxAttempts-1 {
				wait := time.Duration(1<<uint(attempt)) * time.Second
				slog.Warn("Drive export failed, retrying", "attempt", attempt+1, "wait", wait, "err", err)
				time.Sleep(wait)
				continue
			}
			return "", lastErr
		}
		buf, err := readAllLimited(resp.Body, 2<<20) // 2 MiB cap
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("reading doc body: %w", err)
		}
		return string(buf), nil
	}
	return "", lastErr
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	var total int64
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return nil, fmt.Errorf("document exceeds %d byte limit", limit)
			}
			buf = append(buf, tmp[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return buf, nil
}

func escapeQueryString(s string) string {
	result := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] == '\'' {
			result = append(result, '\\', '\'')
		} else {
			result = append(result, s[i])
		}
	}
	return string(result)
}

// UnmarshalDocMeta is a helper for testing — parses DocMeta from JSON.
func UnmarshalDocMeta(data []byte) ([]DocMeta, error) {
	var docs []DocMeta
	if err := json.Unmarshal(data, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}
