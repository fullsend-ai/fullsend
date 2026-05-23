package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNoRawPrinterStepCalls ensures that run.go does not call
// printer.StepStart/StepDone/StepFail/StepWarn directly within
// runAgent(). All lifecycle step output must go through the
// InstrumentedPrinter (ip.StepStart etc.) so that every printed
// step is automatically captured as a telemetry event.
//
// Helper functions that accept *ui.Printer as a parameter (like
// runHeartbeat, runOIDCRefresh) are exempt — they handle progress
// indicators, not lifecycle steps.
func TestNoRawPrinterStepCalls(t *testing.T) {
	data, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("reading run.go: %v", err)
	}
	source := string(data)

	// Extract the runAgent function body (from signature to next top-level func).
	startIdx := strings.Index(source, "func runAgent(")
	if startIdx == -1 {
		t.Fatal("could not find runAgent function")
	}

	// Find the end of runAgent — the next top-level "func " at column 0.
	body := source[startIdx:]
	endIdx := strings.Index(body[1:], "\nfunc ")
	if endIdx == -1 {
		body = body[1:]
	} else {
		body = body[:endIdx+1]
	}

	// These patterns indicate raw printer usage that bypasses telemetry.
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`printer\.StepStart\(`),
		regexp.MustCompile(`printer\.StepDone\(`),
		regexp.MustCompile(`printer\.StepFail\(`),
		regexp.MustCompile(`printer\.StepWarn\(`),
	}

	for _, re := range forbidden {
		matches := re.FindAllStringIndex(body, -1)
		if len(matches) > 0 {
			for _, m := range matches {
				line := 1 + strings.Count(body[:m[0]], "\n")
				t.Errorf("run.go:runAgent: raw %s call at line ~%d — use ip.StepStart/StepDone/StepFail/StepWarn instead",
					re.String(), line)
			}
		}
	}
}

// TestNoRecStepHelperCalls ensures the old recStep/recDone/recFail/recWarn
// closure pattern has been fully removed from runAgent.
func TestNoRecStepHelperCalls(t *testing.T) {
	data, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("reading run.go: %v", err)
	}
	source := string(data)

	startIdx := strings.Index(source, "func runAgent(")
	if startIdx == -1 {
		t.Fatal("could not find runAgent function")
	}

	body := source[startIdx:]
	endIdx := strings.Index(body[1:], "\nfunc ")
	if endIdx == -1 {
		body = body[1:]
	} else {
		body = body[:endIdx+1]
	}

	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`recStep\(`),
		regexp.MustCompile(`recDone\(`),
		regexp.MustCompile(`recFail\(`),
		regexp.MustCompile(`recWarn\(`),
	}

	for _, re := range forbidden {
		matches := re.FindAllStringIndex(body, -1)
		if len(matches) > 0 {
			for _, m := range matches {
				line := 1 + strings.Count(body[:m[0]], "\n")
				t.Errorf("run.go:runAgent: old %s helper at line ~%d — this pattern is replaced by ip.StepStart/StepDone/StepFail/StepWarn",
					re.String(), line)
			}
		}
	}
}
