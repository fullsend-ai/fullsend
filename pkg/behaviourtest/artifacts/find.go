package artifacts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
