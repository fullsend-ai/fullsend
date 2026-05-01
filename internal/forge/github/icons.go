package github

import "embed"

//go:embed icons/*.png
var iconFS embed.FS

// roleIcons maps agent roles to their icon filenames.
var roleIcons = map[string]string{
	"fullsend": "icons/bootstrap.png",
	"triage":   "icons/triage.png",
	"coder":    "icons/coder.png",
	"review":   "icons/review.png",
}

// IconForRole returns the embedded PNG icon for the given agent role.
// Returns nil, false if no icon is available for the role.
func IconForRole(role string) ([]byte, bool) {
	filename, ok := roleIcons[role]
	if !ok {
		return nil, false
	}
	data, err := iconFS.ReadFile(filename)
	if err != nil {
		return nil, false
	}
	return data, true
}
