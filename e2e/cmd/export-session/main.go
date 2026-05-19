// Command export-session logs into GitHub via Playwright and exports the
// browser session (cookies + localStorage) as a Playwright storageState
// JSON file. This is used to generate pre-authenticated sessions for e2e
// tests that run in CI where password login is blocked.
//
// Required environment variables:
//   - E2E_GITHUB_USERNAME: GitHub username
//   - E2E_GITHUB_PASSWORD: GitHub password (use `pass` or similar)
//
// Optional environment variables:
//   - E2E_GITHUB_TOTP_SECRET: Base32-encoded TOTP secret for 2FA.
//     Required when the GitHub account has two-factor authentication enabled.
//
// Output is written to E2E_GITHUB_SESSION_FILE (default: .playwright/session.json).
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/pquerna/otp/totp"
)

func main() {
	username := os.Getenv("E2E_GITHUB_USERNAME")
	password := os.Getenv("E2E_GITHUB_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("Set E2E_GITHUB_USERNAME and E2E_GITHUB_PASSWORD")
	}

	totpSecret := os.Getenv("E2E_GITHUB_TOTP_SECRET")

	outFile := os.Getenv("E2E_GITHUB_SESSION_FILE")
	if outFile == "" {
		outFile = filepath.Join(".playwright", "session.json")
	}

	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		log.Fatalf("creating output directory: %v", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("starting playwright: %v", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		log.Fatalf("launching browser: %v", err)
	}
	defer browser.Close()

	ctx, err := browser.NewContext()
	if err != nil {
		log.Fatalf("creating context: %v", err)
	}

	page, err := ctx.NewPage()
	if err != nil {
		log.Fatalf("creating page: %v", err)
	}

	if _, err := page.Goto("https://github.com/login", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		log.Fatalf("navigating to login: %v", err)
	}

	// Already logged in?
	if !strings.Contains(page.URL(), "/login") && !strings.Contains(page.URL(), "/session") {
		fmt.Println("Already logged in")
		export(ctx, outFile)
		return
	}

	if err := page.Locator("#login_field").Fill(username); err != nil {
		log.Fatalf("filling username: %v", err)
	}
	if err := page.Locator("#password").Fill(password); err != nil {
		log.Fatalf("filling password: %v", err)
	}
	if err := page.Locator("input[type='submit'], button[type='submit']").First().Click(); err != nil {
		log.Fatalf("clicking submit: %v", err)
	}

	// Wait for post-login navigation — this may land on the 2FA page or the
	// dashboard depending on account settings.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		log.Fatalf("waiting for post-login page: %v", err)
	}

	// Handle 2FA challenge if present. GitHub shows a TOTP input with
	// id="app_totp" on the two-factor authentication page.
	if err := handle2FA(page, totpSecret); err != nil {
		log.Fatalf("2FA: %v", err)
	}

	url := page.URL()
	if strings.Contains(url, "/login") || strings.Contains(url, "/session") {
		log.Fatalf("login failed, still at: %s", url)
	}

	fmt.Printf("Logged in (URL: %s)\n", url)
	export(ctx, outFile)
}

// handle2FA detects whether the current page is a GitHub 2FA challenge and,
// if so, generates a TOTP code from the provided secret and submits it.
func handle2FA(page playwright.Page, totpSecret string) error {
	// Check for the TOTP input field. GitHub uses id="app_totp" for the
	// authenticator app code input on the 2FA page.
	totpInput := page.Locator("#app_totp")
	if err := totpInput.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(3000),
	}); err != nil {
		// No 2FA page detected — not an error, account may not have 2FA.
		return nil
	}

	fmt.Println("2FA challenge detected")

	if totpSecret == "" {
		return fmt.Errorf("GitHub is requesting two-factor authentication but E2E_GITHUB_TOTP_SECRET is not set — " +
			"set this environment variable to the base32-encoded TOTP secret for the account")
	}

	code, err := totp.GenerateCode(totpSecret, time.Now())
	if err != nil {
		return fmt.Errorf("generating TOTP code: %w", err)
	}

	if err := totpInput.Fill(code); err != nil {
		return fmt.Errorf("filling TOTP code: %w", err)
	}

	// Wait for navigation after TOTP submission. GitHub auto-submits when
	// the 6-digit code is filled, but we also click submit as a fallback.
	submitBtn := page.Locator("button[type='submit']")
	if err := submitBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(3000),
	}); err != nil {
		// Auto-submit may have already navigated away — ignore click errors.
		fmt.Printf("Note: submit click after TOTP fill returned: %v (may be expected if auto-submitted)\n", err)
	}

	if err := page.WaitForURL("https://github.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("post-2FA navigation timed out (url: %s): %w", page.URL(), err)
	}

	// Verify we're past the 2FA page.
	url := page.URL()
	if strings.Contains(url, "two_factor") || strings.Contains(url, "/sessions/two-factor") {
		return fmt.Errorf("2FA failed — still on 2FA page: %s", url)
	}

	fmt.Println("2FA succeeded")
	return nil
}

func export(ctx playwright.BrowserContext, outFile string) {
	if _, err := ctx.StorageState(outFile); err != nil {
		log.Fatalf("exporting storageState: %v", err)
	}
	fmt.Printf("Session exported to %s\n", outFile)
}
