//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// cleanupStaleResources removes leftover resources from previous test runs.
// This is the "teardown-first" part of the dual cleanup strategy.
func cleanupStaleResources(ctx context.Context, client forge.Client, page playwright.Page, t *testing.T) {
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
				if delErr := deleteAppViaPlaywright(page, inst.AppSlug); delErr != nil {
					t.Logf("[cleanup] Warning: could not delete app %s: %v", inst.AppSlug, delErr)
				}
			}
		}
	}

	// 3. Close any open enrollment PRs in test-repo.
	prs, err := client.ListRepoPullRequests(ctx, testOrg, testRepo)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list PRs: %v", err)
	} else {
		for _, pr := range prs {
			if strings.Contains(pr.Title, "fullsend") {
				t.Logf("[cleanup] Found stale enrollment PR #%d: %s", pr.Number, pr.Title)
				// Note: forge.Client doesn't have ClosePR; log for manual cleanup.
				t.Logf("[cleanup] Manual cleanup needed: close PR %s", pr.URL)
			}
		}
	}

	t.Log("[cleanup] Stale resource scan complete")
}

// registerAppCleanup registers a t.Cleanup that deletes the given app slug.
func registerAppCleanup(t *testing.T, page playwright.Page, slug string) {
	t.Helper()
	t.Cleanup(func() {
		fmt.Printf("[cleanup] Deleting app %s via Playwright\n", slug)
		if err := deleteAppViaPlaywright(page, slug); err != nil {
			t.Logf("[cleanup] Warning: could not delete app %s: %v", slug, err)
		}
	})
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
		fmt.Printf("[cleanup] Deleting repo %s/%s\n", org, repo)
		if delErr := client.DeleteRepo(ctx, org, repo); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete %s/%s: %v", org, repo, delErr)
		}
	})
}
