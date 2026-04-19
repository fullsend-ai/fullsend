//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// cleanupStaleResources removes leftover resources from previous test runs.
// This is the "teardown-first" part of the dual cleanup strategy.
func cleanupStaleResources(ctx context.Context, client forge.Client, page playwright.Page, token, screenshotDir string, t *testing.T) {
	t.Helper()
	t.Log("[cleanup] Scanning for stale resources from previous runs...")

	// 1. Delete .fullsend repo if it exists.
	_, err := client.GetRepo(ctx, testOrg, forge.ConfigRepoName)
	if err == nil {
		t.Logf("[cleanup] Deleting stale %s repo", forge.ConfigRepoName)
		if delErr := client.DeleteRepo(ctx, testOrg, forge.ConfigRepoName); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete %s: %v", forge.ConfigRepoName, delErr)
		}
	}

	// 2. Delete stale FULLSEND_DISPATCH_TOKEN org secret if it exists.
	dispatchExists, dispatchErr := client.OrgSecretExists(ctx, testOrg, "FULLSEND_DISPATCH_TOKEN")
	if dispatchErr != nil {
		t.Logf("[cleanup] Warning: could not check dispatch token org secret: %v", dispatchErr)
	} else if dispatchExists {
		t.Log("[cleanup] Deleting stale FULLSEND_DISPATCH_TOKEN org secret")
		if delErr := client.DeleteOrgSecret(ctx, testOrg, "FULLSEND_DISPATCH_TOKEN"); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete dispatch token org secret: %v", delErr)
		}
	}

	// 3. Delete any stale fullsend GitHub Apps via Playwright.
	// First, try deleting by expected slug for each role (catches apps that
	// were created but never installed, which don't appear in ListOrgInstallations).
	for _, role := range defaultRoles {
		slug := testOrg + "-" + role // v6 convention: halfsend-fullsend, etc.
		t.Logf("[cleanup] Attempting to delete app %s (if it exists)", slug)
		if delErr := deleteAppViaPlaywright(page, slug, t.Logf, screenshotDir); delErr != nil {
			t.Logf("[cleanup] App %s not found or could not delete: %v", slug, delErr)
		}
	}

	// Also clean up apps found via installations (catches old naming conventions).
	installations, err := client.ListOrgInstallations(ctx, testOrg)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list installations: %v", err)
	} else {
		for _, inst := range installations {
			isStale := strings.HasPrefix(inst.AppSlug, "fullsend-"+testOrg) || // old: fullsend-halfsend-*
				strings.HasPrefix(inst.AppSlug, testOrg+"-") // v6: halfsend-*
			if isStale {
				t.Logf("[cleanup] Deleting stale installed app: %s", inst.AppSlug)
				if delErr := deleteAppViaPlaywright(page, inst.AppSlug, t.Logf, screenshotDir); delErr != nil {
					t.Logf("[cleanup] Warning: could not delete app %s: %v", inst.AppSlug, delErr)
				}
			}
		}
	}

	// 4. Delete any stale dispatch PATs from previous runs.
	// Use bulk deletion to clean up all accumulated dispatch PATs — if too
	// many accumulate, GitHub enforces a 50-token limit and blocks creation.
	t.Log("[cleanup] Cleaning up stale dispatch PATs...")
	deleteAllDispatchPATs(page, testOrg, screenshotDir, t.Logf)

	// 5. Ensure test-repo exists (needed for enrollment testing).
	_, err = client.GetRepo(ctx, testOrg, testRepo)
	if forge.IsNotFound(err) {
		t.Logf("[cleanup] Creating missing %s repo", testRepo)
		if _, createErr := client.CreateRepo(ctx, testOrg, testRepo, "E2E test repo", false); createErr != nil {
			t.Logf("[cleanup] Warning: could not create %s: %v", testRepo, createErr)
		}
	}

	// 6. Delete stale enrollment and unenrollment branches from test-repo.
	deleteBranch(ctx, token, testOrg, testRepo, "fullsend/onboard", t)
	deleteBranch(ctx, token, testOrg, testRepo, "fullsend/offboard", t)

	// 7. Delete shim workflow from test-repo's default branch (left behind
	// when a previous run merged the enrollment PR in Phase 2.5).
	deleteShimWorkflow(ctx, token, testOrg, testRepo, t)

	// 8. Close any open fullsend-related PRs in test-repo.
	prs, err := client.ListRepoPullRequests(ctx, testOrg, testRepo)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list PRs: %v", err)
	} else {
		for _, pr := range prs {
			if strings.Contains(pr.Title, "fullsend") {
				t.Logf("[cleanup] Closing stale PR #%d: %s", pr.Number, pr.Title)
				closePR(ctx, token, testOrg, testRepo, pr.Number, t)
			}
		}
	}

	t.Log("[cleanup] Stale resource scan complete")
}

