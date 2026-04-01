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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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

	// Confirm prints a yes/no prompt and returns true if the user answers yes.
	Confirm(prompt string) (bool, error)
}

// BrowserOpener opens URLs in the user's browser.
// Abstracted for testing.
type BrowserOpener interface {
	Open(ctx context.Context, url string) error
}

// SecretExistsFunc checks whether a secret exists for an agent role.
// Returns true if the secret is stored and the app can be reused.
type SecretExistsFunc func(ctx context.Context, role string) bool

// Setup orchestrates the GitHub App creation and installation flow.
type Setup struct {
	printer      *ui.Printer
	prompt       Prompter
	browser      BrowserOpener
	secretExists SecretExistsFunc
	token        string
	baseURL      string // GitHub API base URL (for testing)
	webURL       string // GitHub web base URL (for testing)
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

// WithSecretCheck sets a function to verify that an agent's PEM key
// secret exists in the .fullsend repo. If the secret doesn't exist,
// the app can't be reused (the PEM is only available at creation time).
func WithSecretCheck(fn SecretExistsFunc) Option {
	return func(s *Setup) { s.secretExists = fn }
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

// Run creates or finds the GitHub App for a single agent role and ensures it
// is installed on the organization.  The CLI is expected to call Run once per
// role (e.g. in a loop over DefaultAgentRoles).
func (s *Setup) Run(ctx context.Context, org, role string) (*AppCredentials, error) {
	s.printer.Header(fmt.Sprintf("GitHub App setup: %s agent", role))
	s.printer.Blank()

	// Step 1: Check if a fullsend app for this role already exists on the org
	creds, err := s.resolveApp(ctx, org, role)
	if err != nil {
		return nil, err
	}

	// Step 2: Check installation and handle if needed
	if err := s.ensureInstalled(ctx, org, role, creds.Slug); err != nil {
		return nil, err
	}

	s.printer.Blank()
	return creds, nil
}

// resolveApp either finds an existing fullsend app for the given role on the
// org or walks the user through creating one.
func (s *Setup) resolveApp(ctx context.Context, org, role string) (*AppCredentials, error) {
	s.printer.StepStart(fmt.Sprintf("Checking for existing fullsend %s app...", role))

	existing, err := s.findExistingApp(ctx, org, role)
	if err != nil {
		// Non-fatal — we can still offer to create one
		s.printer.StepInfo(fmt.Sprintf("Could not check for existing app: %v", err))
	}

	if existing != nil {
		s.printer.StepDone(fmt.Sprintf("Found existing app %q installed on this organization", existing.Slug))

		// Check if the secret exists — if not, the PEM is lost and the app can't be reused
		if s.secretExists != nil && !s.secretExists(ctx, role) {
			s.printer.StepWarn(fmt.Sprintf("The private key for %q is not stored as a repo secret", existing.Slug))
			s.printer.StepInfo("The PEM key is only available at app creation time and cannot be retrieved later.")
			s.printer.StepInfo("You need to delete this app and create a new one.")
			s.printer.Blank()

			if promptErr := s.prompt.WaitForEnter(fmt.Sprintf("Press [Enter] to open the app settings page to delete %s...", existing.Slug)); promptErr != nil {
				return nil, fmt.Errorf("waiting for user: %w", promptErr)
			}

			deleteURL := fmt.Sprintf("%s/organizations/%s/settings/apps/%s/advanced",
				s.webURL, org, existing.Slug)
			if openErr := s.browser.Open(ctx, deleteURL); openErr != nil {
				s.printer.StepWarn("Could not open browser automatically")
				s.printer.StepInfo(fmt.Sprintf("Open this URL manually: %s", deleteURL))
			} else {
				s.printer.StepInfo("Opened app settings in your browser.")
			}

			s.printer.StepInfo("Delete the app, then return here.")
			s.printer.Blank()

			if promptErr := s.prompt.WaitForEnter("Press [Enter] when the app has been deleted..."); promptErr != nil {
				return nil, fmt.Errorf("waiting for user: %w", promptErr)
			}
			// Fall through to create a new app
		} else {
			s.printer.Blank()

			reuse, confirmErr := s.prompt.Confirm(fmt.Sprintf("Use existing app %q? [Y/n] ", existing.Slug))
			if confirmErr != nil {
				return nil, fmt.Errorf("reading confirmation: %w", confirmErr)
			}

			if reuse {
				s.printer.StepDone(fmt.Sprintf("Using existing app %q", existing.Slug))
				return existing, nil
			}

			s.printer.StepInfo("OK, we'll create a new app instead.")
			s.printer.Blank()
		}
	}

	// No existing app (or user declined) — create one
	s.printer.StepInfo(fmt.Sprintf("We need you to create a GitHub App for the %s agent.", role))
	s.printer.Blank()

	if promptErr := s.prompt.WaitForEnter(fmt.Sprintf("Press [Enter] to be taken to the %s app creation flow...", role)); promptErr != nil {
		return nil, fmt.Errorf("waiting for user: %w", promptErr)
	}

	creds, createErr := s.createAppViaManifest(ctx, org, role)
	if createErr != nil {
		s.printer.StepFail("GitHub App creation failed")
		return nil, fmt.Errorf("creating GitHub App: %w", createErr)
	}

	s.printer.StepDone(fmt.Sprintf("GitHub App %q created", creds.Name))
	s.printer.StepInfo(fmt.Sprintf("App ID: %d", creds.ID))
	s.printer.StepInfo(fmt.Sprintf("Settings: %s", creds.HTMLURL))
	s.printer.Blank()

	return creds, nil
}

// ensureInstalled checks if the app is already installed with access to all
// repos. If it is, skips the installation step. Otherwise, walks the user
// through the installation flow.
func (s *Setup) ensureInstalled(ctx context.Context, org, role, appSlug string) error {
	s.printer.StepStart(fmt.Sprintf("Checking %s app installation...", role))

	installation, err := s.getInstallation(ctx, org, appSlug)
	if err != nil {
		s.printer.StepInfo(fmt.Sprintf("Could not check installation: %v", err))
		// Fall through to the manual installation flow
	} else if installation != nil && installation.RepoSelection == "all" {
		s.printer.StepDone("App is installed with access to all repositories")
		return nil
	} else if installation != nil {
		s.printer.StepDone("App is installed (with access to selected repositories)")
		s.printer.StepInfo("You may want to update the installation to grant access to additional repos.")
		s.printer.Blank()

		update, confirmErr := s.prompt.Confirm("Update the app installation now? [Y/n] ")
		if confirmErr != nil {
			return fmt.Errorf("reading confirmation: %w", confirmErr)
		}
		if !update {
			return nil
		}
		// Fall through to the installation flow to let them update
	}

	// Not installed or user wants to update — walk through installation
	s.printer.StepInfo(fmt.Sprintf("We need you to install the %s app on your organization.", role))
	s.printer.StepInfo("This grants it access to the repos you choose.")
	s.printer.Blank()

	if promptErr := s.prompt.WaitForEnter("Press [Enter] to be taken to the app installation page..."); promptErr != nil {
		return fmt.Errorf("waiting for user: %w", promptErr)
	}

	installURL := fmt.Sprintf("%s/apps/%s/installations/new",
		s.webURL, appSlug)
	if openErr := s.browser.Open(ctx, installURL); openErr != nil {
		s.printer.StepWarn("Could not open browser automatically")
		s.printer.StepInfo(fmt.Sprintf("Open this URL manually: %s", installURL))
	} else {
		s.printer.StepInfo("Opened installation page in your browser.")
	}

	s.printer.StepInfo("Complete the installation in your browser, then return here.")
	s.printer.Blank()

	if promptErr := s.prompt.WaitForEnter("Press [Enter] when the installation is complete..."); promptErr != nil {
		return fmt.Errorf("waiting for user: %w", promptErr)
	}

	// Verify
	s.printer.StepStart("Verifying app installation...")

	verifyInst, verifyErr := s.getInstallation(ctx, org, appSlug)
	if verifyErr != nil {
		s.printer.StepWarn(fmt.Sprintf("Could not verify installation: %v", verifyErr))
		s.printer.StepInfo("The app may still be installed. Continuing...")
	} else if verifyInst == nil {
		s.printer.StepWarn("App does not appear to be installed on this organization yet")
		s.printer.StepInfo("You can install it later at:")
		s.printer.StepInfo(fmt.Sprintf("  %s/apps/%s/installations/new", s.webURL, appSlug))
	} else {
		s.printer.StepDone("App is installed on the organization")
	}

	return nil
}

// createAppViaManifest runs the manifest flow:
// 1. Start local server
// 2. Open browser to form page that POSTs manifest to GitHub
// 3. Catch redirect with code
// 4. Exchange code for credentials
func (s *Setup) createAppViaManifest(ctx context.Context, org, role string) (*AppCredentials, error) {
	// Pick a free port
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting local server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// Build the manifest
	manifest := s.buildManifest(org, role, redirectURL)
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
	} else {
		s.printer.StepInfo("Opened app creation flow in your browser.")
	}

	s.printer.StepInfo("Name the app and click \"Create GitHub App\", then return here.")
	s.printer.StepInfo("Waiting for you to finish...")

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

// installationInfo holds details about an app installation on an org.
type installationInfo struct {
	AppSlug       string `json:"app_slug"`
	RepoSelection string `json:"repository_selection"` // "all" or "selected"
}

// findExistingApp checks the org's installed apps for one whose slug matches
// the expected pattern for the given role (fullsend-{org}-{role}).
// Returns nil if none found.
func (s *Setup) findExistingApp(ctx context.Context, org, role string) (*AppCredentials, error) {
	installations, err := s.listInstallations(ctx, org)
	if err != nil {
		return nil, err
	}

	expectedSlug := fmt.Sprintf("fullsend-%s-%s", org, role)
	for _, inst := range installations {
		if inst.AppSlug == expectedSlug {
			return &AppCredentials{
				Name:    inst.AppSlug, // Best we can get without app-level auth
				Slug:    inst.AppSlug,
				HTMLURL: fmt.Sprintf("%s/apps/%s", s.webURL, inst.AppSlug),
			}, nil
		}
	}

	return nil, nil
}

// getInstallation returns installation details for a specific app on the org,
// or nil if the app is not installed.
func (s *Setup) getInstallation(ctx context.Context, org, appSlug string) (*installationInfo, error) {
	installations, err := s.listInstallations(ctx, org)
	if err != nil {
		return nil, err
	}

	for i := range installations {
		if installations[i].AppSlug == appSlug {
			return &installations[i], nil
		}
	}

	return nil, nil
}

// listInstallations fetches all app installations for the org.
func (s *Setup) listInstallations(ctx context.Context, org string) ([]installationInfo, error) {
	reqURL := fmt.Sprintf("%s/orgs/%s/installations?per_page=100",
		s.baseURL, url.PathEscape(org))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing installations (HTTP %d): %s",
			resp.StatusCode, string(body))
	}

	var result struct {
		Installations []installationInfo `json:"installations"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding installations: %w", err)
	}

	return result.Installations, nil
}

// buildManifest constructs the GitHub App manifest with the right permissions
// for the given agent role.
func (s *Setup) buildManifest(org, role, redirectURL string) map[string]any {
	appConfig := github.AgentAppConfig(org, role)

	perms := map[string]string{}
	if appConfig.Permissions.Issues != "" {
		perms["issues"] = appConfig.Permissions.Issues
	}
	if appConfig.Permissions.PullRequests != "" {
		perms["pull_requests"] = appConfig.Permissions.PullRequests
	}
	if appConfig.Permissions.Checks != "" {
		perms["checks"] = appConfig.Permissions.Checks
	}
	if appConfig.Permissions.Contents != "" {
		perms["contents"] = appConfig.Permissions.Contents
	}

	return map[string]any{
		"name":         appConfig.Name,
		"url":          appConfig.URL,
		"redirect_url": redirectURL,
		"description":  appConfig.Description,
		"public":       false,
		"hook_attributes": map[string]any{
			"url":    appConfig.URL,
			"active": false,
		},
		"default_permissions": perms,
		"default_events":      appConfig.Events,
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
	reader := bufio.NewReader(os.Stdin)
	_, err := reader.ReadString('\n')
	return err
}

// Confirm prints a yes/no prompt and returns true for yes (or empty/Enter,
// which defaults to yes).
func (StdinPrompter) Confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "" || answer == "y" || answer == "yes", nil
}
