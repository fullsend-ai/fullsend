package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// newScanCmd creates the `fullsend scan` command, designed to run as a
// separate workflow step BEFORE the agent step:
//
//	steps:
//	  - name: Security scan
//	    run: fullsend scan --input payload
//	  - name: Run agent
//	    run: fullsend entrypoint triage --scm github
//
// Reads EVENT_PAYLOAD from the environment, runs all security scanners,
// and exits non-zero if critical findings are detected (blocking the
// agent step). Non-critical findings are sanitized and written to
// GITHUB_OUTPUT for the agent step to consume.
func newScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Security-scan event payloads before agent processing",
		Long: `Runs security scanners on untrusted input from GitHub events.
Designed to run as a workflow step before the agent step.

Exit codes:
  0  — clean or non-critical findings (sanitized output available)
  1  — critical findings detected (blocks agent step)

Reads EVENT_PAYLOAD from environment. Writes sanitized payload to
GITHUB_OUTPUT as 'sanitized_payload' for downstream steps.`,
	}

	cmd.AddCommand(newScanInputCmd())
	cmd.AddCommand(newScanOutputCmd())
	cmd.AddCommand(newScanContextCmd())
	cmd.AddCommand(newScanURLCmd())

	return cmd
}

// newScanInputCmd scans the event payload for injection and unicode tricks.
func newScanInputCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "input",
		Short: "Scan event payload for prompt injection and unicode attacks",
		Long: `Runs the input security pipeline on EVENT_PAYLOAD:
  1. Unicode normalizer — strips invisible chars, NFKC normalization
  2. Context injection scanner — detects prompt injection patterns
  3. SSRF validator — checks URLs in issue/PR bodies

Critical findings exit non-zero. Non-critical findings are sanitized.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			printer.Header("Input Security Scan")

			payload := os.Getenv("EVENT_PAYLOAD")
			if payload == "" || payload == "{}" {
				printer.StepDone("No payload to scan")
				return nil
			}

			fields := extractUntrustedText(payload)
			if len(fields) == 0 {
				printer.StepDone("No untrusted text fields found")
				return nil
			}

			printer.StepStart(fmt.Sprintf("Scanning %d text fields", len(fields)))

			pipeline := security.InputPipeline()
			ssrfValidator := security.NewSSRFValidator()

			var allFindings []security.Finding
			sanitized := make(map[string]string, len(fields))

			for name, text := range fields {
				// Input pipeline (unicode + injection)
				result := pipeline.Scan(text)
				if result.Sanitized != "" {
					sanitized[name] = result.Sanitized
				} else {
					sanitized[name] = text
				}
				allFindings = append(allFindings, result.Findings...)

				// SSRF on body fields — scan sanitized text so fullwidth URLs are caught.
				if strings.Contains(name, "body") {
					ssrfResult := ssrfValidator.Scan(sanitized[name])
					allFindings = append(allFindings, ssrfResult.Findings...)
				}
			}

			// LLM Guard ML scan (fail-open — skips if Python/llm-guard unavailable).
			{
				printer.StepStart("Running LLM Guard ML scan")
				lgScanner := security.NewLLMGuardScanner(0, "") // use defaults
				for name, text := range fields {
					lgResult := lgScanner.Scan(text)
					if !lgResult.Safe {
						for i := range lgResult.Findings {
							lgResult.Findings[i].Detail = fmt.Sprintf("[%s] %s", name, lgResult.Findings[i].Detail)
						}
						allFindings = append(allFindings, lgResult.Findings...)
					}
				}
				printer.StepDone("LLM Guard scan complete")
			}

			// Print findings
			for _, f := range allFindings {
				printer.StepWarn(fmt.Sprintf("[%s] %s: %s", f.Severity, f.Scanner, f.Detail))
			}

			// Generate trace ID for finding correlation.
			traceID := security.GenerateTraceID()

			// Write sanitized payload and trace ID to GITHUB_OUTPUT if available.
			if outputFile := os.Getenv("GITHUB_OUTPUT"); outputFile != "" {
				if err := writeSanitizedOutput(outputFile, sanitized); err != nil {
					printer.StepWarn(fmt.Sprintf("Could not write GITHUB_OUTPUT: %v", err))
				}
				if err := writeGitHubOutput(outputFile, "trace_id", traceID); err != nil {
					printer.StepWarn(fmt.Sprintf("Could not write trace_id to GITHUB_OUTPUT: %v", err))
				}
			}

			if hasCriticalFindings(allFindings) {
				count := countCritical(allFindings)
				printer.StepFail(fmt.Sprintf("BLOCKED: %d critical finding(s) — agent step should not proceed", count))
				return fmt.Errorf("scan blocked: %d critical findings", count)
			}

			if len(allFindings) > 0 {
				printer.StepWarn(fmt.Sprintf("Sanitized: %d finding(s), non-critical — agent may proceed", len(allFindings)))
			} else {
				printer.StepDone("Clean — no findings")
			}

			return nil
		},
	}
}

// newScanOutputCmd scans agent-generated text for leaked secrets.
func newScanOutputCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "output",
		Short: "Scan agent output for leaked secrets before posting",
		Long: `Reads text from stdin and scans for API keys, tokens, credentials,
