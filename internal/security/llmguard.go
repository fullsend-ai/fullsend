package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// LLMGuardResult holds the output from the LLM Guard Python scanner.
type LLMGuardResult struct {
	IsInjection bool    `json:"is_injection"`
	RiskScore   float64 `json:"risk_score"`
	Detail      string  `json:"detail"`
}

// LLMGuardScanner wraps the Python llm-guard library for ML-based prompt
// injection detection. Runs in Path A (GHA workflow pre-step) and Path B
// (sandbox pre-agent scan) when the base sandbox image includes Python,
// llm-guard, and the pre-downloaded DeBERTa-v3 model.
//
// When Required is true (e.g. inside the sandbox where Python + llm-guard
// are baked into the image), a missing Python binary is treated as tampering
// and the scanner fails closed. When Required is false (e.g. Path A GHA
// pre-step), a missing runtime is logged as a warning and the scanner fails
// open.
type LLMGuardScanner struct {
	Threshold float64
	MatchType string // "sentence" or "full"
	Required  bool   // fail closed when Python/llm-guard unavailable
}

// NewLLMGuardScanner creates a scanner with the given threshold, match type,
// and required flag. When required is true the scanner fails closed if Python
// or llm-guard is unavailable (intended for the sandbox where both are baked
// into the image).
func NewLLMGuardScanner(threshold float64, matchType string, required bool) *LLMGuardScanner {
	if threshold == 0 {
		threshold = 0.92
	}
	if matchType == "" {
		matchType = "sentence"
	}
	return &LLMGuardScanner{
		Threshold: threshold,
		MatchType: matchType,
		Required:  required,
	}
}

// Scan runs the LLM Guard prompt injection scanner on the given text.
// When Required is false, fails open if Python is unavailable. When
// Required is true, fails closed (missing Python treated as tampering).
func (s *LLMGuardScanner) Scan(text string) ScanResult {
	script := fmt.Sprintf(`
import json, sys
try:
    from llm_guard.input_scanners import PromptInjection
    from llm_guard.input_scanners.prompt_injection import MatchType
    mt = MatchType.SENTENCE if %q == "sentence" else MatchType.FULL
    scanner = PromptInjection(threshold=%f, match_type=mt, use_onnx=True)
    sanitized, is_valid, risk_score = scanner.scan("", text=sys.stdin.read())
    json.dump({"is_injection": not is_valid, "risk_score": risk_score, "detail": "LLM Guard ML scan"}, sys.stdout)
except ImportError:
    import os
    if os.environ.get("LLM_GUARD_REQUIRED", "") == "1":
        json.dump({"is_injection": True, "risk_score": 1.0, "detail": "llm-guard not installed but LLM_GUARD_REQUIRED=1"}, sys.stdout)
        sys.exit(1)
    json.dump({"is_injection": False, "risk_score": 0, "detail": "llm-guard not installed"}, sys.stdout)
except Exception as e:
    json.dump({"is_injection": True, "risk_score": 1.0, "detail": "scanner error (fail-closed): " + str(e)}, sys.stdout)
`, s.MatchType, s.Threshold)

	cmd := exec.Command("python3", "-c", script)
	cmd.Stdin = strings.NewReader(text)
	if s.Required {
		cmd.Env = append(os.Environ(), "LLM_GUARD_REQUIRED=1")
	}

	output, err := cmd.Output()
	if err != nil {
		// Fail open only when python3 is not installed.
		// Script errors (non-zero exit) may still have JSON output — continue parsing.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			if s.Required {
				return ScanResult{
					Safe: false,
					Findings: []Finding{{
						Scanner:  "llm_guard",
						Name:     "python_unavailable",
						Severity: "critical",
						Detail:   "Python not available but LLM Guard is required (possible tampering)",
						Position: -1,
					}},
				}
			}
			// Python not available — fail open with logged warning.
			fmt.Fprintln(os.Stderr, "WARN: LLM Guard skipped — python3 not available")
			return ScanResult{Safe: true}
		}
	}

	var result LLMGuardResult
	if err := json.Unmarshal(output, &result); err != nil {
		// Cannot parse scanner output — fail closed.
		return ScanResult{
			Safe: false,
			Findings: []Finding{{
				Scanner:  "llm_guard",
				Name:     "scanner_error",
				Severity: "high",
				Detail:   "LLM Guard returned unparseable output (fail-closed)",
				Position: -1,
			}},
		}
	}

	if result.IsInjection {
		return ScanResult{
			Safe: false,
			Findings: []Finding{
				{
					Scanner:  "llm_guard",
					Name:     "prompt_injection_ml",
					Severity: "critical",
					Detail:   fmt.Sprintf("LLM Guard detected injection (risk_score=%.3f, threshold=%.3f)", result.RiskScore, s.Threshold),
					Position: -1,
				},
			},
		}
	}

	return ScanResult{Safe: true}
}

// Name returns the scanner identifier.
func (s *LLMGuardScanner) Name() string {
	return "llm_guard"
}
