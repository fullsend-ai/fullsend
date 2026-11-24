package forgejo

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// OAuth endpoints and callback, relative to the instance base URL. The base
// URL's path prefix is kept.
const (
	oauthAuthorizePath = "/login/oauth/authorize"
	oauthTokenPath     = "/login/oauth/access_token"
	// OAuthCallbackPath is the path of the loopback redirect URI. Register
	// http://127.0.0.1/callback on the instance's public OAuth application;
	// Forgejo accepts any port for a loopback redirect URI.
	OAuthCallbackPath = "/callback"
	// KeyringService is the OS keyring service OAuth tokens are stored
	// under.
	KeyringService = "fullsend-forgejo-oauth"
	// defaultLoginTimeout bounds the wait for the browser callback.
	defaultLoginTimeout = 5 * time.Minute
)

// ErrTokenNotStored is returned by a TokenStore that holds no token for a key.
var ErrTokenNotStored = errors.New("no token stored")

// TokenStore persists OAuth tokens outside the repository.
type TokenStore interface {
	// Get returns the stored value, or an error wrapping
	// ErrTokenNotStored.
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// KeyringStore is a TokenStore backed by the operating-system keyring
// (macOS Keychain, Windows Credential Manager, or the Secret Service on
// Linux).
type KeyringStore struct{}

// Get implements TokenStore.
func (KeyringStore) Get(key string) (string, error) {
	v, err := keyring.Get(KeyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrTokenNotStored
	}
	return v, err
}

// Set implements TokenStore.
func (KeyringStore) Set(key, value string) error { return keyring.Set(KeyringService, key, value) }

// Delete implements TokenStore. Deleting a missing key succeeds.
func (KeyringStore) Delete(key string) error {
	if err := keyring.Delete(KeyringService, key); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// OAuthInstance identifies an instance and its public OAuth client.
type OAuthInstance struct {
	Alias    string
	BaseURL  string
	ClientID string
	// TokenEnvVar names the environment variable that supplies a scoped
	// access token instead; the consent prompt mentions it.
	TokenEnvVar string
}

// storeKey keys a cached token to the alias, instance URL and client ID,
// so a token is never reused for another alias, instance or client.
func (i OAuthInstance) storeKey() string {
	return strings.ToLower(i.Alias) + "|" + strings.TrimRight(i.BaseURL, "/") + "|" + i.ClientID
}

// storedToken is the cached form of an OAuth token.
type storedToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

// OAuth obtains Forgejo API tokens with the OAuth Authorization Code flow
// with PKCE, for a public client (no client secret), and caches them in a
// TokenStore. Token values are never written to Out or included in errors.
type OAuth struct {
	Store TokenStore
	// HTTPClient sends token and API requests; nil uses a client with a
	// 60-second timeout. Redirects are never followed.
	HTTPClient *http.Client
	// OpenBrowser opens the authorization URL in the user's browser.
	OpenBrowser func(ctx context.Context, url string) error
	// In supplies the answer to the consent prompt.
	In io.Reader
	// Out receives the consent prompt and progress messages.
	Out io.Writer
	// LoginTimeout bounds the wait for the browser callback; zero means
	// five minutes.
	LoginTimeout time.Duration

	in *bufio.Reader
}

func (o *OAuth) out() io.Writer {
	if o.Out == nil {
		return io.Discard
	}
	return o.Out
}

// httpContext carries the redirect-refusing HTTP client to the oauth2
// package.
func (o *OAuth) httpContext(ctx context.Context) context.Context {
	c := http.Client{Timeout: 60 * time.Second}
	if o.HTTPClient != nil {
		c = *o.HTTPClient
	}
	c.CheckRedirect = refuseRedirect
	return context.WithValue(ctx, oauth2.HTTPClient, &c)
}

// oauthConfig returns the public OAuth client configuration for inst.
func oauthConfig(inst OAuthInstance, redirectURL string) (*oauth2.Config, error) {
	u, err := url.Parse(inst.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("forgejo base URL %q must be an absolute https URL", inst.BaseURL)
	}
	base := u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/")
	return &oauth2.Config{
		ClientID: inst.ClientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:   base + oauthAuthorizePath,
			TokenURL:  base + oauthTokenPath,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		RedirectURL: redirectURL,
	}, nil
}

// CachedToken returns a usable cached token for inst. An expired token is
// refreshed and the refreshed token cached; the token is then checked
// against the instance. When no cached token is usable it returns "" and
// the reason, and removes a cached token the instance rejected. An error
// means the token could not be checked.
func (o *OAuth) CachedToken(ctx context.Context, inst OAuthInstance) (token, reason string, err error) {
	key := inst.storeKey()
	raw, err := o.Store.Get(key)
	if errors.Is(err, ErrTokenNotStored) {
		return "", "no cached OAuth token", nil
	}
	if err != nil {
		return "", fmt.Sprintf("the OS keyring could not be read (%v)", err), nil
	}
	var st storedToken
	if err := json.Unmarshal([]byte(raw), &st); err != nil || st.AccessToken == "" {
		o.forget(key)
		return "", "the cached OAuth token was unreadable and was removed", nil
	}
	cfg, err := oauthConfig(inst, "")
	if err != nil {
		return "", "", err
	}
	cached := &oauth2.Token{AccessToken: st.AccessToken, RefreshToken: st.RefreshToken, TokenType: st.TokenType, Expiry: st.Expiry}
	fresh, err := cfg.TokenSource(o.httpContext(ctx), cached).Token()
	if err != nil {
		var re *oauth2.RetrieveError
		// Only a definitive OAuth rejection (400/401, e.g. invalid_grant)
		// discards the token. Transient 4xx responses such as 429 and 408
		// keep it so a later run can retry the refresh.
		rejected := errors.As(err, &re) && re.Response != nil &&
			(re.Response.StatusCode == http.StatusBadRequest || re.Response.StatusCode == http.StatusUnauthorized)
		if rejected || cached.RefreshToken == "" {
			o.forget(key)
			return "", "the cached OAuth token expired and could not be refreshed; it was removed", nil
		}
		return "", "", fmt.Errorf("refreshing the cached OAuth token: %w", tokenEndpointError(err))
	}
	if fresh.AccessToken != cached.AccessToken {
		o.save(key, fresh)
	}
	client, err := New(inst.BaseURL, fresh.AccessToken, o.HTTPClient)
	if err != nil {
		return "", "", err
	}
	if _, err := client.CurrentUser(ctx); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			o.forget(key)
			return "", "the instance rejected the cached OAuth token (it may have been revoked); it was removed", nil
		}
		return "", "", fmt.Errorf("checking the cached OAuth token: %w", err)
	}
	return fresh.AccessToken, "", nil
}

