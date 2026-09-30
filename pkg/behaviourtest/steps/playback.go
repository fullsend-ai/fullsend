package steps

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func commitPlaylist(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before committing the playlist")
	}
	if len(w.PlaybackEntries) == 0 {
		return fmt.Errorf("no playback entries; add entries with 'Given a <agent> agent that returns'")
	}

	if strings.TrimSpace(w.FixturesRoot) == "" {
		return fmt.Errorf("world.FixturesRoot is not set")
	}

	moduleRoot, err := findModuleSubdir(w.FixturesRoot)
	if err != nil {
		return err
	}

	ctx := context.Background()
	message := fmt.Sprintf("behaviour: set playback results (%s)", time.Now().UTC().Format(time.RFC3339))

	forge := resolveForge(w)

	var results []string
	for _, entry := range w.PlaybackEntries {
		localDir := filepath.Join(moduleRoot, "results", entry.Result)
		if _, err := os.Stat(localDir); err != nil {
			return fmt.Errorf("result directory %s not found: %w", entry.Result, err)
		}

		if err := commitResultDir(ctx, w, localDir, entry.Result, forge, message); err != nil {
			return err
		}
		results = append(results, entry.Result)
	}

	playlist := runtime.Playlist{
		Current: 1,
		Results: results,
	}
	data, err := yaml.Marshal(&playlist)
	if err != nil {
		return fmt.Errorf("marshaling playlist: %w", err)
	}

	if err := w.SCM.CommitFile(ctx, w.Org, w.RepoName, ".fullsend/results/playlist.yaml", message, data); err != nil {
		return fmt.Errorf("committing playlist: %w", err)
	}

	w.PlaybackCommitted = true
	return nil
}

// commitResultDir walks a result directory and commits all files to the test
// repo. Forge-specific overrides (files in a <forge>/ subdirectory) are
// layered on top of base files: the forge-specific version replaces the base
// version at the same relative path.
func commitResultDir(ctx context.Context, w *world.World, localDir, entryName, forge, message string) error {
	repoPrefix := ".fullsend/results/" + entryName + "/"

	files := map[string]string{}

	if err := filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		parts := strings.SplitN(relPath, string(filepath.Separator), 2)
		if len(parts) > 1 && isForgeDir(parts[0]) {
			return nil
		}
		files[relPath] = path
		return nil
	}); err != nil {
		return fmt.Errorf("walking result directory %s: %w", entryName, err)
	}

	if forge != "" {
		forgeDir := filepath.Join(localDir, forge)
		if st, err := os.Stat(forgeDir); err == nil && st.IsDir() {
			if err := filepath.WalkDir(forgeDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				relPath, err := filepath.Rel(forgeDir, path)
				if err != nil {
					return err
				}
				files[relPath] = path
				return nil
			}); err != nil {
				return fmt.Errorf("walking forge override %s/%s: %w", entryName, forge, err)
			}
		}
	}

	for relPath, localPath := range files {
		content, err := os.ReadFile(localPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", localPath, err)
		}
		repoPath := repoPrefix + relPath
		if err := w.SCM.CommitFile(ctx, w.Org, w.RepoName, repoPath, message, content); err != nil {
			return fmt.Errorf("committing %s: %w", repoPath, err)
		}
	}

	return nil
}

func isForgeDir(name string) bool {
	switch name {
	case "github", "gitlab", "jira":
		return true
	}
	return false
}

// resolveForge returns the forge platform string from the World's SCM driver.
// Returns empty string if the forge cannot be determined.
func resolveForge(w *world.World) string {
	if w.SCM == nil {
		return ""
	}
	if namer, ok := w.SCM.(interface{ ForgeName() string }); ok {
		return namer.ForgeName()
	}
	return ""
}
