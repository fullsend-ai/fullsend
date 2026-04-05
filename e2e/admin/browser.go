//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/playwright-community/playwright-go"
)

// PlaywrightBrowserOpener implements appsetup.BrowserOpener using a
// Playwright browser page with a pre-authenticated persistent context.
type PlaywrightBrowserOpener struct {
	page          playwright.Page
	logf          func(string, ...any)
	screenshotDir string
}

// NewPlaywrightBrowserOpener creates a new PlaywrightBrowserOpener
// using the given Playwright page.
func NewPlaywrightBrowserOpener(page playwright.Page, logf func(string, ...any), screenshotDir string) *PlaywrightBrowserOpener {
	return &PlaywrightBrowserOpener{page: page, logf: logf, screenshotDir: screenshotDir}
}

// Open navigates the Playwright page to the given URL and handles the
// expected interactions based on the page type.
func (b *PlaywrightBrowserOpener) Open(_ context.Context, url string) error {
	b.logf("[browser] Open called with URL: %s", url)

	// Local manifest form — fetch via HTTP to avoid cross-origin SameSite
	// cookie issues, then submit from within GitHub's origin.
	if strings.Contains(url, "127.0.0.1") {
		return b.handleLocalFormSubmission(url)
	}

	if _, err := b.page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(10000),
	}); err != nil {
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-goto-failed", b.logf)
		return fmt.Errorf("navigating to %s: %w", url, err)
	}

	pageURL := b.page.URL()
	b.logf("[browser] After Goto, page URL: %s", pageURL)

	switch {
	case strings.Contains(pageURL, "/settings/apps/new"),
		strings.Contains(pageURL, "/settings/apps/manifest"):
		return b.handleCreateAppPage()
	case strings.Contains(pageURL, "/installations/new"):
		return b.handleInstallAppPage()
	default:
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-unexpected-url", b.logf)
		return fmt.Errorf("unexpected URL: %s", pageURL)
	}
}

// handleLocalFormSubmission fetches the local form via HTTP, extracts the
// manifest + redirect_url, then submits from GitHub's origin so that
// session cookies (SameSite=Lax) are included in the POST.
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

	// Extract values from the hidden inputs using proper HTML parsing.
	manifest, err := extractInputValue(content, "manifest")
	if err != nil {
		return fmt.Errorf("extracting manifest from form: %w", err)
	}
	redirectURL, err := extractInputValue(content, "redirect_url")
	if err != nil {
		return fmt.Errorf("extracting redirect_url from form: %w", err)
	}
	actionURL, err := extractFormAction(content)
	if err != nil {
		return fmt.Errorf("extracting form action: %w", err)
	}

	b.logf("[browser] Extracted manifest (%d bytes), redirect_url=%s, action=%s",
		len(manifest), redirectURL, actionURL)

	// Include redirect_url in the manifest JSON so GitHub accepts it
	// even if the form field encoding is lossy. The GitHub manifest flow
	// spec allows redirect_url inside the manifest JSON.
	var manifestMap map[string]any
	if jsonErr := json.Unmarshal([]byte(manifest), &manifestMap); jsonErr != nil {
		return fmt.Errorf("parsing manifest JSON: %w", jsonErr)
	}
	manifestMap["redirect_url"] = redirectURL
	// GitHub requires hook_attributes.url in the manifest.
	manifestMap["hook_attributes"] = map[string]any{
		"url":    "https://example.com/webhook",
		"active": false,
	}
	manifestWithRedirect, jsonErr := json.Marshal(manifestMap)
	if jsonErr != nil {
		return fmt.Errorf("re-marshaling manifest: %w", jsonErr)
	}
	manifest = string(manifestWithRedirect)

	b.logf("[browser] Manifest with redirect_url: %s", manifest)

	// Navigate to a neutral GitHub page first so we're on the same
	// origin and session cookies will be sent with the POST.
	if _, err := b.page.Goto("https://github.com/settings", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(10000),
	}); err != nil {
		b.logf("[browser] Warning: pre-navigate to GitHub settings failed: %v", err)
	}

	// Submit the form via JS, passing values as arguments to avoid
	// any quoting/escaping issues with string interpolation.
	_, err = b.page.Evaluate(`([action, manifest, redirect]) => {
		const form = document.createElement('form');
		form.method = 'post';
		form.action = action;
		const m = document.createElement('input');
		m.type = 'hidden'; m.name = 'manifest'; m.value = manifest;
		form.appendChild(m);
		const r = document.createElement('input');
		r.type = 'hidden'; r.name = 'redirect_url'; r.value = redirect;
		form.appendChild(r);
		document.body.appendChild(form);
		form.submit();
	}`, []string{actionURL, manifest, redirectURL})
	if err != nil {
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-js-submit-failed", b.logf)
		return fmt.Errorf("submitting manifest form via JS: %w", err)
	}

	// Wait for navigation to the app creation confirmation page.
	// GitHub redirects to /settings/apps/manifest or /settings/apps/new.
	if err := b.page.WaitForURL("**/settings/apps/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/settings/apps/") {
			// We're there.
		} else if strings.Contains(pageURL, "/callback") {
			return nil
		} else {
			saveDebugScreenshot(b.page, b.screenshotDir, "browser-manifest-redirect-failed", b.logf)
			return fmt.Errorf("waiting for manifest page: %w (URL: %s)", err, pageURL)
		}
	}

	return b.handleCreateAppPage()
}

