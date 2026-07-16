package scm

import (
	"fmt"
	"strings"
)

// ParseRepo splits "owner/repo" into owner and repo name.
func ParseRepo(fullName string) (owner, repo string, err error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository %q: expected owner/repo", fullName)
	}
	return parts[0], parts[1], nil
}
