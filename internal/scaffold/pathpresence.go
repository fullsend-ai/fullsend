package scaffold

import (
	"context"
	"fmt"
	"sort"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ComparePathPresence checks which expected paths exist in the repo's
// default branch. It uses forge.Client.ListRepositoryFiles to fetch all
// file paths in a single Git Trees API call, then checks membership
// locally. This replaces O(N) GetFileContent calls with O(1) API calls.
func ComparePathPresence(ctx context.Context, client forge.Client, owner, repo string, expected []string) (missing []string, err error) {
	if len(expected) == 0 {
		return nil, nil
	}

	allPaths, err := client.ListRepositoryFiles(ctx, owner, repo)
	if err != nil {
		return nil, fmt.Errorf("listing repository files: %w", err)
	}

	existing := make(map[string]struct{}, len(allPaths))
	for _, p := range allPaths {
		existing[p] = struct{}{}
	}

	for _, path := range expected {
		if _, ok := existing[path]; !ok {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	return missing, nil
}
