package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
)

// JSON output that cannot be redacted without corrupting its structure is
// replaced with one of these valid JSON markers so downstream parsers (jq, the
// retro and eval tooling) keep working while no secret reaches the artifact.
const (
	failClosedInvalidJSON     = `{"error":"output removed because redaction would invalidate JSON"}`
	failClosedCrossRecordJSON = `{"error":"output removed because output contained a cross-record secret"}`
)

// sensitiveJSONKeyRe matches object keys whose values are masked even when the
// value itself does not match a secret pattern.
var sensitiveJSONKeyRe = regexp.MustCompile(`(?i)(?:key|token|secret|password|credential|auth)`)

// redactedJSONKey replaces an object key that sanitization removed entirely
// (a key made only of bidi overrides or other invisible characters, for
// example). Such a key must never be restored as-is.
const redactedJSONKey = "redacted"

// SanitizeJSONDocument structurally sanitizes a single JSON document so that
// redaction never changes its syntax. It returns the bytes to write and the
// findings that justified a rewrite; no findings means the document is clean
// and the caller must leave it untouched. A document that is not valid JSON is
// returned unchanged with no findings so the caller can fall back to a text
// scan.
func SanitizeJSONDocument(doc []byte, p *Pipeline) ([]byte, []Finding, error) {
	trimmed := bytes.TrimSpace(doc)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return doc, nil, nil
	}
	if HasDuplicateJSONKeys(trimmed) {
		return sanitizeDuplicateKeyDocument(trimmed, doc, p)
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		// json.Valid accepted the text, so this is a decoder limit rather than
		// a syntax error. Keep the bytes and let the caller text-scan it.
		return doc, nil, nil
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return doc, nil, nil
	}

	sanitized, findings := SanitizeJSONStructure(value, p)
	multiline := bytes.Contains(bytes.TrimRight(doc, "\r\n"), []byte("\n"))
	encoded, err := encodeJSONDocument(sanitized, multiline)
	if err != nil {
		return nil, findings, err
	}
	if len(findings) == 0 {
		// A secret can appear only once the decoded leaves are re-serialized,
		// for example a runtime secret that spans two adjacent values.
		out, backstop := backstopSerializedJSON(encoded, doc, p)
		return preserveTrailingNewline(out, doc), backstop, nil
	}
	out, backstop := backstopSerializedJSON(encoded, encoded, p)
	return preserveTrailingNewline(out, doc), append(findings, backstop...), nil
}

// sanitizeDuplicateKeyDocument sanitizes a document that contains duplicate
// object keys. Decoding it into a map would drop the earlier occurrences, so
// every key and value is scanned through a token-preserving walk instead.
func sanitizeDuplicateKeyDocument(trimmed, doc []byte, p *Pipeline) ([]byte, []Finding, error) {
	rebuilt, findings, err := rewriteJSONTokens(trimmed, p)
	if err != nil {
		return nil, nil, err
	}
	if len(findings) == 0 {
		// Nothing in the decoded leaves matched, so scan the raw text for
		// patterns that only exist in the serialized form, such as a JSON
		// field whose value is not otherwise recognizable.
		res := p.Scan(string(trimmed))
		if len(res.Findings) == 0 {
			return doc, nil, nil
		}
		if res.Sanitized == "" || !json.Valid([]byte(res.Sanitized)) {
			return preserveTrailingNewline([]byte(failClosedInvalidJSON), doc), res.Findings, nil
		}
		return preserveTrailingNewline([]byte(res.Sanitized), doc), res.Findings, nil
	}
	out, backstop := backstopSerializedJSON(rebuilt, rebuilt, p)
	return preserveTrailingNewline(out, doc), append(findings, backstop...), nil
}

// preserveTrailingNewline drops the newline json.Encoder always appends and
// restores the document's own trailing newline, so a rewrite never changes the
// file's line framing.
func preserveTrailingNewline(encoded, doc []byte) []byte {
	encoded = bytes.TrimRight(encoded, "\n")
	switch {
	case bytes.HasSuffix(doc, []byte("\r\n")):
		return append(encoded, '\r', '\n')
	case bytes.HasSuffix(doc, []byte("\n")):
		return append(encoded, '\n')
	default:
		return encoded
	}
}

