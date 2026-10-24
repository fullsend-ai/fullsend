package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
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
// Senders holds each completed spawn's sender_thread_id, in stream order, for
// the admission cross-check.
// A final line without a newline waits for flush, once the stream has ended:
// codex killed mid-write can leave a completed spawn there.
type codexChildIDs struct {
	IDs     []string
	Senders []string
	// UnreadSpawn marks a line too long to scan that opened as a spawn_agent
	// item: whether that spawn completed is unknown, so the run fails.
	UnreadSpawn bool
	line        []byte
	overlong    bool
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
// streamBufSize, as parseCodexStream does, noting one that opened as a
// spawn_agent item (the parent chooses the prompt's size).
func (c *codexChildIDs) take(b []byte) {
	if c.overlong || len(c.line)+len(b) >= streamBufSize {
		if !c.overlong {
			var head [256]byte
			n := copy(head[:], c.line)
			n += copy(head[n:], b)
			if bytes.Contains(head[:n], []byte(`"type":"collab_tool_call"`)) && bytes.Contains(head[:n], []byte(`"tool":"spawn_agent"`)) {
				c.UnreadSpawn = true
			}
		}
		c.overlong = true
		c.line = c.line[:0]
		return
	}
	c.line = append(c.line, b...)
}

// codexStderrDefaultFilter is codex exec's own stderr filter when RUST_LOG
// is unset, kept as the base so the always-on module directive adds to it.
const codexStderrDefaultFilter = "error,opentelemetry_sdk=off,opentelemetry_otlp=off"

// codexDroppedNotification is the line codex logs, at warn, when its
// in-process channel is full and it drops a stream notification: an item
// then never reaches `exec --json`, and a completed spawn may be among them.
const codexDroppedNotification = "dropping in-process server notification (queue full)"

// codexBackfillFailed starts the line exec logs, at warn, when the read of
// the turn's items that brings a dropped completed item back fails: such an
// item then stays lost.
const codexBackfillFailed = "thread/read failed while backfilling turn items"

// codexStderrDrops forwards codex's stderr to the runner's and counts the
// lines that carry codexDroppedNotification and codexBackfillFailed; flush
// scans a final line without a newline once the stream has ended.
type codexStderrDrops struct {
	w              io.Writer
	line           []byte
	Dropped        int
	BackfillFailed int
}

func (c *codexStderrDrops) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	rest := p
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			if len(c.line) < 64*1024 {
				c.line = append(c.line, rest...)
			}
			return n, err
		}
		if len(c.line) < 64*1024 {
			c.line = append(c.line, rest[:i]...)
		}
		c.flush()
		rest = rest[i+1:]
	}
}

func (c *codexStderrDrops) flush() {
	switch {
	case bytes.Contains(c.line, []byte(codexDroppedNotification)):
		c.Dropped++
	case bytes.Contains(c.line, []byte(codexBackfillFailed)):
		c.BackfillFailed++
	}
	c.line = c.line[:0]
}

// codexCountDroppedInLog counts codexDroppedNotification and
// codexBackfillFailed in the sandbox debug log, where codex's stderr goes
// under --debug instead of the host. A log that is missing or has no such
// line counts zero.
func codexCountDroppedInLog(sandboxName string) (dropped, backfillFailed int, err error) {
	log := shellQuote(sandbox.SandboxWorkspace + "/" + codexDebugLogFile)
	cmd := fmt.Sprintf(`command -p grep -a -c -F -e %s -- %s 2>/dev/null; command -p grep -a -c -F -e %s -- %s 2>/dev/null; true`,
		shellQuote(codexDroppedNotification), log, shellQuote(codexBackfillFailed), log)
	stdout, _, _, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return 0, 0, err
	}
	counts := strings.Fields(stdout)
	if len(counts) > 0 {
		dropped, _ = strconv.Atoi(counts[0])
	}
	if len(counts) > 1 {
		backfillFailed, _ = strconv.Atoi(counts[1])
	}
	return dropped, backfillFailed, nil
}

