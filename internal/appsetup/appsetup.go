// Package appsetup automates GitHub App creation and installation
// using the manifest flow.
//
// The flow works as follows:
//  1. Start a local HTTP server to receive the redirect
//  2. Open the user's browser to a page that POSTs the app manifest to GitHub
//  3. GitHub shows the app creation form; user clicks "Create GitHub App"
//  4. GitHub redirects to our local server with a temporary code
//  5. Exchange the code for app credentials (ID, PEM, webhook secret)
//  6. Prompt user to install the app on their organization
//  7. Verify the installation exists
package appsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// AppCredentials holds the result of creating a GitHub App via the manifest flow.
type AppCredentials struct {
	// WebhookSecret is the webhook secret for verifying payloads.
	WebhookSecret *string `json:"webhook_secret"`

	// Slug is the URL-friendly name of the app (used in installation URLs).
	Slug string `json:"slug"`

	// Name is the display name of the app.
	Name string `json:"name"`

	// ClientID is the OAuth client ID.
	ClientID string `json:"client_id"`

	// ClientSecret is the OAuth client secret.
	ClientSecret string `json:"client_secret"`

	// PEM is the private key in PEM format.
	PEM string `json:"pem"`

	// HTMLURL is the URL to the app's settings page.
	HTMLURL string `json:"html_url"`

	// ID is the GitHub App ID.
	ID int `json:"id"`
}

// Prompter reads user input from the terminal.
// Abstracted for testing.
type Prompter interface {
	// WaitForEnter prints a message and blocks until the user presses Enter.
	WaitForEnter(prompt string) error
}

// BrowserOpener opens URLs in the user's browser.
// Abstracted for testing.
type BrowserOpener interface {
	Open(ctx context.Context, url string) error
}

// Setup orchestrates the GitHub App creation and installation flow.
type Setup struct {
	printer *ui.Printer
	prompt  Prompter
	browser BrowserOpener
	token   string
	baseURL string // GitHub API base URL (for testing)
	webURL  string // GitHub web base URL (for testing)
}

// Option configures a Setup instance.
type Option func(*Setup)

// WithBaseURL overrides the GitHub API base URL (for testing).
func WithBaseURL(url string) Option {
	return func(s *Setup) { s.baseURL = url }
}

// WithWebURL overrides the GitHub web base URL (for testing).
func WithWebURL(url string) Option {
	return func(s *Setup) { s.webURL = url }
}