// backstopSerializedJSON scans serialized JSON for secrets that only become
// visible once the parsed form is written back out. Such a match no longer
// maps to a single leaf, so it is applied only when the redacted text is still
// valid JSON; otherwise the document fails closed with a valid marker.
// clean is returned unchanged when the scan finds nothing.
func backstopSerializedJSON(serialized, clean []byte, p *Pipeline) ([]byte, []Finding) {
	res := p.Scan(string(serialized))
	if len(res.Findings) == 0 {
		return clean, nil
	}
	for _, finding := range res.Findings {
		// A private-key block spanning values cannot be masked in place
		// without breaking the surrounding JSON.
		if finding.Name == "private_key" {
			return []byte(failClosedInvalidJSON), res.Findings
		}
	}
	if res.Sanitized == "" || !json.Valid([]byte(res.Sanitized)) {
		return []byte(failClosedInvalidJSON), res.Findings
	}
	return []byte(res.Sanitized), res.Findings
}

// SanitizeJSONLines sanitizes newline-delimited JSON one record at a time,
// preserving the record framing so every line stays independently parseable.
// A secret that spans a record boundary cannot be redacted without rewriting
// that framing, so the records it covers are replaced with valid fail-closed
// markers and every other record is left as sanitized.
func SanitizeJSONLines(content []byte, p *Pipeline) ([]byte, []Finding, error) {
	var (
		out      bytes.Buffer
		findings []Finding
		spans    []recordSpan
	)
	for len(content) > 0 {
		line := content
		newline := bytes.IndexByte(content, '\n')
		hasNewline := newline >= 0
		if hasNewline {
			line = content[:newline]
			content = content[newline+1:]
		} else {
			content = nil
		}
		// Keep CRLF framing byte-for-byte for records that are not rewritten.
		carriageReturn := len(line) > 0 && line[len(line)-1] == '\r'
		if carriageReturn {
			line = line[:len(line)-1]
		}
		trimmed := bytes.TrimSpace(line)
		start := out.Len()
		switch {
		case len(trimmed) == 0:
			out.Write(line)
		case json.Valid(trimmed):
			record, recordFindings, err := SanitizeJSONDocument(trimmed, p)
			if err != nil {
				return nil, findings, fmt.Errorf("could not sanitize JSONL record: %w", err)
			}
			findings = append(findings, recordFindings...)
			if len(recordFindings) > 0 {
				out.Write(record)
			} else {
				out.Write(line)
			}
			spans = append(spans, recordSpan{start: start, end: out.Len()})
		default:
			res := p.Scan(string(line))
			if len(res.Findings) > 0 {
				findings = append(findings, res.Findings...)
				out.WriteString(res.Sanitized)
			} else {
				out.Write(line)
			}
			spans = append(spans, recordSpan{start: start, end: out.Len()})
		}
		if carriageReturn {
			out.WriteByte('\r')
		}
		if hasNewline {
			out.WriteByte('\n')
		}
	}

	rebuilt := out.Bytes()
	stream := p.Scan(string(rebuilt))
	if len(stream.Findings) == 0 {
		return rebuilt, findings, nil
	}
	// Every finding a single record could account for was handled above, so
	// what remains spans a record boundary.
	findings = append(findings, stream.Findings...)
	return failClosedRecordSpans(rebuilt, spans, stream), findings, nil
}

// recordSpan is the byte range of one non-empty JSONL record in the rebuilt
// stream, excluding its trailing newline.
type recordSpan struct {
	start, end int
}

// failClosedRecordSpans replaces every record that intersects the region the
// whole-stream scan rewrote with a valid fail-closed marker. Records outside
// that region are copied through untouched, so a single cross-record match
// never discards unrelated records.
func failClosedRecordSpans(stream []byte, spans []recordSpan, scan ScanResult) []byte {
	sanitized := []byte(scan.Sanitized)
	// Only reuse the redacted stream when redaction left the same number of
	// independently parseable records. A match that consumed the JSON framing
	// between two records would otherwise silently drop them.
	if len(sanitized) > 0 && validJSONLines(sanitized, len(spans)) {
		return sanitized
	}
	start, end := changedRegion(stream, sanitized)
	if start >= end {
		// The scan reported a finding but nothing in the bytes changed, so it
		// cannot be localized: fail closed for every record.
		start, end = 0, len(stream)
	}
	var out bytes.Buffer
	cursor := 0
	for _, span := range spans {
		if span.end <= start || span.start >= end {
			continue
		}
		out.Write(stream[cursor:span.start])
		out.WriteString(failClosedCrossRecordJSON)
		cursor = span.end
	}
	out.Write(stream[cursor:])
	return out.Bytes()
}

// changedRegion returns the half-open byte range that differs between a and b.
// It is approximate for length-changing edits, which is enough to find the
// records a cross-record match covers.
func changedRegion(a, b []byte) (int, int) {
	limit := min(len(a), len(b))
	start := 0
	for start < limit && a[start] == b[start] {
		start++
	}
	end := 0
	for end < limit-start && a[len(a)-1-end] == b[len(b)-1-end] {
		end++
	}
	return start, len(a) - end
}

