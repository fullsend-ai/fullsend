// Package artifacts builds the staged copy of a run's output directory
// that CI uploads as the run artifact (ADR 0161).
//
// The runner leaves output/ untouched so eval-measure and post-scripts
// read the full run. Stage then copies what the resolved level allows
// into a separate directory, and only that directory is uploaded:
//
//   - all: everything in output/.
//   - metadata: only metrics.json, eval-measurements.jsonl and
//     run-telemetry.jsonl from the top of each host run directory, with
//     content-capture attributes and events and the pre-script skip
//     reason stripped from the telemetry copy, and skip explanations
//     replaced in the measurements copy. Every other file stays out.
//   - none: nothing.
package artifacts

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
	"github.com/fullsend-ai/fullsend/internal/telemetry"
)

// MetricsFile is the behavioral metrics file the runner writes at the
// top of each run directory.
const MetricsFile = "metrics.json"

// MetadataFiles is the metadata-level allowlist: the run-level files a
// metadata artifact may hold. A file added to output/ later stays out
// until this list names it.
func MetadataFiles() []string {
	return []string{MetricsFile, evalmeasure.MeasurementsFile, telemetry.TelemetryFile}
}

// PrepareStagedDir empties stagedDir (creating it when absent), so a
// failed resolution or stage leaves nothing to upload. It refuses a
// stagedDir that overlaps outputDir.
func PrepareStagedDir(outputDir, stagedDir string) error {
	if stagedDir == "" {
		return errors.New("staged directory is required")
	}
	if err := checkDisjoint(outputDir, stagedDir); err != nil {
		return err
	}
	if err := os.RemoveAll(stagedDir); err != nil {
		return fmt.Errorf("clearing staged directory: %w", err)
	}
	if err := os.MkdirAll(stagedDir, 0o755); err != nil {
		return fmt.Errorf("creating staged directory: %w", err)
	}
	return nil
}

// Stage copies what level allows from outputDir into stagedDir. stagedDir
// is emptied first. On error, stagedDir is emptied again so a partial
// copy is never uploaded. A missing outputDir stages nothing.
func Stage(outputDir, stagedDir, level string) error {
	if err := PrepareStagedDir(outputDir, stagedDir); err != nil {
		return err
	}
	var err error
	switch level {
	case config.ArtifactContentAll:
		err = stageAll(outputDir, stagedDir)
	case config.ArtifactContentMetadata:
		err = stageMetadata(outputDir, stagedDir)
	case config.ArtifactContentNone:
		err = nil
	default:
		err = fmt.Errorf("unknown artifact content level %q", level)
	}
	if err != nil {
		if clearErr := PrepareStagedDir(outputDir, stagedDir); clearErr != nil {
			return errors.Join(err, clearErr)
		}
		return err
	}
	return nil
}

// checkDisjoint rejects a staged directory equal to, inside, or
// containing the output directory: copying into it would recurse or
// clearing it would destroy the raw output. Both paths are compared after
// resolving symlinks, so a path reached through a symlinked parent cannot
// hide an overlap.
func checkDisjoint(outputDir, stagedDir string) error {
	out, err := resolvePath(outputDir)
	if err != nil {
		return err
	}
	staged, err := resolvePath(stagedDir)
	if err != nil {
		return err
	}
	if within(out, staged) || within(staged, out) {
		return fmt.Errorf("staged directory %s must not overlap output directory %s", stagedDir, outputDir)
	}
	return nil
}

// resolvePath returns the absolute path with symlinks resolved. When the
// path does not exist yet, its nearest existing ancestor is resolved and
// the missing components are joined back on.
func resolvePath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var missing []string
	cur := abs
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolving %s: %w", p, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// stageAll copies the whole output tree. Symlinks are recreated as
// symlinks with the same target, so the upload sees what it saw when it
// uploaded output/ directly.
func stageAll(outputDir, stagedDir string) error {
	if _, err := os.Lstat(outputDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(outputDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(outputDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(stagedDir, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case d.IsDir():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		case d.Type().IsRegular():
			return copyFile(path, dst)
		default:
			// Sockets, devices and pipes are not artifact content.
			return nil
		}
	})
}

// metadataFilters rewrite the metadata files that can carry free text.
// A file whose filter fails is left out of the staged copy.
var metadataFilters = map[string]func([]byte) ([]byte, error){
	telemetry.TelemetryFile:      StripTelemetry,
	evalmeasure.MeasurementsFile: StripMeasurements,
}

// unfilteredMetadataFiles are the metadata files copied as written, with
// the reason no filter applies. Every MetadataFiles entry is either here
// or in metadataFilters (TestMetadataFilesAllFilteredOrExplained).
var unfilteredMetadataFiles = map[string]string{
	MetricsFile: "runner-written counts, costs, model fallback reasons and runner paths; no agent, tracker or pre-script text",
}

// stageMetadata copies the allowlisted run-level files from the top of
// each host run directory under outputDir. The telemetry copy has
// content capture and pre-script free text stripped, and the measurements
// copy has skip explanations replaced; a file that cannot be filtered is
// left out.
func stageMetadata(outputDir, stagedDir string) error {
	entries, err := os.ReadDir(outputDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		// DirEntry type comes from Lstat, so a symlink is never a dir here.
		if !e.IsDir() || !evalmeasure.IsHostRunDir(e.Name()) {
			continue
		}
		srcDir := filepath.Join(outputDir, e.Name())
		dstDir := filepath.Join(stagedDir, e.Name())
		for _, name := range MetadataFiles() {
			src := filepath.Join(srcDir, name)
			info, err := os.Lstat(src)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if err := os.MkdirAll(dstDir, 0o755); err != nil {
				return err
			}
			dst := filepath.Join(dstDir, name)
			strip, filtered := metadataFilters[name]
			if !filtered {
				if err := copyFile(src, dst); err != nil {
					return err
				}
				continue
			}
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			stripped, err := strip(data)
			if err != nil {
				// Fail closed for this file only: the rest of the
				// metadata is still useful without it.
				fmt.Fprintf(os.Stderr, "fullsend: leaving %s out of the artifact: %v\n", filepath.Join(e.Name(), name), err)
				continue
			}
			if err := os.WriteFile(dst, stripped, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
