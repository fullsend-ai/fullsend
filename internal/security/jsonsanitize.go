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
	rebuilt, findings, prov, err := rewriteJSONTokens(trimmed, p)
	if err != nil {
		// json.Valid accepted the text and the walk uses the same scanner, so
		// this is a decoder limit rather than a syntax error. Return the error
		// so the caller fails closed instead of writing unscanned bytes.
		return nil, nil, fmt.Errorf("could not sanitize JSON document: %w", err)
	}
	out := doc
	if len(findings) > 0 {
		out = rebuilt
		// Masking a member value can hide a private-key block that spans
		// members: a sensitive member name masks the BEGIN marker, leaving the
		// key body and END marker behind in a form the rewritten scan can no
		// longer recognize. The decoded view knows whether every block was
		// contained in one key or leaf.
		if prov.spanningBlocks() > 0 {
			return preserveTrailingNewline([]byte(failClosedInvalidJSON), doc),
				append(findings, Finding{
					Scanner:  "secret_redactor",
					Name:     "private_key",
					Severity: "critical",
					Detail:   "Private key block spans JSON framing and cannot be localized; output removed",
				}), nil
		}
	}
	// Scan the bytes that will actually be written. The walk above already
	// scanned every decoded key and leaf, so a match here spans JSON framing
	// (for example a PEM block split across members) and cannot be mapped back
	// to a single leaf.
	sanitizedOut, outputFindings := redactJSONOutput(out, p)
	return preserveTrailingNewline(sanitizedOut, doc), append(findings, outputFindings...), nil
}

// privateKeyRe is the structural private-key pattern. A block that spans JSON
// framing has to be detected on the raw document: running the whole pipeline
// first lets a sensitive-named member mask the BEGIN marker and hide it.
var privateKeyRe = func() *regexp.Regexp {
	for _, pattern := range defaultStructuralPatterns() {
		if pattern.name == "private_key" {
			return pattern.regex
		}
	}
	panic("private_key structural pattern is missing")
}()

// jsonProvenance accumulates an order-preserving view of the decoded strings
// in a document so a private-key block that spans members can be detected after
// JSON decoding. The raw bytes are not usable for this: a member may encode a
// marker with \u escapes, and masking a sensitive member removes the BEGIN
// marker before the rewritten document is scanned.
type jsonProvenance struct {
	decoded   bytes.Buffer
	leafCount int
}

// addString appends one decoded key or string leaf to the view and counts the
// private-key blocks it fully contains.
func (v *jsonProvenance) addString(s string) {
	v.leafCount += len(privateKeyRe.FindAllIndex([]byte(s), -1))
	v.decoded.WriteString(s)
	v.decoded.WriteByte('\n')
}

// spanningBlocks reports how many private-key blocks exist in the decoded view
// that no single key or leaf contains.
func (v *jsonProvenance) spanningBlocks() int {
	return len(privateKeyRe.FindAllIndex(v.decoded.Bytes(), -1)) - v.leafCount
}

// preserveTrailingNewline drops the newline json.Encoder always appends and
// restores the document's own trailing newline, so a rewrite never changes the
// file's line framing.
func preserveTrailingNewline(encoded, doc []byte) []byte {
	encoded = bytes.TrimRight(encoded, "\r\n")
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
	// A private-key block that spans records has to be detected on the original
	// framing: per-record masking of a sensitive member removes the BEGIN
	// marker, after which neither the record scan nor the whole-stream scan can
	// recognize the block.
	content, crossRecordFindings := failClosedCrossRecordPrivateKeys(content)
	var (
		out      bytes.Buffer
		findings []Finding
		spans    []recordSpan
	)
	findings = append(findings, crossRecordFindings...)
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

// failClosedCrossRecordPrivateKeys replaces every record covered by a
// private-key block that crosses record boundaries with a valid marker. Match
// offsets come straight from the original stream, so localization is exact.
func failClosedCrossRecordPrivateKeys(content []byte) ([]byte, []Finding) {
	spans := jsonLineSpans(content)
	if len(spans) == 0 {
		return content, nil
	}
	// Match in a decoded, order-preserving view so a marker written with
	// \u escapes is still recognized, and keep each record's range in that
	// view so a match maps back to the records it covers.
	var (
		view      bytes.Buffer
		viewSpans []recordSpan
	)
	for _, span := range spans {
		start := view.Len()
		line := content[span.start:span.end]
		if trimmed := bytes.TrimSpace(line); json.Valid(trimmed) {
			writeDecodedJSONView(&view, trimmed)
		} else {
			view.Write(line)
		}
		view.WriteByte('\n')
		viewSpans = append(viewSpans, recordSpan{start: start, end: view.Len() - 1})
	}
	matches := privateKeyRe.FindAllIndex(view.Bytes(), -1)
	covered := make([]bool, len(spans))
	var findings []Finding
	for _, match := range matches {
		var hit []int
		for i, span := range viewSpans {
			if span.end > match[0] && span.start < match[1] {
				hit = append(hit, i)
			}
		}
		// A block confined to one record is handled by that record's own
		// sanitization.
		if len(hit) < 2 {
			continue
		}
		findings = append(findings, Finding{
			Scanner:  "secret_redactor",
			Name:     "private_key",
			Severity: "critical",
			Detail:   "Private key block spans records and cannot be localized; output removed",
		})
		for _, i := range hit {
			covered[i] = true
		}
	}
	if len(findings) == 0 {
		return content, nil
	}
	var out bytes.Buffer
	cursor := 0
	for i, span := range spans {
		if !covered[i] {
			continue
		}
		out.Write(content[cursor:span.start])
		out.WriteString(failClosedCrossRecordJSON)
		cursor = span.end
	}
	out.Write(content[cursor:])
	return out.Bytes(), findings
}

// writeDecodedJSONView appends every decoded object key and string leaf of doc
// to buf in document order, one per line, so a value split across members is
// still visible as one contiguous run of text.
func writeDecodedJSONView(buf *bytes.Buffer, doc []byte) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var walk func()
	walk = func() {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case json.Delim:
			if t == '{' || t == '[' {
				for dec.More() {
					walk()
				}
				_, _ = dec.Token() // consume the closing delimiter
			}
		case string:
			buf.WriteString(t)
			buf.WriteByte('\n')
		}
	}
	walk()
}