// registerAppCleanup registers a t.Cleanup that deletes the given app slug.
func registerAppCleanup(t *testing.T, page playwright.Page, slug, screenshotDir string) {
	t.Helper()
	t.Cleanup(func() {
		t.Logf("[cleanup] Deleting app %s via Playwright", slug)
		if err := deleteAppViaPlaywright(page, slug, t.Logf, screenshotDir); err != nil {
			t.Logf("[cleanup] Warning: could not delete app %s: %v", slug, err)
		}
	})
}

// deleteBranch deletes a branch from a repo using the GitHub API directly
// (forge.Client doesn't have DeleteBranch).
func deleteBranch(ctx context.Context, token, org, repo, branch string, t *testing.T) {
	t.Helper()
	branchRef := "heads/" + branch
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/refs/%s", org, repo, branchRef)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		t.Logf("[cleanup] Warning: could not create branch delete request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("[cleanup] Warning: could not delete branch %s: %v", branch, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		t.Logf("[cleanup] Deleted stale branch %s", branch)
	} else if resp.StatusCode == http.StatusNotFound {
		// Branch doesn't exist, nothing to do.
	} else {
		t.Logf("[cleanup] Warning: unexpected status deleting branch %s: %d", branch, resp.StatusCode)
	}
}

// deleteShimWorkflow removes the fullsend shim workflow from a repo's default
// branch. This cleans up after Phase 2.5 which merges the enrollment PR.
func deleteShimWorkflow(ctx context.Context, token, org, repo string, t *testing.T) {
	t.Helper()
	shimPath := ".github/workflows/fullsend.yaml"

	// Get the file's SHA (required for deletion).
	getURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", org, repo, shimPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		t.Logf("[cleanup] Warning: could not create request to check shim file: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("[cleanup] Warning: could not check shim file: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return // File doesn't exist, nothing to do.
	}
	if resp.StatusCode != http.StatusOK {
		t.Logf("[cleanup] Warning: unexpected status checking shim file: %d", resp.StatusCode)
		return
	}

	var fileInfo struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileInfo); err != nil {
		t.Logf("[cleanup] Warning: could not decode shim file info: %v", err)
		return
	}

	// Delete the file.
	deleteBody := struct {
		Message string `json:"message"`
		SHA     string `json:"sha"`
	}{
		Message: "chore: cleanup stale shim workflow",
		SHA:     fileInfo.SHA,
	}
	deletePayload, err := json.Marshal(deleteBody)
	if err != nil {
		t.Logf("[cleanup] Warning: could not marshal delete payload: %v", err)
		return
	}
	delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, getURL, strings.NewReader(string(deletePayload)))
	if err != nil {
		t.Logf("[cleanup] Warning: could not create delete request for shim: %v", err)
		return
	}
	delReq.Header.Set("Authorization", "Bearer "+token)
	delReq.Header.Set("Accept", "application/vnd.github+json")
	delReq.Header.Set("Content-Type", "application/json")

	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Logf("[cleanup] Warning: could not delete shim file: %v", err)
		return
	}
	defer delResp.Body.Close()

	if delResp.StatusCode == http.StatusOK || delResp.StatusCode == http.StatusNoContent {
		t.Logf("[cleanup] Deleted stale shim workflow from %s", repo)
	} else {
		t.Logf("[cleanup] Warning: unexpected status deleting shim file: %d", delResp.StatusCode)
	}
}

// closePR closes a pull request using the GitHub API directly.
func closePR(ctx context.Context, token, org, repo string, number int, t *testing.T) {
	t.Helper()
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", org, repo, number)
	body := strings.NewReader(`{"state":"closed"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, body)
	if err != nil {
		t.Logf("[cleanup] Warning: could not create PR close request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("[cleanup] Warning: could not close PR #%d: %v", number, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Logf("[cleanup] Closed stale PR #%d", number)
	} else {
		t.Logf("[cleanup] Warning: unexpected status closing PR #%d: %d", number, resp.StatusCode)
	}
}

// registerRepoCleanup registers a t.Cleanup that deletes a repo.
func registerRepoCleanup(t *testing.T, client forge.Client, org, repo string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := client.GetRepo(ctx, org, repo)
		if err != nil {
			return // Already gone.
		}
		t.Logf("[cleanup] Deleting repo %s/%s", org, repo)
		if delErr := client.DeleteRepo(ctx, org, repo); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete %s/%s: %v", org, repo, delErr)
		}
	})
}