// validJSONLines reports whether data holds exactly want non-empty lines, each
// valid JSON.
func validJSONLines(data []byte, want int) bool {
	count := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if !json.Valid(trimmed) {
			return false
		}
		count++
	}
	return count == want
}

// rewriteJSONTokens walks every token in doc, sanitizes each object key and
// scalar value, and renders a valid JSON document. Unlike map decoding it
// keeps duplicate keys and every occurrence, so a secret in an earlier,
// overwritten value is still found and redacted.
func rewriteJSONTokens(doc []byte, p *Pipeline) ([]byte, []Finding, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var (
		buf      bytes.Buffer
		findings []Finding
	)
	if err := writeSanitizedJSONValue(dec, &buf, p, &findings, ""); err != nil {
		return nil, nil, fmt.Errorf("could not tokenize JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("unexpected trailing JSON content")
	}
	return buf.Bytes(), findings, nil
}

// writeSanitizedJSONValue writes one JSON value to buf, sanitizing object keys
// and scalar leaves as it goes. memberKey is the enclosing object key when the
// value is an object member, so sensitive-key masking still applies; it is
// empty for array elements and the top-level value.
func writeSanitizedJSONValue(dec *json.Decoder, buf *bytes.Buffer, p *Pipeline, findings *[]Finding, memberKey string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		return writeSanitizedJSONContainer(dec, buf, p, findings, t)
	case string:
		sanitized, stringFindings := sanitizeJSONString(t, memberKey, p)
		*findings = append(*findings, stringFindings...)
		return writeJSONString(buf, sanitized)
	case json.Number:
		literal := t.String()
		res := p.Scan(literal)
		if len(res.Findings) == 0 {
			buf.WriteString(literal)
			return nil
		}
		*findings = append(*findings, res.Findings...)
		// A redacted number is no longer a number literal, so write the
		// replacement as a JSON string to keep the document valid.
		return writeJSONString(buf, res.Sanitized)
	case bool:
		buf.WriteString(strconv.FormatBool(t))
		return nil
	case nil:
		buf.WriteString("null")
		return nil
	default:
		return fmt.Errorf("unexpected JSON token %T", tok)
	}
}

// writeSanitizedJSONContainer writes the members of an object or array,
// preserving member order and duplicate keys.
func writeSanitizedJSONContainer(dec *json.Decoder, buf *bytes.Buffer, p *Pipeline, findings *[]Finding, delim json.Delim) error {
	switch delim {
	case '{':
		buf.WriteByte('{')
		first := true
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("JSON object key is %T, want string", keyTok)
			}
			sanitizedKey, keyFindings := sanitizeJSONKey(key, p)
			*findings = append(*findings, keyFindings...)
			if !first {
				buf.WriteByte(',')
			}
			if err := writeJSONString(buf, sanitizedKey); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeSanitizedJSONValue(dec, buf, p, findings, sanitizedKey); err != nil {
				return err
			}
			first = false
		}
		if _, err := dec.Token(); err != nil { // consume '}'
			return err
		}
		buf.WriteByte('}')
		return nil
	case '[':
		buf.WriteByte('[')
		first := true
		for dec.More() {
			if !first {
				buf.WriteByte(',')
			}
			if err := writeSanitizedJSONValue(dec, buf, p, findings, ""); err != nil {
				return err
			}
			first = false
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return err
		}
		buf.WriteByte(']')
		return nil
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim.String())
	}
}

// writeJSONString appends s to buf as a JSON string literal without escaping
// HTML characters, so legitimate markup survives sanitization.
func writeJSONString(buf *bytes.Buffer, s string) error {
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return nil
}

// encodeJSONDocument renders v as JSON, indented when the original document was
// multi-line so the rewritten file keeps its shape.
func encodeJSONDocument(v any, multiline bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if multiline {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("could not encode sanitized JSON: %w", err)
	}
	return buf.Bytes(), nil
}

