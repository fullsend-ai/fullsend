package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
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
//
// The document is rewritten by a token-preserving walk rather than decoded into
// a map and re-encoded: that keeps member order and duplicate keys, so a
// secret that spans JSON framing is still visible in the rewritten bytes.
func SanitizeJSONDocument(doc []byte, p *Pipeline) ([]byte, []Finding, error) {
	trimmed := bytes.TrimSpace(doc)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return doc, nil, nil
	}
	rebuilt, findings, err := rewriteJSONTokens(trimmed, p)
	if err != nil {
		// json.Valid accepted the text and the walk uses the same scanner, so
		// this is a decoder limit rather than a syntax error. Return the error
		// so the caller fails closed instead of writing unscanned bytes.
		return nil, nil, fmt.Errorf("could not sanitize JSON document: %w", err)
	}
	out := doc
	if len(findings) > 0 {
		out = rebuilt
	}
	// Scan the bytes that will actually be written. The walk above already
	// scanned every decoded key and leaf, so a match here spans JSON framing
	// (for example a PEM block split across members) and cannot be mapped back
	// to a single leaf.
	sanitizedOut, outputFindings := redactJSONOutput(out, p)
	return preserveTrailingNewline(sanitizedOut, doc), append(findings, outputFindings...), nil
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

// redactJSONOutput scans the bytes about to be written for secrets that only
// appear in the serialized form. Such a match does not map to a single leaf, so
// it is applied only when the redacted text is still valid JSON; otherwise the
// document fails closed with a valid marker. clean is returned unchanged when
// the scan finds nothing.
func redactJSONOutput(clean []byte, p *Pipeline) ([]byte, []Finding) {
	res := p.Scan(string(clean))
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

// failClosedRecordSpans replaces the records a whole-stream redaction changed
// with a valid fail-closed marker. Records the redaction did not touch are
// copied through unchanged, so two disjoint cross-record matches never discard
// the clean records between them.
//
// The per-record pass has already handled every match a single record could
// account for, so a remaining match spans a record boundary. Redaction masks
// every occurrence of a match, so a record that still appears byte-for-byte in
// the sanitized stream cannot carry one of the secrets just removed.
func failClosedRecordSpans(stream []byte, spans []recordSpan, scan ScanResult) []byte {
	sanitized := []byte(scan.Sanitized)

	var out bytes.Buffer
	cursor := 0
	searchFrom := 0
	for _, span := range spans {
		record := stream[span.start:span.end]
		if idx := bytes.Index(sanitized[searchFrom:], record); idx >= 0 {
			searchFrom += idx + len(record)
			out.Write(stream[cursor:span.end])
			cursor = span.end
			continue
		}
		// The record is not present verbatim, so a match covered part of it (or
		// the scan reported a finding without changing bytes, which cannot be
		// localized at all). Either way it must not be written as-is.
		out.Write(stream[cursor:span.start])
		out.WriteString(failClosedCrossRecordJSON)
		cursor = span.end
	}
	out.Write(stream[cursor:])
	return out.Bytes()
}

// rewriteJSONTokens walks every token in doc, sanitizes each object key and
// scalar value, and renders a valid JSON document. Unlike map decoding it
// keeps member order and duplicate keys, so a secret in an earlier, overwritten
// value is still found, and a secret spanning JSON framing stays visible in the
// rewritten bytes.
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

// jsonMember is one object member whose value has been rendered but whose name
// is not final yet. Names are resolved once the whole object has been read so a
// sanitized name can never collide with a clean key that appears later.
type jsonMember struct {
	originalKey  string
	sanitizedKey string
	rendered     []byte
}

// writeSanitizedJSONContainer writes the members of an object or array,
// preserving member order and duplicate keys.
func writeSanitizedJSONContainer(dec *json.Decoder, buf *bytes.Buffer, p *Pipeline, findings *[]Finding, delim json.Delim) error {
	switch delim {
	case '{':
		var members []jsonMember
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
			var rendered bytes.Buffer
			if err := writeSanitizedJSONValue(dec, &rendered, p, findings, sanitizedKey); err != nil {
				return err
			}
			members = append(members, jsonMember{
				originalKey:  key,
				sanitizedKey: sanitizedKey,
				rendered:     rendered.Bytes(),
			})
		}
		if _, err := dec.Token(); err != nil { // consume '}'
			return err
		}
		assignJSONMemberNames(members)
		buf.WriteByte('{')
		for i, m := range members {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONString(buf, m.sanitizedKey); err != nil {
				return err
			}
			buf.WriteByte(':')
			buf.Write(m.rendered)
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

// assignJSONMemberNames resolves the member names of one object. Clean keys keep
// their names, and a sanitized name that would collide with a different key is
// disambiguated. Repeats of the same original duplicate key keep sharing one
// name, so intentional duplicate keys survive sanitization.
func assignJSONMemberNames(members []jsonMember) {
	claimed := make(map[string]string, len(members))
	for _, m := range members {
		if m.sanitizedKey == m.originalKey {
			claimed[m.sanitizedKey] = m.originalKey
		}
	}
	renamed := make(map[string]string, len(members))
	for i := range members {
		m := &members[i]
		if m.sanitizedKey == m.originalKey {
			continue
		}
		if target, ok := renamed[m.originalKey]; ok {
			m.sanitizedKey = target
			continue
		}
		target := m.sanitizedKey
		if owner, taken := claimed[target]; taken && owner != m.originalKey {
			for n := 1; ; n++ {
				cand := fmt.Sprintf("%s_%d", m.sanitizedKey, n)
				if _, exists := claimed[cand]; !exists {
					target = cand
					break
				}
			}
		}
		renamed[m.originalKey] = target
		claimed[target] = m.originalKey
		m.sanitizedKey = target
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
