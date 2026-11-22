package forgejo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

const testClientID = "client-1"

// fakeOAuthServer is a Forgejo OAuth provider and /api/v1/user endpoint
// served under a path prefix.
type fakeOAuthServer struct {
	mu            sync.Mutex
	prefix        string
	challenge     string
	redirect      string
	validAccess   map[string]bool
	validRefresh  string
	exchangeError string
	tokenCalls    int
	userCalls     int
	srv           *httptest.Server
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	t.Helper()
	f := &fakeOAuthServer{prefix: "/forgejo", validAccess: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOAuthServer) baseURL() string { return f.srv.URL + f.prefix }

func (f *fakeOAuthServer) instance() OAuthInstance {
	return OAuthInstance{Alias: "home", BaseURL: f.baseURL(), ClientID: testClientID, TokenEnvVar: "FULLSEND_FORGEJO_HOME_TOKEN"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOAuthServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case f.prefix + "/login/oauth/access_token":
		f.tokenCalls++
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != testClientID || r.PostForm.Has("client_secret") || r.Header.Get("Authorization") != "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if f.exchangeError != "" || r.PostForm.Get("code") != "the-code" || r.PostForm.Get("redirect_uri") != f.redirect ||
				base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "secret detail"})
				return
			}
			f.validAccess["access-1"] = true
			f.validRefresh = "refresh-1"
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "token_type": "bearer", "expires_in": 3600})
		case "refresh_token":
			if f.validRefresh == "" || r.PostForm.Get("refresh_token") != f.validRefresh {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
				return
			}
			f.validAccess["access-2"] = true
			f.validRefresh = "refresh-2"
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "access-2", "refresh_token": "refresh-2", "token_type": "bearer", "expires_in": 3600})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		}
	case f.prefix + "/api/v1/user":
		f.userCalls++
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "token ")
		if !f.validAccess[tok] {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "invalid token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"login": "alice"})
	default:
		http.NotFound(w, r)
	}
}

// browser plays the user's browser: it checks the authorization request,
// then calls the loopback redirect with the given query.
func (f *fakeOAuthServer) browser(t *testing.T, opened *int, callback func(state string) url.Values) func(context.Context, string) error {
	return func(_ context.Context, raw string) error {
		*opened++
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, f.srv.URL+f.prefix+"/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path, "the path prefix is kept")
		q := u.Query()
		assert.Equal(t, "code", q.Get("response_type"))
		assert.Equal(t, testClientID, q.Get("client_id"))
		assert.Equal(t, "S256", q.Get("code_challenge_method"))
		assert.NotEmpty(t, q.Get("state"))
		redirect, err := url.Parse(q.Get("redirect_uri"))
		require.NoError(t, err)
		assert.Equal(t, "http", redirect.Scheme)
		assert.Equal(t, "127.0.0.1", redirect.Hostname())
		assert.Equal(t, OAuthCallbackPath, redirect.Path)
		f.mu.Lock()
		f.challenge, f.redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		f.mu.Unlock()
		if callback == nil {
			return nil
		}
		redirect.RawQuery = callback(q.Get("state")).Encode()
		resp, err := http.Get(redirect.String())
		require.NoError(t, err)
		_ = resp.Body.Close()
		return nil
	}
}

func approve(state string) url.Values { return url.Values{"code": {"the-code"}, "state": {state}} }

// memStore is an in-memory TokenStore.
type memStore struct {
	values map[string]string
	getErr error
	setErr error
}

func newMemStore() *memStore { return &memStore{values: map[string]string{}} }

func (s *memStore) Get(key string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	v, ok := s.values[key]
	if !ok {
		return "", ErrTokenNotStored
	}
	return v, nil
}