and sensitive patterns. Outputs the redacted version to stdout.

Usage in a workflow step:
  echo "$AGENT_OUTPUT" | fullsend scan output > safe_output.txt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stderr) // status to stderr, clean output to stdout

			// Read from stdin
			var input strings.Builder
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if n > 0 {
					input.Write(buf[:n])
				}
				if err != nil {
					break
				}
			}

			text := input.String()
			if text == "" {
				return nil
			}

			redactor := security.NewSecretRedactor()
			result := redactor.Scan(text)

			if len(result.Findings) > 0 {
				printer.StepWarn(fmt.Sprintf("Redacted %d secret(s) from agent output", len(result.Findings)))
				for _, f := range result.Findings {
					printer.StepWarn(fmt.Sprintf("  %s: %s", f.Name, f.Detail))
				}
				fmt.Fprint(os.Stdout, result.Sanitized)
			} else {
				printer.StepDone("No secrets found in output")
				fmt.Fprint(os.Stdout, text)
			}

			return nil
		},
	}
}

// newScanContextCmd scans repository context files for injection.
func newScanContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context [file...]",
		Short: "Scan context files (AGENTS.md, CLAUDE.md, etc.) for injection",
		Long: `Scans project context files for prompt injection patterns before
they are loaded into an agent's system prompt.

Pass file paths as arguments, or use --dir to scan a directory.
Only scans known context filenames (AGENTS.md, CLAUDE.md, .cursorrules, etc.).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			printer.Header("Context File Security Scan")

			scanner := security.NewContextInjectionScanner()
			normalizer := security.NewUnicodeNormalizer()
			var allFindings []security.Finding

			for _, path := range args {
				basename := baseName(path)
				if !security.ShouldScan(basename) {
					printer.StepInfo(fmt.Sprintf("Skipping %s (not a known context file)", basename))
					continue
				}

				content, err := os.ReadFile(path)
				if err != nil {
					printer.StepWarn(fmt.Sprintf("Could not read %s: %v", path, err))
					continue
				}

				text := string(content)

				// Normalize first
				normResult := normalizer.Scan(text)
				allFindings = append(allFindings, normResult.Findings...)
				if normResult.Sanitized != "" {
					text = normResult.Sanitized
				}

				// Then scan for injection
				injResult := scanner.Scan(text)
				allFindings = append(allFindings, injResult.Findings...)

				if len(normResult.Findings)+len(injResult.Findings) > 0 {
					printer.StepWarn(fmt.Sprintf("%s: %d finding(s)", path, len(normResult.Findings)+len(injResult.Findings)))
					for _, f := range append(normResult.Findings, injResult.Findings...) {
						printer.StepWarn(fmt.Sprintf("  [%s] %s: %s", f.Severity, f.Name, f.Detail))
					}
				} else {
					printer.StepDone(fmt.Sprintf("%s: clean", path))
				}
			}

			if hasCriticalFindings(allFindings) {
				count := countCritical(allFindings)
				printer.StepFail(fmt.Sprintf("BLOCKED: %d critical finding(s) in context files", count))
				return fmt.Errorf("context scan blocked: %d critical findings", count)
			}

			if len(allFindings) == 0 {
				printer.StepDone("All context files clean")
			}

			return nil
		},
	}
}

// newScanURLCmd validates URLs for SSRF.
func newScanURLCmd() *cobra.Command {
	var resolveDNS bool

	cmd := &cobra.Command{
		Use:   "url [url...]",
		Short: "Validate URLs against SSRF blocklists",
		Long: `Checks URLs against RFC 1918 private networks, cloud metadata
