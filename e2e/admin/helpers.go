//go:build e2e

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// defaultRoles is the standard set of agent roles for admin install e2e.
var defaultRoles = []string{"fullsend", "triage", "coder", "review", "retro", "prioritize"}

// e2eAppSet is the app set prefix used by the shared public GitHub Apps.
const e2eAppSet = "fullsend-ai"

func ensureRepoLabel(ctx context.Context, token, owner, repo, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/labels", owner, repo)
	payload, err := json.Marshal(map[string]string{
		"name":  label,
		"color": "5319e7",
	})
	if err != nil {
		return fmt.Errorf("encoding label payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating label request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating repo label: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusUnprocessableEntity {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d creating label %q: %s", resp.StatusCode, label, body)
}

func addIssueLabel(ctx context.Context, token, owner, repo string, issueNum int, label string) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/issues/%d/labels", owner, repo, issueNum)
	payload, err := json.Marshal(map[string][]string{"labels": {label}})
	if err != nil {
		return fmt.Errorf("encoding issue label payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating issue label request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("adding issue label: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d adding label %q: %s", resp.StatusCode, label, body)
}

func registerRepoCleanup(t *testing.T, client forge.Client, org, repo string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := client.GetRepo(ctx, org, repo)
		if err != nil {
			return
		}
		t.Logf("[cleanup] Deleting repo %s/%s", org, repo)
		if delErr := client.DeleteRepo(ctx, org, repo); delErr != nil {
			t.Errorf("[cleanup] could not delete %s/%s: %v", org, repo, delErr)
		}
	})
}
