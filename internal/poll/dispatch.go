package poll

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"gopkg.in/yaml.v3"
)

// fullsendPipelineIncludePath is the committed GitLab pipeline wrapper
// dispatch() reads to confirm a target repository declares the typed
// pipeline-input contract. Keep in sync with fullsendPipelineInclude in
// internal/repos/gitlabci.go — internal/poll cannot import internal/repos
// (internal/repos already imports internal/poll), so this is a separate
// copy, like dispatchInputNames below and gitlabDispatchInputNames there.
const fullsendPipelineIncludePath = ".gitlab/ci/fullsend-pipeline.yml"

// firstTypedDispatchRelease is the first fullsend release whose GitLab
// scaffold carries the typed pipeline-input dispatch contract. Keep in sync
// with gitlabFirstTypedDispatchRelease in internal/repos/gitlabci.go (same
// import-cycle reason as fullsendPipelineIncludePath above).
const firstTypedDispatchRelease = "v0.46.0"

// resourceKey returns a stable entity-based key for concurrency control.
func resourceKey(event RoutableEvent) string {
	prefix := "issue"
	if strings.HasPrefix(event.Type, "mr_") {
		prefix = "mr"
	}
	return fmt.Sprintf("%s-%d", prefix, event.IID)
}

// dispatch builds an event payload, base64-encodes it, and creates a
// pipeline via the GitLab API using typed CI/CD pipeline inputs rather
// than user-defined pipeline variables (#7850): inputs are not governed
// by a project's ci_pipeline_variables_minimum_override_role, so the
// agent launch path keeps working with a Developer-level poller/
// dispatcher credential even when that setting is
// forge.PipelineVarOverrideNoOneAllowed. It logs a clickable URL to the
// created pipeline and appends a Dispatch record for tracking.
func (p *Poller) dispatch(ctx context.Context, owner, repo, stage string, event RoutableEvent, ne *dispatch.NormalizedEvent) error {
	payload, err := buildEventPayload(event, ne)
	if err != nil {
		return fmt.Errorf("build event payload: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	if budget := maxEventPayloadChunks * dispatchPayloadChunkSize; len(encoded) > budget {
		// Only a /fs-* command body is carried untruncated, so this
		// rejects a command that cannot be transported intact rather
		// than letting the agent run on a shortened instruction.
		return fmt.Errorf("event payload for %s/%s needs %d base64 characters, exceeding the %d-character dispatch budget; refusing to dispatch a truncated note body: %w", stage, resourceKey(event), len(encoded), budget, dispatch.ErrTransportBudget)
	}
	// Fork detection applies only to MR events. Issue events never
	// populate MRSource/MRTarget — they are "not applicable", not
	// "unknown". For MR events, fail-closed: unknown project IDs
	// default to fork so IS_FORK:-true is not overridden to "false".
	// Derive dispatch fields from event metadata.
	var isFork bool
	if strings.HasPrefix(event.Type, "mr_") {
		isFork = true
		if event.MRSource != 0 && event.MRTarget != 0 {
			isFork = event.MRSource != event.MRTarget
		}
	}
	var actorID int
	switch event.Type {
	case "issue_note", "mr_note", "mr_event", "issue_label", "issue_event":
		actorID = event.NoteAuthorID
	default:
		log.Printf("WARNING: unrecognized event type %q — no actor ID will be set", event.Type)
	}

	rk := resourceKey(event)

	variables := map[string]string{
		"STAGE":             stage,
		"EVENT_TYPE":        event.Type,
		"EVENT_PAYLOAD_B64": encoded,
		"RESOURCE_KEY":      rk,
		"IS_FORK":           strconv.FormatBool(isFork),
		"ORIGINATING_URL":   entityURL(p.gitlabURL, p.projectPath, event.Type, event.IID),
		"REPO_FULL_NAME":    p.projectPath,
	}
	if event.MRAuthorID != 0 {
		variables["MR_AUTHOR_ID"] = strconv.Itoa(event.MRAuthorID)
	}
	if actorID != 0 {
		variables["ACTOR_ID"] = strconv.Itoa(actorID)
	}
	if event.IID != 0 {
		variables["STATUS_IID"] = strconv.Itoa(event.IID)
	}
	if p.opts.PollJobURL != "" {
		variables[forge.VarPollJobURL] = p.opts.PollJobURL
	}

	if p.opts.DispatchSecret != "" {
		variables[forge.VarDispatchHMAC] = computeDispatchHMAC(p.opts.DispatchSecret, variables)
	} else if !p.warnedNoHMAC {
		p.warnedNoHMAC = true
		log.Printf("WARNING: FULLSEND_DISPATCH_SECRET not set — dispatch variables are unsigned; configure a protected, masked CI/CD variable to enable HMAC verification")
	}

	// Dispatch travels only through typed pipeline inputs, never
	// user-defined pipeline variables. Confirm the target repository's
	// committed contract at the dispatch ref declares them first: the
	// former variable-based dispatch path has been removed, so a
	// repository still on that contract (or with an incomplete one) is
	// refused rather than sent inputs its pipeline would not declare.
	typed, err := p.usesTypedDispatch(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("checking GitLab dispatch transport for %s/%s: %w", stage, rk, err)
	}
	if !typed {
		return fmt.Errorf("dispatch for %s/%s requires the typed pipeline-input contract in %s at ref %q; the legacy pipeline-variable dispatch path is no longer supported, so pin the repository's fullsend_ref to %s or later (or remove the pin) and rerun repos install", stage, rk, fullsendPipelineIncludePath, p.opts.PipelineRef, firstTypedDispatchRelease)
	}

	webURL, err := p.createAgentPipeline(ctx, owner, repo, stage, rk, variables)
	if err != nil {
		return err
	}

	entityPrefix := "#"
	if strings.HasPrefix(event.Type, "mr_") {
		entityPrefix = "!"
	}
	log.Printf("  → %s (%s %s%d, %s)", webURL, event.Type, entityPrefix, event.IID, stage)

	p.dispatches = append(p.dispatches, Dispatch{
		Stage:           stage,
		EventType:       event.Type,
		EventPayloadB64: encoded,
		ResourceKey:     rk,
		MRAuthorID:      event.MRAuthorID,
		ActorID:         actorID,
		IsFork:          isFork,
		IID:             event.IID,
	})
	return nil
}

// createAgentPipeline converts the signed dispatch variables into typed
// pipeline inputs and creates the agent pipeline at p.opts.PipelineRef on
// the selected route: the CI_JOB_TOKEN child-pipeline route when
// ChildDispatchJobToken is set, otherwise the Poller API route. It returns
// the created pipeline's web URL. Callers sign variables and confirm the
// typed pipeline-input contract first.
func (p *Poller) createAgentPipeline(ctx context.Context, owner, repo, stage, rk string, variables map[string]string) (string, error) {
	inputs, err := dispatchInputs(variables)
	if err != nil {
		return "", fmt.Errorf("build dispatch inputs for %s/%s: %w", stage, rk, err)
	}
	var webURL string
	if p.opts.ChildDispatchJobToken != "" {
		_, webURL, err = p.client.CreateChildPipelineWithInputs(ctx, owner, repo, p.opts.PipelineRef, p.opts.ChildDispatchJobToken, inputs)
	} else {
		_, webURL, err = p.client.CreatePipelineWithInputs(ctx, owner, repo, p.opts.PipelineRef, inputs)
	}
	if err != nil {
		return "", fmt.Errorf("create pipeline for %s/%s: %w", stage, rk, err)
	}
	return webURL, nil
}

// usesTypedDispatch reports whether the target repository's committed
// .gitlab/ci/fullsend-pipeline.yml wrapper declares the full pipeline-input
// dispatch contract. A missing wrapper (not found) does not declare it. It
// reads the wrapper at p.opts.PipelineRef — the same ref dispatch() creates
// the pipeline against — rather than the default branch, so detection and
// pipeline creation agree on which contract the target ref declares.
func (p *Poller) usesTypedDispatch(ctx context.Context, owner, repo string) (bool, error) {
	content, err := p.client.GetFileContentAtRef(ctx, owner, repo, fullsendPipelineIncludePath, p.opts.PipelineRef)
	if err != nil {
		if forge.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return gitlabWrapperDeclaresTypedDispatch(content), nil
}

// gitlabWrapperDeclaresTypedDispatch reports whether content's spec:inputs
// header (the first of the wrapper's two YAML documents) declares every
// input name dispatchInputNames and dispatchEventPayloadChunkInputName
// require. Mirrors gitlabWrapperHasDispatchInputs in
// internal/repos/gitlabci.go — see fullsendPipelineIncludePath for why this
// is a separate copy rather than a shared call.
func gitlabWrapperDeclaresTypedDispatch(content []byte) bool {
	var header struct {
		Spec struct {
			Inputs map[string]any `yaml:"inputs"`
		} `yaml:"spec"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(content)).Decode(&header); err != nil {
		return false
	}
	if header.Spec.Inputs == nil {
		return false
	}
	for _, inputName := range dispatchInputNames {
		if _, ok := header.Spec.Inputs[inputName]; !ok {
			return false
		}
	}
	for i := 0; i < maxEventPayloadChunks; i++ {
		if _, ok := header.Spec.Inputs[dispatchEventPayloadChunkInputName(i)]; !ok {
			return false
		}
	}
	return true
}

// dispatchInputNames maps each signed dispatch field (the name
// run-agent-job.sh and computeDispatchHMAC use) to the lowercase GitLab
// CI/CD pipeline input identifier fullsend-agent.yml declares for it
// (spec:inputs). EVENT_PAYLOAD_B64 is handled separately by
// eventPayloadChunks because the payload is split across the fixed
// event_payload_chunk_NN inputs, each capped at dispatchPayloadChunkSize
// base64 characters by the scaffold's own chunk-input pattern — a base64
// event payload with a long note body is far larger than any one chunk.
var dispatchInputNames = map[string]string{
	"STAGE":               "stage",
	"EVENT_TYPE":          "event_type",
	"RESOURCE_KEY":        "resource_key",
	"IS_FORK":             "is_fork",
	"ORIGINATING_URL":     "originating_url",
	"REPO_FULL_NAME":      "repo_full_name",
	"MR_AUTHOR_ID":        "mr_author_id",
	"ACTOR_ID":            "actor_id",
	"STATUS_IID":          "status_iid",
	forge.VarPollJobURL:   "poll_job_url",
	forge.VarDispatchHMAC: "dispatch_hmac",
}

// dispatchEventPayloadChunkInputName returns the name of the i-th
// (0-indexed) fixed scalar pipeline input fullsend-agent.yml declares
// for the chunked event payload — see eventPayloadChunks.
func dispatchEventPayloadChunkInputName(i int) string {
	return fmt.Sprintf("event_payload_chunk_%02d", i)
}

// dispatchInputs converts the signed dispatch variables into typed
// GitLab CI/CD pipeline inputs instead of user-defined pipeline
// variables (#7850). Pipeline inputs are not governed by a project's
// ci_pipeline_variables_minimum_override_role, so the agent launch path
// keeps working on a Developer-level poller/dispatcher credential even
// when that setting is forge.PipelineVarOverrideNoOneAllowed. A field
// absent from variables (e.g. an unset optional ID) is simply omitted
// from inputs; fullsend-agent.yml declares an empty-string default for
// each one, matching the empty value run-agent-job.sh already treats a
// missing variable as. The HMAC in
// variables[forge.VarDispatchHMAC] was computed over the un-chunked
// EVENT_PAYLOAD_B64 value, so splitting that value for transport does
// not change what is authenticated — fullsend-agent.yml reconstructs
// the exact original string before run-agent-job.sh verifies it.
func dispatchInputs(variables map[string]string) (map[string]forge.PipelineInputValue, error) {
	inputs := make(map[string]forge.PipelineInputValue, len(dispatchInputNames)+maxEventPayloadChunks)
	for key, inputName := range dispatchInputNames {
		v, ok := variables[key]
		if !ok {
			continue
		}
		inputs[inputName] = forge.StringInput(v)
	}
	chunks, err := eventPayloadChunks(variables["EVENT_PAYLOAD_B64"])
	if err != nil {
		return nil, err
	}
	for i, chunk := range chunks {
		inputs[dispatchEventPayloadChunkInputName(i)] = forge.StringInput(chunk)
	}
	return inputs, nil
}

const (
	// dispatchPayloadFile is the path fullsend-agent.yml's before_script
	// reconstructs the chunked event payload into, before run-agent-job.sh
	// reads EVENT_PAYLOAD_B64 from the environment. Keep in sync with
	// internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-agent.yml.
	dispatchPayloadFile = "/tmp/.fs-dispatch-payload.b64"
	// dispatchPayloadChunkSize is the max size, in base64 characters, of
	// each event_payload_chunk_NN pipeline-input value. Kept at 1000 to
	// fit the chunk-input pattern the scaffold's own configuration
	// enforces (regex: '^[A-Za-z0-9+/=]{0,1000}$' on each
	// event_payload_chunk_NN input) — the bound this repository
	// controls; GitLab's documented input limits are far larger (a
	// string that uses an input must be less than 1 MB,
	// https://docs.gitlab.com/ci/inputs/).
	dispatchPayloadChunkSize = 1000
	// maxEventPayloadChunks is the number of event_payload_chunk_NN
	// scalar inputs fullsend-agent.yml declares. GitLab limits a
	// pipeline contract to 20 inputs (ADR 0131;
	// https://docs.gitlab.com/ci/inputs/), and the configuration
	// declares the 11 other named dispatch fields (dispatchInputNames)
	// plus these 9 chunks, using every root input GitLab allows — so a
	// 10th chunk (event_payload_chunk_09) would be the pipeline's 21st
	// declared input, a shape GitLab rejects and repos install's
	// dispatch gate refuses rather than growing
	// (internal/repos/gitlabci.go), independently of how many chunks
	// any given dispatch actually fills in. This
	// bounds the largest EVENT_PAYLOAD_B64 dispatch() can
	// transport to maxEventPayloadChunks*dispatchPayloadChunkSize base64
	// characters, which is why buildEventPayload truncates a non-command
	// note body to a smaller bound than before this limit was enforced
	// (see noteBodyTransportLimit): a 270-rune note body, after JSON
	// escaping (Go's encoder expands a run of HTML-sensitive runes like
	// '<' up to 6x) and base64 encoding (4/3x), still leaves margin for
	// the payload's other fields within the 9-chunk budget. A /fs-*
	// command body is never truncated — run-agent-job.sh derives the
	// human instruction from it — so dispatch() fails closed with an
	// error rather than silently dropping data if a payload needs more.
	maxEventPayloadChunks = 9
)

// eventPayloadChunks splits a base64 event payload into fixed,
// individually-declared pipeline-input-sized pieces of plain data — not
// executable code (#7850's injection-vuln fix). Earlier, the chunks were
// shell statements inside a single array-typed pipeline input that
// fullsend-agent.yml's before_script interpolated directly as script
// lines: because any caller with API access to trigger a pipeline can
// supply pipeline-input values directly — bypassing dispatch() and its
// trusted templating entirely — that let an attacker substitute arbitrary
// commands for the poller's own printf statements, which then ran, as
// the runner, before run-agent-job.sh's identity/pipeline-creator/HMAC
// checks ever executed. Each element returned here is instead bound to
// its own event_payload_chunk_NN input, which fullsend-agent.yml bridges
// into a same-named job variable like any other scalar input — a value,
// never script text — and a fixed, caller-independent before_script
// sequence (not generated per element) reassembles them. An error is
// returned, rather than truncating silently, if payloadB64 needs more
// than maxEventPayloadChunks pieces.
func eventPayloadChunks(payloadB64 string) ([]string, error) {
	var chunks []string
	for i := 0; i < len(payloadB64); i += dispatchPayloadChunkSize {
		end := min(i+dispatchPayloadChunkSize, len(payloadB64))
		chunks = append(chunks, payloadB64[i:end])
	}
	if len(chunks) > maxEventPayloadChunks {
		return nil, fmt.Errorf("event payload needs %d chunks, exceeding the %d fullsend-agent.yml declares", len(chunks), maxEventPayloadChunks)
	}
	return chunks, nil
}

// signedDispatchKeys lists the pipeline variables included in the
// HMAC signature, in sorted order. Both the Go poller and the shell
// verifier in run-agent-job.sh must use the same key list and order.
var signedDispatchKeys = []string{
	"ACTOR_ID",
	"EVENT_PAYLOAD_B64",
	"EVENT_TYPE",
	forge.VarPollJobURL,
	"IS_FORK",
	"MR_AUTHOR_ID",
	"ORIGINATING_URL",
	"REPO_FULL_NAME",
	"RESOURCE_KEY",
	"STAGE",
	"STATUS_IID",
}

// computeDispatchHMAC computes an HMAC-SHA256 over a canonical
// representation of the dispatch variables. The canonical format is
// key=value pairs joined by newlines, with keys in sorted order.
// Missing keys use an empty string value.
func computeDispatchHMAC(secret string, variables map[string]string) string {
	parts := make([]string, len(signedDispatchKeys))
	for i, k := range signedDispatchKeys {
		parts[i] = k + "=" + variables[k]
	}
	message := strings.Join(parts, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// noteBodyTruncationNotice is appended by truncateNoteBody when it
// actually shortens a note body. run-agent-job.sh reads retro comments
// directly from the dispatched comment body (see buildEventPayload);
// without an in-band indicator, a note cut off at the dispatch limit is
// indistinguishable from one that was always that short.
const noteBodyTruncationNotice = " […truncated]"

// noteBodyTransportLimit is the rune bound buildEventPayload applies to a
// note body that is not a /fs-* command. 270, not the 4096 convert.go uses
// for display truncation: this payload travels as event_payload_chunk_NN
// pipeline inputs (see maxEventPayloadChunks), which the dispatch
// configuration's 20-input shape caps at 9 chunks of
// dispatchPayloadChunkSize base64 characters — far less room than a bare
// 4096-byte note body's worst-case
// JSON-escaped, base64-encoded size would need. Lowered from the 800 of
// earlier releases as the budget became the sum of every independently
// bounded attacker-controlled field; see transportStringRefLimit for the
// attacker-chosen-fact rationale and the worst-case budget pin.
const noteBodyTransportLimit = 270

// transportStringRefLimit is the JSON-byte bound transport applies to each
// attacker-shaped single string the payload carries: the branch refs (the
// flat source_branch/target_branch and the normalized event's
// head_ref/base_ref), the repo paths head_repo/base_repo carry (a fork's
// path is event-author-chosen; a same-project MR carries the operator's
// own project path there, which this bound cuts even though repo travels
// whole), the
// logins (actor.id, author_id, reviewer_id) and the label name. The
// config-derived repo, entity.url and entity.linked_change_proposal.url
// are operator-chosen, not attacker-shaped, and travel whole: a config
// whose lengths push the complete payload past the chunk budget fails
// closed there (comment-only fallback for a note, command refusal) rather
// than being silently cut. An event author who can name a branch, a label or a login controls
// that string's content, and Go's JSON encoder expands <, > and & to six
// bytes per rune, so without a bound a single 255-rune escape-heavy ref
// would push the complete normalized event past
// maxEventPayloadChunks*dispatchPayloadChunkSize and force
// buildEventPayload's comment-only fallback — making "no event", and with
// it which overlays match, an attacker-chosen fact (ADR 0112 Decision 2
// trusts event absence: the fallback run gets no event file, so its CEL
// event variable is an empty map and event-keyed overlays stop matching
// while eventless ones such as !has(event.source) start — the run never
// binds event as CEL null). Like the labels bound, the truncation
// is unconditional and a documented behavior change: a string whose JSON
// form exceeds the bound travels as the longest rune-aligned prefix that
// fits, so an overlay keyed on the dropped tail no longer matches there.
// A plain ASCII branch name always fits: the normalized-event schema caps
// head_ref/base_ref at 255 characters, which costs 257 JSON bytes with the
// quotes. A plain non-ASCII name within that ceiling can still exceed the
// bound (a 130-rune CJK ref costs about 392 JSON bytes), and so can any
// escape-heavy or oversized string; those are cut. Forge-assigned ids
// (comment id/parent_id, head SHA, entity key) are not bounded — the
// forge, not the event author, chooses them. Together with
// noteBodyTransportLimit and dispatch.TransportLabelsLimit this keeps the worst
// attacker-shaped payload — at a plausible operator config for the
// whole-travelling config-derived fields — inside the transport budget,
// which TestBuildEventPayload_WorstCaseFitsTransportBudget pins.
const transportStringRefLimit = 258

// isFullsendCommandBody reports whether body is a /fs-* slash command,
// matching the poller's slash-command detection (isSlashCommandEvent).
func isFullsendCommandBody(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), "/fs-")
}

// truncateNoteBody truncates s to at most maxLen runes, appending
// noteBodyTruncationNotice (counted against maxLen) when s actually had to
// be cut. Unlike the shared truncate helper — used where silent truncation
// is acceptable (e.g. convert.go's display-only 4096-character bound) —
// this is used only for dispatch-transport comment bodies (see
// buildEventPayload and encodeJiraEventPayload), where a consumer reads
// the truncated text and needs to know it may be incomplete.
func truncateNoteBody(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	notice := []rune(noteBodyTruncationNotice)
	keep := maxLen - len(notice)
	if keep < 0 {
		keep = 0
	}
	return string(r[:keep]) + string(notice)
}

// buildEventPayload creates a JSON payload from a RoutableEvent,
// including only non-zero/non-empty optional fields.
//
// A note body travels only at the canonical normalized comment path,
// _normalized_event.transition.comment.body, which run-agent-job.sh reads
// for /fs-fix instructions and retro comments. Both the scheduled poller
// and the webhook dispatcher reach this function with the body they read
// from the GitLab notes API (the webhook path re-fetches the note after
// validating its ID and actor), never the raw webhook note text. A /fs-*
// command body is carried intact; dispatch() rejects a command whose
// payload does not fit the transport budget instead of truncating it.
// Any other body (for example a bot review note) is bounded to
// noteBodyTransportLimit runes, with noteBodyTruncationNotice appended
// when it is actually cut.
//
// When ne is the event's normalized form and passes fullsend run's
// validation, the payload carries it whole as _normalized_event (#9014), so
// run-agent-job.sh can hand it to fullsend run as --event-file the way the
// Jira path does. If it is missing or invalid, the payload falls back to the
// comment-only _normalized_event, so such a dispatch behaves as it did
// before. A complete form that would not fit the transport budget falls back
// the same way — unless the note body is a /fs-* command, whose payload is
// carried untruncated and so cannot shrink to fit: that dispatch fails
// closed with an error instead, never on a partial event (see below).
func buildEventPayload(event RoutableEvent, ne *dispatch.NormalizedEvent) ([]byte, error) {
	m := map[string]interface{}{
		"type":       event.Type,
		"iid":        event.IID,
		"updated_at": event.UpdatedAt.Format(time.RFC3339),
	}
	body := event.NoteBody
	if body != "" && !isFullsendCommandBody(body) {
		body = truncateNoteBody(body, noteBodyTransportLimit)
	}
	// where names the event for the transport-bound cut WARNINGs below
	// and inside transportNormalizedEvent (logBoundTransportString /
	// dispatch.LogBoundTransportLabels).
	where := fmt.Sprintf("%s event on IID %d", event.Type, event.IID)
	if body != "" {
		m["_normalized_event"] = map[string]any{
			"transition": normevent.Transition{
				Kind:    normevent.TransitionCommentAdded,
				Comment: &normevent.Comment{Body: body},
			},
		}
	}
	if event.NoteID != 0 {
		m["note_id"] = event.NoteID
	}
	if event.NoteAuthorID != 0 {
		m["note_author_id"] = event.NoteAuthorID
	}
	if event.Labels != nil {
		// The raw slice is stored here and bounded only on the fallback
		// path (the return below): the complete form deletes this copy
		// in favour of _normalized_event.state.labels, so bounding it up
		// front would be discarded work there. In the comment-only
		// fallback this outer copy is the payload's only labels, and an
		// unbounded one would reopen the very size channel into "no
		// event" that dispatch.TransportLabelsLimit closes.
		m["labels"] = event.Labels
	}
	if event.MRSource != 0 {
		m["mr_source_project_id"] = event.MRSource
	}
	if event.MRTarget != 0 {
		m["mr_target_project_id"] = event.MRTarget
	}
	if event.SourceBranch != "" {
		m["source_branch"] = logBoundTransportString(where, "source_branch", event.SourceBranch)
	}
	if event.TargetBranch != "" {
		m["target_branch"] = logBoundTransportString(where, "target_branch", event.TargetBranch)
	}
	if event.MRAuthorID != 0 {
		m["mr_author_id"] = event.MRAuthorID
	}
	if event.IsBot {
		m["is_bot"] = true
	}
	if event.Action != "" {
		m["action"] = event.Action
	}
	norm, err := transportNormalizedEvent(where, ne, body)
	switch {
	case err != nil:
		log.Printf("WARNING: %s event on IID %d: sending the comment-only normalized event; the complete event is invalid: %v", event.Type, event.IID, err)
	case norm != nil:
		full := maps.Clone(m)
		// The complete form carries state.labels inside _normalized_event
		// (bounded above), and run-agent-job.sh reads labels only from
		// there, so the payload's flat labels copy is dropped rather than
		// paid for a second time against the transport budget.
		delete(full, "labels")
		full["_normalized_event"] = norm
		data, err := json.Marshal(full)
		if err != nil {
			return nil, err
		}
		budget := maxEventPayloadChunks * dispatchPayloadChunkSize
		n := base64.StdEncoding.EncodedLen(len(data))
		if n <= budget {
			return data, nil
		}
		if isFullsendCommandBody(body) {
			// A /fs-* command body is carried untruncated, so its
			// payload cannot shrink to fit: fail closed here, the
			// way dispatch() refuses an untransportable command,
			// instead of falling back to the comment-only normalized
			// event — a partial _normalized_event the run would
			// reject, leaving the run with an empty event map (no
			// --event-file), so event-keyed overlays stop matching
			// and eventless ones such as !has(event.source) match —
			// an attacker-chosen routing fact (ADR 0112 Decision 2).
			return nil, fmt.Errorf("event payload on IID %d needs %d base64 characters, exceeding the %d-character dispatch budget; refusing to dispatch a command note without its complete event: %w", event.IID, n, budget, dispatch.ErrTransportBudget)
		}
		log.Printf("WARNING: %s event on IID %d: sending the comment-only normalized event; the complete event needs %d base64 characters, over the %d-character budget", event.Type, event.IID, n, budget)
	}
	if event.Labels != nil {
		// The fallback path only, so the complete form above never pays
		// for the bound. The cut logs only when this copy is what ships
		// (no normalized event); otherwise it stays silent — with a
		// normalized event the state.labels cut inside
		// transportNormalizedEvent already logged the one WARNING for
		// the same slice, so a second line would describe a copy that
		// never ships.
		if ne == nil {
			m["labels"] = dispatch.LogBoundTransportLabels(where, event.Labels)
		} else {
			m["labels"] = dispatch.BoundTransportLabels(event.Labels)
		}
	}
	return json.Marshal(m)
}

// boundTransportString returns s unchanged when its JSON serialization fits
// transportStringRefLimit, and otherwise the longest rune-aligned prefix
// whose JSON serialization does. The bound is measured on the JSON form
// because that is what the transport budget pays: an escape-heavy string
// costs up to six bytes per rune where a plain one costs one.
func boundTransportString(s string) string {
	size := 2 // the quotes
	for i, r := range s {
		size += jsonRuneLen(r)
		if size > transportStringRefLimit {
			return s[:i]
		}
	}
	return s
}

// jsonRuneLen is an upper bound on the bytes Go's JSON encoder writes for r
// inside a string: <, >, & and U+2028/U+2029 escape to six-byte \u forms,
// ASCII control characters, the quote and the backslash to two- or six-byte
// escapes, and every other rune travels as its UTF-8 encoding (at most four
// bytes). A byte that is not valid UTF-8 decodes as U+FFFD over the range
// loop, and the encoder writes it as the six-byte \ufffd escape, so
// utf8.RuneError estimates six: a genuinely encoded U+FFFD costs only its
// three UTF-8 bytes there, which over-estimates. Estimating high is safe:
// the bound may cut a string slightly shorter than strictly needed, never
// longer.
func jsonRuneLen(r rune) int {
	switch {
	case r == '<' || r == '>' || r == '&':
		return 6
	case r == utf8.RuneError:
		return 6
	case r >= 0x20 && r != '"' && r != '\\' && r != '\u2028' && r != '\u2029':
		return utf8.RuneLen(r)
	default:
		return 6
	}
}

// boundTransportEventStrings bounds every attacker-shaped single-string
// field of a transported normalized event to transportStringRefLimit JSON
// bytes (see boundTransportString): branch refs, the head_repo/base_repo
// paths (cut even when a same-project MR carries the operator's own
// project path there), logins
// and the label name. The config-derived repo, entity.url and
// entity.linked_change_proposal.url travel whole (see
// transportStringRefLimit). Forge-assigned ids (comment id/parent_id, head
// SHA, entity key) are left alone — the forge, not the event author, chooses
// them, and cutting an id would corrupt reply threading rather than trim
// attacker padding. The cuts are silent; dispatch routes its events through
// the logging variant below so an operator sees each one. This silent
// variant has no production caller — the drift-guard test in
// dispatch_normalized_test.go calls it directly, so it stays.
func boundTransportEventStrings(ev *dispatch.NormalizedEvent) {
	boundEventStrings(ev, func(_, s string) string { return boundTransportString(s) })
}

// logBoundTransportEventStrings bounds ev exactly like
// boundTransportEventStrings and routes every cut through
// logBoundTransportString, so each field the bound actually cut leaves the
// same operator WARNING the flat payload fields do: a trigger keyed on
// a cut field's tail stops matching, and the WARNING is the only signal.
func logBoundTransportEventStrings(where string, ev *dispatch.NormalizedEvent) {
	boundEventStrings(ev, func(field, s string) string { return logBoundTransportString(where, field, s) })
}

// boundEventStrings is the shared core of boundTransportEventStrings and its
// logging variant: cut bounds each attacker-shaped single-string field,
// receiving the field's name for the WARNING the logging variant logs. The
// config-derived repo, entity.url and entity.linked_change_proposal.url
// stay whole — the operator's config, not the event author, picks them, and
// a config that pushes the payload past the chunk budget fails closed there
// (see transportStringRefLimit). head_repo/base_repo are the exception in
// the other direction: they are cut unconditionally, so a same-project MR
// (whose head_repo/base_repo carry the operator's own project path, the
// same value repo carries) sees that path cut there while repo travels
// whole.
func boundEventStrings(ev *dispatch.NormalizedEvent, cut func(field, s string) string) {
	if label := ev.Transition.Label; label != nil {
		bounded := *label
		bounded.Name = cut("transition.label.name", bounded.Name)
		ev.Transition.Label = &bounded
	}
	if review := ev.Transition.Review; review != nil {
		bounded := *review
		bounded.ReviewerID = cut("transition.review.reviewer_id", bounded.ReviewerID)
		ev.Transition.Review = &bounded
	}
	ev.Actor.ID = cut("actor.id", ev.Actor.ID)
	if cp := ev.State.ChangeProposal; cp != nil {
		bounded := *cp
		bounded.HeadRepo = cut("state.change_proposal.head_repo", bounded.HeadRepo)
		bounded.BaseRepo = cut("state.change_proposal.base_repo", bounded.BaseRepo)
		bounded.HeadRef = cut("state.change_proposal.head_ref", bounded.HeadRef)
		bounded.BaseRef = cut("state.change_proposal.base_ref", bounded.BaseRef)
		bounded.AuthorID = cut("state.change_proposal.author_id", bounded.AuthorID)
		ev.State.ChangeProposal = &bounded
	}
}

// logBoundTransportString bounds s like boundTransportString and logs a
// one-line WARNING when the bound actually cut it: the cut is a documented
// behavior change, and the WARNING is the operator signal that a value's
// tail — and with it any overlay or trigger keyed on that tail — was
// dropped in transport.
func logBoundTransportString(where, field, s string) string {
	bounded := boundTransportString(s)
	if bounded != s {
		log.Printf("WARNING: %s: %s cut to the transport bound (%d of %d bytes)", where, field, len(bounded), len(s))
	}
	return bounded
}

// transportNormalizedEvent returns ne as a payload's _normalized_event map,
// with its comment body replaced by the transport-bounded body and the
// command and instruction re-derived from that body, so they always agree
// with what run-agent-job.sh reads, its state.labels bounded by
// dispatch.LogBoundTransportLabels and its attacker-shaped single strings bounded by
// logBoundTransportEventStrings, so every cut leaves the operator WARNING
// the flat payload fields do. It returns nil and no error when ne is nil,
// and an error when ne would fail normevent.ParseJSON — the check fullsend
// run applies to --event-file — so run-agent-job.sh never hands run an
// event it would refuse.
func transportNormalizedEvent(where string, ne *dispatch.NormalizedEvent, body string) (map[string]any, error) {
	if ne == nil {
		return nil, nil
	}
	ev := *ne
	ev.State.Labels = dispatch.LogBoundTransportLabels(where, ev.State.Labels)
	logBoundTransportEventStrings(where, &ev)
	if ev.Transition.Comment != nil {
		c := *ev.Transition.Comment
		c.Body = body
		c.Command, c.Instruction = extractCommand(body)
		ev.Transition.Comment = &c
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	parsed, err := normevent.ParseJSON(data)
	if err != nil {
		return nil, err
	}
	return parsed.ToMap()
}
