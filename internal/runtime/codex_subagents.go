package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// Native child accounting for the Codex runtime. A child's tokens never reach
// the parent's `exec --json` stream; codex records them only in the child's
// rollout under $CODEX_HOME/sessions/ (rollout-<start>-<thread id>.jsonl).
// After codex exits, Run reads each child's model and usage from it and folds
// them into RunMetrics. Child reasoning goes to the run total only, as on pi.

// codexChildUsageMarker starts the line printed before each child's rollout
// lines. A rollout line that imitates it can only misattribute its own child.
const codexChildUsageMarker = "fullsend-child"

// codexChildUnreadableMarker starts the line printed for a child whose
// rollout fails test -r, that a failed search did not find, or that has more
// than one rollout.
const codexChildUnreadableMarker = "fullsend-unreadable"

// codexChildUsageMaxBytes bounds the read: the sessions directory is
// agent-writable. A real read is a few kilobytes.
const codexChildUsageMaxBytes = 1 << 20

// codexChildUsageMaxTokens is far above any real counter. A child past it is
// skipped with a warning, so planted counters cannot wrap the run's sums.
const codexChildUsageMaxTokens = 1 << 40

// codexChildUsageThreadID is a codex thread id. Ids go into a shell command,
// so nothing else is accepted.
var codexChildUsageThreadID = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// codexChildUsage is what one child's rollout reported.
type codexChildUsage struct {
	ID string
	// Model is from the last turn_context that decodes, the child's own (a
	// fork copies the parent's ahead of it); "" when there is none.
	Model string
	// Usage is the thread total of the last token_usage_record that decodes.
	Usage codexUsage
	// HasUsage is false when the rollout has no record; such a child adds
	// nothing, not even a request.
	HasUsage bool
	// Unreadable marks a rollout that failed test -r, a failed search, or an
	// id with more than one rollout.
	Unreadable bool
	// BadLine marks a line that did not decode (torn, cut or planted).
	BadLine bool
}

// codexChildUsageReadCommand renders the in-sandbox command that prints, per
// child, a marker line, the rollout's last two turn_context and last two
// token_usage_record lines: the parser keeps the last that decodes, so a torn
// last line falls back to the one before it. The record, not token_count: codex zeroes token_count's
// counters when a request overflows the context window. A rollout it cannot
// read, or that a failed search cannot find, prints the unreadable marker,
// and so does an id with more than one rollout (codex writes one; copies are
// planted), whose copies all go uncounted;
// one a clean search misses prints nothing; a path outside sessionsDir is
// skipped; head -c bounds the output.
func codexChildUsageReadCommand(sessionsDir string, ids []string) (string, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		if !codexChildUsageThreadID.MatchString(id) {
			return "", fmt.Errorf("child thread id %q is not a codex thread id", id)
		}
		quoted = append(quoted, shellQuote(id))
	}
	return fmt.Sprintf(
		`for id in %s; do files=$(command -p find %s -type f -name "rollout-*-$id.jsonl" 2>/dev/null) || [ -n "$files" ] || printf '%%s %%s\n' %s "$id"; if [ "$(printf '%%s\n' "$files" | command -p grep -c .)" -gt 1 ]; then printf '%%s %%s\n' %s "$id"; continue; fi; printf '%%s\n' "$files" | while IFS= read -r f; do case $f in %s/*) ;; *) continue ;; esac; if [ ! -r "$f" ]; then printf '%%s %%s\n' %s "$id"; continue; fi; printf '%%s %%s\n' %s "$id"; command -p grep -a -e '"type":"turn_context"' -- "$f" 2>/dev/null | command -p tail -n 2; command -p grep -a -e '"type":"token_usage_record"' -- "$f" 2>/dev/null | command -p tail -n 2; done; done | command -p head -c %d`,
		strings.Join(quoted, " "), shellQuote(sessionsDir), shellQuote(codexChildUnreadableMarker), shellQuote(codexChildUnreadableMarker), shellQuote(sessionsDir), shellQuote(codexChildUnreadableMarker), shellQuote(codexChildUsageMarker), codexChildUsageMaxBytes), nil
}

// parseCodexChildUsage decodes the read command's output. Lines before the
// first marker, and lines that do not decode (a cut, planted text, a counter
// past the int range), are skipped.
func parseCodexChildUsage(data []byte) []codexChildUsage {
	var out []codexChildUsage
	marker := []byte(codexChildUsageMarker + " ")
	unreadable := []byte(codexChildUnreadableMarker + " ")
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if id, ok := bytes.CutPrefix(line, marker); ok {
			out = append(out, codexChildUsage{ID: string(id)})
			continue
		}
		if id, ok := bytes.CutPrefix(line, unreadable); ok {
			out = append(out, codexChildUsage{ID: string(id), Unreadable: true})
			continue
		}
		if len(out) == 0 {
			continue
		}
		var env struct {
			Type    string `json:"type"`
			Payload struct {
				Model  string     `json:"model"`
				Thread codexUsage `json:"thread_token_usage"`
			} `json:"payload"`
		}
		cur := &out[len(out)-1]
		if json.Unmarshal(line, &env) != nil {
			cur.BadLine = true
			continue
		}
		switch {
		case env.Type == "turn_context":
			cur.Model = strings.TrimSpace(env.Payload.Model)
		case env.Type == "token_usage_record":
			cur.Usage = env.Payload.Thread
			cur.HasUsage = true
		}
	}
	return out
}

