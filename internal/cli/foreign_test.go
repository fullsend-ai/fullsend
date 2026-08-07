package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

func TestParseForeignVariableName(t *testing.T) {
	role, ok := parseForeignVariableName("FULLSEND_FOREIGN_E2E_REPOS")
	if !ok || role != "e2e" {
		t.Fatalf("got role=%q ok=%v", role, ok)
	}
	if _, ok := parseForeignVariableName("FULLSEND_MINT_URL"); ok {
		t.Fatal("expected non-foreign name to fail")
	}
	if _, ok := parseForeignVariableName("FULLSEND_FOREIGN_123_REPOS"); ok {
		t.Fatal("expected invalid role suffix to fail")
	}
}

func TestValidateForeignCaller(t *testing.T) {
	if err := validateForeignCaller("fullsend-ai/fullsend"); err != nil {
		t.Fatalf("org/repo: %v", err)
	}
	if err := validateForeignCaller("fullsend-ai"); err != nil {
		t.Fatalf("bare org: %v", err)
	}
	if err := validateForeignCaller("halfsend-01/.fullsend"); err != nil {
		t.Fatalf("dot-prefixed config repo: %v", err)
	}
	if err := validateForeignCaller("bad org/repo"); err == nil {
		t.Fatal("expected invalid caller")
	}
	if err := validateForeignCaller(""); err == nil {
		t.Fatal("expected empty caller error")
	}
}

func TestForeignAllowRevokeHelpers(t *testing.T) {
	list := []string{"a/b", "c"}
	if !containsForeignCaller(list, "a/b") {
		t.Fatal("expected contains")
	}
	if containsForeignCaller(list, "missing") {
		t.Fatal("expected missing")
	}
	updated, changed := removeForeignCaller(list, "a/b")
	if !changed || len(updated) != 1 || updated[0] != "c" {
		t.Fatalf("got %v changed=%v", updated, changed)
	}
	_, changed = removeForeignCaller(list, "missing")
	if changed {
		t.Fatal("expected no change")
	}
}

func TestLoadForeignAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/FULLSEND_FOREIGN_E2E_REPOS"):
			json.NewEncoder(w).Encode(map[string]string{
				"name":  "FULLSEND_FOREIGN_E2E_REPOS",
				"value": "fullsend-ai/fullsend, fullsend-ai",
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.New("token").WithBaseURL(srv.URL)
	got, err := loadForeignAllowlist(context.Background(), client, "pool-org", "FULLSEND_FOREIGN_E2E_REPOS")
	require.NoError(t, err)
	assert.Equal(t, []string{"fullsend-ai/fullsend", "fullsend-ai"}, got)
}

func TestLoadForeignAllowlist_NotSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := gh.New("token").WithBaseURL(srv.URL)
	got, err := loadForeignAllowlist(context.Background(), client, "pool-org", "FULLSEND_FOREIGN_E2E_REPOS")
	require.NoError(t, err)
	assert.Nil(t, got)
}

type foreignVarState struct {
	mu       sync.Mutex
	vars     map[string]string // org-level vars
	repoVars map[string]string // repo-level vars, keyed by "owner/repo/VAR_NAME"
	deleted  []string
}

func (s *foreignVarState) repoVarKey(owner, repo, name string) string {
	return owner + "/" + repo + "/" + name
}

