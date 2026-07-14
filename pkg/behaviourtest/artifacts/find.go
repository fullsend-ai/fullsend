package artifacts

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FindBehaviourResults locates behaviour-results.json in downloaded artifacts.
func FindBehaviourResults(artifactRoot string) ([]byte, error) {
	return findFileByName(artifactRoot, "behaviour-results.json")
}

// FindOutputFile searches artifact downloads for a sandbox output file by name.
func FindOutputFile(artifactRoot, fileName string) ([]byte, error) {
	return findFileByName(artifactRoot, fileName)
}

func findFileByName(artifactRoot, fileName string) ([]byte, error) {
	var found []byte
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
		found = data
		return filepath.SkipAll
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("%s not found under %s", fileName, artifactRoot)
	}
	return found, nil
}
