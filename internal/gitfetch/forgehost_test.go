package gitfetch

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// authRecordingRemote serves a bare repo "o/r.git" over HTTPS via
// git-http-backend and records whether any request carried an
// Authorization header. GIT_CONFIG_GLOBAL points git at a config that
// rewrites https://github.com/ to the local server (url.insteadOf), so a
// github.com clone URL is fetched from it without network access while
// FetchTree still sees the github.com URL.
type authRecordingRemote struct {
	url string
	sha string

	mu        sync.Mutex
	sawAuth   bool
	sawAnyReq bool
}

func (r *authRecordingRemote) authSent(t *testing.T) bool {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sawAnyReq {
		t.Fatal("remote received no requests")
	}
	return r.sawAuth
}

func newAuthRecordingRemote(t *testing.T) *authRecordingRemote {
	t.Helper()
	gitBackend, err := exec.LookPath("git-http-backend")
	if err != nil {
		execPath, epErr := exec.Command("git", "--exec-path").Output()
		if epErr != nil {
			t.Skip("git-http-backend not available")
		}
		gitBackend = filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
		if _, sErr := os.Stat(gitBackend); sErr != nil {
			t.Skip("git-http-backend not available")
		}
	}

	gitCmd := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), testGitEnv()...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	root := t.TempDir()
	bare := filepath.Join(root, "o", "r.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(bare, "init", "--bare")
	gitCmd(bare, "config", "uploadpack.allowFilter", "true")

	workDir := t.TempDir()
	gitCmd(workDir, "clone", bare, "work")
	work := filepath.Join(workDir, "work")
	if err := os.MkdirAll(filepath.Join(work, "skills", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "skills", "review", "SKILL.md"), []byte("# Review"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(work, "add", ".")
	gitCmd(work, "commit", "-m", "init")
	gitCmd(work, "push", "origin", "HEAD")
	gitCmd(bare, "update-server-info")

	r := &authRecordingRemote{sha: gitCmd(work, "rev-parse", "HEAD")}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.sawAnyReq = true
		if req.Header.Get("Authorization") != "" {
			r.sawAuth = true
		}
		r.mu.Unlock()
		(&cgi.Handler{
			Path: gitBackend,
			Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		}).ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)
	r.url = ts.URL

	certFile := filepath.Join(t.TempDir(), "cert.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", certFile)

	globalCfg := filepath.Join(t.TempDir(), "gitconfig")
	cfg := "[url \"" + ts.URL + "/\"]\n\tinsteadOf = https://github.com/\n"
	if err := os.WriteFile(globalCfg, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return r
}

func fetchReview(t *testing.T, cloneURL, sha string) {
	t.Helper()
	files, err := FetchTree(context.Background(), cloneURL, "skills/review", sha, "run-token")
	if err != nil {
		t.Fatalf("FetchTree: %v", err)
	}
	if string(files["SKILL.md"]) != "# Review" {
		t.Fatalf("unexpected content: %q", files["SKILL.md"])
	}
}

// (a) The clone URL is on the configured forge host: the token is sent.
func TestFetchTree_TokenSentToConfiguredForgeHost(t *testing.T) {
	r := newAuthRecordingRemote(t)
	t.Setenv("GITHUB_SERVER_URL", r.url)
	fetchReview(t, r.url+"/o/r.git", r.sha)
	if !r.authSent(t) {
		t.Error("token must be sent to the configured forge host")
	}
}

// (b) The configured forge is another host and the clone URL is
// github.com: no Authorization header is sent.
func TestFetchTree_TokenWithheldFromOtherHost(t *testing.T) {
	r := newAuthRecordingRemote(t)
	t.Setenv("GITHUB_SERVER_URL", "https://ghe.example.com")
	fetchReview(t, "https://github.com/o/r.git", r.sha)
	if r.authSent(t) {
		t.Error("the configured forge's token must not be sent to github.com")
	}
}

// (c) No configured forge, or github.com: the token is sent to
// github.com as before.
func TestFetchTree_TokenSentToGitHubByDefault(t *testing.T) {
	for _, serverURL := range []string{"", "https://github.com"} {
		t.Run("GITHUB_SERVER_URL="+serverURL, func(t *testing.T) {
			r := newAuthRecordingRemote(t)
			t.Setenv("GITHUB_SERVER_URL", serverURL)
			fetchReview(t, "https://github.com/o/r.git", r.sha)
			if !r.authSent(t) {
				t.Error("token must still be sent to github.com when no other forge is configured")
			}
		})
	}
}

func TestTokenForCloneURL(t *testing.T) {
	tests := []struct {
		name, cloneURL, token, forgeURL, want string
	}{
		// Default scope: unset or github.com sends the token to github.com only.
		{"default: github.com", "https://github.com/o/r.git", "tok", "", "tok"},
		{"default: GITHUB.COM any case", "https://GITHUB.COM/o/r.git", "tok", "", "tok"},
		{"default: github.com explicit 443", "https://github.com:443/o/r.git", "tok", "", "tok"},
		{"default: github.com on another port", "https://github.com:8443/o/r.git", "tok", "", ""},
		{"default: github.com over http", "http://github.com/o/r.git", "tok", "", ""},
		{"default: gitlab.com", "https://gitlab.com/o/r.git", "tok", "", ""},
		{"default: other host", "https://ghe.example.com/o/r.git", "tok", "", ""},
		{"github.com forge: github.com", "https://github.com/o/r.git", "tok", "https://github.com", "tok"},
		{"github.com forge: gitlab.com", "https://gitlab.com/o/r.git", "tok", "https://github.com/", ""},
		// A value without a scheme or one that fails to parse counts as
		// unset, as elsewhere in fullsend.
		{"schemeless github.com is the default", "https://github.com/o/r.git", "tok", "github.com", "tok"},
		{"schemeless host is the default", "https://ghe.example.com/o/r.git", "tok", "ghe.example.com", ""},
		{"unparseable forge is the default", "https://github.com/o/r.git", "tok", "https://ghe.example.com:bad port", "tok"},
		// Another configured host: the token goes to that origin only.
		{"same host", "https://ghe.example.com/o/r.git", "tok", "https://ghe.example.com", "tok"},
		{"same host mixed case", "https://GHE.example.com/o/r.git", "tok", "https://ghe.EXAMPLE.com/", "tok"},
		{"default port equals explicit 443", "https://ghe.example.com:443/o/r.git", "tok", "https://ghe.example.com", "tok"},
		{"same host and port", "https://ghe.example.com:8443/o/r.git", "tok", "https://ghe.example.com:8443", "tok"},
		{"configured :8443, clone on default port", "https://ghe.example.com/o/r.git", "tok", "https://ghe.example.com:8443", ""},
		{"configured default port, clone on :8443", "https://ghe.example.com:8443/o/r.git", "tok", "https://ghe.example.com", ""},
		{"github.com", "https://github.com/o/r.git", "tok", "https://ghe.example.com", ""},
		{"gitlab.com", "https://gitlab.com/o/r.git", "tok", "https://ghe.example.com", ""},
		{"scheme mismatch", "http://ghe.example.com/o/r.git", "tok", "https://ghe.example.com", ""},
		{"userinfo naming the forge", "https://ghe.example.com@evil.com/x", "tok", "https://ghe.example.com", ""},
		{"userinfo on the forge host", "https://user@ghe.example.com/o/r.git", "tok", "https://ghe.example.com", ""},
		{"trailing dot", "https://ghe.example.com./o/r.git", "tok", "https://ghe.example.com", ""},
		{"empty token", "https://ghe.example.com/o/r.git", "", "https://ghe.example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenForCloneURL(tt.cloneURL, tt.token, tt.forgeURL); got != tt.want {
				t.Errorf("tokenForCloneURL(%q, %q) = %q, want %q", tt.cloneURL, tt.forgeURL, got, tt.want)
			}
		})
	}
}
