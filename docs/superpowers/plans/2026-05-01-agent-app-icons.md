# Agent App Icons Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upload role-specific PNG icons as GitHub App logos during `fullsend admin install`.

**Architecture:** Embed four PNG icons in the binary via `//go:embed`. After the manifest exchange creates an app, generate an RS256 JWT from the returned credentials and call `PATCH /app` with a multipart form to upload the logo. Failure is non-fatal (warning only).

**Tech Stack:** Go stdlib (`crypto/rsa`, `crypto/x509`, `encoding/pem`, `mime/multipart`, `encoding/base64`)

---

### Task 1: Embed icons and add role lookup

**Files:**
- Create: `internal/forge/github/icons/bootstrap.png` (copy from `~/Downloads/icons/bootstrap.png`)
- Create: `internal/forge/github/icons/triage.png` (copy from `~/Downloads/icons/triage.png`)
- Create: `internal/forge/github/icons/coder.png` (copy from `~/Downloads/icons/coder.png`)
- Create: `internal/forge/github/icons/review.png` (copy from `~/Downloads/icons/review.png`)
- Create: `internal/forge/github/icons.go`
- Create: `internal/forge/github/icons_test.go`

- [ ] **Step 1: Copy icon PNGs into the icons directory**

```bash
mkdir -p internal/forge/github/icons
cp ~/Downloads/icons/bootstrap.png internal/forge/github/icons/
cp ~/Downloads/icons/triage.png internal/forge/github/icons/
cp ~/Downloads/icons/coder.png internal/forge/github/icons/
cp ~/Downloads/icons/review.png internal/forge/github/icons/
```

- [ ] **Step 2: Write the failing test for IconForRole**

Create `internal/forge/github/icons_test.go`:

```go
package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIconForRole(t *testing.T) {
	roles := []string{"fullsend", "triage", "coder", "review"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			icon, ok := IconForRole(role)
			require.True(t, ok, "expected icon for role %q", role)
			assert.True(t, len(icon) > 100, "icon bytes should not be trivially small")
			// PNG magic bytes: \x89PNG\r\n\x1a\n
			assert.Equal(t, byte(0x89), icon[0], "should start with PNG magic byte")
			assert.Equal(t, byte('P'), icon[1])
			assert.Equal(t, byte('N'), icon[2])
			assert.Equal(t, byte('G'), icon[3])
		})
	}
}

func TestIconForRole_Unknown(t *testing.T) {
	icon, ok := IconForRole("unknown-agent")
	assert.False(t, ok)
	assert.Nil(t, icon)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/forge/github/ -run TestIconForRole -v`
Expected: FAIL — `IconForRole` undefined.

- [ ] **Step 4: Write the implementation**

Create `internal/forge/github/icons.go`:

```go
package github

import "embed"

//go:embed icons/*.png
var iconFS embed.FS

// roleIcons maps agent roles to their icon filenames.
var roleIcons = map[string]string{
	"fullsend": "icons/bootstrap.png",
	"triage":   "icons/triage.png",
	"coder":    "icons/coder.png",
	"review":   "icons/review.png",
}

// IconForRole returns the embedded PNG icon for the given agent role.
// Returns nil, false if no icon is available for the role.
func IconForRole(role string) ([]byte, bool) {
	filename, ok := roleIcons[role]
	if !ok {
		return nil, false
	}
	data, err := iconFS.ReadFile(filename)
	if err != nil {
		return nil, false
	}
	return data, true
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/forge/github/ -run TestIconForRole -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/forge/github/icons/ internal/forge/github/icons.go internal/forge/github/icons_test.go
git commit -m "feat: embed agent role icons with go:embed lookup"
```

---

### Task 2: JWT generation for GitHub App auth

**Files:**
- Create: `internal/appsetup/jwt.go`
- Create: `internal/appsetup/jwt_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/appsetup/jwt_test.go`:

