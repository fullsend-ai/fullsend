package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// Project deploy keys and protected-branch deploy-key push grants
// ---------------------------------------------------------------------------
//
// These are installer transport for the GitLab trigger-account design
// (#8430). GitLab.com Free refuses per-user protected-branch merge grants
// (HTTP 422, "Merge access levels user must be blank"), but it accepts a
// deploy key in a protected branch's allowed_to_push list. GitLab treats
// the user who added the key as allowed to start pipelines on that branch.
// Like the service-account and personal-access-token methods, these are
// concrete LiveClient methods. They encode no ownership policy, so the caller
// must verify that fullsend owns a key or grant before changing it.

// DeployKey is an SSH deploy key enabled on a GitLab project.
type DeployKey struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	// FingerprintSHA256 identifies the public key without carrying it.
	FingerprintSHA256 string `json:"fingerprint_sha256"`
	CanPush           bool   `json:"can_push"`
	// ExpiresAt is an RFC 3339 timestamp, empty when the key does not expire.
	ExpiresAt string `json:"expires_at"`
}

const (
	deployKeyPerPage  = 100
	deployKeyMaxPages = 100
)

// CreateProjectDeployKey adds a deploy key to the project with the calling
// credential's authority. GitLab records the authenticated user as the key's
// owner, and a protected-branch grant for the key admits that user. So the
// caller chooses whose authority backs the grant by choosing the credential.
// Only the public key is sent; the private key never reaches GitLab.
func (c *LiveClient) CreateProjectDeployKey(ctx context.Context, owner, repo, title, publicKey string, canPush bool) (*DeployKey, error) {
	if strings.TrimSpace(title) == "" || strings.TrimSpace(publicKey) == "" {
		return nil, errors.New("create project deploy key: title and public key are required")
	}
	path := fmt.Sprintf("/projects/%s/deploy_keys", projectPath(owner, repo))
	resp, err := c.post(ctx, path, map[string]any{
		"title":    title,
		"key":      publicKey,
		"can_push": canPush,
	})
	if err != nil {
		return nil, fmt.Errorf("create project deploy key %q: %w", title, err)
	}
	var key DeployKey
	if err := decodeJSON(resp, &key); err != nil {
		return nil, fmt.Errorf("decode project deploy key %q: %w", title, err)
	}
	if key.ID <= 0 {
		return nil, fmt.Errorf("create project deploy key %q: response carried no key ID", title)
	}
	return &key, nil
}

// ListProjectDeployKeys lists every deploy key enabled on the project. A
// malformed or null page fails the listing instead of passing as an empty
// inventory, so a caller can never mistake an unreadable answer for "no keys
// to remove".
func (c *LiveClient) ListProjectDeployKeys(ctx context.Context, owner, repo string) ([]DeployKey, error) {
	proj := projectPath(owner, repo)
	var result []DeployKey
	for page := 1; page <= deployKeyMaxPages; page++ {
		path := fmt.Sprintf("/projects/%s/deploy_keys?per_page=%d&page=%d", proj, deployKeyPerPage, page)
		resp, err := c.get(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("list project deploy keys page %d: %w", page, err)
		}
		var keys []DeployKey
		if err := decodeJSON(resp, &keys); err != nil {
			return nil, fmt.Errorf("decode project deploy keys page %d: %w", page, err)
		}
		if keys == nil {
			return nil, fmt.Errorf("list project deploy keys page %d: null inventory", page)
		}
		for _, key := range keys {
			if key.ID <= 0 {
				return nil, fmt.Errorf("list project deploy keys page %d: invalid key ID", page)
			}
		}
		result = append(result, keys...)
		if len(keys) < deployKeyPerPage {
			return result, nil
		}
	}
	return nil, fmt.Errorf("list project deploy keys: pagination exceeded %d pages", deployKeyMaxPages)
}

// DeleteProjectDeployKey removes a deploy key from the project. GitLab
// removes any protected-branch grant that names the key along with it. A key
// that is already gone is the desired state, so the call succeeds.
func (c *LiveClient) DeleteProjectDeployKey(ctx context.Context, owner, repo string, keyID int) error {
	if keyID <= 0 {
		return fmt.Errorf("delete project deploy key: invalid key ID %d", keyID)
	}
	path := fmt.Sprintf("/projects/%s/deploy_keys/%d", projectPath(owner, repo), keyID)
	if err := c.delete_(ctx, path); err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("delete project deploy key %d: %w", keyID, err)
	}
	return nil
}

