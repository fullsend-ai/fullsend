//go:build e2e

package admin

import (
	"context"
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

	// 2. Delete any fullsend-halfsend* GitHub Apps via Playwright.
	installations, err := client.ListOrgInstallations(ctx, testOrg)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list installations: %v", err)
	} else {
		for _, inst := range installations {
			if strings.HasPrefix(inst.AppSlug, "fullsend-"+testOrg) {
				t.Logf("[cleanup] Deleting stale app: %s", inst.AppSlug)
				if delErr := deleteAppViaPlaywright(page, inst.AppSlug, t.Logf, screenshotDir); delErr != nil {
					t.Logf("[cleanup] Warning: could not delete app %s: %v", inst.AppSlug, delErr)
				}
			}
		}
	}

	// 3. Ensure test-repo exists (needed for enrollment testing).
	_, err = client.GetRepo(ctx, testOrg, testRepo)
	if forge.IsNotFound(err) {
		t.Logf("[cleanup] Creating missing %s repo", testRepo)
		if _, createErr := client.CreateRepo(ctx, testOrg, testRepo, "E2E test repo", false); createErr != nil {
			t.Logf("[cleanup] Warning: could not create %s: %v", testRepo, createErr)
		}
	}

	// 4. Delete stale enrollment branch from test-repo.
	deleteEnrollmentBranch(ctx, token, testOrg, testRepo, t)

	// 5. Close any open enrollment PRs in test-repo (informational only).
	prs, err := client.ListRepoPullRequests(ctx, testOrg, testRepo)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list PRs: %v", err)
	} else {
		for _, pr := range prs {
			if strings.Contains(pr.Title, "fullsend") {
				t.Logf("[cleanup] Found stale enrollment PR #%d: %s", pr.Number, pr.Title)
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

// deleteEnrollmentBranch deletes the fullsend/onboard branch from a repo
// using the GitHub API directly (forge.Client doesn't have DeleteBranch).
func deleteEnrollmentBranch(ctx context.Context, token, org, repo string, t *testing.T) {
	t.Helper()
	branchRef := "heads/fullsend/onboard"
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
		t.Logf("[cleanup] Warning: could not delete enrollment branch: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		t.Log("[cleanup] Deleted stale enrollment branch fullsend/onboard")
	} else if resp.StatusCode == http.StatusNotFound {
		// Branch doesn't exist, nothing to do.
	} else {
		t.Logf("[cleanup] Warning: unexpected status deleting enrollment branch: %d", resp.StatusCode)
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