```go
package appsetup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPEMKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func TestGenerateAppJWT(t *testing.T) {
	pemKey := testPEMKey(t)

	token, err := generateAppJWT(12345, pemKey)
	require.NoError(t, err)

	// JWT has three dot-separated segments.
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "JWT must have 3 parts")

	// Decode and verify header.
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]string
	require.NoError(t, json.Unmarshal(headerJSON, &header))
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, "JWT", header["typ"])

	// Decode and verify claims.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]interface{}
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	assert.Equal(t, float64(12345), claims["iss"])
	assert.Contains(t, claims, "iat")
	assert.Contains(t, claims, "exp")

	// exp should be after iat.
	iat := claims["iat"].(float64)
	exp := claims["exp"].(float64)
	assert.Greater(t, exp, iat)
}

func TestGenerateAppJWT_InvalidPEM(t *testing.T) {
	_, err := generateAppJWT(1, []byte("not a pem key"))
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appsetup/ -run TestGenerateAppJWT -v`
Expected: FAIL — `generateAppJWT` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/appsetup/jwt.go`:

```go
package appsetup

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"
)

// generateAppJWT creates a short-lived RS256 JWT for GitHub App API authentication.
// The token is valid for 5 minutes, which is sufficient for a single API call.
func generateAppJWT(appID int, pemKey []byte) (string, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing private key: %w", err)
	}

	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss": appID,
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshaling header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshaling claims: %w", err)
	}

	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(claimsJSON)

	hash := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	return signingInput + "." + enc.EncodeToString(sig), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/appsetup/ -run TestGenerateAppJWT -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appsetup/jwt.go internal/appsetup/jwt_test.go
git commit -m "feat: add RS256 JWT generation for GitHub App auth"
```

---

### Task 3: Logo upload via GitHub API

**Files:**
- Create: `internal/appsetup/logo.go`
- Create: `internal/appsetup/logo_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/appsetup/logo_test.go`:

```go
package appsetup

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadAppLogo(t *testing.T) {
	fakeLogo := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}

	var gotAuth string
	var gotBody []byte
	var gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/app", r.URL.Path)

		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		gotBody = body

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	err := uploadAppLogo(context.Background(), server.URL, "fake-jwt-token", fakeLogo)
	require.NoError(t, err)

	// Check auth header.
	assert.Equal(t, "Bearer fake-jwt-token", gotAuth)

	// Check Content-Type is multipart/form-data.
	mediaType, params, err := mime.ParseMediaType(gotContentType)
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)

	// Parse multipart body and verify the logo field.
	reader := multipart.NewReader(bytes(gotBody), params["boundary"])
	part, err := reader.NextPart()
	require.NoError(t, err)
	assert.Equal(t, "logo", part.FormName())
	assert.Equal(t, "logo.png", part.FileName())

	partBody, err := io.ReadAll(part)
	require.NoError(t, err)
	assert.Equal(t, fakeLogo, partBody)
}

func TestUploadAppLogo_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := uploadAppLogo(context.Background(), server.URL, "token", []byte("png"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}
```

Wait — I used `bytes(gotBody)` which isn't valid Go. Let me fix the test. The multipart reader needs `bytes.NewReader`. Let me correct:

Actually, the test needs a small fix. Replace `bytes(gotBody)` with using `bytes.NewReader`. Here is the corrected test file:

Create `internal/appsetup/logo_test.go`:

```go
package appsetup

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadAppLogo(t *testing.T) {
	fakeLogo := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}

	var gotAuth string
	var gotBody []byte
	var gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/app", r.URL.Path)

		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		gotBody = body

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	err := uploadAppLogo(context.Background(), server.URL, "fake-jwt-token", fakeLogo)
	require.NoError(t, err)

	assert.Equal(t, "Bearer fake-jwt-token", gotAuth)

	mediaType, params, err := mime.ParseMediaType(gotContentType)
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)

	reader := multipart.NewReader(bytes.NewReader(gotBody), params["boundary"])
	part, err := reader.NextPart()
	require.NoError(t, err)
	assert.Equal(t, "logo", part.FormName())
	assert.Equal(t, "logo.png", part.FileName())

	partBody, err := io.ReadAll(part)
	require.NoError(t, err)
	assert.Equal(t, fakeLogo, partBody)
}

