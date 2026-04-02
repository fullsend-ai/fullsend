// Package appsetup handles creating and installing per-role GitHub Apps
// using the manifest flow. It checks for existing app installations before
// creating new ones, and supports reusing apps whose private keys are
// already stored as secrets.
package appsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	ghTypes "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// AppCredentials holds the credentials returned from the manifest flow.
type AppCredentials struct {
	AppID         int
	Slug          string
	Name          string
	PEM           string
	ClientID      string
	ClientSecret  string
	WebhookSecret *string
	HTMLURL       string
}

// Prompter handles user interaction during app setup.
type Prompter interface {
	WaitForEnter(prompt string) error
	Confirm(prompt string) (bool, error)
}

// BrowserOpener opens URLs in the user's browser.
type BrowserOpener interface {
	Open(ctx context.Context, url string) error
}

// SecretExistsFunc checks if a secret exists for a given role.
type SecretExistsFunc func(role string) (bool, error)

// DefaultBrowser opens URLs using platform-specific commands.
type DefaultBrowser struct{}

func (DefaultBrowser) Open(_ context.Context, url string) error {
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
	return exec.Command(cmd, args...).Start()
}

// StdinPrompter reads user input from stdin.
type StdinPrompter struct{}

func (StdinPrompter) WaitForEnter(prompt string) error {
	fmt.Print(prompt)
	var input string
	_, err := fmt.Scanln(&input)
	// Ignore EOF / empty input — just means they pressed Enter.
	if err != nil && err.Error() != "unexpected newline" {
		return nil
	}
	return nil
}

func (StdinPrompter) Confirm(prompt string) (bool, error) {
	fmt.Printf("%s [Y/n] ", prompt)
	var input string
	_, err := fmt.Scanln(&input)
	if err != nil {
		// Empty input / just Enter → default yes.
		return true, nil
	}
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "" || input == "y" || input == "yes", nil
}

// Setup orchestrates the creation or reuse of GitHub Apps for agent roles.
type Setup struct {
	client       forge.Client
	prompter     Prompter
	browser      BrowserOpener
	ui           *ui.Printer
	knownSlugs   map[string]string
	secretExists SecretExistsFunc
}

// NewSetup creates a new Setup instance.
func NewSetup(client forge.Client, prompter Prompter, browser BrowserOpener, printer *ui.Printer) *Setup {
	return &Setup{
		client:   client,
		prompter: prompter,
		browser:  browser,
		ui:       printer,
	}
}

// WithKnownSlugs sets a mapping of role → app slug for matching
// existing installations that don't follow the default naming convention.
func (s *Setup) WithKnownSlugs(slugs map[string]string) *Setup {
	s.knownSlugs = slugs
	return s
}

// WithSecretExists sets the function used to check whether a private key
// secret already exists for a given role.
func (s *Setup) WithSecretExists(fn SecretExistsFunc) *Setup {
	s.secretExists = fn
	return s
}

// Run creates or reuses a GitHub App for the given org and role.
//
// The flow:
//  1. Check for an existing installation matching this org/role.
//  2. If found and the PEM secret exists, offer to reuse.
//  3. If found but PEM is lost, return an error.
//  4. If not found, run the manifest flow to create a new app.
//  5. After creation, ensure the app is installed on the org.
func (s *Setup) Run(ctx context.Context, org, role string) (*AppCredentials, error) {
	slug := expectedAppSlug(org, role)
	s.ui.StepStart(fmt.Sprintf("Checking for existing app: %s", slug))

	inst, found, err := s.findExistingInstallation(ctx, org, role, slug)
	if err != nil {
		return nil, fmt.Errorf("checking existing installations: %w", err)
	}

	if found {
		return s.handleExistingApp(inst, role)
	}

	// No existing app found — run the manifest flow.
	s.ui.StepStart(fmt.Sprintf("Creating new GitHub App: %s", slug))
	creds, err := s.runManifestFlow(ctx, org, role)
	if err != nil {
		return nil, fmt.Errorf("manifest flow: %w", err)
	}

	// Ensure the new app is installed on the org.
	if err := s.ensureInstalled(ctx, org, creds.Slug); err != nil {
		return nil, fmt.Errorf("ensuring installation: %w", err)
	}

	return creds, nil
}

