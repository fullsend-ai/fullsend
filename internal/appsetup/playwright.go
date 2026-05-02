package appsetup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	ghTypes "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/playwright-community/playwright-go"
	xhtml "golang.org/x/net/html"
)

// PlaywrightBrowserOpener implements BrowserOpener and RoleAware using
// a Playwright browser page with a pre-authenticated session.
type PlaywrightBrowserOpener struct {
	page playwright.Page
	ui   *ui.Printer
	role string
}

// NewPlaywrightBrowserOpener creates a new PlaywrightBrowserOpener.
func NewPlaywrightBrowserOpener(page playwright.Page, printer *ui.Printer) *PlaywrightBrowserOpener {
	return &PlaywrightBrowserOpener{page: page, ui: printer}
}

// SetRole sets the current agent role for icon lookup.
func (b *PlaywrightBrowserOpener) SetRole(role string) {
	b.role = role
}

// Open navigates the Playwright page to the given URL and handles
// interactions based on the page type.
func (b *PlaywrightBrowserOpener) Open(_ context.Context, url string) error {
	// Local manifest form — fetch via HTTP, submit from GitHub's origin.
	if strings.Contains(url, "127.0.0.1") {
		return b.handleLocalFormSubmission(url)
	}

	if _, err := b.page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("navigating to %s: %w", url, err)
	}

	pageURL := b.page.URL()

	switch {
	case strings.Contains(pageURL, "/settings/apps/new"),
		strings.Contains(pageURL, "/settings/apps/manifest"):
		return b.handleCreateAppPage()
	case strings.Contains(pageURL, "/installations/new"):
		return b.handleInstallAppPage()
	default:
		return fmt.Errorf("unexpected URL: %s", pageURL)
	}
}

// handleLocalFormSubmission fetches the local form via HTTP, extracts the
// manifest, then submits from GitHub's origin so session cookies are included.
func (b *PlaywrightBrowserOpener) handleLocalFormSubmission(localURL string) error {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(localURL)
	if err != nil {
		return fmt.Errorf("fetching local form page: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading local form page: %w", err)
	}
	content := string(body)

	manifest, err := extractInputValue(content, "manifest")
	if err != nil {
		return fmt.Errorf("extracting manifest from form: %w", err)
	}
	actionURL, err := extractFormAction(content)
	if err != nil {
		return fmt.Errorf("extracting form action: %w", err)
	}

	// Ensure hook_attributes exists in the manifest.
	var manifestMap map[string]any
	if jsonErr := json.Unmarshal([]byte(manifest), &manifestMap); jsonErr != nil {
		return fmt.Errorf("parsing manifest JSON: %w", jsonErr)
	}
	if _, ok := manifestMap["hook_attributes"]; !ok {
		manifestMap["hook_attributes"] = map[string]any{
			"url":    "https://example.com/webhook",
			"active": false,
		}
		patched, jsonErr := json.Marshal(manifestMap)
		if jsonErr != nil {
			return fmt.Errorf("re-marshaling manifest: %w", jsonErr)
		}
		manifest = string(patched)
	}

	b.ui.StepInfo(fmt.Sprintf("Submitting manifest (%d bytes) to %s", len(manifest), actionURL))

	// Navigate to GitHub's origin first so session cookies are sent.
	if _, err := b.page.Goto("https://github.com/settings", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		b.ui.StepWarn(fmt.Sprintf("Pre-navigate to GitHub failed: %v", err))
	}

	// Submit the form via JS.
	_, err = b.page.Evaluate(`([action, manifest]) => {
		const form = document.createElement('form');
		form.method = 'post';
		form.action = action;
		const m = document.createElement('input');
		m.type = 'hidden'; m.name = 'manifest'; m.value = manifest;
		form.appendChild(m);
		document.body.appendChild(form);
		form.submit();
	}`, []string{actionURL, manifest})
	if err != nil {
		return fmt.Errorf("submitting manifest form via JS: %w", err)
	}

	// Wait for navigation to app creation page.
	if err := b.page.WaitForURL("**/settings/apps/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/settings/apps/") {
			// We're there.
		} else if strings.Contains(pageURL, "/callback") {
			return nil
		} else {
			return fmt.Errorf("waiting for manifest page: %w (URL: %s)", err, pageURL)
		}
	}

	return b.handleCreateAppPage()
}