func (s *foreignVarState) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		// Repo-level variable endpoints: /repos/{owner}/{repo}/actions/variables[/{name}]
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			s.handleRepoVars(t, w, r)
			return
		}

		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/actions/variables/FULLSEND_FOREIGN_"):
			name := strings.TrimPrefix(r.URL.Path, "/orgs/pool-org/actions/variables/")
			if val, ok := s.vars[name]; ok {
				json.NewEncoder(w).Encode(map[string]string{"name": name, "value": val})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/orgs/pool-org/actions/variables":
			var out []map[string]string
			for name, val := range s.vars {
				out = append(out, map[string]string{"name": name, "value": val})
			}
			json.NewEncoder(w).Encode(map[string]any{
				"total_count": len(out),
				"variables":   out,
			})
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/actions/variables/"):
			name := strings.TrimPrefix(r.URL.Path, "/orgs/pool-org/actions/variables/")
			var body struct {
				Value string `json:"value"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			s.vars[name] = body.Value
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/pool-org/actions/variables":
			var body struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			s.vars[body.Name] = body.Value
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/actions/variables/"):
			name := strings.TrimPrefix(r.URL.Path, "/orgs/pool-org/actions/variables/")
			delete(s.vars, name)
			s.deleted = append(s.deleted, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

// handleRepoVars dispatches repo-level variable API requests.
// Path format: /repos/{owner}/{repo}/actions/variables[/{name}]
func (s *foreignVarState) handleRepoVars(t *testing.T, w http.ResponseWriter, r *http.Request) {
	// Parse: /repos/{owner}/{repo}/actions/variables[/{name}]
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.SplitN(path, "/", 3) // owner, repo, rest
	if len(parts) < 3 {
		t.Errorf("malformed repo path: %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	owner, repo := parts[0], parts[1]
	rest := parts[2] // "actions/variables" or "actions/variables/{name}"

	switch {
	case r.Method == http.MethodGet && rest == "actions/variables":
		// List repo variables.
		prefix := owner + "/" + repo + "/"
		var out []map[string]string
		for key, val := range s.repoVars {
			if strings.HasPrefix(key, prefix) {
				name := strings.TrimPrefix(key, prefix)
				out = append(out, map[string]string{"name": name, "value": val})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": len(out),
			"variables":   out,
		})
	case r.Method == http.MethodGet && strings.HasPrefix(rest, "actions/variables/"):
		name := strings.TrimPrefix(rest, "actions/variables/")
		key := s.repoVarKey(owner, repo, name)
		if val, ok := s.repoVars[key]; ok {
			json.NewEncoder(w).Encode(map[string]string{"name": name, "value": val})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodPatch && strings.HasPrefix(rest, "actions/variables/"):
		name := strings.TrimPrefix(rest, "actions/variables/")
		key := s.repoVarKey(owner, repo, name)
		var body struct {
			Value string `json:"value"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		s.repoVars[key] = body.Value
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && rest == "actions/variables":
		var body struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		key := s.repoVarKey(owner, repo, body.Name)
		s.repoVars[key] = body.Value
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && strings.HasPrefix(rest, "actions/variables/"):
		name := strings.TrimPrefix(rest, "actions/variables/")
		key := s.repoVarKey(owner, repo, name)
		delete(s.repoVars, key)
		s.deleted = append(s.deleted, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		t.Errorf("unexpected repo %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func runForeignCmd(t *testing.T, srvURL string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_API_URL", srvURL)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"admin", "foreign"}, args...))
	err := root.Execute()
	return buf.String(), err
}

