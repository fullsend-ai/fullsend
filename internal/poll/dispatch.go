package poll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var validStage = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var validYAMLField = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// appendDispatch appends a dispatch record to the poller's in-memory list.
func (p *Poller) appendDispatch(d Dispatch) error {
	p.dispatches = append(p.dispatches, d)
	return nil
}

// writeDispatches marshals the accumulated dispatches as a JSON array
// and writes them to the given file path.
func (p *Poller) writeDispatches(path string) error {
	dispatches := p.dispatches
	if len(dispatches) == 0 {
		return os.WriteFile(path, []byte("[]\n"), 0o644)
	}
	data, err := json.MarshalIndent(dispatches, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dispatches: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// resourceKey returns a stable entity-based key for concurrency control.
func resourceKey(event RoutableEvent) string {
	prefix := "issue"
	if strings.HasPrefix(event.Type, "mr_") {
		prefix = "mr"
	}
	return fmt.Sprintf("%s-%d", prefix, event.IID)
}

// dispatch builds an event payload, base64-encodes it, and appends
// a Dispatch record for child pipeline generation.
func (p *Poller) dispatch(ctx context.Context, owner, repo, stage string, event RoutableEvent) error {
	_ = ctx   // reserved for future use
	_ = owner // included in signature for routing context
	_ = repo
	payload, err := buildEventPayload(event)
	if err != nil {
		return fmt.Errorf("build event payload: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	return p.appendDispatch(Dispatch{
		Stage:           stage,
		EventType:       event.Type,
		EventPayloadB64: encoded,
		ResourceKey:     resourceKey(event),
	})
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
		m["note_body"] = truncate(event.NoteBody, 4096)
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
	return json.Marshal(m)
}

// generateChildPipelineYAML produces GitLab CI YAML that triggers
// one child pipeline job per dispatch record. Returns an error if
// any stage name contains invalid characters.
func generateChildPipelineYAML(dispatches []Dispatch) (string, error) {
	var buf bytes.Buffer
	for i, d := range dispatches {
		if !validStage.MatchString(d.Stage) {
			return "", fmt.Errorf("invalid stage name %q: must match %s", d.Stage, validStage.String())
		}
		if !validYAMLField.MatchString(d.EventType) {
			return "", fmt.Errorf("invalid event type %q: must match %s", d.EventType, validYAMLField.String())
		}
		if !validYAMLField.MatchString(d.ResourceKey) {
			return "", fmt.Errorf("invalid resource key %q: must match %s", d.ResourceKey, validYAMLField.String())
		}
		fmt.Fprintf(&buf, "agent-%d:\n", i)
		fmt.Fprintf(&buf, "  trigger:\n")
		fmt.Fprintf(&buf, "    include: .gitlab/ci/fullsend-%s.yml\n", d.Stage)
		fmt.Fprintf(&buf, "    strategy: depend\n")
		fmt.Fprintf(&buf, "  variables:\n")
		fmt.Fprintf(&buf, "    STAGE: %q\n", d.Stage)
		fmt.Fprintf(&buf, "    EVENT_TYPE: %q\n", d.EventType)
		fmt.Fprintf(&buf, "    EVENT_PAYLOAD_B64: %q\n", d.EventPayloadB64)
		fmt.Fprintf(&buf, "    RESOURCE_KEY: %q\n", d.ResourceKey)
		fmt.Fprintf(&buf, "  rules:\n")
		fmt.Fprintf(&buf, "    - when: always\n")
	}
	return buf.String(), nil
}

// GenerateChildPipelineFromFile reads a dispatches JSON file and
// writes the corresponding child pipeline YAML to outputPath.
// It is used by the CLI "fullsend poll generate-child-pipeline" subcommand.
func GenerateChildPipelineFromFile(dispatchesPath, outputPath string) error {
	data, err := os.ReadFile(dispatchesPath)
	if err != nil {
		return fmt.Errorf("read dispatches file: %w", err)
	}
	var dispatches []Dispatch
	if err := json.Unmarshal(data, &dispatches); err != nil {
		return fmt.Errorf("unmarshal dispatches: %w", err)
	}
	yaml, err := generateChildPipelineYAML(dispatches)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte(yaml), 0o644)
}
