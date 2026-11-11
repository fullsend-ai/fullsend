package artifacts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// contentAttributeKeys are the span attributes Level 3 content capture
// adds (ADR 0050): the assembled messages (attachContent and attachInput
// in internal/cli/content_collector.go), plus the other GenAI content
// attributes the semantic conventions define, so a later capture of
// them is stripped too.
var contentAttributeKeys = map[string]bool{
	"gen_ai.input.messages":      true,
	"gen_ai.output.messages":     true,
	"gen_ai.system_instructions": true,
	"gen_ai.tool.definitions":    true,
	"gen_ai.tool.call.arguments": true,
	"gen_ai.tool.call.result":    true,
}

// contentAttributePrefix covers the content-capture markers
// (fullsend.content.truncated, dropped_bytes, redactions,
// sanitizations).
const contentAttributePrefix = "fullsend.content."

// contentEventPrefix covers GenAI content events. Fullsend records content
// on span attributes only, but an event-based capture would use this
// namespace.
const contentEventPrefix = "gen_ai."

func isContentAttribute(key string) bool {
	return contentAttributeKeys[key] || strings.HasPrefix(key, contentAttributePrefix)
}

// freeTextAttributeKeys are span attributes that carry free text from
// outside the agent run, removed like content attributes. The pre-script
// skip reason is up to 8 KiB of pre-script output; the boolean
// fullsend.prescript.skipped stays.
var freeTextAttributeKeys = map[string]bool{
	"fullsend.prescript.skip_reason": true,
}

// StripTelemetry returns a copy of a run-telemetry.jsonl file (one OTLP
// JSON TracesData per line) with every content-capture attribute and
// event, span status messages, exception text and the pre-script skip
// reason removed. Blank lines are dropped. Any record that is not valid
// JSON or does not have the TracesData shape is an error, so the caller
// leaves the file out instead of publishing it unstripped.
func StripTelemetry(data []byte) ([]byte, error) {
	var out bytes.Buffer
	reader := bufio.NewReader(bytes.NewReader(data))
	lineNo := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				stripped, err := stripRecord(trimmed)
				if err != nil {
					return nil, fmt.Errorf("record %d: %w", lineNo, err)
				}
				out.Write(stripped)
				out.WriteByte('\n')
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return out.Bytes(), nil
}

func stripRecord(record []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(record))
	dec.UseNumber() // keep integer values exact through the round trip
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data after record")
	}
	if root == nil {
		return nil, errors.New("record is not a JSON object")
	}
	resourceSpans, err := objectList(root, "resourceSpans", true)
	if err != nil {
		return nil, err
	}
	for _, rs := range resourceSpans {
		if res, ok := rs["resource"]; ok && res != nil {
			resource, ok := res.(map[string]any)
			if !ok {
				return nil, errors.New("resource is not an object")
			}
			if err := stripAttributes(resource); err != nil {
				return nil, err
			}
		}
		scopeSpans, err := objectList(rs, "scopeSpans", false)
		if err != nil {
			return nil, err
		}
		for _, ss := range scopeSpans {
			if sc, ok := ss["scope"]; ok && sc != nil {
				scope, ok := sc.(map[string]any)
				if !ok {
					return nil, errors.New("scope is not an object")
				}
				if err := stripAttributes(scope); err != nil {
					return nil, err
				}
			}
			spans, err := objectList(ss, "spans", false)
			if err != nil {
				return nil, err
			}
			for _, span := range spans {
				if err := stripSpan(span); err != nil {
					return nil, err
				}
			}
		}
	}
	return json.Marshal(root)
}

// exceptionEventName is the OpenTelemetry exception event. Its message and
// stack trace carry free error text (err.Error()), which can quote what the
// agent read, so the staged copy keeps only exception.type.
const exceptionEventName = "exception"

// exceptionTextAttributeKeys are the exception event attributes that hold
// free error text.
var exceptionTextAttributeKeys = map[string]bool{
	"exception.message":    true,
	"exception.stacktrace": true,
}

func stripSpan(span map[string]any) error {
	if err := stripAttributes(span); err != nil {
		return err
	}
	if err := stripStatusMessage(span); err != nil {
		return err
	}
	events, err := objectList(span, "events", false)
	if err != nil {
		return err
	}
	if events != nil {
		kept := make([]any, 0, len(events))
		for _, ev := range events {
			name, _ := ev["name"].(string)
			if strings.HasPrefix(name, contentEventPrefix) {
				continue
			}
			carries, err := carriesContent(ev)
			if err != nil {
				return err
			}
			if carries {
				continue
			}
			if name == exceptionEventName {
				if err := dropAttributes(ev, exceptionTextAttributeKeys); err != nil {
					return err
				}
			}
			kept = append(kept, ev)
		}
		span["events"] = kept
	}
	links, err := objectList(span, "links", false)
	if err != nil {
		return err
	}
	for _, link := range links {
		if err := stripAttributes(link); err != nil {
			return err
		}
	}
	return nil
}

// stripStatusMessage drops the span status description, which carries free
// error text; the status code stays.
func stripStatusMessage(span map[string]any) error {
	raw, ok := span["status"]
	if !ok || raw == nil {
		return nil
	}
	status, ok := raw.(map[string]any)
	if !ok {
		return errors.New("status is not an object")
	}
	delete(status, "message")
	return nil
}

// dropAttributes removes the named keys from obj["attributes"].
func dropAttributes(obj map[string]any, keys map[string]bool) error {
	attrs, err := objectList(obj, "attributes", false)
	if err != nil {
		return err
	}
	if attrs == nil {
		return nil
	}
	kept := make([]any, 0, len(attrs))
	for _, kv := range attrs {
		key, ok := kv["key"].(string)
		if !ok {
			return errors.New("attribute has no string key")
		}
		if keys[key] {
			continue
		}
		kept = append(kept, kv)
	}
	obj["attributes"] = kept
	return nil
}

// stripAttributes removes content attributes from obj["attributes"].
func stripAttributes(obj map[string]any) error {
	attrs, err := objectList(obj, "attributes", false)
	if err != nil {
		return err
	}
	if attrs == nil {
		return nil
	}
	kept := make([]any, 0, len(attrs))
	for _, kv := range attrs {
		key, ok := kv["key"].(string)
		if !ok {
			return errors.New("attribute has no string key")
		}
		if isContentAttribute(key) || freeTextAttributeKeys[key] {
			continue
		}
		kept = append(kept, kv)
	}
	obj["attributes"] = kept
	return nil
}

func carriesContent(obj map[string]any) (bool, error) {
	attrs, err := objectList(obj, "attributes", false)
	if err != nil {
		return false, err
	}
	for _, kv := range attrs {
		key, ok := kv["key"].(string)
		if !ok {
			return false, errors.New("attribute has no string key")
		}
		if isContentAttribute(key) {
			return true, nil
		}
	}
	return false, nil
}

// objectList returns obj[key] as a list of objects. A missing or null
// value returns nil unless required. Any other shape is an error.
func objectList(obj map[string]any, key string, required bool) ([]map[string]any, error) {
	raw, ok := obj[key]
	if !ok || raw == nil {
		if required {
			return nil, fmt.Errorf("missing %s", key)
		}
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a list", key)
	}
	out := make([]map[string]any, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", key, i)
		}
		out[i] = m
	}
	return out, nil
}
