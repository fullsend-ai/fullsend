package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/security"
)

var skillMarkerNames = [...]string{"SKILL.md", "skill.md", "Skill.md"}

// scanRuntimeContent runs InputPipeline on the agent definition, SKILL.md
// files, the JSON of each declared Claude plugin, and every text file of
// each declared pi extension.
func scanRuntimeContent(input runtime.BootstrapInput, failClosed bool) error {
	agentPath := input.AgentPath()
	if agentPath == "" {
		return fmt.Errorf("agent path is required for runtime content scan")
	}

	pipeline := security.InputPipeline()

	if err := scanAgentFile(pipeline, agentPath, failClosed); err != nil {
		return err
	}

	for _, skillPath := range input.SkillDirs() {
		if skillPath == "" {
			continue
		}
		if err := scanSkillDir(pipeline, skillPath, failClosed); err != nil {
			return err
		}
	}

	// Each format is scanned the way its runtime reads it: a Claude plugin
	// through its manifest files, a pi extension through its whole tree,
	// which is code the runtime executes.
	for _, plugin := range input.Plugins() {
		if plugin.Path == "" {
			continue
		}
		var err error
		switch plugin.Kind {
		case pluginformat.KindPi:
			err = scanPluginTree(pipeline, plugin.Path, failClosed, true)
		case pluginformat.KindClaude:
			// Claude Code reads prompt-bearing content from all over the
			// tree (commands/, agents/, skills/, hooks/, .mcp.json, the
			// manifest), so the whole tree is scanned; a symlink is
			// skipped rather than refused, since no run-time preflight
			// re-hashes a Claude plugin.
			err = scanPluginTree(pipeline, plugin.Path, failClosed, false)
		default:
			err = fmt.Errorf("plugin %q: unknown format kind %q", plugin.Path, plugin.Kind)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

// Bounds on the extension scan. An extension ships its dependencies, so
// the tree can be large; these keep bootstrap from turning into a
// multi-minute regex run over vendored bundles without letting an
// extension hide code behind sheer volume.
// They are variables, not constants, only so tests can lower them without
// writing 20 000 files.
var (
	// maxExtensionScanFileBytes is the largest file the injection pipeline
	// is asked to look at. Bigger files are noted and skipped: they are
	// minified bundles or data blobs, where the heuristics produce noise
	// rather than signal.
	maxExtensionScanFileBytes int64 = 1 << 20 // 1 MiB
	// maxExtensionScanFiles bounds the number of files in one extension.
	// Above it the scan gives up and the bootstrap fails, in either
	// fail_mode: an extension with more files than this is not something
	// the scan can vouch for. Files skipped for size count towards it, so a
	// tree made entirely of oversized blobs still hits a bound.
	maxExtensionScanFiles = 20000
)

// errExtensionScanBlocked marks the fail-closed verdict so the caller can
// tell it apart from a walk error (a permission problem, a vanished file)
// without matching on message text.
var errExtensionScanBlocked = errors.New("blocked: critical injection findings")

// errExtensionScanUnbounded marks the too-many-files refusal, which is not
// a scan failure fail_mode may downgrade either.
var errExtensionScanUnbounded = errors.New("too many files to scan")

// errExtensionScanRefused marks an entry the extension tree may not hold at
// all (a symlink, a special file, an unreproducible name). Like the two
// above it is a refusal in its own right, not a scan failure fail_mode may
// downgrade: the Run-time preflight would fail the same tree closed.
var errExtensionScanRefused = errors.New("refused: inadmissible entry")

// (the pi extension scan)
// directory (node_modules included — vendored dependencies are code the
// model's tools will run). Binary files are skipped by a cheap NUL-byte
// probe, oversized ones by maxExtensionScanFileBytes; the scan is
// heuristic, so breadth matters more than precision, and a finding in
// third-party JavaScript or prose is as likely to be a false positive as a
// real one (see docs/runtimes/pi.md).
// scanPluginTree scans every regular text file under a plugin directory.
// refuseSpecial is the pi rule: a symlink or special file is a refusal
// (the run-time preflight would reject the tree anyway); for a Claude
// plugin such entries are skipped instead.
func scanPluginTree(pipeline *security.Pipeline, extPath string, failClosed bool, refuseSpecial bool) error {
	var scanned, skippedLarge int
	root, err := filepath.EvalSymlinks(extPath)
	if err == nil {
		err = filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				rel = p
			}
			rel = filepath.ToSlash(rel)
			if p == root {
				return nil
			}
			// Same rule as harness validation and the tree hash: a symlink
			// or a special file is a refusal, not something to walk past.
			// Skipping it silently here would let a tree the Run-time
			// preflight rejects sail through bootstrap unscanned.
			if problem := pluginformat.ExtensionEntryProblem(rel, d.Type()); problem != "" {
				if !refuseSpecial {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				return fmt.Errorf("extension %q: %w: %s", extPath, errExtensionScanRefused, problem)
			}
			if d.IsDir() {
				return nil
			}
			// Counted before the size check so a tree of oversized blobs
			// still hits the cap.
			scanned++
			if scanned > maxExtensionScanFiles {
				return fmt.Errorf("extension %q: %w (more than %d); refusing to bootstrap an extension the injection scan cannot cover", extPath, errExtensionScanUnbounded, maxExtensionScanFiles)
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Size() > maxExtensionScanFileBytes {
				skippedLarge++
				fmt.Fprintf(os.Stderr, "WARNING: extension %q: %s is %d bytes, over the %d-byte scan limit — not scanned\n", extPath, rel, info.Size(), maxExtensionScanFileBytes)
				return nil
			}
			content, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			if looksBinary(content) {
				return nil
			}
			result := pipeline.Scan(string(content))
			if security.HasCriticalFindings(result.Findings) {
				if failClosed {
					return fmt.Errorf("extension %q: %w in %s", extPath, errExtensionScanBlocked, rel)
				}
				fmt.Fprintf(os.Stderr, "WARNING: extension %q has critical injection findings in %s (fail_mode: open)\n", extPath, rel)
				for _, f := range result.Findings {
					fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
				}
			} else if len(result.Findings) > 0 {
				fmt.Fprintf(os.Stderr, "WARNING: extension %q has %d injection finding(s) in %s\n", extPath, len(result.Findings), rel)
				for _, f := range result.Findings {
					fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
				}
			}
			return nil
		})
	}
	if skippedLarge > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: extension %q: %d file(s) skipped by the %d-byte scan limit\n", extPath, skippedLarge, maxExtensionScanFileBytes)
	}
	if err == nil {
		return nil
	}
	// A blocked verdict and an unscannable tree are refusals in their own
	// right, not scan failures the fail_mode can downgrade.
	if errors.Is(err, errExtensionScanBlocked) || errors.Is(err, errExtensionScanUnbounded) ||
		errors.Is(err, errExtensionScanRefused) {
		return err
	}
	if failClosed {
		return fmt.Errorf("cannot scan extension %q: %w", extPath, err)
	}
	fmt.Fprintf(os.Stderr, "WARNING: could not scan extension %q: %v\n", extPath, err)
	return nil
}

// looksBinary reports whether content is not text: a NUL byte in the first
// 8 KiB, the same heuristic git uses.
func looksBinary(content []byte) bool {
	probe := content
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	return bytes.IndexByte(probe, 0) >= 0
}

func scanAgentFile(pipeline *security.Pipeline, agentPath string, failClosed bool) error {
	content, err := os.ReadFile(agentPath)
	if err != nil {
		if failClosed {
			return fmt.Errorf("cannot scan agent definition %q: %w", agentPath, err)
		}
		fmt.Fprintf(os.Stderr, "WARNING: could not read agent definition %q for scan: %v\n", agentPath, err)
		return nil
	}
	result := pipeline.Scan(string(content))
	if security.HasCriticalFindings(result.Findings) {
		if failClosed {
			return fmt.Errorf("agent definition %q blocked: critical injection findings", agentPath)
		}
		fmt.Fprintf(os.Stderr, "WARNING: agent definition %q has critical injection findings (fail_mode: open)\n", agentPath)
		for _, f := range result.Findings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
		}
	} else if len(result.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: agent definition %q has %d injection finding(s)\n", agentPath, len(result.Findings))
		for _, f := range result.Findings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
		}
	}
	return nil
}