// Login explains the OAuth token's authority and asks for consent, then
// runs the Authorization Code flow with PKCE through a loopback callback on
// 127.0.0.1, validates state, exchanges the code, and caches the token.
func (o *OAuth) Login(ctx context.Context, inst OAuthInstance) (string, error) {
	cfg, err := oauthConfig(inst, "")
	if err != nil {
		return "", err
	}
	if err := o.consent(inst); err != nil {
		return "", err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("starting the loopback callback listener: %w", err)
	}
	cfg.RedirectURL = "http://" + ln.Addr().String() + OAuthCallbackPath
	state, err := randomState()
	if err != nil {
		_ = ln.Close()
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	results := make(chan callbackResult, 1)
	srv := &http.Server{Handler: callbackHandler(state, results), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(o.out(), "Opening your browser to authorize Fullsend on Forgejo instance %q. If it does not open, visit:\n  %s\n", inst.Alias, authURL)
	if o.OpenBrowser != nil {
		if err := o.OpenBrowser(ctx, authURL); err != nil {
			fmt.Fprintf(o.out(), "Could not open a browser (%v); open the URL above manually.\n", err)
		}
	}
	timeout := o.LoginTimeout
	if timeout <= 0 {
		timeout = defaultLoginTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var res callbackResult
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", fmt.Errorf("timed out after %s waiting for the browser authorization", timeout)
	case res = <-results:
	}
	if res.err != nil {
		return "", res.err
	}
	tok, err := cfg.Exchange(o.httpContext(ctx), res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("exchanging the authorization code: %w", tokenEndpointError(err))
	}
	if tok.AccessToken == "" {
		return "", errors.New("the token endpoint returned no access token")
	}
	o.save(inst.storeKey(), tok)
	fmt.Fprintf(o.out(), "Authorized Fullsend on Forgejo instance %q.\n", inst.Alias)
	return tok.AccessToken, nil
}

// consent explains the OAuth token's authority and reads a yes/no answer.
func (o *OAuth) consent(inst OAuthInstance) error {
	fmt.Fprintf(o.out(), "\nForgejo instance %q (%s) has no API token: %s is not set and no cached OAuth token is usable.\n"+
		"Fullsend can log in through your browser with OAuth instead.\n"+
		"WARNING: Forgejo does not implement OAuth scopes. The OAuth token can act with your FULL Forgejo user\n"+
		"permissions on this instance: every repository, organization and setting your account can reach, not\n"+
		"only the selected repositories. Fullsend stores it only in your operating-system keyring and never\n"+
		"writes it to repos.yaml. For narrower access, set %s to a scoped access token instead.\n"+
		"Authorize Fullsend on %q with OAuth? [y/N]: ",
		inst.Alias, inst.BaseURL, inst.TokenEnvVar, inst.TokenEnvVar, inst.Alias)
	if o.in == nil {
		in := o.In
		if in == nil {
			in = strings.NewReader("")
		}
		o.in = bufio.NewReader(in)
	}
	line, err := o.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(o.out())
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return errors.New("OAuth authorization was declined; no browser was opened")
	}
}

// save caches tok. A keyring failure is reported but does not fail the
// command: the token is still used for this run.
func (o *OAuth) save(key string, tok *oauth2.Token) {
	data, err := json.Marshal(storedToken{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, TokenType: tok.TokenType, Expiry: tok.Expiry})
	if err == nil {
		err = o.Store.Set(key, string(data))
	}
	if err != nil {
		fmt.Fprintf(o.out(), "Warning: could not cache the OAuth token in the OS keyring (%v); you will be asked to log in again next time.\n", err)
	}
}

// forget removes a cached token that is no longer usable.
func (o *OAuth) forget(key string) {
	if err := o.Store.Delete(key); err != nil {
		fmt.Fprintf(o.out(), "Warning: could not remove the unusable OAuth token from the OS keyring (%v).\n", err)
	}
}

// tokenEndpointError describes a token endpoint failure without the
// response body, which is server-controlled.
func tokenEndpointError(err error) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return err
	}
	status := 0
	if re.Response != nil {
		status = re.Response.StatusCode
	}
	if re.ErrorCode != "" {
		return fmt.Errorf("token endpoint returned HTTP %d: %s", status, sanitizeMessage(re.ErrorCode))
	}
	return fmt.Errorf("token endpoint returned HTTP %d", status)
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// callbackResult is the outcome of the loopback callback.
type callbackResult struct {
	code string
	err  error
}

// callbackHandler serves the loopback redirect. Only the first request to
// OAuthCallbackPath decides the outcome; its state must match.
func callbackHandler(state string, results chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != OAuthCallbackPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res callbackResult
		switch {
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			res.err = errors.New("the OAuth callback's state did not match; the authorization was rejected")
		case q.Get("error") != "":
			res.err = fmt.Errorf("authorization was not granted (%s)", sanitizeMessage(q.Get("error")))
		case q.Get("code") == "":
			res.err = errors.New("the OAuth callback had no authorization code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "Fullsend authorization failed. Return to the terminal for details.\n")
		} else {
			_, _ = io.WriteString(w, "Fullsend is authorized. You can close this window.\n")
		}
		select {
		case results <- res:
		default:
		}
	})
}
