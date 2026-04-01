// Package uninstall removes fullsend from a GitHub organization.
//
// The uninstall process:
//  1. Reads config.yaml from the .fullsend repo to find the app slug
//  2. Deletes the .fullsend configuration repository
//  3. Removes the app installation from the organization
//  4. Directs the user to delete the app registration (requires browser)
package uninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"gopkg.in/yaml.v3"
)

// Options holds the parameters for an uninstall operation.
type Options struct {
	// Org is the GitHub organization to uninstall fullsend from.
	Org string

	// Yolo skips the confirmation prompt.
	Yolo bool
}

// Prompter reads user input from the terminal.
type Prompter interface {
	// ConfirmWithInput asks the user to type a specific string to confirm.
	ConfirmWithInput(prompt, expected string) (bool, error)
}

// BrowserOpener opens URLs in the user's browser.
type BrowserOpener interface {
	Open(ctx context.Context, url string) error
}

// Uninstaller performs the fullsend uninstall workflow.
type Uninstaller struct {
	client  forge.Client
	printer *ui.Printer
	prompt  Prompter
	browser BrowserOpener
	token   string
	baseURL string
	webURL  string
}

// Option configures an Uninstaller.
type Option func(*Uninstaller)

// WithBaseURL overrides the GitHub API base URL (for testing).
func WithBaseURL(u string) Option {
	return func(un *Uninstaller) { un.baseURL = u }
}

// WithWebURL overrides the GitHub web base URL (for testing).
func WithWebURL(u string) Option {
	return func(un *Uninstaller) { un.webURL = u }
}

// New creates an Uninstaller.
func New(client forge.Client, printer *ui.Printer, prompt Prompter, browser BrowserOpener, token string, opts ...Option) *Uninstaller {
	un := &Uninstaller{
		client:  client,
		printer: printer,
		prompt:  prompt,
		browser: browser,
		token:   token,
		baseURL: "https://api.github.com",
		webURL:  "https://github.com",
	}
	for _, opt := range opts {
		opt(un)
	}
	return un
}

// Run executes the uninstall workflow.
func (un *Uninstaller) Run(ctx context.Context, opts Options) error {
	un.printer.Banner()
	un.printer.Header(fmt.Sprintf("Uninstalling fullsend from %s", opts.Org))
	un.printer.Blank()

	// Step 0: Confirm with the user (unless --yolo)
	if !opts.Yolo {
		un.printer.StepWarn("This will permanently delete:")
		un.printer.StepInfo(fmt.Sprintf("  • The %s/.fullsend repository and all its contents", opts.Org))
		un.printer.StepInfo(fmt.Sprintf("  • The fullsend GitHub App installation on %s", opts.Org))
		un.printer.Blank()

		confirmed, err := un.prompt.ConfirmWithInput(
			fmt.Sprintf("Type the organization name (%s) to confirm: ", opts.Org),
			opts.Org,
		)
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if !confirmed {
			un.printer.StepInfo("Aborted.")
			return nil
		}
		un.printer.Blank()
	}

	// Step 1: Read app slug from .fullsend/config.yaml
	un.printer.StepStart("Reading configuration from .fullsend repo...")

	appSlug, err := un.readAppSlug(ctx, opts.Org)
	if err != nil {
		un.printer.StepWarn(fmt.Sprintf("Could not read app slug from config: %v", err))
		un.printer.StepInfo("Will attempt to find the app from org installations instead.")
	} else {
		un.printer.StepDone(fmt.Sprintf("Found app: %s", appSlug))
	}

	// Step 2: Delete the .fullsend repo
	un.printer.StepStart("Deleting .fullsend repository...")

	if deleteErr := un.client.DeleteRepo(ctx, opts.Org, ".fullsend"); deleteErr != nil {
		un.printer.StepFail(fmt.Sprintf("Failed to delete .fullsend repo: %v", deleteErr))
		un.printer.StepInfo("You may need to delete it manually.")
	} else {
		un.printer.StepDone("Deleted .fullsend repository")
	}

	// Step 3: Remove the app installation from the org
	if appSlug == "" {
		// Try to find it from installations
		appSlug, err = un.findFullsendApp(ctx, opts.Org)
		if err != nil || appSlug == "" {
			un.printer.StepWarn("Could not determine the fullsend app to uninstall")
			un.printer.StepInfo("Check your org's installed apps manually:")
			un.printer.StepInfo(fmt.Sprintf("  %s/organizations/%s/settings/installations", un.webURL, opts.Org))
			un.printer.Blank()
			return nil
		}
	}

	un.printer.StepStart(fmt.Sprintf("Removing %s app installation...", appSlug))

	if removeErr := un.removeInstallation(ctx, opts.Org, appSlug); removeErr != nil {
		un.printer.StepFail(fmt.Sprintf("Failed to remove app installation: %v", removeErr))
		un.printer.StepInfo("You may need to remove it manually at:")
		un.printer.StepInfo(fmt.Sprintf("  %s/organizations/%s/settings/installations", un.webURL, opts.Org))
	} else {
		un.printer.StepDone(fmt.Sprintf("Removed %s installation from organization", appSlug))
	}

	// Step 4: Direct user to delete the app registration
	un.printer.Blank()
	un.printer.StepInfo("To complete the cleanup, delete the app registration:")
	appSettingsURL := fmt.Sprintf("%s/settings/apps/%s", un.webURL, appSlug)
	un.printer.StepInfo(fmt.Sprintf("  %s", appSettingsURL))
	un.printer.StepInfo("(This requires browser access and cannot be done via API with a PAT.)")
	un.printer.Blank()

	if openErr := un.browser.Open(ctx, appSettingsURL); openErr != nil {
		// Silently ignore — we already printed the URL
		_ = openErr
	}

	un.printer.Summary("Uninstall complete", []string{
		fmt.Sprintf("Deleted: %s/.fullsend", opts.Org),
		fmt.Sprintf("Removed: %s app installation", appSlug),
		"Action required: delete the app registration in your browser",
	})

	return nil
}