func TestUploadAppLogo_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := uploadAppLogo(context.Background(), server.URL, "token", []byte("png"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appsetup/ -run TestUploadAppLogo -v`
Expected: FAIL — `uploadAppLogo` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/appsetup/logo.go`:

```go
package appsetup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// uploadAppLogo uploads a PNG image as the GitHub App's logo.
// It calls PATCH /app with JWT authentication and a multipart form body.
func uploadAppLogo(ctx context.Context, baseURL, jwtToken string, logo []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("logo", "logo.png")
	if err != nil {
		return fmt.Errorf("creating form file: %w", err)
	}
	if _, err := part.Write(logo); err != nil {
		return fmt.Errorf("writing logo data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, baseURL+"/app", &buf)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("uploading logo: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("logo upload failed with status %d", resp.StatusCode)
	}

	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/appsetup/ -run TestUploadAppLogo -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appsetup/logo.go internal/appsetup/logo_test.go
git commit -m "feat: add logo upload for GitHub Apps via PATCH /app"
```

---

### Task 4: Wire logo upload into the manifest flow

**Files:**
- Modify: `internal/appsetup/appsetup.go:318-438` (runManifestFlow)

- [ ] **Step 1: Add the import for the icons package**

In `internal/appsetup/appsetup.go`, the existing import block already has:
```go
ghTypes "github.com/fullsend-ai/fullsend/internal/forge/github"
```

No new imports needed — `ghTypes.IconForRole` is already accessible and `generateAppJWT` / `uploadAppLogo` are in the same package.

- [ ] **Step 2: Add logo upload after the manifest exchange succeeds**

In `internal/appsetup/appsetup.go`, modify `runManifestFlow`. After line 433 (`s.ui.StepDone(fmt.Sprintf("App created: %s", res.creds.Slug))`), add the logo upload call before the return. Replace lines 428-437:

```go
	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		s.ui.StepDone(fmt.Sprintf("App created: %s", res.creds.Slug))

		// Upload the role-specific icon as the app's logo.
		if logo, ok := ghTypes.IconForRole(role); ok {
			s.ui.StepStart(fmt.Sprintf("Uploading logo for %s", res.creds.Slug))
			jwt, err := generateAppJWT(res.creds.AppID, []byte(res.creds.PEM))
			if err != nil {
				s.ui.StepWarn(fmt.Sprintf("Could not generate JWT for logo upload: %v", err))
			} else if err := uploadAppLogo(ctx, "https://api.github.com", jwt, logo); err != nil {
				s.ui.StepWarn(fmt.Sprintf("Could not upload logo: %v", err))
			} else {
				s.ui.StepDone(fmt.Sprintf("Logo uploaded for %s", res.creds.Slug))
			}
		}

		return res.creds, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
```

- [ ] **Step 3: Run all tests to verify nothing broke**

Run: `go test ./internal/appsetup/ -v`
Expected: All existing tests PASS.

Run: `go test ./internal/forge/github/ -v`
Expected: All existing tests PASS.

- [ ] **Step 4: Run vet and lint**

Run: `make go-vet && make lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/appsetup/appsetup.go
git commit -m "feat: upload agent icon after GitHub App creation"
```

---

### Task 5: Full integration verification

- [ ] **Step 1: Run all unit tests**

Run: `make go-test`
Expected: All tests PASS.

- [ ] **Step 2: Run vet and lint**

Run: `make go-vet && make lint`
Expected: PASS.

- [ ] **Step 3: Verify embedded icons are accessible from the built binary**

Run: `go build ./cmd/fullsend/`
Expected: Build succeeds. The binary should be larger by ~50-100KB due to the embedded PNGs.
