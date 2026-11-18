package botidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// Wire contract of the mint's GitHub bot-role lookup (POST /v1/bot-role in
// internal/mintcore). The mint authenticates the caller with its GitHub
// Actions OIDC token, binds Repo to the token's repository claim, and
// answers with role names only, never credentials. internal/mintcore is a
// separate module and duplicates these names; change both together.
const (
	MintBotRolePath = "/v1/bot-role"

	MintOutcomeRecognized = "recognized"
	MintOutcomeNoMatch    = "no_match"

	// maxMintResponseBytes bounds the response body read from the mint.
	maxMintResponseBytes = 64 << 10
)

// mintRequestTimeout bounds one lookup (connect, request and response body)
// so a stalled mint cannot block the authorization decision. Tests shorten it.
var mintRequestTimeout = 30 * time.Second

// MintBotRoleRequest is the JSON body of POST /v1/bot-role.
type MintBotRoleRequest struct {
	ActorLogin string `json:"actor_login"`
	Repo       string `json:"repo"`
}

// MintBotRoleResponse is the JSON body the mint answers with: a 200 carries
// Outcome (and Role when recognized); an error status carries Error.
type MintBotRoleResponse struct {
	Outcome string `json:"outcome,omitempty"`
	Role    string `json:"role,omitempty"`
	Error   string `json:"error,omitempty"`
}

// canonicalRoleRe bounds a role name accepted from the mint. It matches the
// mint's RolePattern (internal/mintcore/patterns.go), which allows
// underscores in custom role names.
var canonicalRoleRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// refuseTLSDowngrade is the mandatory redirect policy of every mint lookup:
// it stops after 10 redirects and refuses a redirect that downgrades from
// HTTPS, so the bearer token is never replayed over plaintext.
func refuseTLSDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect from https to %s", req.URL.Scheme)
	}
	return nil
}

// defaultMintClient is used when MintResolver.HTTPClient is nil. It carries
// its own request timeout and the mandatory redirect policy.
var defaultMintClient = &http.Client{
	Timeout:       mintRequestTimeout,
	CheckRedirect: refuseTLSDowngrade,
}

// mintClient returns the client a lookup uses. A supplied client is copied
// so its transport, timeout and jar are kept, but its redirect policy is
// always applied first: a caller-supplied CheckRedirect can only add
// restrictions, never allow a downgrade.
func mintClient(supplied *http.Client) *http.Client {
	if supplied == nil {
		return defaultMintClient
	}
	c := *supplied
	custom := supplied.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := refuseTLSDowngrade(req, via); err != nil {
			return err
		}
		if custom != nil {
			return custom(req, via)
		}
		return nil
	}
	return &c
}

// validMintURL reports whether the OIDC bearer token may be sent to u: HTTPS,
// or plain HTTP only to a loopback host (local tests and development).
func validMintURL(u *url.URL) bool {
	if u.Scheme == "https" {
		return u.Host != ""
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ParseMintBotRoleResponse maps a mint answer to a lookup outcome. Only a
// 200 with a well-formed outcome is a completed lookup; every other status
// or body is a lookup failure wrapping ErrLookup, so a failure can never
// read as a successful no-match.
func ParseMintBotRoleResponse(status int, body []byte) (BotRoleResult, error) {
	var resp MintBotRoleResponse
	decodeErr := json.Unmarshal(body, &resp)
	if status != http.StatusOK {
		msg := strings.TrimSpace(resp.Error)
		if decodeErr != nil || msg == "" {
			msg = "no error message"
		}
		return BotRoleResult{}, fmt.Errorf("%w: mint answered HTTP %d: %s", ErrLookup, status, msg)
	}
	if decodeErr != nil {
		return BotRoleResult{}, fmt.Errorf("%w: unreadable mint response: %w", ErrLookup, decodeErr)
	}
	switch resp.Outcome {
	case MintOutcomeRecognized:
		if !canonicalRoleRe.MatchString(resp.Role) {
			return BotRoleResult{}, fmt.Errorf("%w: mint recognized the bot with an invalid role %q", ErrLookup, resp.Role)
		}
		return BotRoleResult{Role: resp.Role}, nil
	case MintOutcomeNoMatch:
		if resp.Role != "" {
			return BotRoleResult{}, fmt.Errorf("%w: mint answered no_match with role %q", ErrLookup, resp.Role)
		}
		return BotRoleResult{}, nil
	default:
		return BotRoleResult{}, fmt.Errorf("%w: mint answered unknown outcome %q", ErrLookup, resp.Outcome)
	}
}

// MintResolver resolves GitHub bot roles through the mint's
// POST /v1/bot-role endpoint. It implements BotRoleResolver.
type MintResolver struct {
	// MintURL is the mint base URL.
	MintURL string
	// OIDCToken returns the caller's GitHub Actions OIDC token for the
	// mint audience. The token's repository claim must be the query target.
	OIDCToken func(ctx context.Context) (string, error)
	// HTTPClient sends the request; a client with a request timeout when
	// nil. The resolver always enforces its redirect policy (no
	// HTTPS-to-HTTP downgrade) on top of the client's own CheckRedirect,
	// and bounds every lookup by mintRequestTimeout through its request
	// context.
	HTTPClient *http.Client
}

// ResolveBotRole implements BotRoleResolver for the GitHub forge.
func (m *MintResolver) ResolveBotRole(ctx context.Context, q BotRoleQuery) (BotRoleResult, error) {
	if q.Forge != string(normevent.SystemGitHub) {
		return BotRoleResult{}, fmt.Errorf("%w: forge %q has no mint bot-role lookup", ErrLookup, q.Forge)
	}
	if q.Login == "" || q.Target == "" {
		return BotRoleResult{}, fmt.Errorf("%w: login and target are required", ErrLookup)
	}
	if m.MintURL == "" || m.OIDCToken == nil {
		return BotRoleResult{}, fmt.Errorf("%w: mint resolver is not configured", ErrLookup)
	}
	ctx, cancel := context.WithTimeout(ctx, mintRequestTimeout)
	defer cancel()
	token, err := m.OIDCToken(ctx)
	if err != nil {
		return BotRoleResult{}, fmt.Errorf("%w: fetching OIDC token: %w", ErrLookup, err)
	}
	body, err := json.Marshal(MintBotRoleRequest{ActorLogin: q.Login, Repo: q.Target})
	if err != nil {
		return BotRoleResult{}, fmt.Errorf("%w: encoding request: %w", ErrLookup, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.MintURL, "/")+MintBotRolePath, bytes.NewReader(body))
	if err != nil {
		return BotRoleResult{}, fmt.Errorf("%w: building request: %w", ErrLookup, err)
	}
	if !validMintURL(req.URL) {
		return BotRoleResult{}, fmt.Errorf("%w: mint URL must use https", ErrLookup)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := mintClient(m.HTTPClient).Do(req)
	if err != nil {
		return BotRoleResult{}, fmt.Errorf("%w: calling mint: %w", ErrLookup, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxMintResponseBytes))
	if err != nil {
		return BotRoleResult{}, fmt.Errorf("%w: reading mint response: %w", ErrLookup, err)
	}
	return ParseMintBotRoleResponse(resp.StatusCode, respBody)
}