// codexChildIDs collects the thread ids of completed spawn_agent items from
// a tee of the `exec --json` stream: the parser's events carry no thread id.
// A final line without a newline is not scanned; in codex it is the terminal
// event.
type codexChildIDs struct {
	IDs      []string
	line     []byte
	overlong bool
}

func (c *codexChildIDs) Write(p []byte) (int, error) {
	rest := p
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			c.take(rest)
			return len(p), nil
		}
		c.take(rest[:i])
		if !c.overlong {
			c.scan(c.line)
		}
		c.line, c.overlong = c.line[:0], false
		rest = rest[i+1:]
	}
}

// take appends to the pending line and drops a line that reaches
// streamBufSize, as parseCodexStream does.
func (c *codexChildIDs) take(b []byte) {
	if c.overlong || len(c.line)+len(b) >= streamBufSize {
		c.overlong = true
		c.line = c.line[:0]
		return
	}
	c.line = append(c.line, b...)
}

func (c *codexChildIDs) scan(line []byte) {
	// Pre-filter: few lines name the tool.
	if !bytes.Contains(line, []byte("spawn_agent")) {
		return
	}
	var evt struct {
		Type string `json:"type"`
		Item struct {
			codexCollabToolCallItem
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &evt) != nil || evt.Type != "item.completed" || evt.Item.Type != "collab_tool_call" ||
		evt.Item.Tool != "spawn_agent" || evt.Item.Status != "completed" {
		return
	}
	c.IDs = append(c.IDs, evt.Item.ReceiverThreadIDs...)
}

// foldCodexChildUsage adds the parent's entry to m.PerModelUsage, always, so
// the breakdown sums to the totals when iterations are summed. It then folds
// each child's usage into the totals and the breakdown, one request per
// rollout, and returns how many it folded. The error lists what was not
// counted; the caller warns and keeps the run. No sandbox command runs
// without child ids. A child with no model goes under piSubagentUnknownModel,
// as on pi. The figures are agent-written, so counters are floored at zero
// and a child past codexChildUsageMaxTokens is skipped.
func (r CodexRuntime) foldCodexChildUsage(sandboxName string, childIDs []string, m *RunMetrics) (folded int, err error) {
	per := m.PerModelUsage
	if per == nil {
		per = make(map[string]ModelUsage, len(childIDs)+1)
	}
	add := func(key string, u ModelUsage) {
		entry := per[key]
		entry.Add(u)
		per[key] = entry
	}
	parent := m.Model
	if parent == "" {
		parent = piSubagentUnknownModel
	}
	add(parent, ModelUsage{
		Requests:                 1,
		InputTokens:              m.InputTokens,
		OutputTokens:             m.OutputTokens,
		CacheCreationInputTokens: m.CacheCreationInputTokens,
		CacheReadInputTokens:     m.CacheReadInputTokens,
		CostUSD:                  m.TotalCostUSD,
	})
	m.PerModelUsage = per
	if len(childIDs) == 0 {
		return 0, nil
	}

	cmd, err := codexChildUsageReadCommand(r.codexSessionsDir(), childIDs)
	if err != nil {
		return 0, err
	}
	// A read that fails still folds whatever stdout came back.
	var lost []string
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		lost = append(lost, err.Error())
	} else if exitCode != 0 {
		lost = append(lost, fmt.Sprintf("exited %d: %s", exitCode, strings.TrimSpace(sanitizeOutput(stderr))))
	}
	unreadable, overMaxTokens, badLines := 0, 0, 0
	for _, child := range parseCodexChildUsage([]byte(stdout)) {
		if child.Unreadable {
			unreadable++
		}
		if child.BadLine {
			badLines++
		}
		if !child.HasUsage {
			continue
		}
		// counters() floors only its subtractions; the raw fields are
		// floored here.
		c := child.Usage.counters()
		reasoning, cacheWrite, cacheRead := nonNegative(c.Reasoning), nonNegative(c.CacheWrite), nonNegative(c.CacheRead)
		if max(c.Input, c.Output, reasoning, cacheWrite, cacheRead) > codexChildUsageMaxTokens {
			overMaxTokens++
			continue
		}
		// The model is agent-written and becomes a metrics.json key.
		key := redactSummary(child.Model)
		if key == "" {
			key = piSubagentUnknownModel
		}
		add(key, ModelUsage{
			Requests:                 1,
			InputTokens:              c.Input,
			OutputTokens:             c.Output,
			CacheCreationInputTokens: cacheWrite,
			CacheReadInputTokens:     cacheRead,
		})
		m.InputTokens += c.Input
		m.OutputTokens += c.Output
		m.ReasoningTokens += reasoning
		m.CacheCreationInputTokens += cacheWrite
		m.CacheReadInputTokens += cacheRead
		folded++
	}
	if unreadable > 0 {
		lost = append(lost, fmt.Sprintf("%d child rollout(s) could not be read", unreadable))
	}
	if badLines > 0 {
		lost = append(lost, fmt.Sprintf("%d child rollout(s) had a line that did not decode", badLines))
	}
	if overMaxTokens > 0 {
		lost = append(lost, fmt.Sprintf("%d child(ren) with a counter past %d not counted", overMaxTokens, codexChildUsageMaxTokens))
	}
	if len(stdout) >= codexChildUsageMaxBytes {
		lost = append(lost, fmt.Sprintf("cut at %d bytes; children at and past the cut are not fully counted", codexChildUsageMaxBytes))
	}
	if len(lost) > 0 {
		return folded, fmt.Errorf("reading the child rollouts: %s", strings.Join(lost, "; "))
	}
	return folded, nil
}