func TestForeignAllowCmd_CreatesVariable(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	out, err := runForeignCmd(t, srv.URL, "allow", "--org", "pool-org", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	_ = out
	assert.Equal(t, "fullsend-ai/fullsend", state.vars["FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignAllowCmd_AppendsCaller(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "konflux-ci",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--org", "pool-org", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.Contains(t, state.vars["FULLSEND_FOREIGN_E2E_REPOS"], "konflux-ci")
	assert.Contains(t, state.vars["FULLSEND_FOREIGN_E2E_REPOS"], "fullsend-ai/fullsend")
}

func TestForeignAllowCmd_AlreadyListed(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--org", "pool-org", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.vars["FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignListCmd_SingleRole(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--org", "pool-org", "--role", "e2e")
	require.NoError(t, err)
}

func TestForeignListCmd_AllForeignVariables(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai",
		"FULLSEND_MINT_URL":          "https://example.com",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--org", "pool-org")
	require.NoError(t, err)
}

func TestForeignListCmd_RoleNotSet(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--org", "pool-org", "--role", "e2e")
	require.NoError(t, err)
}

func TestForeignRevokeCmd_RemovesCaller(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend, konflux-ci",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "revoke", "--org", "pool-org", "--role", "e2e", "--caller", "konflux-ci")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.vars["FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignRevokeCmd_DeletesEmptyVariable(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "revoke", "--org", "pool-org", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.NotContains(t, state.vars, "FULLSEND_FOREIGN_E2E_REPOS")
	assert.Equal(t, []string{"FULLSEND_FOREIGN_E2E_REPOS"}, state.deleted)
}

func TestForeignRevokeCmd_NotPresent(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{
		"FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
	}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "revoke", "--org", "pool-org", "--role", "e2e", "--caller", "missing/repo")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.vars["FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignListCmd_NoForeignVariables(t *testing.T) {
	state := &foreignVarState{vars: map[string]string{"OTHER_VAR": "x"}}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--org", "pool-org")
	require.NoError(t, err)
}

func TestForeignAllowCmd_UpdateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--org", "pool-org", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.Error(t, err)
}

func TestForeignCmd_ValidationErrors(t *testing.T) {
	_, err := runForeignCmd(t, "http://unused", "allow", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--org is required")
}

// --- Repo-level foreign CLI tests ---

func TestResolveForeignTarget(t *testing.T) {
	tests := []struct {
		name     string
		org      string
		repo     string
		wantOrg  string
		wantRepo string
		wantErr  string
	}{
		{"org only", "pool-org", "", "pool-org", "", ""},
		{"full owner/repo", "", "pool-org/target-repo", "pool-org", "target-repo", ""},
		{"full owner/repo with --org match", "pool-org", "pool-org/target-repo", "pool-org", "target-repo", ""},
		{"full owner/repo with --org conflict", "other-org", "pool-org/target-repo", "", "", "--org \"other-org\" conflicts"},
		{"bare repo with --org", "pool-org", "target-repo", "pool-org", "target-repo", ""},
		{"bare repo without --org", "", "target-repo", "", "", "--org is required"},
		{"no flags", "", "", "", "", "--org is required"},
		{"invalid org in owner/repo", "", "bad org/repo", "", "", "invalid owner"},
		{"invalid repo name", "pool-org", "bad repo", "", "", "invalid repo name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveForeignTarget(tc.org, tc.repo)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOrg, got.org)
			assert.Equal(t, tc.wantRepo, got.repo)
		})
	}
}

func TestForeignAllowCmd_RepoLevel(t *testing.T) {
	state := &foreignVarState{
		vars:     map[string]string{},
		repoVars: map[string]string{},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--repo", "pool-org/target-repo", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.repoVars["pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignAllowCmd_RepoLevel_SplitForm(t *testing.T) {
	state := &foreignVarState{
		vars:     map[string]string{},
		repoVars: map[string]string{},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--org", "pool-org", "--repo", "target-repo", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.repoVars["pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignAllowCmd_RepoLevel_AppendsCaller(t *testing.T) {
	state := &foreignVarState{
		vars: map[string]string{},
		repoVars: map[string]string{
			"pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS": "konflux-ci",
		},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--repo", "pool-org/target-repo", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	val := state.repoVars["pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS"]
	assert.Contains(t, val, "konflux-ci")
	assert.Contains(t, val, "fullsend-ai/fullsend")
}

func TestForeignListCmd_RepoLevel_SingleRole(t *testing.T) {
	state := &foreignVarState{
		vars: map[string]string{},
		repoVars: map[string]string{
			"pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
		},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--repo", "pool-org/target-repo", "--role", "e2e")
	require.NoError(t, err)
}

func TestForeignListCmd_RepoLevel_AllVars(t *testing.T) {
	state := &foreignVarState{
		vars: map[string]string{},
		repoVars: map[string]string{
			"pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS":   "fullsend-ai",
			"pool-org/target-repo/FULLSEND_FOREIGN_CODER_REPOS": "acme/api",
			"pool-org/target-repo/OTHER_VAR":                    "ignored",
		},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--repo", "pool-org/target-repo")
	require.NoError(t, err)
}

func TestForeignListCmd_RepoLevel_NoVars(t *testing.T) {
	state := &foreignVarState{
		vars:     map[string]string{},
		repoVars: map[string]string{},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "list", "--repo", "pool-org/target-repo")
	require.NoError(t, err)
}

func TestForeignRevokeCmd_RepoLevel(t *testing.T) {
	state := &foreignVarState{
		vars: map[string]string{},
		repoVars: map[string]string{
			"pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend, konflux-ci",
		},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "revoke", "--repo", "pool-org/target-repo", "--role", "e2e", "--caller", "konflux-ci")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.repoVars["pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignRevokeCmd_RepoLevel_DeletesEmpty(t *testing.T) {
	state := &foreignVarState{
		vars: map[string]string{},
		repoVars: map[string]string{
			"pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
		},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "revoke", "--repo", "pool-org/target-repo", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.NotContains(t, state.repoVars, "pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS")
}

func TestForeignRevokeCmd_RepoLevel_NotPresent(t *testing.T) {
	state := &foreignVarState{
		vars: map[string]string{},
		repoVars: map[string]string{
			"pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS": "fullsend-ai/fullsend",
		},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "revoke", "--repo", "pool-org/target-repo", "--role", "e2e", "--caller", "missing/repo")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.repoVars["pool-org/target-repo/FULLSEND_FOREIGN_E2E_REPOS"])
}

func TestForeignAllowCmd_OrgLevelStillWorks(t *testing.T) {
	// Ensure existing org-level behavior is preserved when --repo is not used.
	state := &foreignVarState{
		vars:     map[string]string{},
		repoVars: map[string]string{},
	}
	srv := httptest.NewServer(state.handler(t))
	defer srv.Close()

	_, err := runForeignCmd(t, srv.URL, "allow", "--org", "pool-org", "--role", "e2e", "--caller", "fullsend-ai/fullsend")
	require.NoError(t, err)
	assert.Equal(t, "fullsend-ai/fullsend", state.vars["FULLSEND_FOREIGN_E2E_REPOS"])
	assert.Empty(t, state.repoVars)
}
