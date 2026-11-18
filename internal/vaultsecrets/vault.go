package vaultsecrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAuthPath is the default JWT auth mount path.
	DefaultAuthPath = "jwt"
	// DefaultRole is the default Vault JWT role.
	DefaultRole = "fullsend-runner"
	// DefaultAudience is the default OIDC audience requested for Vault.
	DefaultAudience = "https://fullsend.sh/vault"

	// maxResponseBytes bounds a Vault response body.
	maxResponseBytes = 1 << 20
	// httpTimeout is the per-request timeout of the default client.
	httpTimeout = 30 * time.Second
)

// Connection is a resolved Vault connection: the address and the JWT auth
// settings. Empty optional fields take the ADR 0155 defaults.
type Connection struct {
	Address   string
	Namespace string
	AuthPath  string
	Role      string
	Audience  string
}

// WithDefaults returns c with empty optional fields set to their defaults.
func (c Connection) WithDefaults() Connection {
	if c.AuthPath == "" {
		c.AuthPath = DefaultAuthPath
	}
	if c.Role == "" {
		c.Role = DefaultRole
	}
	if c.Audience == "" {
		c.Audience = DefaultAudience
	}
	return c
}

// ValidateAddress checks a Vault API base URL: HTTPS, a host, and no user
// information, query, or fragment.
func ValidateAddress(addr string) error {
	u, err := url.Parse(addr)
	if err != nil {
		return fmt.Errorf("vault_address is not a valid URL")
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("vault_address must use https")
	case u.Host == "":
		return fmt.Errorf("vault_address must include a host")
	case u.User != nil:
		return fmt.Errorf("vault_address must not include user information")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(addr, "#"):
		return fmt.Errorf("vault_address must not include a query or fragment")
	}
	return nil
}

// ValidateAuthPath checks a JWT auth mount path (without the auth/
// prefix): one or more non-empty components with no "." or "..".
func ValidateAuthPath(p string) error {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "auth/") {
		return fmt.Errorf("vault_auth_path must not include the auth/ prefix")
	}
	if err := ValidatePath(p); err != nil {
		return fmt.Errorf("vault_auth_path: %w", err)
	}
	return nil
}

// Client talks to one Vault server. It never logs or returns token or
// secret values in errors.
type Client struct {
	conn Connection
	base string
	http *http.Client
}

// NewClient returns a Client for conn. httpClient overrides the HTTP
// client (tests only); the default client refuses redirects so a bearer
// token is never forwarded to another address.
func NewClient(conn Connection, httpClient *http.Client) (*Client, error) {
	conn = conn.WithDefaults()
	if err := ValidateAddress(conn.Address); err != nil {
		return nil, err
	}
	if err := ValidateAuthPath(conn.AuthPath); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Client{conn: conn, base: strings.TrimRight(conn.Address, "/") + "/v1/", http: httpClient}, nil
}

// ErrVault marks an authentication, transport, or server failure that must
// abort resolution rather than advance to the next candidate.
var ErrVault = errors.New("vault request failed")

func (c *Client) do(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: encoding request", ErrVault)
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: building request", ErrVault)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.conn.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.conn.Namespace)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error names the URL, which holds no secret; drop the
		// wrapped detail anyway so a proxy error cannot echo a header.
		return 0, nil, fmt.Errorf("%w: %s %s: transport error", ErrVault, method, path)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s %s: reading response", ErrVault, method, path)
	}
	if len(data) > maxResponseBytes {
		return 0, nil, fmt.Errorf("%w: %s %s: response too large", ErrVault, method, path)
	}
	return resp.StatusCode, data, nil
}

// Login exchanges a job OIDC token for a short-lived Vault token at the
// configured JWT auth mount and role.
func (c *Client) Login(ctx context.Context, jwt string) (string, error) {
	comps := strings.Split(c.conn.AuthPath, "/")
	path := "auth/" + strings.Join(comps, "/") + "/login"
	status, data, err := c.do(ctx, http.MethodPost, path, "", map[string]string{"role": c.conn.Role, "jwt": jwt})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%w: JWT login at %s (role %q) returned HTTP %d", ErrVault, path, c.conn.Role, status)
	}
	var out struct {
		Auth *struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.Auth == nil || out.Auth.ClientToken == "" {
		return "", fmt.Errorf("%w: JWT login at %s returned no client token", ErrVault, path)
	}
	return out.Auth.ClientToken, nil
}

// ReadField reads the latest version of the KV v2 secret at path and
// returns the named field from data.data. found is false, with a nil
// error, when the path is missing, denied by policy, or holds no
// non-empty string field of that name: the caller advances to the next
// candidate. Any other status is an error.
func (c *Client) ReadField(ctx context.Context, token, path, field string) (value string, found bool, err error) {
	status, data, err := c.do(ctx, http.MethodGet, path, token, nil)
	if err != nil {
		return "", false, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("%w: reading %s returned HTTP %d", ErrVault, path, status)
	}
	var out struct {
		Data *struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", false, fmt.Errorf("%w: reading %s returned invalid JSON", ErrVault, path)
	}
	if out.Data == nil || out.Data.Data == nil {
		return "", false, nil
	}
	s, ok := out.Data.Data[field].(string)
	if !ok || s == "" {
		return "", false, nil
	}
	return s, true, nil
}

// Revoke revokes token. It is best effort: the token is short-lived and
// is never persisted, so a failure is returned for the caller to ignore
// or report.
func (c *Client) Revoke(ctx context.Context, token string) error {
	status, _, err := c.do(ctx, http.MethodPost, "auth/token/revoke-self", token, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("%w: token revocation returned HTTP %d", ErrVault, status)
	}
	return nil
}
