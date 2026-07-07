package jira

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// JiraProjectConfig represents a single Jira project enrollment entry
// in .jira.yml.
type JiraProjectConfig struct {
	ProjectKey        string   `yaml:"project_key"`
	Host              string   `yaml:"host"`
	LinkedGitHubRepos []string `yaml:"linked_github_repos"`
}

// JiraConfig represents the .jira.yml file at the root of an enrolled repo.
type JiraConfig struct {
	Version      int                 `yaml:"version"`
	JiraProjects []JiraProjectConfig `yaml:"jira_projects"`
}

// ParseJiraConfig parses .jira.yml content into a JiraConfig.
func ParseJiraConfig(data []byte) (*JiraConfig, error) {
	var cfg JiraConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing .jira.yml: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	return &cfg, nil
}

// Marshal serializes the config back to YAML with a header comment.
func (c *JiraConfig) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshaling .jira.yml: %w", err)
	}
	return data, nil
}

// HasProject checks whether a project with the given key and host is
// already enrolled.
func (c *JiraConfig) HasProject(projectKey, host string) bool {
	for _, p := range c.JiraProjects {
		if p.ProjectKey == projectKey && p.Host == host {
			return true
		}
	}
	return false
}

// AddProject adds a project to the config if it is not already enrolled.
// Returns true if the project was added, false if it already existed.
func (c *JiraConfig) AddProject(project JiraProjectConfig) bool {
	if c.HasProject(project.ProjectKey, project.Host) {
		return false
	}
	c.JiraProjects = append(c.JiraProjects, project)
	return true
}

// NewJiraConfig returns an empty config with version set.
func NewJiraConfig() *JiraConfig {
	return &JiraConfig{Version: 1}
}