// flush scans the pending final line, if any.
func (c *codexChildIDs) flush() {
	if len(c.line) > 0 {
		c.scan(c.line)
	}
	c.line, c.overlong = c.line[:0], false
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
			Type           string `json:"type"`
			SenderThreadID string `json:"sender_thread_id"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &evt) != nil || evt.Type != "item.completed" || evt.Item.Type != "collab_tool_call" ||
		evt.Item.Tool != "spawn_agent" || evt.Item.Status != "completed" {
		return
	}
	c.IDs = append(c.IDs, evt.Item.ReceiverThreadIDs...)
	c.Senders = append(c.Senders, evt.Item.SenderThreadID)
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

// The admission cross-check. Codex records no hook outcome in `exec --json`
// or the rollout, so a spawn the guard never decided (a starved or failed
// handler lets the call through) is found by comparing two records: the
// completed spawns of the stream, read on the host, and the guard's admission
// lines in the sandbox findings log. The log is agent-writable: a planted or
// duplicated admission only raises the count, so a forged one is not caught,
// while a torn, cut or missing log can only lower it: it can fail a run that
// would have passed, never pass one that would have failed. The stream side
// is not that strict: codex can drop an item notification under
// backpressure, and the completed count is then too low; it says so on its
// stderr, which Run scans (codexStderrDrops) and warns on. A completed item
// codex saw start comes back when the turn completes, if exec's read of the
// turn's items succeeds; exec warns when it does not, and Run counts that
// line too. A spawn that lost both notifications, or whose dropped
// completed item that read did not bring back, is not counted, and the
// count cannot tell it from a harmless drop. A resume
// that got through appears in the stream
// as a wait item, so it is not caught either, nor is an overlong spawn line
// whose first bytes do not show its type and tool. Counts are compared, not
// ids: a stream item carries no tool_use_id.

// codexSpawnAdmitFinding is the adapter's SPAWN_ADMIT_FINDING.
const codexSpawnAdmitFinding = "codex_spawn_guard_admit"

// codexFindingsLogPath is the adapter's FINDINGS_PATH.
const codexFindingsLogPath = sandbox.SandboxWorkspace + "/.security/findings.jsonl"

// codexSpawnWithoutAdmission starts the cross-check's error.
const codexSpawnWithoutAdmission = "codex_spawn_without_admission"

// codexSpawnAdmissionsReadCommand prints the findings log's lines that name
// an admission, bounded like the usage read. A log grep cannot open prints
// nothing and exits 0: the pipeline's status is head's.
func codexSpawnAdmissionsReadCommand(findingsLog string) string {
	return fmt.Sprintf(`command -p grep -a -e %s -- %s 2>/dev/null | command -p head -c %d`,
		shellQuote(`"`+codexSpawnAdmitFinding+`"`), shellQuote(findingsLog), codexChildUsageMaxBytes)
}

// countCodexSpawnAdmissions counts the lines that decode as a finding named
// codexSpawnAdmitFinding; a cut or planted line that does not decode, or
// that only quotes the name, is not counted.
func countCodexSpawnAdmissions(data []byte) int {
	n := 0
	for line := range bytes.Lines(data) {
		var finding struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(line, &finding) == nil && finding.Name == codexSpawnAdmitFinding {
			n++
		}
	}
	return n
}

// compareCodexSpawnAdmissions returns an error for a spawn without a sender
// or sent by any thread but the primary one (two unknown ids are not the
// same thread), and for more completed spawns in the run than admissions.
// More admissions is not an error: an admitted spawn codex then refused (at
// the thread limit) completed nothing. No id is in the error: the stream
// comes out of the sandbox. At the pinned codex the stream carries the
// primary thread's items only, so the sender rule is defence in depth; a
// child's spawn is the guard's deny.
func compareCodexSpawnAdmissions(primary string, senders []string, completed, admitted int) error {
	for _, sender := range senders {
		if sender == "" || sender != primary {
			return fmt.Errorf("%s: a completed spawn was not sent by the primary thread", codexSpawnWithoutAdmission)
		}
	}
	if completed > admitted {
		return fmt.Errorf("%s: %d completed spawn(s) in this run's stream(s) but %d recorded admission(s)",
			codexSpawnWithoutAdmission, completed, admitted)
	}
	return nil
}

// checkCodexSpawnAdmissions reads the admissions and compares them with an
// iteration's completed spawns (senders) and the run's (completed). Unlike
// the usage read, a read that fails or times out fails the run: this is a
// policy check, not a metric.
func checkCodexSpawnAdmissions(sandboxName, primary string, senders []string, completed int) error {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, codexSpawnAdmissionsReadCommand(codexFindingsLogPath), 10*time.Second)
	if err != nil {
		return fmt.Errorf("reading the spawn admissions: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("reading the spawn admissions: exited %d: %s", exitCode, redactSummary(strings.TrimSpace(sanitizeOutput(stderr))))
	}
	admitted := countCodexSpawnAdmissions([]byte(stdout))
	err = compareCodexSpawnAdmissions(primary, senders, completed, admitted)
	if err != nil && admitted < completed && len(stdout) >= codexChildUsageMaxBytes {
		return fmt.Errorf("%w; the admissions read was cut at %d bytes", err, codexChildUsageMaxBytes)
	}
	return err
}