// findExistingInstallation looks for an installation matching the role,
// first by known slug override, then by expected slug convention.
//
// Users can rename GitHub Apps during the manifest creation flow, so the
// actual slug may differ from the convention (fullsend-{org}-{role}). We
// store the actual slug in config.yaml and check knownSlugs first to handle
// renamed apps. The expected slug is only used as a fallback for first-time
// detection.
func (s *Setup) findExistingInstallation(
	ctx context.Context, org, role, expectedSlug string,
) (*forge.Installation, bool, error) {
	installations, err := s.client.ListOrgInstallations(ctx, org)
	if err != nil {
		return nil, false, err
	}

	// Check known slugs first (override mapping).
	if s.knownSlugs != nil {
		if knownSlug, ok := s.knownSlugs[role]; ok {
			for i := range installations {
				if installations[i].AppSlug == knownSlug {
					return &installations[i], true, nil
				}
			}
		}
	}

	// Fall back to expected slug convention.
	for i := range installations {
		if installations[i].AppSlug == expectedSlug {
			return &installations[i], true, nil
		}
	}

	return nil, false, nil
}

// handleExistingApp reuses an existing app if its credentials are still
// available, or reports that the private key is lost.
//
// GitHub App PEM private keys are only available at creation time — the
// manifest code exchange (POST /app-manifests/{code}/conversions) is the
// one and only time the PEM is returned. If the secret wasn't stored or
// was deleted, the key is lost and the app must be deleted and recreated.
// This is why we check RepoSecretExists before reusing.
//
// When an existing app is found with valid credentials, it is reused
// automatically. To get fresh apps, run uninstall first, then install.
func (s *Setup) handleExistingApp(inst *forge.Installation, role string) (*AppCredentials, error) {
	s.ui.StepDone(fmt.Sprintf("Found existing app: %s (ID: %d)", inst.AppSlug, inst.AppID))

	if s.secretExists != nil {
		exists, err := s.secretExists(role)
		if err != nil {
			return nil, fmt.Errorf("checking secret for role %s: %w", role, err)
		}

		if exists {
			s.ui.StepDone(fmt.Sprintf("Reusing existing app %s (credentials present)", inst.AppSlug))
			return &AppCredentials{
				AppID: inst.AppID,
				Slug:  inst.AppSlug,
				Name:  inst.AppSlug,
				// Empty PEM signals reuse of existing credentials.
			}, nil
		}

		// Secret doesn't exist — private key is lost.
		return nil, fmt.Errorf(
			"app %s exists but its private key secret is missing; "+
				"run 'fullsend admin uninstall' first, then delete the app at "+
				"https://github.com/apps/%s and re-run install",
			inst.AppSlug, inst.AppSlug,
		)
	}

	// No secretExists function — can't check, assume reuse.
	s.ui.StepDone(fmt.Sprintf("Reusing existing app %s", inst.AppSlug))
	return &AppCredentials{
		AppID: inst.AppID,
		Slug:  inst.AppSlug,
		Name:  inst.AppSlug,
	}, nil
}

// manifestResponse is the JSON response from GitHub's app manifest conversion.
type manifestResponse struct {
	ID            int     `json:"id"`
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	PEM           string  `json:"pem"`
	ClientID      string  `json:"client_id"`
	ClientSecret  string  `json:"client_secret"`
	WebhookSecret *string `json:"webhook_secret"`
	HTMLURL       string  `json:"html_url"`
}