// handleCreateAppPage clicks "Create GitHub App" on the manifest confirmation page.
func (b *PlaywrightBrowserOpener) handleCreateAppPage() error {
	b.ui.StepInfo(fmt.Sprintf("On app creation page: %s", b.page.URL()))

	// Click "Create GitHub App".
	btn := b.page.Locator("button:has-text('Create GitHub App'), input[type='submit'][value*='Create GitHub App']")
	if err := btn.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("waiting for 'Create GitHub App' button: %w", err)
	}
	if err := btn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("clicking 'Create GitHub App': %w", err)
	}

	// Wait for redirect back to callback URL.
	if err := b.page.WaitForURL("**/callback**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000),
	}); err != nil {
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/callback") || strings.Contains(pageURL, "127.0.0.1") {
			return nil
		}
		return fmt.Errorf("waiting for callback: %w", err)
	}

	return nil
}

// UploadAppLogo navigates to the app's settings page and uploads the
// role-specific icon via the file input. This must be called after app
// creation since the manifest confirmation page has no file input.
func (b *PlaywrightBrowserOpener) UploadAppLogo(ctx context.Context, org, slug, role string) error {
	icon, ok := ghTypes.IconForRole(role)
	if !ok {
		b.ui.StepInfo(fmt.Sprintf("No icon available for role %s, skipping logo upload", role))
		return nil
	}

	// Navigate to the app's settings page.
	settingsURL := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s", org, slug)
	b.ui.StepStart(fmt.Sprintf("Uploading logo for %s at %s", slug, settingsURL))

	if _, err := b.page.Goto(settingsURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("navigating to app settings: %w", err)
	}

	// Write the icon to a temp file for Playwright's SetInputFiles.
	tmpFile, err := os.CreateTemp("", fmt.Sprintf("fullsend-icon-%s-*.png", role))
	if err != nil {
		return fmt.Errorf("creating temp file for icon: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(icon); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing icon to temp file: %w", err)
	}
	tmpFile.Close()

	// Find the file input for the logo upload.
	fileInput := b.page.Locator("input[type='file']")
	if err := fileInput.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateAttached,
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("logo file input not found on %s: %w", settingsURL, err)
	}

	if err := fileInput.First().SetInputFiles(tmpFile.Name()); err != nil {
		return fmt.Errorf("setting logo file: %w", err)
	}

	// GitHub shows a crop dialog after selecting a file. Accept the crop.
	cropBtn := b.page.Locator("button:has-text('Set new avatar')")
	if err := cropBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		// Take a debug screenshot so we can see what's on the page.
		if _, sErr := b.page.Screenshot(playwright.PageScreenshotOptions{
			Path: playwright.String("/tmp/fullsend-logo-debug.png"),
		}); sErr == nil {
			b.ui.StepInfo("Debug screenshot saved to /tmp/fullsend-logo-debug.png")
		}
		return fmt.Errorf("accepting avatar crop dialog: %w", err)
	}

	// Wait for the upload to complete.
	time.Sleep(2 * time.Second)

	b.ui.StepDone(fmt.Sprintf("Logo uploaded for %s", slug))
	return nil
}

