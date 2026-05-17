// Package appsetup handles creating and installing per-role GitHub Apps
// using the manifest flow. It checks for existing app installations before
// creating new ones, and supports reusing apps whose private keys are
// already stored as secrets.
package appsetup

import (
	"bufio"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
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
	ReadLine(prompt string) (string, error)
}

// BrowserOpener opens URLs in the user's browser.
type BrowserOpener interface {
	Open(ctx context.Context, url string) error
}

// SecretExistsFunc checks if a secret exists for a given role.
type SecretExistsFunc func(role string) (bool, error)

// StoreSecretFunc stores a PEM secret for a given role immediately after app creation.
type StoreSecretFunc func(ctx context.Context, role, pem string) error

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
	// Ignore "unexpected newline" — just means they pressed Enter.
	if err != nil && err.Error() != "unexpected newline" {
		return err
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

func (StdinPrompter) ReadLine(prompt string) (string, error) {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("no input provided")
	}
	return strings.TrimSpace(scanner.Text()), nil
}

// Setup orchestrates the creation or reuse of GitHub Apps for agent roles.
type Setup struct {
	client       forge.Client
	prompter     Prompter
	browser      BrowserOpener
	ui           *ui.Printer
	knownSlugs   map[string]string
	secretExists SecretExistsFunc
	storeSecret  StoreSecretFunc
	permErrors   []string
	publicApps   bool
	appSet       string
	storedAppIDs map[string]string // org/role → app_id from ROLE_APP_IDS
}

