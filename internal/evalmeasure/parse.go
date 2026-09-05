package evalmeasure

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type otlpTracesData struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpScopeSpans struct {
	Spans []otlpSpan `json:"spans"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId"`
	Name              string         `json:"name"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes"`
	Status            *otlpStatus    `json:"status"`
}

type otlpStatus struct {
	Code int `json:"code"`
}

type otlpKeyValue struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

// ParseStats counts telemetry JSONL lines and unusable spans so operators
// can tell "no traces" from "file present but partially unreadable".
type ParseStats struct {
	NonEmptyLines int
	SkippedLines  int // whole JSONL lines that failed to unmarshal
	SkippedSpans  int // spans inside a valid line that failed convertSpan
	// Incomplete is set when the scanner stopped early (e.g. bufio.ErrTooLong)
	// but some traces were still recovered. Callers should warn; MeasureAndExport
	// still scores those traces and returns a nil error.
	Incomplete string
	// RemoteExportWarning is set when portable OTLP score export failed
	// after local JSONL persistence. Measurements stay fail-open.
	RemoteExportWarning string
}

// ParseTelemetryFile reads OTLP JSON TracesData lines from run-telemetry.jsonl
// and merges spans by trace id. Truncated or corrupt lines, and spans that
// fail conversion, are skipped (fail-open). SkippedLines counts whole-line
// failures; SkippedSpans counts per-span convert failures. An oversized-line
// scanner error returns traces gathered so far alongside the error so the
// caller can still score them.
func ParseTelemetryFile(path string) ([]Trace, ParseStats, error) {
	var stats ParseStats
	f, err := os.Open(path)
	if err != nil {
		return nil, stats, err
	}
	defer f.Close()

	byID := make(map[string]*Trace)
	var order []string

	sc := bufio.NewScanner(f)
	// Spans can be large; raise buffer.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 10*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		stats.NonEmptyLines++
		var doc otlpTracesData
		if err := json.Unmarshal(line, &doc); err != nil {
			stats.SkippedLines++
			continue
		}
		for _, rs := range doc.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, raw := range ss.Spans {
					sp, err := convertSpan(raw)
					if err != nil {
						stats.SkippedSpans++
						continue
					}
					tr, ok := byID[sp.TraceID]
					if !ok {
						tr = &Trace{TraceID: sp.TraceID}
						byID[sp.TraceID] = tr
						order = append(order, sp.TraceID)
					}
					tr.Spans = append(tr.Spans, sp)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		// Oversized line (bufio.ErrTooLong) or other scanner error: keep
		// traces already parsed and surface the error to the caller.
		out := make([]Trace, 0, len(order))
		for _, id := range order {
			out = append(out, *byID[id])
		}
		return out, stats, err
	}

	out := make([]Trace, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, stats, nil
}

func convertSpan(raw otlpSpan) (Span, error) {
	start, err := parseUint(raw.StartTimeUnixNano)
	if err != nil {
		return Span{}, fmt.Errorf("startTimeUnixNano: %w", err)
	}
	end, err := parseUint(raw.EndTimeUnixNano)
	if err != nil {
		return Span{}, fmt.Errorf("endTimeUnixNano: %w", err)
	}
	attrs := make(map[string]any, len(raw.Attributes))
	for _, kv := range raw.Attributes {
		if kv.Key == "" {
			continue
		}
		if v, ok := decodeAny(kv.Value); ok {
			attrs[kv.Key] = v
		}
	}
	status := 0
	if raw.Status != nil {
		status = raw.Status.Code
	}
	return Span{
		TraceID:       raw.TraceID,
		SpanID:        raw.SpanID,
		ParentSpanID:  raw.ParentSpanID,
		Name:          raw.Name,
		StartUnixNano: start,
		EndUnixNano:   end,
		StatusCode:    status,
		Attrs:         attrs,
	}, nil
}

func parseUint(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, 64)
}

// decodeAny extracts scalar OTLP attribute values. arrayValue and kvlistValue
// are intentionally unsupported — fitness scoring uses only scalar attributes.
func decodeAny(m map[string]any) (any, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m["stringValue"]; ok {
		return fmt.Sprint(v), true
	}
	if v, ok := m["boolValue"]; ok {
		switch t := v.(type) {
		case bool:
			return t, true
		default:
			return nil, false
		}
	}
	if v, ok := m["doubleValue"]; ok {
		switch t := v.(type) {
		case float64:
			return t, true
		case string:
			n, err := strconv.ParseFloat(t, 64)
			if err != nil {
				return nil, false
			}
			return n, true
		default:
			return nil, false
		}
	}
	if v, ok := m["intValue"]; ok {
		switch t := v.(type) {
		case string:
			n, err := strconv.ParseInt(t, 10, 64)
			if err != nil {
				return nil, false
			}
			return n, true
		case float64:
			return int64(t), true
		default:
			return nil, false
		}
	}
	return nil, false
}
