package artifacts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// FindBehaviourResults locates behaviour-results.json in downloaded artifacts.
func FindBehaviourResults(artifactRoot string) ([]byte, error) {
	return FindOutputFile(artifactRoot, "behaviour-results.json")
}

// FindOutputFile searches artifact downloads for a sandbox output file by name.
func FindOutputFile(artifactRoot, fileName string) ([]byte, error) {
	found, err := FindOutputFiles(artifactRoot, fileName)
	if err != nil {
		return nil, err
	}
	return found[0], nil
}

// FindOutputFiles returns every sandbox output file with the given name from
// downloaded artifacts. Multiple matrix jobs for one agent can publish the
// same output filename in separate artifact directories.
func FindOutputFiles(artifactRoot, fileName string) ([][]byte, error) {
	return findFileByName(artifactRoot, fileName)
}

var iterationDirPattern = regexp.MustCompile(`^iteration-([0-9]+)$`)

// FindOutputFilesByArtifact returns the sandbox output files with the given
// name grouped by the physical artifact they came from, so callers can pick
// one eligible result per artifact. Each artifact is the first path component
// under artifactRoot; files sitting directly in artifactRoot form their own
// group. Within a group the files are ordered newest first: higher
// iteration-N directories come before lower ones, ties broken by path.
func FindOutputFilesByArtifact(artifactRoot, fileName string) ([][][]byte, error) {
	type match struct {
		path      string
		iteration int
	}
	groups := map[string][]match{}
	err := filepath.WalkDir(artifactRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != fileName {
			return nil
		}
		rel, relErr := filepath.Rel(artifactRoot, path)
		if relErr != nil {
			return relErr
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		key := ""
		if len(parts) > 1 {
			key = parts[0]
		}
		iteration := -1
		for _, part := range parts {
			if m := iterationDirPattern.FindStringSubmatch(part); m != nil {
				if n, convErr := strconv.Atoi(m[1]); convErr == nil {
					iteration = n
				}
			}
		}
		groups[key] = append(groups[key], match{path: path, iteration: iteration})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("%s not found under %s", fileName, artifactRoot)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([][][]byte, 0, len(keys))
	for _, key := range keys {
		matches := groups[key]
		sort.SliceStable(matches, func(i, j int) bool {
			if matches[i].iteration != matches[j].iteration {
				return matches[i].iteration > matches[j].iteration
			}
			return matches[i].path < matches[j].path
		})
		files := make([][]byte, 0, len(matches))
		for _, m := range matches {
			data, readErr := os.ReadFile(m.path)
			if readErr != nil {
				return nil, readErr
			}
			files = append(files, data)
		}
		result = append(result, files)
	}
	return result, nil
}

func findFileByName(artifactRoot, fileName string) ([][]byte, error) {
	var found [][]byte
	err := filepath.WalkDir(artifactRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) != fileName {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found = append(found, data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s not found under %s", fileName, artifactRoot)
	}
	return found, nil
}
