package jira

import (
	"testing"
)

func TestParseJiraConfig(t *testing.T) {
	input := []byte(`version: 1
jira_projects:
  - project_key: KFLUXINFRA
    host: stage-redhat.atlassian.net
    linked_github_repos:
      - redhat-appstudio/infra-deployments
`)
	cfg, err := ParseJiraConfig(input)
	if err != nil {
		t.Fatalf("ParseJiraConfig: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	if len(cfg.JiraProjects) != 1 {
		t.Fatalf("JiraProjects len = %d, want 1", len(cfg.JiraProjects))
	}
	p := cfg.JiraProjects[0]
	if p.ProjectKey != "KFLUXINFRA" {
		t.Errorf("ProjectKey = %q, want KFLUXINFRA", p.ProjectKey)
	}
	if p.Host != "stage-redhat.atlassian.net" {
		t.Errorf("Host = %q, want stage-redhat.atlassian.net", p.Host)
	}
	if len(p.LinkedGitHubRepos) != 1 || p.LinkedGitHubRepos[0] != "redhat-appstudio/infra-deployments" {
		t.Errorf("LinkedGitHubRepos = %v, want [redhat-appstudio/infra-deployments]", p.LinkedGitHubRepos)
	}
}

func TestParseJiraConfig_DefaultVersion(t *testing.T) {
	input := []byte(`jira_projects: []`)
	cfg, err := ParseJiraConfig(input)
	if err != nil {
		t.Fatalf("ParseJiraConfig: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1 (default)", cfg.Version)
	}
}

func TestParseJiraConfig_InvalidYAML(t *testing.T) {
	input := []byte(`{{{invalid`)
	_, err := ParseJiraConfig(input)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestHasProject(t *testing.T) {
	cfg := &JiraConfig{
		Version: 1,
		JiraProjects: []JiraProjectConfig{
			{ProjectKey: "PROJ", Host: "example.atlassian.net"},
		},
	}

	if !cfg.HasProject("PROJ", "example.atlassian.net") {
		t.Error("HasProject should return true for enrolled project")
	}
	if cfg.HasProject("PROJ", "other.atlassian.net") {
		t.Error("HasProject should return false for different host")
	}
	if cfg.HasProject("OTHER", "example.atlassian.net") {
		t.Error("HasProject should return false for different key")
	}
}

func TestAddProject_Idempotent(t *testing.T) {
	cfg := NewJiraConfig()
	p := JiraProjectConfig{
		ProjectKey:        "PROJ",
		Host:              "example.atlassian.net",
		LinkedGitHubRepos: []string{"org/repo"},
	}

	added := cfg.AddProject(p)
	if !added {
		t.Error("first AddProject should return true")
	}
	if len(cfg.JiraProjects) != 1 {
		t.Fatalf("JiraProjects len = %d, want 1", len(cfg.JiraProjects))
	}

	added = cfg.AddProject(p)
	if added {
		t.Error("second AddProject should return false (idempotent)")
	}
	if len(cfg.JiraProjects) != 1 {
		t.Fatalf("JiraProjects len = %d, want 1 after duplicate add", len(cfg.JiraProjects))
	}
}

func TestAddProject_DifferentProjects(t *testing.T) {
	cfg := NewJiraConfig()
	cfg.AddProject(JiraProjectConfig{ProjectKey: "PROJ1", Host: "a.atlassian.net"})
	cfg.AddProject(JiraProjectConfig{ProjectKey: "PROJ2", Host: "a.atlassian.net"})
	cfg.AddProject(JiraProjectConfig{ProjectKey: "PROJ1", Host: "b.atlassian.net"})

	if len(cfg.JiraProjects) != 3 {
		t.Errorf("JiraProjects len = %d, want 3", len(cfg.JiraProjects))
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	cfg := NewJiraConfig()
	cfg.AddProject(JiraProjectConfig{
		ProjectKey:        "TEST",
		Host:              "test.atlassian.net",
		LinkedGitHubRepos: []string{"org/repo1", "org/repo2"},
	})

	data, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	cfg2, err := ParseJiraConfig(data)
	if err != nil {
		t.Fatalf("ParseJiraConfig after Marshal: %v", err)
	}
	if len(cfg2.JiraProjects) != 1 {
		t.Fatalf("round-trip: JiraProjects len = %d, want 1", len(cfg2.JiraProjects))
	}
	if cfg2.JiraProjects[0].ProjectKey != "TEST" {
		t.Errorf("round-trip: ProjectKey = %q, want TEST", cfg2.JiraProjects[0].ProjectKey)
	}
	if len(cfg2.JiraProjects[0].LinkedGitHubRepos) != 2 {
		t.Errorf("round-trip: LinkedGitHubRepos len = %d, want 2", len(cfg2.JiraProjects[0].LinkedGitHubRepos))
	}
}