// handleCreateAppPage clicks "Create GitHub App" on the confirmation page.
func (b *PlaywrightBrowserOpener) handleCreateAppPage() error {
	b.logf("[browser] handleCreateAppPage at URL: %s", b.page.URL())

	// The button text varies: "Create GitHub App" or "Create GitHub App for {org}".
	btn := b.page.Locator("button:has-text('Create GitHub App'), input[type='submit'][value*='Create GitHub App']")
	if err := btn.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-create-btn-failed", b.logf)
		return fmt.Errorf("waiting for 'Create GitHub App' button: %w", err)
	}
	if err := btn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-create-btn-failed", b.logf)
		return fmt.Errorf("clicking 'Create GitHub App': %w", err)
	}

	// Wait for redirect back to our callback URL.
	if err := b.page.WaitForURL("**/callback**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/callback") || strings.Contains(pageURL, "127.0.0.1") {
			return nil
		}
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-callback-failed", b.logf)
		return fmt.Errorf("waiting for callback: %w", err)
	}

	return nil
}

// handleInstallAppPage clicks "Install" on the GitHub App installation page.
func (b *PlaywrightBrowserOpener) handleInstallAppPage() error {
	b.logf("[browser] handleInstallAppPage at URL: %s", b.page.URL())

	btn := b.page.Locator("button[type='submit']:has-text('Install')")
	if err := btn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(b.page, b.screenshotDir, "browser-install-btn-failed", b.logf)
		return fmt.Errorf("clicking 'Install': %w", err)
	}

	// Wait for URL to change away from the installations/new page.
	if err := b.page.WaitForURL("!**/installations/new**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		// Fall through to WaitForLoadState.
		b.logf("[browser] Warning: WaitForURL after install timed out: %v", err)
	}
	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for install to complete: %w", err)
	}
	b.logf("[browser] After install, page URL: %s", b.page.URL())

	return nil
}

// deleteAppViaPlaywright navigates to the app's advanced settings and deletes it.
func deleteAppViaPlaywright(page playwright.Page, slug string, logf func(string, ...any), screenshotDir string) error {
	url := fmt.Sprintf("https://github.com/organizations/%s/settings/apps/%s/advanced", testOrg, slug)
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("navigating to app settings for %s: %w", slug, err)
	}

	deleteBtn := page.Locator("button:has-text('Delete GitHub App')")
	if err := deleteBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "app-delete-"+slug, logf)
		logf("[cleanup] Delete button not found at %s, current URL: %s", url, page.URL())
		return fmt.Errorf("clicking 'Delete GitHub App' for %s: %w", slug, err)
	}

	// GitHub requires typing the app name to confirm deletion.
	// Wait for the confirmation input to appear in the modal.
	confirmInput := page.Locator("input[aria-label='Type the name of the GitHub App to confirm'], .Box-body input[type='text'], [role='dialog'] input[type='text'], .facebox-content input[type='text']")
	if err := confirmInput.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "app-confirm-wait-"+slug, logf)
		return fmt.Errorf("waiting for confirmation dialog for %s: %w", slug, err)
	}

	if err := confirmInput.First().Fill(slug, playwright.LocatorFillOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		// Last resort: try any visible text input on the page.
		anyInput := page.Locator("input[type='text']:visible")
		if fillErr := anyInput.First().Fill(slug, playwright.LocatorFillOptions{
			Timeout: playwright.Float(2000),
		}); fillErr != nil {
			saveDebugScreenshot(page, screenshotDir, "app-confirm-input-"+slug, logf)
			return fmt.Errorf("filling app name for deletion of %s: primary=%w, fallback=%v", slug, err, fillErr)
		}
	}

	// Click the confirmation button.
	confirmBtn := page.Locator("button:has-text('I understand the consequences')")
	if err := confirmBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(3000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "app-confirm-btn-"+slug, logf)
		return fmt.Errorf("confirming deletion of %s: %w", slug, err)
	}

	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for deletion of %s: %w", slug, err)
	}

	logf("[cleanup] Deleted GitHub App: %s", slug)
	return nil
}

// extractInputValue extracts the value attribute of a hidden input with the
// given name from raw HTML using proper HTML parsing. The html package
// handles entity decoding automatically.
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

// extractFormAction extracts the action URL from the first form element
// using proper HTML parsing.
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
