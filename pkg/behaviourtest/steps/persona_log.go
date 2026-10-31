package steps

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// logTimestamp matches the RFC 3339 prefix GitHub Actions puts on each log line.
var logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z `)

const subagentsLinePrefix = "subagents: "

// personaBlock is one contiguous run of pi's persona registration lines.
type personaBlock struct {
	// Names are the names on the block's "subagents: <name> → ..." lines.
	Names []string
	// Complete is set when an ordinary line precedes and follows the block,
	// so the block was not cut by the start or end of the fetched log.
	Complete bool
}

// logLineBody strips a trailing CR and a leading Actions timestamp.
func logLineBody(line string) string {
	line = strings.TrimSuffix(line, "\r")
	return logTimestamp.ReplaceAllString(line, "")
}

// personaBlocks finds pi's persona registration blocks: maximal runs of
// "subagents: " lines. pi prints its "Warning: persona ..." skip lines in
// the same loop, so those may sit inside a block. A partial-log marker
// after a block, or nothing at all, means the log was cut there, so that
// block is incomplete.
func personaBlocks(logs string) []personaBlock {
	lines := strings.Split(logs, "\n")
	inBlock := func(body string) bool {
		return strings.HasPrefix(body, subagentsLinePrefix) || strings.HasPrefix(body, "Warning: persona ")
	}
	var blocks []personaBlock
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(logLineBody(lines[i]), subagentsLinePrefix) {
			continue
		}
		start := i
		for start > 0 && inBlock(logLineBody(lines[start-1])) {
			start--
		}
		end := i
		var names []string
		for ; end < len(lines) && inBlock(logLineBody(lines[end])); end++ {
			body := logLineBody(lines[end])
			if rest, ok := strings.CutPrefix(body, subagentsLinePrefix); ok {
				name, _, _ := strings.Cut(rest, " ")
				names = append(names, name)
			}
		}
		complete := start > 0 && ordinaryLine(lines[start-1]) &&
			end < len(lines) && ordinaryLine(lines[end])
		blocks = append(blocks, personaBlock{Names: names, Complete: complete})
		i = end
	}
	return blocks
}

// ordinaryLine reports whether line can bound a block: not blank (the
// split after a final newline) and not a partial-log marker.
func ordinaryLine(line string) bool {
	if strings.TrimSpace(logLineBody(line)) == "" {
		return false
	}
	for _, m := range partialLogMarkers {
		if strings.Contains(line, m) {
			return false
		}
	}
	return true
}

// checkPersonaRegistration requires at least one persona block, every
// block complete, want registered and notWant absent in each.
func checkPersonaRegistration(logs, want, notWant string) error {
	blocks := personaBlocks(logs)
	if len(blocks) == 0 {
		return fmt.Errorf("no %q persona registration lines in the logs", strings.TrimSpace(subagentsLinePrefix))
	}
	for i, b := range blocks {
		if !b.Complete {
			return fmt.Errorf("persona registration block %d (%s) is cut off by the end of the fetched logs, so it cannot show which personas registered", i+1, strings.Join(b.Names, ", "))
		}
		if !slices.Contains(b.Names, want) {
			return fmt.Errorf("persona registration block %d registers %s, not %q", i+1, strings.Join(b.Names, ", "), want)
		}
		if slices.Contains(b.Names, notWant) {
			return fmt.Errorf("persona registration block %d unexpectedly registers %q", i+1, notWant)
		}
	}
	return nil
}

// thenHarnessLogsRegisterPersona checks pi's persona registration block in
// the harness run's logs. Only the block must be intact; truncation
// elsewhere in the logs does not matter.
func thenHarnessLogsRegisterPersona(w *world.World, agent, want, notWant string) error {
	agent = strings.TrimSpace(agent)
	if w.WorkflowRun == nil {
		if err := thenHarnessWorkflowCompletes(w, agent); err != nil {
			return err
		}
	}
	run := w.WorkflowRun
	logs, err := w.CI.GetRunLogs(context.Background(), w.Org, w.RepoName, run.ID)
	if err != nil {
		return fmt.Errorf("reading harness %q workflow logs (run %d): %w", agent, run.ID, err)
	}
	if err := checkPersonaRegistration(logs, want, notWant); err != nil {
		return fmt.Errorf("harness %q workflow logs (run %d): %w", agent, run.ID, err)
	}
	return nil
}
