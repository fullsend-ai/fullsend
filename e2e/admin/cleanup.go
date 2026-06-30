//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// cleanupStaleResources removes leftover resources from previous test runs.
// This is the "teardown-first" part of the dual cleanup strategy.
func cleanupStaleResources(ctx context.Context, client forge.Client, token, org string, t *testing.T) {
	t.Helper()
	t.Log("[cleanup] Scanning for stale resources from previous runs...")

	// 1. Close open PRs on .fullsend, then delete the config repo and any
	// numbered forks (.fullsend-1, …) left from prior PR-based install runs.
	if _, err := client.GetRepo(ctx, org, forge.ConfigRepoName); err == nil {
		prs, listErr := client.ListRepoPullRequests(ctx, org, forge.ConfigRepoName)
		if listErr != nil {
			t.Logf("[cleanup] Warning: could not list PRs on %s: %v", forge.ConfigRepoName, listErr)
		} else {
			for _, pr := range prs {
				t.Logf("[cleanup] Closing stale PR #%d on %s: %s", pr.Number, forge.ConfigRepoName, pr.Title)
				closePR(ctx, token, org, forge.ConfigRepoName, pr.Number, t)
			}
		}
	}
	for _, name := range listOrgRepoNames(ctx, token, org, t) {
		if strings.HasPrefix(name, ".fullsend") {
			t.Logf("[cleanup] Deleting stale repo %s", name)
			if delErr := client.DeleteRepo(ctx, org, name); delErr != nil {
				t.Logf("[cleanup] Warning: could not delete %s: %v", name, delErr)
			}
		}
	}

	// 2. Delete stale FULLSEND_DISPATCH_TOKEN org secret if it exists (legacy PAT mode artifact).
	dispatchExists, dispatchErr := client.OrgSecretExists(ctx, org, "FULLSEND_DISPATCH_TOKEN")
	if dispatchErr != nil {
		t.Logf("[cleanup] Warning: could not check dispatch token org secret: %v", dispatchErr)
	} else if dispatchExists {
		t.Log("[cleanup] Deleting stale FULLSEND_DISPATCH_TOKEN org secret")
		if delErr := client.DeleteOrgSecret(ctx, org, "FULLSEND_DISPATCH_TOKEN"); delErr != nil {
			t.Logf("[cleanup] Warning: could not delete dispatch token org secret: %v", delErr)
		}
	}

	// 3. Ensure test-repo exists and has at least one commit (needed for
	// enrollment testing). An empty repo (no commits) causes the
	// reconcile-repos script to fail with "Could not get default branch tree".
	_, err := client.GetRepo(ctx, org, testRepo)
	if forge.IsNotFound(err) {
		t.Logf("[cleanup] Creating missing %s repo", testRepo)
		if _, createErr := client.CreateRepo(ctx, org, testRepo, "E2E test repo", false); createErr != nil {
			t.Logf("[cleanup] Warning: could not create %s: %v", testRepo, createErr)
		}
	}
	if _, getErr := client.GetFileContent(ctx, org, testRepo, "README.md"); forge.IsNotFound(getErr) {
		t.Logf("[cleanup] Seeding %s with initial commit (repo is empty)", testRepo)
		if seedErr := client.CreateFile(ctx, org, testRepo, "README.md", "chore: initialize repo for e2e testing", []byte("# test-repo\n\nE2E test repository.\n")); seedErr != nil {
			t.Logf("[cleanup] Warning: could not seed %s: %v", testRepo, seedErr)
		}
	} else if getErr != nil {
		t.Logf("[cleanup] Warning: could not check README in %s: %v", testRepo, getErr)
	}

	// 4. Delete stale enrollment and unenrollment branches from test-repo.
	deleteBranch(ctx, token, org, testRepo, "fullsend/onboard", t)
	deleteBranch(ctx, token, org, testRepo, "fullsend/offboard", t)

	// 5. Delete shim workflow from test-repo's default branch (left behind
	// when a previous run merged the enrollment PR in Phase 2.5).
	deleteShimWorkflow(ctx, token, org, testRepo, t)

	// 6. Close any open fullsend-related PRs in test-repo.
	prs, err := client.ListRepoPullRequests(ctx, org, testRepo)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list PRs: %v", err)
	} else {
		for _, pr := range prs {
			if strings.Contains(pr.Title, "fullsend") {
				t.Logf("[cleanup] Closing stale PR #%d: %s", pr.Number, pr.Title)
				closePR(ctx, token, org, testRepo, pr.Number, t)
			}
		}
	}

	t.Log("[cleanup] Stale resource scan complete")
}

// listOrgRepoNames returns repository names in an org via the GitHub REST API.
func listOrgRepoNames(ctx context.Context, token, org string, t *testing.T) []string {
	t.Helper()
	var names []string
	page := 1
	for {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=100&page=%d&type=all", org, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			t.Logf("[cleanup] Warning: could not list org repos: %v", err)
			return names
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("[cleanup] Warning: could not list org repos: %v", err)
			return names
		}

		var batch []struct {
			Name string `json:"name"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&batch)
		resp.Body.Close()
		if decodeErr != nil {
			t.Logf("[cleanup] Warning: could not decode org repo list: %v", decodeErr)
			return names
		}
		if resp.StatusCode != http.StatusOK {
			t.Logf("[cleanup] Warning: list org repos returned status %d", resp.StatusCode)
			return names
		}
		if len(batch) == 0 {
			break
		}
		for _, repo := range batch {
			names = append(names, repo.Name)
		}
		if len(batch) < 100 {
			break
		}
		page++
	}
	return names
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
			t.Errorf("[cleanup] could not delete %s/%s: %v", org, repo, delErr)
		}
	})
}