// New creates a Setup with the given dependencies.
func New(printer *ui.Printer, prompt Prompter, browser BrowserOpener, token string, opts ...Option) *Setup {
	s := &Setup{
		printer: printer,
		prompt:  prompt,
		browser: browser,
		token:   token,
		baseURL: "https://api.github.com",
		webURL:  "https://github.com",
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run executes the full app creation and installation flow.
// Returns the app credentials or an error.
func (s *Setup) Run(ctx context.Context, org string) (*AppCredentials, error) {
	s.printer.Header("GitHub App setup")
	s.printer.Blank()
	s.printer.StepInfo("fullsend needs a GitHub App to operate on your organization.")
	s.printer.StepInfo("We'll walk you through creating and installing it.")
	s.printer.Blank()

	// Step 1: Create the app via manifest flow
	if err := s.prompt.WaitForEnter("Press [Enter] to open the GitHub App creation page..."); err != nil {
		return nil, fmt.Errorf("waiting for user: %w", err)
	}

	creds, err := s.createAppViaManifest(ctx, org)
	if err != nil {
		s.printer.StepFail("GitHub App creation failed")
		return nil, fmt.Errorf("creating GitHub App: %w", err)
	}

	s.printer.StepDone(fmt.Sprintf("GitHub App %q created", creds.Name))
	s.printer.StepInfo(fmt.Sprintf("App ID: %d", creds.ID))
	s.printer.StepInfo(fmt.Sprintf("Settings: %s", creds.HTMLURL))
	s.printer.Blank()

	// Step 2: Install the app on the organization
	s.printer.StepInfo("Now the app needs to be installed on your organization.")
	s.printer.StepInfo("This grants it access to the repos you choose.")
	s.printer.Blank()

	if promptErr := s.prompt.WaitForEnter("Press [Enter] to open the installation page..."); promptErr != nil {
		return nil, fmt.Errorf("waiting for user: %w", promptErr)
	}

	installURL := fmt.Sprintf("%s/apps/%s/installations/new/permissions?target_id=0",
		s.webURL, creds.Slug)
	if openErr := s.browser.Open(ctx, installURL); openErr != nil {
		s.printer.StepWarn("Could not open browser automatically")
		s.printer.StepInfo(fmt.Sprintf("Open this URL manually: %s", installURL))
	}

	s.printer.StepInfo("Complete the installation in your browser, then return here.")
	s.printer.Blank()

	if promptErr := s.prompt.WaitForEnter("Press [Enter] when the installation is complete..."); promptErr != nil {
		return nil, fmt.Errorf("waiting for user: %w", promptErr)
	}

	// Step 3: Verify the app is installed on the org
	s.printer.StepStart("Verifying app installation...")

	installed, err := s.verifyInstallation(ctx, org, creds.Slug)
	if err != nil {
		s.printer.StepWarn(fmt.Sprintf("Could not verify installation: %v", err))
		s.printer.StepInfo("The app may still be installed. Continuing...")
	} else if !installed {
		s.printer.StepWarn("App does not appear to be installed on this organization yet")
		s.printer.StepInfo("You can install it later at:")
		s.printer.StepInfo(fmt.Sprintf("  %s/apps/%s/installations/new", s.webURL, creds.Slug))
	} else {
		s.printer.StepDone("App is installed on the organization")
	}

	s.printer.Blank()
	return creds, nil
}

// createAppViaManifest runs the manifest flow:
// 1. Start local server
// 2. Open browser to form page that POSTs manifest to GitHub
// 3. Catch redirect with code
// 4. Exchange code for credentials
func (s *Setup) createAppViaManifest(ctx context.Context, org string) (*AppCredentials, error) {
	// Pick a free port
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting local server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Build the manifest
	manifest := s.buildManifest(org, redirectURL)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("encoding manifest: %w", err)
	}

	// Channel to receive the code from the redirect
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Create the HTTP server
	mux := http.NewServeMux()

	// Serve the form page that auto-submits to GitHub
	formURL := fmt.Sprintf("%s/organizations/%s/settings/apps/new", s.webURL, org)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, formPage(formURL, string(manifestJSON)))
	})

	// Handle the redirect from GitHub
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, errorPage("No code received from GitHub"))
			errCh <- fmt.Errorf("no code in redirect")
			return
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, successPage())
		codeCh <- code
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	// Open the browser to our local form page
	localURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := s.browser.Open(ctx, localURL); err != nil {
		s.printer.StepWarn("Could not open browser automatically")
		s.printer.StepInfo(fmt.Sprintf("Open this URL manually: %s", localURL))
	}

	s.printer.StepInfo("Waiting for GitHub App creation in your browser...")

	// Wait for the code or an error
	var code string
	select {
	case code = <-codeCh:
		// Got the code
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Exchange the code for credentials
	return s.exchangeCode(ctx, code)
}

// exchangeCode calls POST /app-manifests/{code}/conversions to get the app credentials.
func (s *Setup) exchangeCode(ctx context.Context, code string) (*AppCredentials, error) {
	reqURL := fmt.Sprintf("%s/app-manifests/%s/conversions",
		s.baseURL, url.PathEscape(code))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging manifest code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("manifest code exchange failed (HTTP %d): %s",
			resp.StatusCode, string(body))
	}

	var creds AppCredentials
	if err := json.Unmarshal(body, &creds); err != nil {
		return nil, fmt.Errorf("decoding app credentials: %w", err)
	}

	return &creds, nil
}