// GrantProtectedBranchPushDeployKey adds deployKeyID to allowed_to_push on
// the branch's exact-name protected-branch rule. It does nothing when the rule
// already names the key. A branch that has no exact-name rule fails closed
// without sending a PATCH, whether it is unprotected or covered only by a
// wildcard rule. A wildcard rule also applies to other branches, so changing
// it would grant the key far more than the one branch this call is scoped to.
func (c *LiveClient) GrantProtectedBranchPushDeployKey(ctx context.Context, owner, repo, branch string, deployKeyID int) error {
	if deployKeyID <= 0 {
		return fmt.Errorf("grant protected branch deploy key push: invalid deploy key ID %d", deployKeyID)
	}
	if strings.Contains(branch, "*") {
		return fmt.Errorf("grant protected branch deploy key push: branch %q is a wildcard pattern, not an exact branch name", branch)
	}
	exact, err := c.getProtectedBranchExact(ctx, owner, repo, branch)
	if err != nil {
		return fmt.Errorf("grant protected branch deploy key push: %w", err)
	}
	if exact == nil {
		return fmt.Errorf("grant protected branch deploy key push: %s has no exact-name protected-branch rule; fullsend does not change wildcard rules, which also cover other branches", branch)
	}
	for _, l := range exact.PushAccessLevels {
		if l.DeployKeyID == deployKeyID {
			return nil
		}
	}
	path := fmt.Sprintf("/projects/%s/protected_branches/%s",
		projectPath(owner, repo), url.PathEscape(branch))
	body := map[string]any{
		"allowed_to_push": []map[string]any{{"deploy_key_id": deployKeyID}},
	}
	resp, err := c.patch(ctx, path, body)
	if err != nil {
		return fmt.Errorf("grant protected branch deploy key push: %w", err)
	}
	resp.Body.Close()
	return nil
}

// RevokeProtectedBranchPushDeployKeyEntry removes one deploy-key push entry,
// identified by its entry ID, from the branch's exact-name protected-branch
// rule. The entry is read first and removed only when it is a grant for
// deployKeyID. A stale or mismatched ID therefore cannot remove an
// administrator's entry. A missing rule or entry is already the desired state,
// so the call succeeds without sending a request.
func (c *LiveClient) RevokeProtectedBranchPushDeployKeyEntry(ctx context.Context, owner, repo, branch string, entryID, deployKeyID int) error {
	if entryID <= 0 || deployKeyID <= 0 {
		return fmt.Errorf("revoke protected branch deploy key push: invalid entry ID %d or deploy key ID %d", entryID, deployKeyID)
	}
	if strings.Contains(branch, "*") {
		return fmt.Errorf("revoke protected branch deploy key push: branch %q is a wildcard pattern, not an exact branch name", branch)
	}
	exact, err := c.getProtectedBranchExact(ctx, owner, repo, branch)
	if err != nil {
		return fmt.Errorf("revoke protected branch deploy key push: %w", err)
	}
	if exact == nil {
		return nil
	}
	found := false
	for _, l := range exact.PushAccessLevels {
		if l.ID != entryID {
			continue
		}
		if l.DeployKeyID != deployKeyID {
			return fmt.Errorf("revoke protected branch deploy key push: entry %d is not a grant for deploy key %d", entryID, deployKeyID)
		}
		found = true
	}
	if !found {
		return nil
	}
	path := fmt.Sprintf("/projects/%s/protected_branches/%s",
		projectPath(owner, repo), url.PathEscape(branch))
	body := map[string]any{
		"allowed_to_push": []map[string]any{{"id": entryID, "_destroy": true}},
	}
	resp, err := c.patch(ctx, path, body)
	if err != nil {
		return fmt.Errorf("revoke protected branch deploy key push: %w", err)
	}
	resp.Body.Close()
	return nil
}

// HasExactProtectedBranchRule reports whether branch has an exact-name
// protected-branch rule. A branch that is unprotected or covered only by
// wildcard rules reports false, so a caller can defer before it creates any
// resource that GrantProtectedBranchPushDeployKey would then refuse to grant.
func (c *LiveClient) HasExactProtectedBranchRule(ctx context.Context, owner, repo, branch string) (bool, error) {
	if strings.Contains(branch, "*") {
		return false, fmt.Errorf("read exact protected branch rule: branch %q is a wildcard pattern, not an exact branch name", branch)
	}
	exact, err := c.getProtectedBranchExact(ctx, owner, repo, branch)
	if err != nil {
		return false, fmt.Errorf("read exact protected branch rule: %w", err)
	}
	return exact != nil, nil
}

// ProtectedBranchPushDeployKeyEntryID returns the ID of the push entry on the
// branch's exact-name protected-branch rule that grants deployKeyID. It
// returns 0 when the rule or the entry is absent. Entries on wildcard rules
// are never reported, because fullsend never grants or revokes them.
func (c *LiveClient) ProtectedBranchPushDeployKeyEntryID(ctx context.Context, owner, repo, branch string, deployKeyID int) (int, error) {
	if deployKeyID <= 0 {
		return 0, fmt.Errorf("read protected branch deploy key push entry: invalid deploy key ID %d", deployKeyID)
	}
	if strings.Contains(branch, "*") {
		return 0, fmt.Errorf("read protected branch deploy key push entry: branch %q is a wildcard pattern, not an exact branch name", branch)
	}
	exact, err := c.getProtectedBranchExact(ctx, owner, repo, branch)
	if err != nil {
		return 0, fmt.Errorf("read protected branch deploy key push entry: %w", err)
	}
	if exact == nil {
		return 0, nil
	}
	for _, l := range exact.PushAccessLevels {
		if l.DeployKeyID == deployKeyID && l.ID > 0 {
			return l.ID, nil
		}
	}
	return 0, nil
}
