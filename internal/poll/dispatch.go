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
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"gopkg.in/yaml.v3"
)

// fullsendPipelineIncludePath is the committed GitLab pipeline wrapper
// dispatch() reads to decide which create-pipeline transport a target
// repository supports. Keep in sync with fullsendPipelineInclude in
// internal/repos/gitlabci.go — internal/poll cannot import internal/repos
// (internal/repos already imports internal/poll), so this is a separate
// copy, like dispatchInputNames below and gitlabDispatchInputNames there.
const fullsendPipelineIncludePath = ".gitlab/ci/fullsend-pipeline.yml"

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
func (p *Poller) dispatch(ctx context.Context, owner, repo, stage string, event RoutableEvent) error {
	payload, err := buildEventPayload(event)
	if err != nil {
		return fmt.Errorf("build event payload: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
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

	// Select the create-pipeline transport from the target repository's
	// own committed contract, not from this binary's feature set: the
	// CLI installer this poll job's wrapper script builds fetches and
	// rebuilds a non-pinned ref (default upstream main) on every run, so
	// an upgraded poller binary can execute against a repository whose
	// root/wrapper has not migrated off the variable-based contract.
	// Submitting typed pipeline inputs there would not reach the agent
	// job instead of dispatching through the contract it actually
	// declares (#7850).
	typed, err := p.usesTypedDispatch(ctx, owner, repo)
	if err != nil {
		return fmt.Errorf("checking GitLab dispatch transport for %s/%s: %w", stage, rk, err)
	}

	var webURL string
	if typed {
		inputs, inputsErr := dispatchInputs(variables)
		if inputsErr != nil {
			return fmt.Errorf("build dispatch inputs for %s/%s: %w", stage, rk, inputsErr)
		}
		_, webURL, err = p.client.CreatePipelineWithInputs(ctx, owner, repo, p.opts.PipelineRef, inputs)
	} else {
		_, webURL, err = p.client.CreatePipeline(ctx, owner, repo, p.opts.PipelineRef, variables)
	}
	if err != nil {
		return fmt.Errorf("create pipeline for %s/%s: %w", stage, rk, err)
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

// usesTypedDispatch reports whether the target repository's committed
// .gitlab/ci/fullsend-pipeline.yml wrapper declares the full pipeline-input
// dispatch contract. A missing wrapper (not found) is treated as the
// legacy, variable-based contract, matching the pre-#7850 transport. It
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
// eventPayloadChunks because a single pipeline-input string value
// is capped around 1 KB (https://docs.gitlab.com/ci/inputs/) — too small
// for a base64 event payload with a long note body.
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
	// each event_payload_chunk_NN pipeline-input value. Kept safely under
	// GitLab's ~1 KB per-pipeline-input-string-value limit
	// (https://docs.gitlab.com/ci/inputs/).
	dispatchPayloadChunkSize = 1000
	// maxEventPayloadChunks is the number of event_payload_chunk_NN
	// scalar inputs fullsend-agent.yml declares. GitLab caps the number
	// of pipeline inputs a single configuration can declare at 20; the 11
	// other named dispatch fields (dispatchInputNames) already consume 11
	// of that budget, leaving at most 9 for chunks — a 10th
	// (event_payload_chunk_09) would be the pipeline's 21st declared
	// input and GitLab would reject the configuration outright,
	// independently of how many chunks any given dispatch actually
	// fills in. This bounds the largest EVENT_PAYLOAD_B64 dispatch() can
	// transport to maxEventPayloadChunks*dispatchPayloadChunkSize base64
	// characters, which is why buildEventPayload truncates the note body
	// to a smaller bound than before this limit was enforced (see its
	// truncate call): a truncate(event.NoteBody, 800) note body, after
	// JSON escaping (Go's encoder expands a run of HTML-sensitive runes
	// like '<' up to 6x) and base64 encoding (4/3x), still leaves margin
	// for the payload's other fields within the 9-chunk budget.
	// dispatch() fails closed with an error rather than silently
	// dropping data if a payload ever needs more.
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
// actually shortens a note body. run-agent-job.sh derives /fs-fix
// instructions and retro comments directly from the dispatched note_body
// (see buildEventPayload); without an in-band indicator, a note cut off
// at the 800-character dispatch limit is indistinguishable from one that
// was always that short, and the consumer silently acts on a shortened
// instruction with no way to detect the loss.
const noteBodyTruncationNotice = " […truncated]"

// truncateNoteBody truncates s to at most maxLen runes, appending
// noteBodyTruncationNotice (counted against maxLen) when s actually had to
// be cut. Unlike the shared truncate helper — used where silent truncation
// is acceptable (e.g. convert.go's display-only 4096-character bound) —
// this is used only for the dispatch-transport note_body (see
// buildEventPayload), where a consumer parses the truncated text as an
// instruction and needs to know it may be incomplete.
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
func buildEventPayload(event RoutableEvent) ([]byte, error) {
	m := map[string]interface{}{
		"type":       event.Type,
		"iid":        event.IID,
		"updated_at": event.UpdatedAt.Format(time.RFC3339),
	}
	if event.NoteBody != "" {
		// 800, not the 4096 convert.go uses for display truncation:
		// this payload travels as event_payload_chunk_NN pipeline
		// inputs (see maxEventPayloadChunks), which GitLab's 20-input
		// ceiling caps at 9 chunks of dispatchPayloadChunkSize base64
		// characters — far less room than a bare 4096-byte note body's
		// worst-case JSON-escaped, base64-encoded size would need.
		// truncateNoteBody (not the shared truncate helper) appends
		// noteBodyTruncationNotice when it actually cuts the body, so a
		// consumer deriving a /fs-fix instruction or retro comment from
		// this field (run-agent-job.sh) can tell the instruction was
		// shortened instead of silently acting on a truncated one.
		m["note_body"] = truncateNoteBody(event.NoteBody, 800)
	}
	if event.NoteID != 0 {
		m["note_id"] = event.NoteID
	}
	if event.NoteAuthorID != 0 {
		m["note_author_id"] = event.NoteAuthorID
	}
	if event.Labels != nil {
		m["labels"] = event.Labels
	}
	if event.MRSource != 0 {
		m["mr_source_project_id"] = event.MRSource
	}
	if event.MRTarget != 0 {
		m["mr_target_project_id"] = event.MRTarget
	}
	if event.SourceBranch != "" {
		m["source_branch"] = event.SourceBranch
	}
	if event.TargetBranch != "" {
		m["target_branch"] = event.TargetBranch
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
	return json.Marshal(m)
}