// runManifestFlow starts a local HTTP server, opens the browser to
// GitHub's app creation page with a manifest, and waits for the
// callback with the conversion code.
func (s *Setup) runManifestFlow(ctx context.Context, org, role string) (*AppCredentials, error) {
	appCfg := ghTypes.AgentAppConfig(org, role)
	manifest, err := json.Marshal(appCfg)
	if err != nil {
		return nil, fmt.Errorf("marshaling app manifest: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting local listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	formURL := fmt.Sprintf("http://127.0.0.1:%d/", port)
	githubFormAction := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/new", org)

	type result struct {
		creds *AppCredentials
		err   error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()

	// Serve the auto-submitting form page.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Creating %s</title></head>
<body>
<h2>Creating GitHub App: %s</h2>
<p>Redirecting to GitHub...</p>
<form id="manifest-form" method="post" action="%s">
  <input type="hidden" name="manifest" value="%s">
  <input type="hidden" name="redirect_url" value="%s">
</form>
<script>document.getElementById('manifest-form').submit();</script>
</body>
</html>`,
			html.EscapeString(appCfg.Name),
			html.EscapeString(appCfg.Name),
			html.EscapeString(githubFormAction),
			html.EscapeString(string(manifest)),
			html.EscapeString(callbackURL),
		)
		fmt.Fprint(w, page)
	})

	// Handle the callback from GitHub with the conversion code.
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "Missing code parameter")
			resultCh <- result{err: fmt.Errorf("callback received without code parameter")}
			return
		}

		creds, err := s.exchangeManifestCode(ctx, code)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Error: %v", err)
			resultCh <- result{err: err}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Success</title></head>
<body>
<h2>App %s created successfully!</h2>
<p>You can close this tab and return to the terminal.</p>
</body>
</html>`, creds.Name)
		resultCh <- result{creds: creds}
	})

	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			resultCh <- result{err: fmt.Errorf("local server error: %w", err)}
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	s.ui.StepInfo(fmt.Sprintf("Opening browser to create app at %s", formURL))
	if err := s.browser.Open(ctx, formURL); err != nil {
		s.ui.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
		s.ui.StepInfo(fmt.Sprintf("Please open this URL manually: %s", formURL))
	}

	s.ui.StepInfo("Waiting for GitHub callback...")

	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		s.ui.StepDone(fmt.Sprintf("App created: %s", res.creds.Slug))
		return res.creds, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// exchangeManifestCode posts the conversion code to GitHub and returns
// the resulting app credentials.
func (s *Setup) exchangeManifestCode(ctx context.Context, code string) (*AppCredentials, error) {
	url := fmt.Sprintf("https://api.github.com/app-manifests/%s/conversions", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating conversion request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exchanging manifest code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("manifest conversion failed with status %d", resp.StatusCode)
	}

	var mr manifestResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decoding conversion response: %w", err)
	}

	return &AppCredentials{
		AppID:         mr.ID,
		Slug:          mr.Slug,
		Name:          mr.Name,
		PEM:           mr.PEM,
		ClientID:      mr.ClientID,
		ClientSecret:  mr.ClientSecret,
		WebhookSecret: mr.WebhookSecret,
		HTMLURL:       mr.HTMLURL,
	}, nil
}

// ensureInstalled checks that the app is installed on the org, prompting
// the user to install it if not.
//
// The installation URL must be /apps/{slug}/installations/new without any
// query parameters. Earlier iterations used target_id=0 which is invalid.
func (s *Setup) ensureInstalled(ctx context.Context, org, slug string) error {
	installations, err := s.client.ListOrgInstallations(ctx, org)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}

	for _, inst := range installations {
		if inst.AppSlug == slug {
			s.ui.StepDone(fmt.Sprintf("App %s is installed on %s", slug, org))
			return nil
		}
	}

	// App not installed — prompt user to install.
	installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new", slug)
	s.ui.StepWarn(fmt.Sprintf("App %s is not yet installed on %s", slug, org))
	s.ui.StepInfo(fmt.Sprintf("Please install it at: %s", installURL))

	if err := s.browser.Open(ctx, installURL); err != nil {
		s.ui.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
	}

	if err := s.prompter.WaitForEnter("Press Enter after installing the app..."); err != nil {
		return fmt.Errorf("waiting for user: %w", err)
	}

	// Verify installation.
	installations, err = s.client.ListOrgInstallations(ctx, org)
	if err != nil {
		return fmt.Errorf("verifying installation: %w", err)
	}

	for _, inst := range installations {
		if inst.AppSlug == slug {
			s.ui.StepDone(fmt.Sprintf("App %s installed successfully", slug))
			return nil
		}
	}

	return fmt.Errorf("app %s was not found in org %s after installation attempt", slug, org)
}

// expectedAppSlug returns the conventional app slug for a given org and role.
// This matches the naming convention used by ghTypes.AgentAppConfig.
func expectedAppSlug(org, role string) string {
	if role == "fullsend" {
		return "fullsend-" + org
	}
	return "fullsend-" + org + "-" + role
}
