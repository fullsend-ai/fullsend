package poll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

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

// dispatch builds an event payload, base64-encodes it, and appends
// a Dispatch record for child pipeline generation.
func (p *Poller) dispatch(ctx context.Context, owner, repo, stage string, event RoutableEvent) error {
	_ = ctx   // reserved for future use
	_ = owner // included in signature for routing context
	_ = repo
	payload := buildEventPayload(event)
	encoded := base64.StdEncoding.EncodeToString(payload)
	return p.appendDispatch(Dispatch{
		Stage:           stage,
		EventType:       event.Type,
		EventPayloadB64: encoded,
		ResourceKey:     fmt.Sprintf("%s-%d", event.Type, event.IID),
	})
}

// buildEventPayload creates a JSON payload from a RoutableEvent,
// including only non-zero/non-empty optional fields.
func buildEventPayload(event RoutableEvent) []byte {
	m := map[string]interface{}{
		"type":       event.Type,
		"iid":        event.IID,
		"updated_at": event.UpdatedAt.Format(time.RFC3339),
	}
	if event.NoteBody != "" {
		m["note_body"] = event.NoteBody
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
	data, err := json.Marshal(m)
	if err != nil {
		panic(fmt.Sprintf("buildEventPayload: marshal failed: %v", err))
	}
	return data
}

// generateChildPipelineYAML produces GitLab CI YAML that triggers
// one child pipeline job per dispatch record.
func generateChildPipelineYAML(dispatches []Dispatch) string {
	var buf bytes.Buffer
	for i, d := range dispatches {
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
	return buf.String()
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
	yaml := generateChildPipelineYAML(dispatches)
	return os.WriteFile(outputPath, []byte(yaml), 0o644)
}