// verifyInstallation checks whether the app is installed on the org
// by looking at the org's installed apps list.
func (s *Setup) verifyInstallation(ctx context.Context, org, appSlug string) (bool, error) {
	// List the org's app installations
	reqURL := fmt.Sprintf("%s/orgs/%s/installations?per_page=100",
		s.baseURL, url.PathEscape(org))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return false, err
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("listing installations (HTTP %d): %s",
			resp.StatusCode, string(body))
	}

	var result struct {
		Installations []struct {
			AppSlug string `json:"app_slug"`
		} `json:"installations"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("decoding installations: %w", err)
	}

	for _, inst := range result.Installations {
		if inst.AppSlug == appSlug {
			return true, nil
		}
	}

	return false, nil
}

// buildManifest constructs the GitHub App manifest with the right permissions.
func (s *Setup) buildManifest(org, redirectURL string) map[string]any {
	appConfig := github.DefaultAppConfig(org)

	return map[string]any{
		"name":         appConfig.Name,
		"url":          appConfig.URL,
		"redirect_url": redirectURL,
		"description":  appConfig.Description,
		"public":       false,
		"default_permissions": map[string]string{
			"issues":        appConfig.Permissions.Issues,
			"pull_requests": appConfig.Permissions.PullReqs,
			"checks":        appConfig.Permissions.Checks,
			"contents":      appConfig.Permissions.Contents,
		},
		"default_events": appConfig.Events,
	}
}

// HTML page templates for the local server

func formPage(actionURL, manifestJSON string) string {
	// Escape the manifest JSON for safe embedding in an HTML attribute
	escaped := strings.ReplaceAll(manifestJSON, `"`, `&quot;`)

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>fullsend — Creating GitHub App</title></head>
<body style="font-family: system-ui, sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background: #0d1117; color: #c9d1d9;">
  <div style="text-align: center;">
    <h1>⚡ fullsend</h1>
    <p>Redirecting to GitHub to create your app...</p>
    <form id="manifest-form" action="%s" method="post">
      <input type="hidden" name="manifest" value="%s">
    </form>
    <script>document.getElementById('manifest-form').submit();</script>
    <noscript>
      <p>JavaScript is disabled. Click the button below:</p>
      <button onclick="document.getElementById('manifest-form').submit()">Create GitHub App</button>
    </noscript>
  </div>
</body>
</html>`, actionURL, escaped)
}

func successPage() string {
	return `<!DOCTYPE html>
<html>
<head><title>fullsend — App Created</title></head>
<body style="font-family: system-ui, sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background: #0d1117; color: #c9d1d9;">
  <div style="text-align: center;">
    <h1>✓ GitHub App created</h1>
    <p>You can close this tab and return to your terminal.</p>
  </div>
</body>
</html>`
}

func errorPage(msg string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>fullsend — Error</title></head>
<body style="font-family: system-ui, sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background: #0d1117; color: #c9d1d9;">
  <div style="text-align: center;">
    <h1>✗ Error</h1>
    <p>%s</p>
    <p>Return to your terminal for details.</p>
  </div>
</body>
</html>`, msg)
}

// DefaultBrowser opens URLs using the system's default browser.
type DefaultBrowser struct{}

// Open opens the URL in the default browser.
func (DefaultBrowser) Open(ctx context.Context, url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return exec.CommandContext(ctx, cmd, args...).Start()
}

// StdinPrompter reads from stdin.
type StdinPrompter struct{}

// WaitForEnter prints the prompt and waits for Enter.
func (StdinPrompter) WaitForEnter(prompt string) error {
	fmt.Print(prompt)
	var b [1]byte
	_, err := fmt.Scanln(&b)
	// Scanln returns an error for empty input (just Enter), which is fine
	if err != nil && err.Error() == "unexpected newline" {
		return nil
	}
	return nil
}