func (s *memStore) Set(key, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func (s *memStore) Delete(key string) error {
	delete(s.values, key)
	return nil
}

func storeKeyFor(f *fakeOAuthServer) string { return "home|" + f.baseURL() + "|" + testClientID }

func TestOAuthLogin_PKCEFlowCachesToken(t *testing.T) {
	f := newFakeOAuthServer(t)
	store := newMemStore()
	var out bytes.Buffer
	opened := 0
	o := &OAuth{Store: store, OpenBrowser: f.browser(t, &opened, approve), In: strings.NewReader("y\n"), Out: &out}

	tok, err := o.Login(context.Background(), f.instance())
	require.NoError(t, err)
	assert.Equal(t, "access-1", tok)
	assert.Equal(t, 1, opened)
	assert.Equal(t, 1, f.tokenCalls)

	var cached storedToken
	require.NoError(t, json.Unmarshal([]byte(store.values[storeKeyFor(f)]), &cached))
	assert.Equal(t, "access-1", cached.AccessToken)
	assert.Equal(t, "refresh-1", cached.RefreshToken)
	assert.False(t, cached.Expiry.IsZero())

	text := out.String()
	assert.Contains(t, text, "Forgejo does not implement OAuth scopes")
	assert.Contains(t, text, "FULL Forgejo user")
	assert.Contains(t, text, "FULLSEND_FORGEJO_HOME_TOKEN")
	assert.Contains(t, text, `Authorized Fullsend on Forgejo instance "home"`)
	assert.NotContains(t, text, "access-1")
	assert.NotContains(t, text, "refresh-1")

	// The cached token is now used without another login.
	tok, reason, err := o.CachedToken(context.Background(), f.instance())
	require.NoError(t, err)
	assert.Empty(t, reason)
	assert.Equal(t, "access-1", tok)
	assert.Equal(t, 1, f.userCalls)
	assert.Equal(t, 1, opened)
}

func TestOAuthLogin_Failures(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		callback func(string) url.Values
		exchange string
		timeout  time.Duration
		want     string
		opened   int
	}{
		{name: "consent declined", answer: "n\n", want: "declined", opened: 0},
		{name: "no answer", answer: "", want: "declined", opened: 0},
		{name: "state mismatch", answer: "yes\n", callback: func(string) url.Values {
			return url.Values{"code": {"the-code"}, "state": {"forged"}}
		}, want: "state did not match", opened: 1},
		{name: "access denied", answer: "y\n", callback: func(s string) url.Values {
			return url.Values{"error": {"access_denied"}, "state": {s}}
		}, want: "authorization was not granted (access_denied)", opened: 1},
		{name: "missing code", answer: "y\n", callback: func(s string) url.Values {
			return url.Values{"state": {s}}
		}, want: "no authorization code", opened: 1},
		{name: "timeout", answer: "y\n", timeout: 50 * time.Millisecond, want: "timed out", opened: 1},
		{name: "exchange rejected", answer: "y\n", callback: approve, exchange: "invalid_grant", want: "token endpoint returned HTTP 400: invalid_grant", opened: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeOAuthServer(t)
			f.exchangeError = tt.exchange
			store := newMemStore()
			var out bytes.Buffer
			opened := 0
			o := &OAuth{Store: store, OpenBrowser: f.browser(t, &opened, tt.callback), In: strings.NewReader(tt.answer), Out: &out, LoginTimeout: tt.timeout}
			_, err := o.Login(context.Background(), f.instance())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "secret detail", "the token endpoint's response body is not echoed")
			assert.Equal(t, tt.opened, opened)
			assert.Empty(t, store.values)
			if tt.exchange == "" {
				assert.Zero(t, f.tokenCalls, "no code exchange after a failed authorization")
			}
		})
	}
}

func TestOAuthLogin_ContextCanceledAndBrowserError(t *testing.T) {
	f := newFakeOAuthServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	o := &OAuth{Store: newMemStore(), In: strings.NewReader("y\n"), Out: &out, OpenBrowser: func(context.Context, string) error {
		cancel()
		return errors.New("no display")
	}}
	_, err := o.Login(ctx, f.instance())
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, out.String(), "Could not open a browser (no display)")
}

func TestOAuthLogin_KeyringFailureStillReturnsToken(t *testing.T) {
	f := newFakeOAuthServer(t)
	store := newMemStore()
	store.setErr = errors.New("keyring locked")
	var out bytes.Buffer
	opened := 0
	o := &OAuth{Store: store, OpenBrowser: f.browser(t, &opened, approve), In: strings.NewReader("y\n"), Out: &out}
	tok, err := o.Login(context.Background(), f.instance())
	require.NoError(t, err)
	assert.Equal(t, "access-1", tok)
	assert.Contains(t, out.String(), "could not cache the OAuth token in the OS keyring (keyring locked)")
}

func TestOAuthLogin_InvalidBaseURL(t *testing.T) {
	store := newMemStore()
	o := &OAuth{Store: store}
	_, err := o.Login(context.Background(), OAuthInstance{Alias: "x", BaseURL: "ftp://x", ClientID: "c"})
	assert.ErrorContains(t, err, "absolute https URL")
	cacheToken(t, store, "x|ftp://x|c", storedToken{AccessToken: "a"})
	_, _, err = o.CachedToken(context.Background(), OAuthInstance{Alias: "x", BaseURL: "ftp://x", ClientID: "c"})
	assert.ErrorContains(t, err, "absolute https URL")
}