// DeleteApp navigates to the app's advanced settings page and deletes it.
func (b *PlaywrightBrowserOpener) DeleteApp(_ context.Context, org, slug string) error {
	advancedURL := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s/advanced", org, slug)
	b.ui.StepStart(fmt.Sprintf("Deleting app %s", slug))

	if _, err := b.page.Goto(advancedURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("navigating to app settings for %s: %w", slug, err)
	}

	// Check for 404 — app doesn't exist.
	is404, _ := b.page.Locator("img[alt='404'], h1:has-text('404')").Count()
	if is404 > 0 {
		b.ui.StepInfo(fmt.Sprintf("App %s does not exist (404), skipping", slug))
		return nil
	}

	// Click "Delete GitHub App".
	deleteBtn := b.page.Locator("button:has-text('Delete GitHub App')")
	if err := deleteBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("clicking 'Delete GitHub App' for %s: %w", slug, err)
	}

	// GitHub shows a confirmation modal requiring the app name to be typed.
	time.Sleep(1 * time.Second)

	confirmInput := b.page.Locator("input[type='text']")
	if err := confirmInput.Last().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("waiting for confirmation input for %s: %w", slug, err)
	}

	if err := confirmInput.Last().Fill(slug, playwright.LocatorFillOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("filling app name for deletion of %s: %w", slug, err)
	}

	// Click the confirmation button.
	confirmBtn := b.page.Locator("button:has-text('I understand'), button:has-text('Delete this'), button[type='submit'].btn-danger")
	if err := confirmBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("confirming deletion of %s: %w", slug, err)
	}

	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for deletion of %s: %w", slug, err)
	}

	b.ui.StepDone(fmt.Sprintf("Deleted app %s", slug))
	return nil
}

// handleInstallAppPage clicks "Install" on the GitHub App installation page.
// Retries on 404 since GitHub needs time to provision the app.
func (b *PlaywrightBrowserOpener) handleInstallAppPage() error {
	pageURL := b.page.URL()
	b.ui.StepInfo(fmt.Sprintf("On installation page: %s", pageURL))

	for attempt := range 5 {
		// Check for 404.
		is404, _ := b.page.Locator("img[alt='404'], h1:has-text('404')").Count()
		if is404 > 0 {
			b.ui.StepInfo(fmt.Sprintf("Got 404, retrying in %ds (attempt %d/5)", (attempt+1)*2, attempt+1))
			time.Sleep(time.Duration((attempt+1)*2) * time.Second)
			if _, err := b.page.Goto(pageURL, playwright.PageGotoOptions{
				WaitUntil: playwright.WaitUntilStateDomcontentloaded,
				Timeout:   playwright.Float(15000),
			}); err != nil {
				continue
			}
			continue
		}

		btn := b.page.Locator("button[type='submit']:has-text('Install')")
		if err := btn.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); err != nil {
			if attempt < 4 {
				b.ui.StepInfo(fmt.Sprintf("Install button not found, retrying (attempt %d/5)", attempt+1))
				time.Sleep(time.Duration((attempt+1)*2) * time.Second)
				if _, navErr := b.page.Goto(pageURL, playwright.PageGotoOptions{
					WaitUntil: playwright.WaitUntilStateDomcontentloaded,
					Timeout:   playwright.Float(15000),
				}); navErr != nil {
					continue
				}
				continue
			}
			return fmt.Errorf("clicking 'Install': %w", err)
		}

		break
	}

	// Wait for redirect to the installation settings page (e.g.
	// /organizations/{org}/settings/installations/{id}).
	if err := b.page.WaitForURL("**/settings/installations/*", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		// Not fatal — the install may have succeeded even if the redirect
		// went somewhere unexpected.
		b.ui.StepWarn(fmt.Sprintf("WaitForURL after install: %v (url: %s)", err, b.page.URL()))
	}

	return nil
}

// extractInputValue extracts the value attribute of a hidden input with the
// given name from raw HTML using proper HTML parsing.
func extractInputValue(rawHTML, name string) (string, error) {
	doc, err := xhtml.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}
	var value string
	var found bool
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if found {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "input" {
			var nameAttr, valueAttr string
			for _, a := range n.Attr {
				if a.Key == "name" {
					nameAttr = a.Val
				}
				if a.Key == "value" {
					valueAttr = a.Val
				}
			}
			if nameAttr == name {
				value = valueAttr
				found = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if !found {
		return "", fmt.Errorf("input %q not found in HTML", name)
	}
	return value, nil
}

// extractFormAction extracts the action URL from the first form element.
func extractFormAction(rawHTML string) (string, error) {
	doc, err := xhtml.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}
	var action string
	var found bool
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if found {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "action" {
					action = a.Val
					found = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if !found {
		return "", fmt.Errorf("form action not found in HTML")
	}
	return action, nil
}
