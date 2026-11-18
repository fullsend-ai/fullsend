package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// maxBotRoleRequestBytes bounds the request body a script may send.
const maxBotRoleRequestBytes = 1 << 20

// botRoleHelperEnv holds the loopback URL and bearer token of the
// runner-side bot-role helper service (#9602). When non-empty, the run
// injects them as FULLSEND_BOT_ROLE_URL / FULLSEND_BOT_ROLE_TOKEN into the
// environment of host-side harness pre- and post-scripts, so a script can
// resolve a bot's registered role (ADR 0107) for the run's target
// repository without ever seeing the Actions OIDC credentials the lookup
// authenticates with (#5832, ADR 0073).
type botRoleHelperEnv struct {
	url   string
	token string
}

// exports returns the environment entries that carry the helper to a
// host-side script, or nil when no helper is running.
func (e botRoleHelperEnv) exports() []string {
	if e.url == "" {
		return nil
	}
	return []string{"FULLSEND_BOT_ROLE_URL=" + e.url, "FULLSEND_BOT_ROLE_TOKEN=" + e.token}
}

// botRoleHelper is the runner-side bot-role lookup service: it answers a
// host-side script's `fullsend bot-role <login>` request with the ADR 0107
// result of resolving that login for the run's target repository through
// the mint (or an equivalent trusted resolver). The helper holds the OIDC
// credential and the target; the script holds neither — it cannot choose
// the repository it asks about.
type botRoleHelper struct {
	resolver botidentity.BotRoleResolver
	target   string
	// errLog receives lookup-failure detail runner-side; the response a
	// script sees carries a fixed message only, so wrapped transport
	// errors (mint URL, OIDC fetch detail) never reach script logs.
	errLog *log.Logger
}

// ServeHTTP answers one lookup. A completed lookup answers HTTP 200 with
// the mint's bot-role response shape ({"outcome":"recognized","role":…} or
// {"outcome":"no_match"}); a lookup that cannot be completed answers an
// error status with an error body, so the caller's `fullsend bot-role`
// fails instead of printing no_match — a failed lookup stays distinct from
// a no-match and the caller fails closed.
func (h *botRoleHelper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBotRoleRequestBytes)
	var req botRoleScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBotRoleJSON(w, http.StatusBadRequest, botidentity.MintBotRoleResponse{Error: "invalid request body"})
		return
	}
	if req.Login == "" {
		writeBotRoleJSON(w, http.StatusBadRequest, botidentity.MintBotRoleResponse{Error: "login is required"})
		return
	}
	// A login that cannot be a GitHub App bot login (the mint's
	// botLoginPattern requires the "[bot]" suffix) can never match, so
	// answer the completed no-match here instead of forwarding it to a
	// certain 400: a human author is not a registered bot, not a lookup
	// failure. This mirrors fullsend dispatch's ActorBot gate.
	// shortcut: only the missing-suffix case is gated here; a login that
	// is pattern-invalid another way (an empty slug, a "/" in the name,
	// over-long) still reaches the mint and fails closed as a lookup
	// failure.
	if !strings.HasSuffix(strings.ToLower(req.Login), "[bot]") {
		writeBotRoleJSON(w, http.StatusOK, botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeNoMatch})
		return
	}
	res, err := h.resolver.ResolveBotRole(r.Context(), botidentity.BotRoleQuery{
		Forge:  string(normevent.SystemGitHub),
		Target: h.target,
		Login:  req.Login,
	})
	if err != nil {
		if h.errLog != nil {
			h.errLog.Printf("bot-role lookup failed: %v", err)
		}
		writeBotRoleJSON(w, http.StatusBadGateway, botidentity.MintBotRoleResponse{Error: "bot-role lookup failed"})
		return
	}
	if res.Recognized() {
		writeBotRoleJSON(w, http.StatusOK, botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeRecognized, Role: res.Role})
		return
	}
	writeBotRoleJSON(w, http.StatusOK, botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeNoMatch})
}

// writeBotRoleJSON writes one helper response in the mint's bot-role
// response shape.
func writeBotRoleJSON(w http.ResponseWriter, status int, resp botidentity.MintBotRoleResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// startBotRoleHelperService starts the loopback bot-role helper service for
// one run. It returns the helper's env (its URL and bearer token), a
// shutdown function, and any error. Unlike the runtime fetch service, which
// the sandbox container must reach and therefore binds all interfaces
// (ADR-0046), the consumers here are host-side scripts, so the listener is
// loopback-only.
func startBotRoleHelperService(resolver botidentity.BotRoleResolver, target string) (botRoleHelperEnv, func(), error) {
	token, err := generateToken()
	if err != nil {
		return botRoleHelperEnv{}, nil, fmt.Errorf("generating bot-role helper token: %w", err)
	}
	// The token reaches host-side scripts, whose stdout/stderr is tee'd to
	// the Actions log and can flow into the completion comment: register
	// it with the runtime-secret redaction registry like every other
	// runner-held credential (see the pre-mint workflow token).
	security.RegisterRuntimeSecret(token)

	errLog := log.New(log.Writer(), "botrole: ", log.LstdFlags)
	handler := withBearerAuth(token, &botRoleHelper{resolver: resolver, target: target, errLog: errLog})
	mux := http.NewServeMux()
	mux.Handle("/bot-role", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return botRoleHelperEnv{}, nil, fmt.Errorf("listening for bot-role helper service: %w", err)
	}

	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // beyond the helper's whole lookup (OIDC fetch included), which internal/botidentity bounds to 30s
		IdleTimeout:  60 * time.Second,
		ErrorLog:     errLog,
	}
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			server.ErrorLog.Printf("server error: %v", err)
		}
	}()

	shutdownFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}

	return botRoleHelperEnv{url: "http://" + ln.Addr().String() + "/bot-role", token: token}, shutdownFn, nil
}