func scanSkillDir(pipeline *security.Pipeline, skillPath string, failClosed bool) error {
	var skillContent []byte
	for _, name := range skillMarkerNames {
		if c, err := os.ReadFile(filepath.Join(skillPath, name)); err == nil {
			skillContent = c
			break
		}
	}
	if skillContent == nil {
		if failClosed {
			fmt.Fprintf(os.Stderr, "WARNING: skill %q has no SKILL.md to scan\n", skillPath)
		}
		return nil
	}
	result := pipeline.Scan(string(skillContent))
	if security.HasCriticalFindings(result.Findings) {
		if failClosed {
			return fmt.Errorf("skill %q blocked: critical injection findings in SKILL.md", skillPath)
		}
		fmt.Fprintf(os.Stderr, "WARNING: skill %q has critical injection findings (fail_mode: open) — uploading anyway\n", skillPath)
		for _, f := range result.Findings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
		}
	} else if len(result.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: skill %q has %d non-critical injection finding(s) — not blocked (only critical findings block); uploading\n", skillPath, len(result.Findings))
		for _, f := range result.Findings {
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
		}
	}
	return nil
}

func scanPluginDir(pipeline *security.Pipeline, pluginPath string, failClosed bool) error {
	for _, name := range []string{"plugin.json", ".lsp.json"} {
		content, err := os.ReadFile(filepath.Join(pluginPath, name))
		if err != nil {
			continue
		}
		result := pipeline.Scan(string(content))
		if security.HasCriticalFindings(result.Findings) {
			if failClosed {
				return fmt.Errorf("plugin %q blocked: critical injection findings in %s", pluginPath, name)
			}
			fmt.Fprintf(os.Stderr, "WARNING: plugin %q has critical injection findings in %s (fail_mode: open)\n", pluginPath, name)
			for _, f := range result.Findings {
				fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
			}
		} else if len(result.Findings) > 0 {
			fmt.Fprintf(os.Stderr, "WARNING: plugin %q has %d injection finding(s) in %s\n", pluginPath, len(result.Findings), name)
			for _, f := range result.Findings {
				fmt.Fprintf(os.Stderr, "  [%s] %s: %s\n", f.Severity, f.Name, f.Detail)
			}
		}
	}
	return nil
}
