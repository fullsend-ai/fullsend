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
//   - E2E_GITHUB_TOTP_SECRET: Base32-encoded TOTP secret for 2FA accounts
//
// Output is written to E2E_GITHUB_SESSION_FILE (default: .playwright/session.json).
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/e2e/internal/otp"
	"github.com/playwright-community/playwright-go"
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

	if err := page.WaitForURL("https://github.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		log.Fatalf("post-login navigation: %v (url: %s)", err, page.URL())
	}

	// Handle 2FA if the account has TOTP enabled.
	url := page.URL()
	if strings.Contains(url, "/two-factor") || strings.Contains(url, "/2fa") {
		if totpSecret == "" {
			log.Fatalf("2FA page detected at %s but E2E_GITHUB_TOTP_SECRET is not set", url)
		}
		fmt.Println("2FA page detected, entering TOTP code...")

		handled, err := otp.EnterTOTPCode(page, totpSecret, log.Printf)
		if err != nil {
			log.Fatalf("TOTP submission failed: %v", err)
		}
		if !handled {
			log.Fatalf("2FA page detected but TOTP input not found at %s", url)
		}

		url = page.URL()
	}

	if strings.Contains(url, "/login") || strings.Contains(url, "/session") {
		log.Fatalf("login failed, still at: %s", url)
	}

	fmt.Printf("Logged in (URL: %s)\n", url)
	export(ctx, outFile)
}

func export(ctx playwright.BrowserContext, outFile string) {
	if _, err := ctx.StorageState(outFile); err != nil {
		log.Fatalf("exporting storageState: %v", err)
	}
	fmt.Printf("Session exported to %s\n", outFile)
}