// readAppSlug reads the app slug from .fullsend/config.yaml.
func (un *Uninstaller) readAppSlug(ctx context.Context, org string) (string, error) {
	data, err := un.client.GetFileContent(ctx, org, ".fullsend", "config.yaml")
	if err != nil {
		return "", fmt.Errorf("reading config.yaml: %w", err)
	}

	var cfg config.OrgConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parsing config.yaml: %w", err)
	}

	if cfg.App.Slug == "" {
		return "", fmt.Errorf("no app slug found in config.yaml")
	}

	return cfg.App.Slug, nil
}

// findFullsendApp scans org installations for an app with a "fullsend" prefix.
func (un *Uninstaller) findFullsendApp(ctx context.Context, org string) (string, error) {
	installations, err := un.listInstallations(ctx, org)
	if err != nil {
		return "", err
	}

	for _, inst := range installations {
		if len(inst.AppSlug) >= 8 && inst.AppSlug[:8] == "fullsend" {
			return inst.AppSlug, nil
		}
	}

	return "", nil
}

type orgInstallation struct {
	AppSlug string `json:"app_slug"`
	ID      int    `json:"id"`
}

// removeInstallation finds the installation ID for the app and deletes it.
func (un *Uninstaller) removeInstallation(ctx context.Context, org, appSlug string) error {
	installations, err := un.listInstallations(ctx, org)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}

	var installID int
	for _, inst := range installations {
		if inst.AppSlug == appSlug {
			installID = inst.ID
			break
		}
	}

	if installID == 0 {
		return fmt.Errorf("app %q not found in org installations", appSlug)
	}

	// DELETE /app/installations/{installation_id} requires JWT auth (app-level).
	// With a PAT, we can use the user-facing endpoint instead.
	// Actually, the user-level endpoint is:
	// DELETE /user/installations/{installation_id}
	// which removes the installation as the authenticated user.
	reqURL := fmt.Sprintf("%s/user/installations/%d",
		un.baseURL, installID)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+un.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	return fmt.Errorf("removing installation (HTTP %d): %s", resp.StatusCode, string(body))
}

// listInstallations fetches all app installations for the org.
func (un *Uninstaller) listInstallations(ctx context.Context, org string) ([]orgInstallation, error) {
	reqURL := fmt.Sprintf("%s/orgs/%s/installations?per_page=100",
		un.baseURL, url.PathEscape(org))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+un.token)
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
		Installations []orgInstallation `json:"installations"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decoding installations: %w", err)
	}

	return result.Installations, nil
}