// SanitizeJSONStructure recursively visits all keys and scalar values in parsed
// JSON data, scans each one with the pipeline, and returns the sanitized value
// together with every finding. The result is always structurally valid JSON.
func SanitizeJSONStructure(v any, p *Pipeline) (any, []Finding) {
	switch val := v.(type) {
	case string:
		res := p.Scan(val)
		if len(res.Findings) > 0 {
			// An empty sanitized value means everything was removed; that is a
			// valid replacement for a string leaf.
			return res.Sanitized, res.Findings
		}
		return val, nil
	case map[string]any:
		newMap := make(map[string]any, len(val))
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// Reserve clean original names before assigning sanitized names so a
		// secret key can never steal a legitimate key's name/value slot.
		for _, k := range keys {
			if keyRes := p.Scan(k); len(keyRes.Findings) == 0 {
				newMap[k] = nil
			}
		}
		var findings []Finding
		for _, k := range keys {
			item := val[k]
			keyRes := p.Scan(k)
			keyNeedsSanitization := len(keyRes.Findings) > 0
			sanitizedKey := k
			if keyNeedsSanitization {
				findings = append(findings, keyRes.Findings...)
				sanitizedKey = keyRes.Sanitized
				if sanitizedKey == "" {
					// The whole key was removable characters; never restore it.
					sanitizedKey = redactedJSONKey
				}
			}
			targetKey := sanitizedKey
			if !keyNeedsSanitization {
				targetKey = k
			}
			if _, exists := newMap[targetKey]; exists && keyNeedsSanitization {
				for i := 1; ; i++ {
					cand := fmt.Sprintf("%s_%d", sanitizedKey, i)
					if _, exists := newMap[cand]; !exists {
						targetKey = cand
						break
					}
				}
			}
			if strVal, ok := item.(string); ok {
				out, stringFindings := sanitizeJSONString(strVal, sanitizedKey, p)
				findings = append(findings, stringFindings...)
				newMap[targetKey] = out
				continue
			}
			newVal, f := SanitizeJSONStructure(item, p)
			findings = append(findings, f...)
			newMap[targetKey] = newVal
		}
		return newMap, findings
	case []any:
		newSlice := make([]any, len(val))
		var findings []Finding
		for i, item := range val {
			newVal, f := SanitizeJSONStructure(item, p)
			findings = append(findings, f...)
			newSlice[i] = newVal
		}
		return newSlice, findings
	case json.Number:
		res := p.Scan(val.String())
		if len(res.Findings) > 0 {
			if res.Sanitized == "" {
				return "", res.Findings
			}
			return res.Sanitized, res.Findings
		}
		return val, nil
	default:
		return val, nil
	}
}

// sanitizeJSONKey sanitizes one object key. A key whose sanitized value is
// empty was removed entirely, so it is replaced with a placeholder rather than
// kept as-is.
func sanitizeJSONKey(key string, p *Pipeline) (string, []Finding) {
	res := p.Scan(key)
	if len(res.Findings) == 0 {
		return key, nil
	}
	sanitized := res.Sanitized
	if sanitized == "" {
		sanitized = redactedJSONKey
	}
	return sanitized, res.Findings
}

// sanitizeJSONString sanitizes one string leaf. memberKey is the enclosing
// object key, used to mask values whose key name marks them as sensitive even
// when the value itself matches no secret pattern.
func sanitizeJSONString(value, memberKey string, p *Pipeline) (string, []Finding) {
	res := p.Scan(value)
	out := value
	var findings []Finding
	if len(res.Findings) > 0 {
		findings = append(findings, res.Findings...)
		out = res.Sanitized
	}
	if memberKey != "" && sensitiveJSONKeyRe.MatchString(memberKey) && len(out) >= 8 && !isAlreadyMasked(out) {
		masked := Mask(out)
		findings = append(findings, Finding{
			Scanner:  "secret_redactor",
			Name:     "json_field",
			Severity: "high",
			Detail:   fmt.Sprintf("Redacted json_field (%d chars) -> %s", len(out), masked),
		})
		out = masked
	}
	return out, findings
}

// HasDuplicateJSONKeys parses content token by token and reports whether any
// JSON object within the payload contains duplicate keys.
func HasDuplicateJSONKeys(content []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	var stack []*jsonContainerFrame
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, &jsonContainerFrame{
					isObject:     true,
					expectingKey: true,
					keys:         make(map[string]bool),
				})
			case '[':
				stack = append(stack, &jsonContainerFrame{isObject: false})
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				if len(stack) > 0 && stack[len(stack)-1].isObject {
					stack[len(stack)-1].expectingKey = true
				}
			}
		case string:
			if len(stack) > 0 && stack[len(stack)-1].isObject {
				top := stack[len(stack)-1]
				if top.expectingKey {
					if top.keys[t] {
						return true
					}
					top.keys[t] = true
					top.expectingKey = false
				} else {
					top.expectingKey = true
				}
			}
		default:
			if len(stack) > 0 && stack[len(stack)-1].isObject {
				stack[len(stack)-1].expectingKey = true
			}
		}
	}
}

// jsonContainerFrame tracks one open object or array while scanning for
// duplicate keys.
type jsonContainerFrame struct {
	isObject     bool
	expectingKey bool
	keys         map[string]bool
}