// NewSetup creates a new Setup instance.
func NewSetup(client forge.Client, prompter Prompter, browser BrowserOpener, printer *ui.Printer) *Setup {
	return &Setup{
		client:   client,
		prompter: prompter,
		browser:  browser,
		ui:       printer,
		appSet:   DefaultAppSet,
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

// WithStoreSecret sets the function used to store a PEM secret immediately
// after creating a new app, before proceeding to the next role.
func (s *Setup) WithStoreSecret(fn StoreSecretFunc) *Setup {
	s.storeSecret = fn
	return s
}

// WithPublicApps sets whether created apps should be public (unlisted).
// Public apps can be installed by any org via URL.
func (s *Setup) WithPublicApps(public bool) *Setup {
	s.publicApps = public
	return s
}

// WithStoredAppIDs sets the stored ROLE_APP_IDS mapping (org/role → app_id)
// used to detect stale credentials when an app is deleted and recreated.
func (s *Setup) WithStoredAppIDs(ids map[string]string) *Setup {
	s.storedAppIDs = ids
	return s
}

// appSetPattern validates app set slugs: lowercase alphanumeric with hyphens,
// must start with a letter or digit, no leading/trailing/consecutive hyphens.
var appSetPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidateAppSet checks that an app set slug is well-formed.
// The max length is 23 characters because the final GitHub App name is
// "{appSet}-{role}" and GitHub limits app names to 34 characters.
// The longest built-in role is "prioritize" (10 chars), so: 23 + 1 + 10 = 34.
func ValidateAppSet(appSet string) error {
	if appSet == "" {
		return fmt.Errorf("app set must not be empty")
	}
	if len(appSet) > 23 {
		return fmt.Errorf("app set %q exceeds max length of 23 characters (GitHub App names are limited to 34 characters, and the role suffix is appended)", appSet)
	}
	if !appSetPattern.MatchString(appSet) {
		return fmt.Errorf("app set %q must be lowercase alphanumeric with hyphens (e.g., fullsend-ai)", appSet)
	}
	return nil
}

// WithAppSet sets the app set prefix for GitHub App naming.
// Apps are named "{appSet}-{role}" (e.g., "fullsend-ai-coder").
// Callers must validate appSet via ValidateAppSet before calling this method.
func (s *Setup) WithAppSet(appSet string) *Setup {
	s.appSet = appSet
	return s
}

// Run creates or reuses a GitHub App for the given org and role.
//
// The flow:
//  1. Check for an existing installation matching this org/role.
//  2. If found and the PEM secret exists, offer to reuse.
//  3. If found but PEM is lost, return an error.
//  4. If not installed but app exists (PEM stored, installation missing),
//     resume by installing the existing app on the org.
//  5. If not found, run the manifest flow to create a new app.
//  6. After creation, store the PEM immediately, then install on the org.
func (s *Setup) Run(ctx context.Context, org, role string) (*AppCredentials, error) {
	slug := AppSlug(s.appSet, role)
	s.ui.StepStart(fmt.Sprintf("Checking for existing app: %s", slug))

	inst, found, err := s.findExistingInstallation(ctx, org, role, slug)
	if err != nil {
		return nil, fmt.Errorf("checking existing installations: %w", err)
	}

	if found {
		return s.handleExistingApp(ctx, inst, org, role)
	}

	// No installation — check if app was created in a previous run that
	// failed before the app was installed on the org.
	if recovered, err := s.recoverCreatedApp(ctx, org, role, slug); err != nil {
		return nil, err
	} else if recovered != nil {
		if err := s.ensureInstalled(ctx, org, recovered.Slug); err != nil {
			return nil, fmt.Errorf("ensuring installation: %w", err)
		}
		return recovered, nil
	}

	// No existing app found — run the manifest flow.
	s.ui.StepStart(fmt.Sprintf("Creating new GitHub App: %s", slug))
	creds, err := s.runManifestFlow(ctx, org, role)
	if err != nil {
		return nil, fmt.Errorf("manifest flow: %w", err)
	}

	// Store PEM immediately so it survives partial install failures.
	if s.storeSecret != nil && creds.PEM != "" {
		s.ui.StepStart(fmt.Sprintf("Storing private key for %s", role))
		if err := s.storeSecret(ctx, role, creds.PEM); err != nil {
			return nil, fmt.Errorf("storing secret for %s: %w", role, err)
		}
		s.ui.StepDone(fmt.Sprintf("Stored private key for %s", role))
	}

	// Ensure the new app is installed on the org.
	if err := s.ensureInstalled(ctx, org, creds.Slug); err != nil {
		return nil, fmt.Errorf("ensuring installation: %w", err)
	}

	return creds, nil
}

// recoverCreatedApp handles partial failure recovery: the app was created
// and its PEM stored, but the process exited before the app was installed
// on the org. Detects this by checking if the PEM secret exists and the
// app is reachable via GetAppClientID.
func (s *Setup) recoverCreatedApp(ctx context.Context, org, role, slug string) (*AppCredentials, error) {
	if s.secretExists == nil {
		return nil, nil
	}

	exists, err := s.secretExists(role)
	if err != nil {
		return nil, fmt.Errorf("checking secret for role %s: %w", role, err)
	}
	if !exists {
		return nil, nil
	}

	// PEM secret exists — try to find the app. Check known slug first,
	// then expected slug convention.
	candidates := []string{slug}
	if s.knownSlugs != nil {
		if ks, ok := s.knownSlugs[role]; ok {
			candidates = []string{ks, slug}
		}
	}

	for _, candidate := range candidates {
		clientID, err := s.client.GetAppClientID(ctx, candidate)
		if err != nil {
			continue
		}

		s.ui.StepDone(fmt.Sprintf("Recovering previously created app: %s", candidate))
		return &AppCredentials{
			Slug:     candidate,
			Name:     candidate,
			ClientID: clientID,
		}, nil
	}

	return nil, nil
}

// findExistingInstallation looks for an installation matching the role,
// first by known slug override, then by expected slug convention.
//
// Users can rename GitHub Apps during the manifest creation flow, so the
// actual slug may differ from the convention ({org}-{role}). We
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

// ValidateRSAPEM checks that data contains a valid RSA private key in PEM format.
func ValidateRSAPEM(data []byte) error {
	block, _ := pem.Decode(data)
	if block == nil {
		return fmt.Errorf("no PEM block found in data")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("not a valid RSA private key (tried PKCS#1 and PKCS#8)")
	}
	if _, ok := key.(*rsa.PrivateKey); !ok {
		return fmt.Errorf("PEM contains a %T key, expected RSA", key)
	}
	return nil
}

// recoverPEM guides the user through providing or generating a private key
// for an existing GitHub App whose key is missing from Secret Manager.
func (s *Setup) recoverPEM(ctx context.Context, org, slug, role string) (string, error) {
	s.ui.StepInfo(fmt.Sprintf("App %s exists but its private key is missing.", slug))

	hasKey, err := s.prompter.Confirm("Do you already have the .pem file?")
	if err != nil {
		return "", fmt.Errorf("prompting for existing key: %w", err)
	}

	if !hasKey {
		generate, err := s.prompter.Confirm("Open GitHub to generate a new key?")
		if err != nil {
			return "", fmt.Errorf("prompting for key generation: %w", err)
		}
		if !generate {
			return "", nil
		}

		settingsURL := fmt.Sprintf(
			"https://github.com/organizations/%s/settings/apps/%s", org, slug)
		s.ui.StepInfo("Opening app settings page...")
		s.ui.StepInfo(fmt.Sprintf("URL: %s", settingsURL))
		s.ui.StepInfo("Scroll to 'Private keys' and click 'Generate a private key'.")
		s.ui.StepInfo("Save the downloaded .pem file and provide the path below.")

		if err := s.browser.Open(ctx, settingsURL); err != nil {
			s.ui.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
			s.ui.StepInfo(fmt.Sprintf("Please open this URL manually: %s", settingsURL))
		}
	}

	path, err := s.prompter.ReadLine("Path to .pem file: ")
	if err != nil {
		return "", fmt.Errorf("reading PEM file path: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("no file path provided")
	}

	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("checking PEM file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("PEM path %s must be a regular file", path)
	}

	pemData, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading PEM file: %w", err)
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()

	if err := ValidateRSAPEM(pemData); err != nil {
		return "", fmt.Errorf("invalid PEM file %s: %w", path, err)
	}

	pemStr := string(pemData)

	if s.storeSecret != nil {
		s.ui.StepStart(fmt.Sprintf("Storing recovered private key for %s", role))
		if err := s.storeSecret(ctx, role, pemStr); err != nil {
			return "", fmt.Errorf("storing recovered secret for %s: %w", role, err)
		}
		s.ui.StepDone(fmt.Sprintf("Stored recovered private key for %s", role))
	}

	return pemStr, nil
}

// isAppIDStale checks whether the live installation's app ID differs from the
// stored ROLE_APP_IDS value, indicating the app was deleted and recreated.
func (s *Setup) isAppIDStale(org, role string, liveID int) bool {
	if s.storedAppIDs == nil {
		return false
	}
	storedID, ok := s.storedAppIDs[org+"/"+role]
	if !ok {
		return false
	}
	return storedID != strconv.Itoa(liveID)
}

// handleExistingApp reuses an existing app if its credentials are still
// available, or reports that the private key is lost.
//
// GitHub App PEM private keys are only available at creation time — the
// manifest code exchange (POST /app-manifests/{code}/conversions) is the
// one and only time the PEM is returned. If the secret wasn't stored or
// was deleted, the key is lost and the app must be deleted and recreated.
//
// The secretExists callback checks the appropriate backend (Secret Manager
// in OIDC mint mode, GitHub repo secrets otherwise).
//
// When an existing app is found with valid credentials, it is reused
// automatically. To get fresh apps, run uninstall first, then install.
func (s *Setup) handleExistingApp(ctx context.Context, inst *forge.Installation, org, role string) (*AppCredentials, error) {
	s.ui.StepDone(fmt.Sprintf("Found existing app: %s (ID: %d)", inst.AppSlug, inst.AppID))

	clientID, err := s.client.GetAppClientID(ctx, inst.AppSlug)
	if err != nil {
		return nil, fmt.Errorf("looking up client ID for %s: %w", inst.AppSlug, err)
	}

	if s.secretExists != nil {
		exists, err := s.secretExists(role)
		if err != nil {
			return nil, fmt.Errorf("checking secret for role %s: %w", role, err)
		}

		stale := s.isAppIDStale(org, role, inst.AppID)

		if exists && !stale {
			s.checkPermissions(inst, org, role)
			s.ui.StepDone(fmt.Sprintf("Reusing existing app %s (credentials present)", inst.AppSlug))
			return &AppCredentials{
				AppID:    inst.AppID,
				Slug:     inst.AppSlug,
				Name:     inst.AppSlug,
				ClientID: clientID,
				// Empty PEM signals reuse of existing credentials.
			}, nil
		}

		if exists && stale {
			s.ui.StepWarn(fmt.Sprintf(
				"App %s was recreated (ID changed) — stored key is invalid",
				inst.AppSlug))
		}

		// Secret doesn't exist or is stale — try to recover by generating a new key.
		pemStr, recoverErr := s.recoverPEM(ctx, org, inst.AppSlug, role)
		if recoverErr != nil {
			return nil, fmt.Errorf("recovering PEM for %s: %w", inst.AppSlug, recoverErr)
		}
		if pemStr == "" {
			if stale {
				return nil, fmt.Errorf(
					"app %s was recreated (ID changed) and needs a new private key; "+
						"generate one at https://github.com/apps/%s "+
						"or run 'fullsend admin uninstall' and re-run install",
					inst.AppSlug, inst.AppSlug,
				)
			}
			return nil, fmt.Errorf(
				"app %s exists but its private key secret is missing; "+
					"run 'fullsend admin uninstall' first, then delete the app at "+
					"https://github.com/apps/%s and re-run install",
				inst.AppSlug, inst.AppSlug,
			)
		}

		s.checkPermissions(inst, org, role)
		s.ui.StepDone(fmt.Sprintf("Recovered private key for %s", inst.AppSlug))
		return &AppCredentials{
			AppID:    inst.AppID,
			Slug:     inst.AppSlug,
			Name:     inst.AppSlug,
			PEM:      pemStr,
			ClientID: clientID,
		}, nil
	}

	// No secretExists function — can't check, assume reuse.
	s.ui.StepDone(fmt.Sprintf("Reusing existing app %s", inst.AppSlug))
	return &AppCredentials{
		AppID:    inst.AppID,
		Slug:     inst.AppSlug,
		Name:     inst.AppSlug,
		ClientID: clientID,
	}, nil
}

// checkPermissions warns if the installed app is missing permissions that
// the current manifest expects.
// PermissionErrors returns a combined error if any apps have stale permissions,
// or nil if all permissions are up to date. Call after all roles have been
// processed so the user sees every mismatch at once.
func (s *Setup) PermissionErrors() error {
	if len(s.permErrors) == 0 {
		return nil
	}
	return fmt.Errorf("apps have stale permissions:\n  %s", strings.Join(s.permErrors, "\n  "))
}

func (s *Setup) checkPermissions(inst *forge.Installation, org, role string) {
	if inst.Permissions == nil {
		s.ui.StepWarn(fmt.Sprintf("app %s: permissions not available, skipping check", inst.AppSlug))
		return
	}
	expected := ghTypes.AgentAppConfig(org, role, s.appSet).Permissions
	data, _ := json.Marshal(expected)
	var want map[string]string
	_ = json.Unmarshal(data, &want)
	var missing []string
	for perm, level := range want {
		if level == "" {
			continue
		}
		if inst.Permissions[perm] != level {
			missing = append(missing, fmt.Sprintf("%s:%s", perm, level))
		}
	}
	if len(missing) == 0 {
		return
	}
	s.ui.StepWarn(fmt.Sprintf("app %s missing permissions: %s", inst.AppSlug, strings.Join(missing, ", ")))
	permURL := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s/permissions", org, inst.AppSlug)
	installURL := fmt.Sprintf("https://github.com/organizations/%s/settings/installations/%d", org, inst.ID)
	s.permErrors = append(s.permErrors, fmt.Sprintf(
		"%s — update at %s then approve at %s",
		inst.AppSlug, permURL, installURL,
	))
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
	// Start the local listener first so we know the port for the redirect URL.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting local listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	formURL := fmt.Sprintf("http://127.0.0.1:%d/", port)
	githubFormAction := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/new", org)

	// Build the manifest with redirect_url included — GitHub requires it
	// inside the JSON manifest, not as a separate form field.
	appCfg := ghTypes.AgentAppConfig(org, role, s.appSet)
	appCfg.RedirectURL = callbackURL
	appCfg.Public = s.publicApps
	manifest, err := json.Marshal(appCfg)
	if err != nil {
		return nil, fmt.Errorf("marshaling app manifest: %w", err)
	}

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
</form>
<script>document.getElementById('manifest-form').submit();</script>
</body>
</html>`,
			html.EscapeString(appCfg.Name),
			html.EscapeString(appCfg.Name),
			html.EscapeString(githubFormAction),
			html.EscapeString(string(manifest)),
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
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Error creating app. Please return to the terminal.")
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
</html>`, html.EscapeString(creds.Name))
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
// installPollInterval is how often we check for the app installation.
const installPollInterval = 2 * time.Second

// installPollTimeout is how long we wait for the user to install the app.
const installPollTimeout = 5 * time.Minute

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

	// App not installed — open browser and poll until it appears.
	installURL := fmt.Sprintf("https://github.com/apps/%s/installations/new", slug)
	s.ui.StepWarn(fmt.Sprintf("App %s is not yet installed on %s", slug, org))
	s.ui.StepStart("Opening browser for installation...")
	s.ui.StepInfo(fmt.Sprintf("URL: %s", installURL))

	if err := s.browser.Open(ctx, installURL); err != nil {
		s.ui.StepWarn(fmt.Sprintf("Could not open browser: %v", err))
	}

	s.ui.StepInfo("Waiting for installation (will detect automatically)...")

	// Poll until the app appears in installations or we time out.
	pollCtx, cancel := context.WithTimeout(ctx, installPollTimeout)
	defer cancel()

	for {
		select {
		case <-pollCtx.Done():
			return fmt.Errorf("timed out waiting for app %s to be installed on %s", slug, org)
		case <-time.After(installPollInterval):
			installations, err := s.client.ListOrgInstallations(pollCtx, org)
			if err != nil {
				continue // transient errors — keep polling
			}
			for _, inst := range installations {
				if inst.AppSlug == slug {
					s.ui.StepDone(fmt.Sprintf("App %s installed successfully", slug))
					return nil
				}
			}
		}
	}
}

// DefaultAppSet is the default app set prefix for GitHub Apps.
// Orgs that created apps under a different prefix (e.g., "fullsend-ai")
// pass --app-set explicitly.
const DefaultAppSet = "fullsend"

// AppSlug returns the conventional app slug for a given app set and role.
func AppSlug(appSet, role string) string {
	return appSet + "-" + role
}