func cacheToken(t *testing.T, store *memStore, key string, st storedToken) {
	t.Helper()
	data, err := json.Marshal(st)
	require.NoError(t, err)
	store.values[key] = string(data)
}

func TestOAuthCachedToken(t *testing.T) {
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	t.Run("nothing cached", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		o := &OAuth{Store: newMemStore()}
		tok, reason, err := o.CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Empty(t, tok)
		assert.Equal(t, "no cached OAuth token", reason)
		assert.Zero(t, f.userCalls)
	})
	t.Run("valid token is checked and used", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		f.validAccess["access-1"] = true
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", RefreshToken: "refresh-1", Expiry: future})
		tok, _, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Equal(t, "access-1", tok)
		assert.Equal(t, 1, f.userCalls)
		assert.Zero(t, f.tokenCalls)
	})
	t.Run("another alias or client does not see the token", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", Expiry: future})
		other := f.instance()
		other.Alias = "work"
		_, reason, err := (&OAuth{Store: store}).CachedToken(ctx, other)
		require.NoError(t, err)
		assert.Equal(t, "no cached OAuth token", reason)
		other = f.instance()
		other.ClientID = "client-2"
		_, reason, err = (&OAuth{Store: store}).CachedToken(ctx, other)
		require.NoError(t, err)
		assert.Equal(t, "no cached OAuth token", reason)
	})
	t.Run("expired token is refreshed and recached", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		f.validRefresh = "refresh-1"
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", RefreshToken: "refresh-1", Expiry: past})
		tok, _, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Equal(t, "access-2", tok)
		assert.Equal(t, 1, f.tokenCalls)
		assert.Contains(t, store.values[storeKeyFor(f)], "refresh-2")
	})
	t.Run("rejected refresh removes the token", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", RefreshToken: "revoked", Expiry: past})
		tok, reason, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Empty(t, tok)
		assert.Contains(t, reason, "could not be refreshed")
		assert.Empty(t, store.values)
	})
	t.Run("expired token without refresh token is removed", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", Expiry: past})
		_, reason, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Contains(t, reason, "could not be refreshed")
		assert.Empty(t, store.values)
		assert.Zero(t, f.tokenCalls)
	})
	t.Run("revoked token is removed", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "revoked", Expiry: future})
		_, reason, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Contains(t, reason, "rejected the cached OAuth token")
		assert.Empty(t, store.values)
	})
	t.Run("unreadable entry is removed", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		store.values[storeKeyFor(f)] = "not json"
		_, reason, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Contains(t, reason, "unreadable")
		assert.Empty(t, store.values)
	})
	t.Run("keyring read failure is a cache miss", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		store.getErr = errors.New("no secret service")
		_, reason, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.NoError(t, err)
		assert.Equal(t, "the OS keyring could not be read (no secret service)", reason)
	})
	t.Run("unreachable instance is an error and keeps the token", func(t *testing.T) {
		f := newFakeOAuthServer(t)
		store := newMemStore()
		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", RefreshToken: "refresh-1", Expiry: future})
		f.srv.Close()
		_, _, err := (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.ErrorContains(t, err, "checking the cached OAuth token")
		assert.NotContains(t, err.Error(), "access-1")
		assert.Len(t, store.values, 1)

		cacheToken(t, store, storeKeyFor(f), storedToken{AccessToken: "access-1", RefreshToken: "refresh-1", Expiry: past})
		_, _, err = (&OAuth{Store: store}).CachedToken(ctx, f.instance())
		require.ErrorContains(t, err, "refreshing the cached OAuth token")
		assert.NotContains(t, err.Error(), "refresh-1")
		assert.Len(t, store.values, 1)
	})
}

func TestCallbackHandler_IgnoresOtherPathsAndLaterCallbacks(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := callbackHandler("s", results)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, OAuthCallbackPath+"?code=c&state=s", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, OAuthCallbackPath+"?code=x&state=bad", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	res := <-results
	assert.Equal(t, "c", res.code, "the first callback decides the outcome")
}

func TestKeyringStore(t *testing.T) {
	keyring.MockInit()
	var s KeyringStore
	_, err := s.Get("k")
	assert.ErrorIs(t, err, ErrTokenNotStored)
	require.NoError(t, s.Set("k", "v"))
	v, err := s.Get("k")
	require.NoError(t, err)
	assert.Equal(t, "v", v)
	require.NoError(t, s.Delete("k"))
	require.NoError(t, s.Delete("k"), "deleting a missing key succeeds")
	_, err = s.Get("k")
	assert.ErrorIs(t, err, ErrTokenNotStored)

	keyring.MockInitWithError(errors.New("unavailable"))
	assert.Error(t, s.Delete("k"))
}
