package artifacts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
)

// StagedSkipExplanation replaces the explanation of every skip row in the
// staged eval-measurements.jsonl.
const StagedSkipExplanation = "skipped; explanation removed from the staged copy"

// StripMeasurements returns a copy of an eval-measurements.jsonl file
// (one evalmeasure.EvaluationResult per line) in which every skip row's
// explanation is replaced with StagedSkipExplanation. The trace_fitness
// and run_health scorers append the pre-script's free-text skip reason to
// the explanation of their skip rows; the rows are found by label, not by
// their text, because a measurement manifest can rename either scorer.
//
// Every row is decoded and re-encoded: it keeps the same fields and values
// (numbers exactly, HTML characters unescaped), but key order and spacing
// may differ from the input. A skip row without an explanation gets none.
// Blank lines are dropped. A record that is not a JSON object with a
// string label is an error, so the caller leaves the file out instead of
// publishing it unchanged.
func StripMeasurements(data []byte) ([]byte, error) {
	var out bytes.Buffer
	reader := bufio.NewReader(bytes.NewReader(data))
	lineNo := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				if err := stripMeasurement(&out, trimmed); err != nil {
					return nil, fmt.Errorf("record %d: %w", lineNo, err)
				}
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

// stripMeasurement writes one re-encoded row, newline-terminated, to out.
func stripMeasurement(out *bytes.Buffer, record []byte) error {
	dec := json.NewDecoder(bytes.NewReader(record))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("parsing: %w", err)
	}
	if dec.More() {
		return errors.New("trailing data after record")
	}
	if root == nil {
		return errors.New("record is not a JSON object")
	}
	label, ok := root["label"].(string)
	if !ok {
		return errors.New("record has no string label")
	}
	if _, has := root["explanation"]; has && label == evalmeasure.LabelSkip {
		root["explanation"] = StagedSkipExplanation
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return enc.Encode(root) // Encode appends the newline
}
