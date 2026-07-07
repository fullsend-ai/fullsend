package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// fallbackHandler routes token requests to the local handler if the role
// has a local PEM, otherwise proxies the request to a fallback (upstream) mint.
type fallbackHandler struct {
	local       http.Handler
	localRoles  map[string]bool
	fallbackURL string
	httpClient  *http.Client
}

// writeJSONError mirrors mintcore's unexported writeError; duplicated here
// because cmd/mint is a separate Go module and cannot import unexported helpers.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func newFallbackHandler(local http.Handler, localRoles map[string]bool, fallbackURL string) *fallbackHandler {
	return &fallbackHandler{
		local:       local,
		localRoles:  localRoles,
		fallbackURL: fallbackURL,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (f *fallbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/token" {
		log.Printf("non-token request: %s %s", r.Method, r.URL.Path)
		f.local.ServeHTTP(w, r)
		return
	}
	log.Printf("token request from %s", r.RemoteAddr)

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	r.Body.Close()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Role == "" {
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.local.ServeHTTP(w, r)
		return
	}

	if f.localRoles[strings.ToLower(req.Role)] || f.fallbackURL == "" {
		log.Printf("routing role %q locally", req.Role)
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.local.ServeHTTP(w, r)
		return
	}

	log.Printf("proxying role %q to fallback %s", req.Role, f.fallbackURL)
	f.proxyToFallback(w, r, body)
}

func (f *fallbackHandler) proxyToFallback(w http.ResponseWriter, r *http.Request, body []byte) {
	target, err := url.JoinPath(f.fallbackURL, "/v1/token")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "invalid fallback URL")
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create proxy request")
		return
	}
	proxyReq.Header.Set("Authorization", r.Header.Get("Authorization"))
	proxyReq.Header.Set("Content-Type", "application/json")

	resp, err := f.httpClient.Do(proxyReq)
	if err != nil {
		log.Printf("fallback proxy error: %v", err)
		writeJSONError(w, http.StatusBadGateway, "fallback mint unreachable")
		return
	}
	defer resp.Body.Close()

	log.Printf("fallback responded: %d", resp.StatusCode)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Printf("fallback proxy read error: %v", err)
		writeJSONError(w, http.StatusBadGateway, "failed to read fallback response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
