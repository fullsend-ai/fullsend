package authorization

import (
	"path"
	"strings"
)

// WorkflowFilePatterns returns the registered workflow-file path patterns.
// Initial scope is GitHub Actions only (.github/workflows/).
func WorkflowFilePatterns() []string {
	return []string{".github/workflows/**"}
}

// MatchesAny reports whether filePath matches any of the given glob patterns.
// Patterns use ** for recursive directory matching.
func MatchesAny(filePath string, patterns []string) bool {
	filePath = strings.TrimPrefix(path.Clean(filePath), "./")
	for _, pattern := range patterns {
		if matchPattern(filePath, pattern) {
			return true
		}
	}
	return false
}

func matchPattern(filePath, pattern string) bool {
	pattern = strings.TrimPrefix(path.Clean(pattern), "./")
	if pattern == filePath {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if filePath == prefix {
			return true
		}
		return strings.HasPrefix(filePath, prefix+"/")
	}
	if strings.Contains(pattern, "*") {
		ok, _ := path.Match(pattern, filePath)
		return ok
	}
	return pattern == filePath
}
