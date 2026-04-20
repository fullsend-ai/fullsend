// Package security provides input sanitization and output scanning for
// fullsend's agent entrypoints. These scanners run at the boundaries:
//
//   - Input boundary: before untrusted text (issue bodies, PR descriptions,
//     code comments, context files) reaches agent processing
//   - Output boundary: before agent-generated text (PR comments, issue
//     comments) is posted via the forge API
//
// The scanners are adapted from Hermes Agent's production security controls,
// ported to Go for integration into fullsend's CLI entrypoints.
//
// See: experiments/hermes-security-patterns/ for the Python prototypes
// and evaluation results.
package security

// Finding represents a single security issue detected by a scanner.
type Finding struct {
	Scanner  string // "secret_redactor", "ssrf_validator", "context_injection", "unicode_normalizer"
	Name     string // pattern name or category
	Severity string // "critical", "high", "medium"
	Detail   string // human-readable description
	Position int    // byte offset in original text, -1 if N/A
}

// ScanResult holds the outcome of a security scan.
type ScanResult struct {
	Safe      bool
	Findings  []Finding
	Sanitized string // cleaned/redacted version of input (empty if unchanged)
}

// Scanner is the interface for all security scanners.
type Scanner interface {
	// Name returns the scanner identifier.
	Name() string

	// Scan checks text for security issues. Returns a ScanResult with
	// findings and optionally a sanitized version of the input.
	Scan(text string) ScanResult
}

// Pipeline chains multiple scanners in sequence. Each scanner's sanitized
// output feeds into the next scanner's input.
type Pipeline struct {
	scanners []Scanner
}

// NewPipeline creates a scanner pipeline from the given scanners.
// Scanners run in order; place normalizers first, detectors after.
func NewPipeline(scanners ...Scanner) *Pipeline {
	return &Pipeline{scanners: scanners}
}

// Scan runs all scanners in sequence. Returns the aggregate result.
// The pipeline is fail-open for sanitization (each scanner transforms
// the text) but fail-closed for safety (any scanner marking unsafe
// makes the whole result unsafe).
func (p *Pipeline) Scan(text string) ScanResult {
	aggregate := ScanResult{Safe: true, Sanitized: text}
	current := text

	for _, s := range p.scanners {
		result := s.Scan(current)

		aggregate.Findings = append(aggregate.Findings, result.Findings...)
		if !result.Safe {
			aggregate.Safe = false
		}
		if result.Sanitized != "" {
			current = result.Sanitized
			aggregate.Sanitized = current
		}
	}

	if aggregate.Sanitized == text {
		aggregate.Sanitized = "" // no changes
	}

	return aggregate
}

// InputPipeline returns the standard input scanning pipeline for
// untrusted text entering the agent. Order matters:
//  1. UnicodeNormalizer — strip invisible chars, normalize fullwidth
//  2. ContextInjectionScanner — detect prompt injection patterns
func InputPipeline() *Pipeline {
	return NewPipeline(
		NewUnicodeNormalizer(),
		NewContextInjectionScanner(),
	)
}

// OutputPipeline returns the standard output scanning pipeline for
// agent-generated text before posting to the forge.
//  1. SecretRedactor — redact API keys, tokens, credentials
func OutputPipeline() *Pipeline {
	return NewPipeline(
		NewSecretRedactor(),
	)
}
