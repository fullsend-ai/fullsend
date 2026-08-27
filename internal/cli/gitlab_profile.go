package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
)

// gitlabForgeEndpoint is the YAML shape of a single provider endpoint.
type gitlabForgeEndpoint struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Protocol    string `yaml:"protocol"`
	Access      string `yaml:"access"`
	Enforcement string `yaml:"enforcement"`
}

// gitlabForgeProfileSpec is the YAML shape of a provider profile.
type gitlabForgeProfileSpec struct {
	ID          string                `yaml:"id"`
	DisplayName string                `yaml:"display_name"`
	Description string                `yaml:"description"`
	Category    string                `yaml:"category"`
	Endpoints   []gitlabForgeEndpoint `yaml:"endpoints"`
	Binaries    []string              `yaml:"binaries"`
}

// generateGitLabForgeProfile creates a temporary provider profile YAML
// for the GitLab forge host, analogous to the scaffold's
// fullsend-github.yaml. Returns the temp file path and a cleanup
// function, or ("", nil, nil) when no GitLab host can be resolved.
func generateGitLabForgeProfile() (string, func(), error) {
	host, port := gl.ResolveForgeHostPort()
	if host == "" {
		return "", nil, nil
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return "", nil, fmt.Errorf("GitLab port %q is not a valid integer", port)
	}

	profile := gitlabForgeProfileSpec{
		ID:          "fullsend-gitlab-forge",
		DisplayName: "Fullsend GitLab (auto)",
		Description: "GitLab API and Git operations for fullsend agents (auto-generated from forge host)",
		Category:    "source_control",
		Endpoints: []gitlabForgeEndpoint{
			{Host: host, Port: portNum, Protocol: "rest", Access: "read-write", Enforcement: "enforce"},
		},
		Binaries: []string{"**/git", "**/glab", "**/node", "**/pre-commit"},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&profile); err != nil {
		return "", nil, fmt.Errorf("marshaling GitLab profile YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", nil, fmt.Errorf("closing YAML encoder: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "fullsend-gitlab-profile-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp dir for GitLab profile: %w", err)
	}
	profilePath := filepath.Join(tmpDir, "fullsend-gitlab-forge.yaml")
	if err := os.WriteFile(profilePath, buf.Bytes(), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("writing GitLab profile: %w", err)
	}
	return profilePath, func() { os.RemoveAll(tmpDir) }, nil
}
