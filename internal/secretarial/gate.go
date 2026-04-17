package secretarial

import (
	"fmt"
	"strings"
)

// GateConfig controls the deterministic security gate that sits between
// LLM output and any GitHub write. Every field has a safe default.
type GateConfig struct {
	MinConfidence      float64 // reject topics below this threshold (default 0.6)
	MaxCommentLen      int     // reject comments longer than this (default 2000 chars)
	MaxIssueBodyLen    int     // reject new issue bodies longer than this (default 15000 chars)
	MaxTitleLen        int     // reject issue titles longer than this (default 200 chars)
	AllowCodeBlocks    bool    // meeting summaries should never contain code blocks
}

// DefaultGateConfig returns conservative defaults.
func DefaultGateConfig() GateConfig {
	return GateConfig{
		MinConfidence:   0.6,
		MaxCommentLen:   2000,
		MaxIssueBodyLen: 15000,
		MaxTitleLen:     200,
		AllowCodeBlocks: false,
	}
}

// Rejection describes why a topic was blocked by the security gate.
type Rejection struct {
	Topic  string
	Reason string
}

func (r Rejection) Error() string {
	return fmt.Sprintf("gate rejected %q: %s", r.Topic, r.Reason)
}

// ValidateForPublishing runs deterministic checks on a topic before it can
// be written to GitHub. This is the hard security boundary — the LLM is
// untrusted, and this function decides whether the output is safe to publish.
//
// Design principle: REJECT, never redact-and-post. If anything looks wrong,
// the entire action is dropped. A missed regex in scrub-and-post leaks data;
// a missed regex here means we over-reject (safe failure mode).
func ValidateForPublishing(t Topic, cfg GateConfig) *Rejection {
	if t.Confidence < cfg.MinConfidence {
		return &Rejection{t.Title, fmt.Sprintf("confidence %.2f below threshold %.2f", t.Confidence, cfg.MinConfidence)}
	}

	if ContainsSensitiveContent(t.Summary) {
		return &Rejection{t.Title, "summary contains sensitive content (PII, secrets, or suspicious patterns)"}
	}

	if ContainsSensitiveContent(t.Title) {
		return &Rejection{t.Title, "title contains sensitive content"}
	}

	if t.NewIssueTitle != nil && ContainsSensitiveContent(*t.NewIssueTitle) {
		return &Rejection{t.Title, "new issue title contains sensitive content"}
	}

	isNewIssue := t.NewIssueTitle != nil

	if !cfg.AllowCodeBlocks && !isNewIssue && containsCodeBlock(t.Summary) {
		return &Rejection{t.Title, "summary contains a code block (unexpected in meeting notes)"}
	}

	maxLen := cfg.MaxCommentLen
	if isNewIssue {
		maxLen = cfg.MaxIssueBodyLen
	}
	if len(t.Summary) > maxLen {
		return &Rejection{t.Title, fmt.Sprintf("summary length %d exceeds max %d", len(t.Summary), maxLen)}
	}

	if t.NewIssueTitle != nil && len(*t.NewIssueTitle) > cfg.MaxTitleLen {
		return &Rejection{t.Title, fmt.Sprintf("issue title length %d exceeds max %d", len(*t.NewIssueTitle), cfg.MaxTitleLen)}
	}

	if containsSuspiciousUnicode(t.Summary) || containsSuspiciousUnicode(t.Title) {
		return &Rejection{t.Title, "contains suspicious Unicode (potential prompt injection)"}
	}

	return nil
}

func containsCodeBlock(s string) bool {
	return strings.Contains(s, "```")
}

func containsSuspiciousUnicode(s string) bool {
	return suspiciousUnicode.MatchString(s)
}
