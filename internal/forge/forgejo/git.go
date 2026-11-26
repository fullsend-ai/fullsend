package forgejo

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Git object reads. They address commits, trees, and blobs by object ID
// through Forgejo's git data API, so the content they return is the
// content of that object: unlike the contents API's ref parameter, a full
// object ID is never resolved as a branch or tag name, so a branch named
// like a pinned commit cannot stand in for it.

// TreeEntry is one entry of a git tree.
type TreeEntry struct {
	// Path is relative to the listed tree.
	Path string `json:"path"`
	// Mode is the git file mode: "100644" or "100755" for a regular file,
	// "120000" for a symlink, "040000" for a tree, "160000" for a
	// submodule.
	Mode string `json:"mode"`
	// Type is "blob", "tree", or "commit" (a submodule).
	Type string `json:"type"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

// treePageSize is the page size requested from the git trees API. Forgejo
// caps it at its own default (1000), and reports truncated when more
// entries remain.
const treePageSize = 1000

// GetCommitSHA returns the SHA of commit sha in owner/repo as Forgejo
// reports it, or an error wrapping forge.ErrNotFound when the repository
// has no such commit. A sha that names a tree, blob, or tag object is an
// error rather than a commit.
func (c *LiveClient) GetCommitSHA(ctx context.Context, owner, repo, sha string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	u := c.repoURL(owner, repo, "/git/commits/"+url.PathEscape(sha)) + "?stat=false&verification=false&files=false"
	if err := c.do(ctx, http.MethodGet, u, nil, &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("commit %q in %q/%q: response has no SHA", sha, owner, repo)
	}
	return out.SHA, nil
}

// GetTree returns the entries of the tree sha in owner/repo; sha may also
// be a commit, whose root tree is listed. With recursive set every entry
// below the tree is listed, with paths relative to it. A listing of more
// than maxEntries entries is an error rather than a partial result.
func (c *LiveClient) GetTree(ctx context.Context, owner, repo, sha string, recursive bool, maxEntries int) ([]TreeEntry, error) {
	var entries []TreeEntry
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("recursive", strconv.FormatBool(recursive))
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(treePageSize))
		var out struct {
			Tree      []TreeEntry `json:"tree"`
			Truncated bool        `json:"truncated"`
		}
		if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/git/trees/"+url.PathEscape(sha))+"?"+q.Encode(), nil, &out); err != nil {
			return nil, err
		}
		entries = append(entries, out.Tree...)
		if len(entries) > maxEntries {
			return nil, fmt.Errorf("tree %q in %q/%q has more than %d entries", sha, owner, repo, maxEntries)
		}
		if !out.Truncated {
			return entries, nil
		}
		if len(out.Tree) == 0 {
			return nil, fmt.Errorf("tree %q in %q/%q: page %d is truncated but empty", sha, owner, repo, page)
		}
	}
}

// GetBlob returns the content of blob sha in owner/repo, or an error
// wrapping forge.ErrNotFound when it does not exist. Content larger than
// limit bytes is an error.
func (c *LiveClient) GetBlob(ctx context.Context, owner, repo, sha string, limit int64) ([]byte, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoURL(owner, repo, "/git/blobs/"+url.PathEscape(sha)), nil, &out); err != nil {
		return nil, err
	}
	if out.Size > limit {
		return nil, fmt.Errorf("blob %q in %q/%q is %d bytes, over the %d-byte limit", sha, owner, repo, out.Size, limit)
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("blob %q in %q/%q: unsupported content encoding %q", sha, owner, repo, out.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("blob %q in %q/%q: decoding content: %w", sha, owner, repo, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("blob %q in %q/%q exceeds the %d-byte limit", sha, owner, repo, limit)
	}
	return data, nil
}
