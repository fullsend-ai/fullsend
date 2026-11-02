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

// codexChildIDs collects the thread ids of the spawn_agent items codex
// finished, completed or failed, from a tee of the `exec --json` stream: the
// parser's events carry no thread id. Senders holds each one's
// sender_thread_id, in stream order, for the admission cross-check.
// A final line without a newline waits for flush, once the stream has ended:
// codex killed mid-write can leave a completed spawn there.
type codexChildIDs struct {
	// IDs are the receivers of completed spawn items, for the usage fold.
	IDs []string
	// Senders has one entry per spawn item the handler finished, completed
	// or failed: the stream side of the admission count.
	Senders []string
	// Receivers are the receivers of every finished spawn item, completed or
	// failed, and NoReceiver counts the finished items that name none (a
	// spawn codex refused before a thread existed): with the children on
	// disk they count each spawn once, whichever side saw it.
	Receivers  []string
	NoReceiver int
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
// in-process channel is full and it drops a notification, of any thread
// (children included); the line does not say which. A completed spawn may be
// among them.
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
// under --debug instead of the host. A missing log counts zero, as does a
// log with no such line; a log grep cannot read (unreadable, a directory), a
// command that did not finish, or output that is not two counts is an
// error, not a zero count.
func codexCountDroppedInLog(sandboxName string) (dropped, backfillFailed int, err error) {
	cmd := fmt.Sprintf(`log=%s; [ -e "$log" ] || { echo 0 0; exit 0; }; `+
		`d=$(command -p grep -a -c -F -e %s -- "$log"); rd=$?; `+
		`b=$(command -p grep -a -c -F -e %s -- "$log"); rb=$?; `+
		`[ "$rd" -le 1 ] && [ "$rb" -le 1 ] || exit 3; echo "$d $b"`,
		shellQuote(sandbox.SandboxWorkspace+"/"+codexDebugLogFile), shellQuote(codexDroppedNotification), shellQuote(codexBackfillFailed))
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 10*time.Second)
	if err != nil {
		return 0, 0, err
	}
	if exitCode != 0 {
		return 0, 0, fmt.Errorf("exited %d: %s", exitCode, redactSummary(strings.TrimSpace(sanitizeOutput(stderr))))
	}
	counts := strings.Fields(stdout)
	if len(counts) != 2 {
		return 0, 0, fmt.Errorf("read %d counts, want 2", len(counts))
	}
	if dropped, err = strconv.Atoi(counts[0]); err == nil {
		backfillFailed, err = strconv.Atoi(counts[1])
	}
	if err != nil {
		return 0, 0, fmt.Errorf("reading the counts: %w", err)
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
		evt.Item.Tool != "spawn_agent" || (evt.Item.Status != "completed" && evt.Item.Status != "failed") {
		return
	}
	// A failed item is a spawn the handler refused (at the thread limit, for
	// one) after the guard admitted it, so it counts against the admissions;
	// a spawn the guard blocked never reaches the handler and emits no item.
	// Only a completed spawn has a child to fold.
	if evt.Item.Status == "completed" {
		c.IDs = append(c.IDs, evt.Item.ReceiverThreadIDs...)
	}
	c.Senders = append(c.Senders, evt.Item.SenderThreadID)
	c.Receivers = append(c.Receivers, evt.Item.ReceiverThreadIDs...)
	if len(evt.Item.ReceiverThreadIDs) == 0 {
		c.NoReceiver++
	}
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
// handler lets the call through) is found by comparing the guard's admission
// lines in the sandbox findings log with the spawns seen on either side,
// each counted once per thread: the stream's spawn items, read on the host,
// completed or failed (a blocked spawn emits none; a refused one used its
// admission), and the children's rollouts on disk (countCodexChildRollouts). The log is
// agent-writable: a planted or duplicated admission only raises the count, so
// a forged one is not caught, while a torn, cut or missing log can only lower
// it: it can fail a run that would have passed, never pass one that would
// have failed. Codex can drop a stream notification under backpressure and
// says so on stderr, which Run scans (codexStderrDrops) and warns on: a lost
// spawn item lowers the stream's count but not the rollouts'. A resume that
// got through appears in the stream as a wait item, so it is not caught, nor
// is a spawn hidden from both sides (its stream items lost and its rollout,
// or an ancestor's, deleted, renamed or edited), nor an overlong spawn line
// whose first bytes do not show its type and tool. Admissions are compared
// by count, not id (a stream item carries no tool_use_id), so an admission
// no spawn used (codex rejected the call before it emitted an item, or
// another PreToolUse hook blocked it) can cover a later unadmitted spawn.

// codexSpawnAdmitFinding is the adapter's SPAWN_ADMIT_FINDING.
const codexSpawnAdmitFinding = "codex_spawn_guard_admit"

// codexFindingsLogPath is the adapter's FINDINGS_PATH.
const codexFindingsLogPath = sandbox.SandboxWorkspace + "/.security/findings.jsonl"

// codexSpawnWithoutAdmission starts the cross-check's error.
const codexSpawnWithoutAdmission = "codex_spawn_without_admission"

// codexChildRolloutsMarker opens the child-rollout read, so a test double
// can route it; it is a shell no-op.
const codexChildRolloutsMarker = "fullsend-rollout-heads"

// codexChildRolloutsReadCommand prints, for each *.jsonl under the sessions
// directory (recursively: codex files rollouts under YYYY/MM/DD), one line:
// 1 for a first line that is a whole session_meta record (it ends in } and a
// newline), 2 for one that is not (cut, still being written, a fragment
// codex closed with a newline after a failed write, or 1 MiB or longer), 0
// for any other, then the first `"id":"..."` and `"parent_thread_id":"..."`
// on that line, or none, and the thread id in the file's name, or none.
// Codex names a rollout rollout-<ts>-<thread id>.jsonl (with _<rollout id>
// after a revert), creates it, and then writes its session_meta as the first
// line, carrying parent_thread_id for a child; a root carries none (codex
// rust-v0.159.3). So a child's file can be empty while that line waits, or
// start for good with a fragment a failed write left (codex closes it with a
// newline and writes session_meta on the next line). Every field before
// parent_thread_id is a scalar, so a line cut before its parent cannot end
// in }. A key quoted inside a string is escaped there, so it cannot match. A
// missing directory prints nothing; a find that fails exits 3; head -c
// bounds each line read and the output.
func codexChildRolloutsReadCommand(sessionsDir string) string {
	return fmt.Sprintf(`: %s; d=%s; [ -d "$d" ] || exit 0; files=$(command -p find "$d" -type f -name '*.jsonl') || exit 3; `+
		`nl=$(printf '\nx'); nl=${nl%%x}; printf '%%s\n' "$files" | while IFS= read -r f; do [ -n "$f" ] || continue; `+
		`l=$(command -p head -n 1 -- "$f" 2>/dev/null | command -p head -c %d; printf x); c=0; case $l in *'}'"$nl"x) c=1 ;; esac; l=${l%%x}; `+
		`case $l in *'"type":"session_meta"'*) m=$((2 - c)) ;; *) m=0 ;; esac; `+
		`i=$(printf '%%s' "$l" | command -p grep -o -e '"id":"[0-9a-f-]*"' | command -p head -n 1); `+
		`p=$(printf '%%s' "$l" | command -p grep -o -e '"parent_thread_id":"[0-9a-f-]*"' | command -p head -n 1); `+
		`b=${f##*/}; case $b in rollout-*.jsonl) u=${b#rollout-???????????????????-}; u=${u%%.jsonl}; u=${u%%%%_*} ;; *) u= ;; esac; `+
		`case $u in ''|*[!0-9a-f-]*) u=none ;; esac; `+
		`printf '%%s %%s %%s %%s\n' "$m" "${i:-none}" "${p:-none}" "$u"; done | command -p head -c %d`,
		codexChildRolloutsMarker, shellQuote(sessionsDir), codexChildUsageMaxBytes, codexChildUsageMaxBytes)
}

// codexRolloutHead is what the read printed for one rollout file: the first
// line's session_meta marker, whether that line was cut (not a whole record
// ending in } and a newline), its id and parent, and the thread id in the
// file's name.
type codexRolloutHead struct {
	Meta, Cut          bool
	ID, Parent, NameID string
}

// parseCodexRolloutHeads decodes the read's output. A line that is not four
// fields is a head with nothing usable, counted with no id; a name that
// carries no thread id has none.
func parseCodexRolloutHeads(data []byte) []codexRolloutHead {
	value := func(field, key string) string {
		v, ok := strings.CutPrefix(field, `"`+key+`":"`)
		if !ok {
			return ""
		}
		return strings.TrimSuffix(v, `"`)
	}
	var heads []codexRolloutHead
	for line := range bytes.Lines(data) {
		fields := strings.Fields(string(line))
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 4 {
			heads = append(heads, codexRolloutHead{})
			continue
		}
		head := codexRolloutHead{Meta: fields[0] == "1" || fields[0] == "2", Cut: fields[0] == "2", ID: value(fields[1], "id"), Parent: value(fields[2], "parent_thread_id")}
		if codexChildUsageThreadID.MatchString(fields[3]) {
			head.NameID = fields[3]
		}
		heads = append(heads, head)
	}
	return heads
}

// countCodexChildRollouts counts the iteration's children on disk, each
// thread once: a file whose session_meta parent is the primary thread or a
// child already counted (a grandchild that got past the guard), and a file
// whose first line does not show what it is: empty, cut before it names a
// parent, unreadable, a fragment a failed write left, or not a session_meta
// record. Only a whole session_meta line with no parent proves a root. A
// head without a usable id takes the thread id in its file name, so it
// dedupes against the stream's receivers; such a head under the primary's
// own name is the primary's file, not a child, and a head with no id at all
// counts on its own. A name says which thread a file is, not whose child, so
// a file known by its name is a parent in the walk only when the stream's
// receivers name it. A root and a file of another parent are not counted: a
// previous iteration's files left by a failed clear, or a root the agent's
// own codex exec wrote under the exported CODEX_HOME, unless its first line
// is empty or cut (read while it is written, left by a failed write, or 1 MiB
// or longer). The ids returned are the counted children's, for the usage fold
// and the union with the stream. With a primary id that is not a thread id,
// any file that could be a child is an error rather than a count of zero.
func countCodexChildRollouts(heads []codexRolloutHead, primary string, receivers []string) (int, []string, error) {
	byName := func(h codexRolloutHead) bool { return !h.Meta || (h.Cut && h.Parent == "") }
	if !codexChildUsageThreadID.MatchString(primary) {
		for _, h := range heads {
			if byName(h) || h.Parent != "" {
				return 0, nil, fmt.Errorf("the run's primary thread id is unknown, so its child rollouts cannot be told apart")
			}
		}
		return 0, nil, nil
	}
	parents := map[string]bool{primary: true}
	seen := map[string]bool{primary: true}
	counted := make([]bool, len(heads))
	idless := 0
	var ids []string
	add := func(id string, parent bool) {
		switch {
		case id == "":
			idless++
			return
		case !seen[id]:
			seen[id] = true
			ids = append(ids, id)
		}
		if parent {
			parents[id] = true
		}
	}
	for i, h := range heads {
		if !byName(h) {
			continue
		}
		counted[i] = true
		id := h.NameID
		if id == "" && codexChildUsageThreadID.MatchString(h.ID) {
			id = h.ID
		}
		add(id, slices.Contains(receivers, id))
	}
	for changed := true; changed; {
		changed = false
		for i, h := range heads {
			if counted[i] || h.Parent == "" || !parents[h.Parent] {
				continue
			}
			counted[i], changed = true, true
			id := h.ID
			if !codexChildUsageThreadID.MatchString(id) {
				id = h.NameID
			}
			if id == primary {
				id = ""
			}
			add(id, true)
		}
	}
	return len(ids) + idless, ids, nil
}

// readCodexChildRollouts runs the read and counts, given the iteration's
// stream receivers. Unlike the usage read, a read that fails, times out or
// is cut is an error: this is a policy check.
func (r CodexRuntime) readCodexChildRollouts(sandboxName, primary string, receivers []string) (int, []string, error) {
	stdout, stderr, exitCode, err := sandbox.Exec(sandboxName, codexChildRolloutsReadCommand(r.codexSessionsDir()), 10*time.Second)
	if err != nil {
		return 0, nil, err
	}
	if exitCode != 0 {
		return 0, nil, fmt.Errorf("exited %d: %s", exitCode, redactSummary(strings.TrimSpace(sanitizeOutput(stderr))))
	}
	if len(stdout) >= codexChildUsageMaxBytes {
		return 0, nil, fmt.Errorf("the read was cut at %d bytes", codexChildUsageMaxBytes)
	}
	return countCodexChildRollouts(parseCodexRolloutHeads([]byte(stdout)), primary, receivers)
}

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
// same thread), and for more spawns in the run than admissions. More
// admissions is not an error: codex can reject an admitted call before it
// emits an item, and another hook can block a call the guard admitted. No id
// is in the error: the stream comes out of the sandbox. At the pinned codex
// the stream carries the primary thread's items only, so the sender rule is
// defence in depth; a child's spawn is the guard's deny.
func compareCodexSpawnAdmissions(primary string, senders []string, completed, admitted int) error {
	for _, sender := range senders {
		if sender == "" || sender != primary {
			return fmt.Errorf("%s: a spawn was not sent by the primary thread", codexSpawnWithoutAdmission)
		}
	}
	if completed > admitted {
		return fmt.Errorf("%s: %d spawn(s) in this run (the stream's spawn items and the child rollouts on disk, each child counted once) but %d recorded admission(s)",
			codexSpawnWithoutAdmission, completed, admitted)
	}
	return nil
}

// checkCodexSpawnAdmissions reads the admissions and compares them with an
// iteration's spawn senders and the run's spawn count (completed). Unlike
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
