package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/mintclient"
)

func TestMintTokenCmd_RequiredFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			"missing role",
			[]string{"--repos", "my-repo", "--mint-url", "http://mint"},
			`required flag(s) "role" not set`,
		},
		{
			"missing repos",
			[]string{"--role", "triage", "--mint-url", "http://mint"},
			`required flag(s) "repos" not set`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newMintTokenCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); got != tt.wantErr {
				t.Errorf("error = %q, want %q", got, tt.wantErr)
			}
		})
	}
}

func TestMintTokenCmd_MintURLFallback(t *testing.T) {
	t.Setenv("FULLSEND_MINT_URL", "")
	cmd := newMintTokenCmd()
	cmd.SetArgs([]string{"--role", "triage", "--repos", "my-repo"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	want := "--mint-url or FULLSEND_MINT_URL required"
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestMintTokenCmd_EmptyRepos(t *testing.T) {
	cmd := newMintTokenCmd()
	cmd.SetArgs([]string{"--role", "triage", "--repos", ",,,", "--mint-url", "http://mint"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	want := "--repos must contain at least one repo name"
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestMintTokenCmd_InvalidRepoName(t *testing.T) {
	cmd := newMintTokenCmd()
	cmd.SetArgs([]string{"--role", "triage", "--repos", "valid-repo,bad repo!", "--mint-url", "https://mint.example.com"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid repo name") {
		t.Errorf("error = %q, want to contain 'invalid repo name'", err.Error())
	}
}

func TestMintTokenCmd_InvalidRole(t *testing.T) {
	cmd := newMintTokenCmd()
	cmd.SetArgs([]string{"--role", "UPPER", "--repos", "r", "--mint-url", "https://mint.example.com"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid role") {
		t.Errorf("error = %q, want to contain 'invalid role'", err.Error())
	}
}

func TestMintTokenCmd_RoleAliasResolved(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(struct {
			Value string `json:"value"`
		}{Value: "oidc-jwt"})
	}))
	defer oidcServer.Close()

	var gotRole string
	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Role string `json:"role"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotRole = body.Role
		json.NewEncoder(w).Encode(mintclient.MintResult{
			Token:     "ghu_test",
			ExpiresAt: "2026-06-11T23:30:00Z",
		})
	}))
	defer mintServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"?d=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	var stdout bytes.Buffer
	cmd := newMintTokenCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--role", "fix", "--repos", "r", "--mint-url", mintServer.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRole != "coder" {
		t.Errorf("role sent to mint = %q, want %q (alias 'fix' should resolve to 'coder')", gotRole, "coder")
	}
}

func TestMintTokenCmd_SuccessPath(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(struct {
			Value string `json:"value"`
		}{Value: "oidc-jwt"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mintclient.MintResult{
			Token:     "ghu_cli_test_token",
			ExpiresAt: "2026-06-11T23:30:00Z",
		})
	}))
	defer mintServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"?d=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	var stdout bytes.Buffer
	cmd := newMintTokenCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--role", "triage", "--repos", "my-repo,other-repo", "--mint-url", mintServer.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := stdout.String(); got != "ghu_cli_test_token" {
		t.Errorf("stdout = %q, want %q", got, "ghu_cli_test_token")
	}
}

func TestMintTokenCmd_ReposTrimsWhitespace(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(struct {
			Value string `json:"value"`
		}{Value: "jwt"})
	}))
	defer oidcServer.Close()

	var gotRepos []string
	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Repos []string `json:"repos"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotRepos = body.Repos
		json.NewEncoder(w).Encode(mintclient.MintResult{
			Token:     "tok",
			ExpiresAt: "2026-01-01T00:00:00Z",
		})
	}))
	defer mintServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"?d=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	var stdout bytes.Buffer
	cmd := newMintTokenCmd()
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--role", "triage", "--repos", " repo-a , repo-b ", "--mint-url", mintServer.URL})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotRepos) != 2 || gotRepos[0] != "repo-a" || gotRepos[1] != "repo-b" {
		t.Errorf("repos = %v, want [repo-a repo-b]", gotRepos)
	}
}

func TestMintTokenCmd_RejectsInvalidTokenChars(t *testing.T) {
	oidcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(struct {
			Value string `json:"value"`
		}{Value: "jwt"})
	}))
	defer oidcServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mintclient.MintResult{
			Token:     "bad\ntoken::set-env name=FOO::bar",
			ExpiresAt: "2026-01-01T00:00:00Z",
		})
	}))
	defer mintServer.Close()

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidcServer.URL+"?d=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "tok")

	cmd := newMintTokenCmd()
	cmd.SetArgs([]string{"--role", "triage", "--repos", "r", "--mint-url", mintServer.URL})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for token with invalid characters")
	}
	if !strings.Contains(err.Error(), "unexpected characters") {
		t.Errorf("error = %q, want to contain 'unexpected characters'", err.Error())
	}
}

func TestMintTokenCmd_MintTokenError(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	cmd := newMintTokenCmd()
	cmd.SetArgs([]string{"--role", "triage", "--repos", "r", "--mint-url", "https://mint.example.com"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ACTIONS_ID_TOKEN_REQUEST_URL") {
		t.Errorf("error = %q, want to contain ACTIONS_ID_TOKEN_REQUEST_URL", err.Error())
	}
}
