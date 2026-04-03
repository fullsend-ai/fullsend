//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// PlaywrightBrowserOpener implements appsetup.BrowserOpener using a
// Playwright browser page with a pre-authenticated persistent context.
type PlaywrightBrowserOpener struct {
	page playwright.Page
}

// NewPlaywrightBrowserOpener creates a new PlaywrightBrowserOpener
// using the given Playwright page.
func NewPlaywrightBrowserOpener(page playwright.Page) *PlaywrightBrowserOpener {
	return &PlaywrightBrowserOpener{page: page}
}

// Open navigates the Playwright page to the given URL and handles the
// expected interactions based on the page type.
//
// The manifest flow calls Open twice per app role:
//  1. With the local form URL — auto-submits to GitHub, we click "Create"
//  2. With the installation URL — we click "Install"
func (b *PlaywrightBrowserOpener) Open(_ context.Context, url string) error {
	if _, err := b.page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("navigating to %s: %w", url, err)
	}

	pageURL := b.page.URL()

	switch {
	case strings.Contains(pageURL, "/settings/apps/new"):
		// GitHub "Register new GitHub App" confirmation page.
		// The local form auto-submitted the manifest; we're now on GitHub.
		return b.handleCreateAppPage()

	case strings.Contains(pageURL, "/installations/new"):
		// GitHub App installation page.
		return b.handleInstallAppPage()

	case strings.Contains(url, "127.0.0.1"):
		// Local manifest form — it auto-submits via JS.
		// Wait for the redirect to GitHub, then handle the create page.
		if err := b.page.WaitForURL("**/settings/apps/new**", playwright.PageWaitForURLOptions{
			Timeout: playwright.Float(30000),
		}); err != nil {
			// The auto-submit may have already redirected. Check current URL.
			pageURL = b.page.URL()
			if strings.Contains(pageURL, "/settings/apps/new") {
				return b.handleCreateAppPage()
			}
			// May have gone all the way through to callback.
			if strings.Contains(pageURL, "/callback") {
				return nil // Success — callback already handled.
			}
			return fmt.Errorf("waiting for GitHub app creation page: %w", err)
		}
		return b.handleCreateAppPage()

	default:
		return fmt.Errorf("unexpected URL: %s", pageURL)
	}
}

// handleCreateAppPage clicks the "Create GitHub App" button on GitHub's
// app registration confirmation page.
func (b *PlaywrightBrowserOpener) handleCreateAppPage() error {
	// GitHub's "Create GitHub App" button is the form submit button.
	btn := b.page.Locator("button[type='submit']:has-text('Create GitHub App')")
	if err := btn.Click(); err != nil {
		return fmt.Errorf("clicking 'Create GitHub App' button: %w", err)
	}

	// Wait for GitHub to process and redirect back to our callback URL.
	// The callback URL is on 127.0.0.1, so wait for that navigation.
	if err := b.page.WaitForURL("**/callback**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(60000),
	}); err != nil {
		// Check if we ended up on the success page already.
		pageURL := b.page.URL()
		if strings.Contains(pageURL, "/callback") || strings.Contains(pageURL, "127.0.0.1") {
			return nil
		}
		return fmt.Errorf("waiting for callback after app creation: %w", err)
	}

	return nil
}

// handleInstallAppPage clicks through GitHub's app installation UI.
func (b *PlaywrightBrowserOpener) handleInstallAppPage() error {
	// Click "Install" button on the installation page.
	// GitHub shows a page where the user selects repos and clicks Install.
	btn := b.page.Locator("button[type='submit']:has-text('Install')")
	if err := btn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("clicking 'Install' button: %w", err)
	}

	// Wait for the installation to process.
	// GitHub redirects to the app's settings page after installation.
	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("waiting for install to complete: %w", err)
	}

	return nil
}

// deleteAppViaPlaywright navigates to the GitHub App settings page and
// clicks through the deletion flow.
func deleteAppViaPlaywright(page playwright.Page, slug string) error {
	url := fmt.Sprintf("https://github.com/settings/apps/%s/advanced", slug)
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("navigating to app settings for %s: %w", slug, err)
	}

	// Click "Delete GitHub App" in the danger zone.
	deleteBtn := page.Locator("button:has-text('Delete GitHub App')")
	if err := deleteBtn.Click(); err != nil {
		return fmt.Errorf("clicking 'Delete GitHub App' for %s: %w", slug, err)
	}

	// Confirm deletion in the modal dialog.
	// GitHub shows a confirmation dialog with a button to confirm.
	confirmBtn := page.Locator("button.btn-danger:has-text('Delete this GitHub App')")
	if err := confirmBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		// Try alternative selector for the confirmation.
		altBtn := page.Locator("dialog button:has-text('delete'), .js-confirm-button")
		if altErr := altBtn.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); altErr != nil {
			return fmt.Errorf("confirming app deletion for %s: primary=%w, alt=%v", slug, err, altErr)
		}
	}

	// Wait for deletion to process.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("waiting for app deletion to complete for %s: %w", slug, err)
	}

	fmt.Printf("[cleanup] Deleted GitHub App: %s\n", slug)
	return nil
}