// jsonLineSpans returns the byte range of every non-empty line in content,
// excluding its trailing CR/LF framing.
func jsonLineSpans(content []byte) []recordSpan {
	var spans []recordSpan
	for start := 0; start < len(content); {
		end := bytes.IndexByte(content[start:], '\n')
		if end < 0 {
			end = len(content)
		} else {
			end += start
		}
		lineEnd := end
		if lineEnd > start && content[lineEnd-1] == '\r' {
			lineEnd--
		}
		if len(bytes.TrimSpace(content[start:lineEnd])) > 0 {
			spans = append(spans, recordSpan{start: start, end: lineEnd})
		}
		start = end + 1
	}
	return spans
}

// failClosedRecordSpans replaces the records a whole-stream redaction changed
// with a valid fail-closed marker. Records the redaction did not touch are
// copied through unchanged, so two disjoint cross-record matches never discard
// the clean records between them.
//
// The per-record pass has already handled every match a single record could
// account for, so a remaining match spans a record boundary. Locating the
// records it covers means finding each record's bytes again in the redacted
// stream, which is only trustworthy when every record is distinct: with a
// repeated record, a later untouched copy can stand in for the copy a match
// covered, and the original bytes — carrying part of the secret — would be
// written back. When that happens the whole stream fails closed.
func failClosedRecordSpans(stream []byte, spans []recordSpan, scan ScanResult) []byte {
	sanitized := []byte(scan.Sanitized)

	seen := make(map[string]struct{}, len(spans))
	for _, span := range spans {
		record := string(stream[span.start:span.end])
		if _, ok := seen[record]; ok {
			return replaceRecordSpans(stream, spans)
		}
		seen[record] = struct{}{}
	}

	var out bytes.Buffer
	cursor := 0
	searchFrom := 0
	for _, span := range spans {
		record := stream[span.start:span.end]
		idx := bytes.Index(sanitized[searchFrom:], record)
		// A surviving copy must be a whole record, not a substring of a longer
		// record that happens to contain the same bytes.
		if idx >= 0 && lineBounded(sanitized, searchFrom+idx, searchFrom+idx+len(record)) {
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

// replaceRecordSpans replaces every record with a valid marker when the
// records a match covers cannot be identified unambiguously.
func replaceRecordSpans(stream []byte, spans []recordSpan) []byte {
	var out bytes.Buffer
	cursor := 0
	for _, span := range spans {
		out.Write(stream[cursor:span.start])
		out.WriteString(failClosedCrossRecordJSON)
		cursor = span.end
	}
	out.Write(stream[cursor:])
	return out.Bytes()
}

// lineBounded reports whether data[start:end] is a whole newline-delimited
// line.
func lineBounded(data []byte, start, end int) bool {
	if start > 0 && data[start-1] != '\n' {
		return false
	}
	switch {
	case end == len(data), data[end] == '\n':
		return true
	case data[end] == '\r':
		return end+1 < len(data) && data[end+1] == '\n'
	default:
		return false
	}
}

// rewriteJSONTokens walks every token in doc, sanitizes each object key and
// scalar value, and renders a valid JSON document. Unlike map decoding it
// keeps member order and duplicate keys, so a secret in an earlier, overwritten
// value is still found, and a secret spanning JSON framing stays visible in the
// rewritten bytes.
func rewriteJSONTokens(doc []byte, p *Pipeline) ([]byte, []Finding, *jsonProvenance, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var (
		buf      bytes.Buffer
		findings []Finding
	)
	prov := &jsonProvenance{}
	if err := writeSanitizedJSONValue(dec, &buf, p, &findings, prov, ""); err != nil {
		return nil, nil, nil, fmt.Errorf("could not tokenize JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, nil, fmt.Errorf("unexpected trailing JSON content")
	}
	return buf.Bytes(), findings, prov, nil
}

// writeSanitizedJSONValue writes one JSON value to buf, sanitizing object keys
// and scalar leaves as it goes. memberKey is the enclosing object key when the
// value is an object member, so sensitive-key masking still applies; it is
// empty for array elements and the top-level value.
func writeSanitizedJSONValue(dec *json.Decoder, buf *bytes.Buffer, p *Pipeline, findings *[]Finding, prov *jsonProvenance, memberKey string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		return writeSanitizedJSONContainer(dec, buf, p, findings, prov, t)
	case string:
		prov.addString(t)
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
func writeSanitizedJSONContainer(dec *json.Decoder, buf *bytes.Buffer, p *Pipeline, findings *[]Finding, prov *jsonProvenance, delim json.Delim) error {
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
			prov.addString(key)
			sanitizedKey, keyFindings := sanitizeJSONKey(key, p)
			*findings = append(*findings, keyFindings...)
			var rendered bytes.Buffer
			if err := writeSanitizedJSONValue(dec, &rendered, p, findings, prov, sanitizedKey); err != nil {
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
			if err := writeSanitizedJSONValue(dec, buf, p, findings, prov, ""); err != nil {
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