endpoints, CGNAT, loopback, and blocked schemes. Exits non-zero if
any URL is blocked.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(os.Stdout)
			printer.Header("SSRF URL Validation")

			validator := security.NewSSRFValidator()
			blocked := false

			for _, rawURL := range args {
				result := validator.ValidateURL(rawURL, resolveDNS)
				if result.Safe {
					printer.StepDone(fmt.Sprintf("SAFE: %s", rawURL))
				} else {
					blocked = true
					for _, f := range result.Findings {
						printer.StepFail(fmt.Sprintf("BLOCKED: %s — %s", rawURL, f.Detail))
					}
				}
			}

			if blocked {
				return fmt.Errorf("one or more URLs blocked")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&resolveDNS, "resolve-dns", false, "resolve hostnames and check resolved IPs")
	return cmd
}

// --- helpers ---

// extractUntrustedText pulls user-generated text from a GitHub event payload.
func extractUntrustedText(payload string) map[string]string {
	fields := make(map[string]string)

	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		fields["raw_payload"] = payload
		return fields
	}

	extract := func(key string, path ...string) {
		current := any(event)
		for _, p := range path {
			m, ok := current.(map[string]any)
			if !ok {
				return
			}
			current = m[p]
		}
		if s, ok := current.(string); ok && s != "" {
			fields[key] = s
		}
	}

	extract("issue.title", "issue", "title")
	extract("issue.body", "issue", "body")
	extract("pull_request.title", "pull_request", "title")
	extract("pull_request.body", "pull_request", "body")
	extract("comment.body", "comment", "body")
	extract("review.body", "review", "body")

	return fields
}

func hasCriticalFindings(findings []security.Finding) bool {
	for _, f := range findings {
		if f.Severity == "critical" {
			return true
		}
	}
	return false
}

func countCritical(findings []security.Finding) int {
	count := 0
	for _, f := range findings {
		if f.Severity == "critical" {
			count++
		}
	}
	return count
}

func writeSanitizedOutput(outputFile string, sanitized map[string]string) error {
	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(sanitized)
	if err != nil {
		return err
	}

	// Use GitHub Actions multiline delimiter syntax to prevent injection
	// via newlines in the JSON payload. A random delimiter ensures the
	// payload cannot close the block prematurely.
	delimBytes := make([]byte, 16)
	if _, err := rand.Read(delimBytes); err != nil {
		return fmt.Errorf("generating delimiter: %w", err)
	}
	delimiter := fmt.Sprintf("FULLSEND_EOF_%s", hex.EncodeToString(delimBytes))
	_, err = fmt.Fprintf(f, "sanitized_payload<<%s\n%s\n%s\n", delimiter, string(data), delimiter)
	return err
}

func writeGitHubOutput(outputFile, key, value string) error {
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("value for %q contains newlines — use writeSanitizedOutput for multiline values", key)
	}
	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s=%s\n", key, value)
	return err
}

// baseName returns the base filename from a path.
func baseName(path string) string {
	return filepath.Base(path)
}
