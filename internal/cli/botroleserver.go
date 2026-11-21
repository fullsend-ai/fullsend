package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// maxBotRoleRequestBytes bounds the request body a script may send.
const maxBotRoleRequestBytes = 1 << 20

// botRoleHelperEnv holds the loopback URL and bearer token of the
// runner-side bot-role helper service (#9602), and the canonical ID of the
// entity the run handles. When the URL is non-empty, the run injects them
// as FULLSEND_BOT_ROLE_URL / FULLSEND_BOT_ROLE_TOKEN / FULLSEND_ENTITY_ID
// into the environment of host-side harness pre- and post-scripts, so a
// script can resolve the registered role (ADR 0107) of a comment's actor
// on that entity without ever seeing the Actions OIDC credentials the
// lookup authenticates with (#5832, ADR 0073).
type botRoleHelperEnv struct {
	url    string
	token  string
	entity string
}

// exports returns the environment entries that carry the helper to a
// host-side script, or nil when no helper is running. FULLSEND_ENTITY_ID is
// left out when the run's entity could not be resolved: the helper then
// refuses every lookup, and a script has no entity to name.
func (e botRoleHelperEnv) exports() []string {
	if e.url == "" {
		return nil
	}
	out := []string{"FULLSEND_BOT_ROLE_URL=" + e.url, "FULLSEND_BOT_ROLE_TOKEN=" + e.token}
	if e.entity != "" {
		out = append(out, "FULLSEND_ENTITY_ID="+e.entity)
	}
	return out
}

// commentActor is the current-revision actor of one comment and the
// canonical ID of the entity the comment is on. Login is the REST form a
// mint lookup takes (slug[bot] for a bot).
type commentActor struct {
	Entity string
	Bot    bool
	Login  string
}

// commentActorLookup reads a comment's current-revision actor from the
// forge: the last editor when the comment was edited, its author otherwise
// (ADR 0133, ADR 0141).
type commentActorLookup interface {
	CommentActor(ctx context.Context, commentID string) (commentActor, error)
}

// botRoleHelper is the runner-side bot-role lookup service: it answers a
// host-side script's `fullsend bot-role --entity --comment` request with
// the ADR 0107 result for the comment's current-revision actor, resolved
// for the run's target repository through the mint. The helper holds the
// OIDC credential, the target and the entity; the script holds none of
// them — it cannot choose the repository or the entity it asks about.
// With no comment lookup (GitLab, Jira, or a run whose entity could not be
// resolved) every request is refused, so a caller fails closed.
type botRoleHelper struct {
	resolver botidentity.BotRoleResolver
	target   string
	entity   string
	comments commentActorLookup
	// errLog receives lookup-failure detail runner-side; the response a
	// script sees carries a fixed message only, so wrapped transport
	// errors (mint URL, OIDC fetch detail, forge errors) never reach
	// script logs.
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
	if req.Entity == "" || req.Comment == "" {
		writeBotRoleJSON(w, http.StatusBadRequest, botidentity.MintBotRoleResponse{Error: "entity and comment are required"})
		return
	}
	if h.comments == nil || h.entity == "" {
		writeBotRoleJSON(w, http.StatusNotImplemented, botidentity.MintBotRoleResponse{Error: "no trusted bot-role lookup for this run"})
		return
	}
	if req.Entity != h.entity {
		writeBotRoleJSON(w, http.StatusBadRequest, botidentity.MintBotRoleResponse{Error: "entity is not the one this run handles"})
		return
	}
	// One GitHub comment lookup runs under githubGraphQLTimeout (tighter
	// than the run-start entity call's botRoleEntityTimeout, which sits on
	// the critical path before a pre-script), so a slow lookup cannot
	// outlive the bounds the WriteTimeout and botRoleCmdTimeout comments
	// count.
	ctx, cancel := context.WithTimeout(r.Context(), githubGraphQLTimeout)
	defer cancel()
	actor, err := h.comments.CommentActor(ctx, req.Comment)
	if err != nil {
		h.logf("comment lookup failed: %v", err)
		writeBotRoleJSON(w, http.StatusBadGateway, botidentity.MintBotRoleResponse{Error: "bot-role lookup failed"})
		return
	}
	if actor.Entity != h.entity {
		writeBotRoleJSON(w, http.StatusBadRequest, botidentity.MintBotRoleResponse{Error: "comment is not on the entity this run handles"})
		return
	}
	// A human actor, including a human who last edited a bot's comment,
	// is not a registered bot: a completed no-match that never reaches
	// the mint.
	if !actor.Bot {
		writeBotRoleJSON(w, http.StatusOK, botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeNoMatch})
		return
	}
	res, err := h.resolver.ResolveBotRole(r.Context(), botidentity.BotRoleQuery{
		Forge:  string(normevent.SystemGitHub),
		Target: h.target,
		Login:  actor.Login,
	})
	if err != nil {
		h.logf("bot-role lookup failed: %v", err)
		writeBotRoleJSON(w, http.StatusBadGateway, botidentity.MintBotRoleResponse{Error: "bot-role lookup failed"})
		return
	}
	if res.Recognized() {
		writeBotRoleJSON(w, http.StatusOK, botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeRecognized, Role: res.Role})
		return
	}
	writeBotRoleJSON(w, http.StatusOK, botidentity.MintBotRoleResponse{Outcome: botidentity.MintOutcomeNoMatch})
}

// logf records failure detail runner-side, when the helper has a log.
func (h *botRoleHelper) logf(format string, args ...any) {
	if h.errLog != nil {
		h.errLog.Printf(format, args...)
	}
}

// writeBotRoleJSON writes one helper response in the mint's bot-role
// response shape.
func writeBotRoleJSON(w http.ResponseWriter, status int, resp botidentity.MintBotRoleResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// startBotRoleHelperService starts the loopback bot-role helper service for
// one run, answering for comments on entity through comments (nil when the
// run has no trusted comment lookup). It returns the helper's env (its URL,
// bearer token and entity), a shutdown function, and any error. Unlike the
// runtime fetch service, which the sandbox container must reach and
// therefore binds all interfaces (ADR-0046), the consumers here are
// host-side scripts, so the listener is loopback-only.
func startBotRoleHelperService(resolver botidentity.BotRoleResolver, target, entity string, comments commentActorLookup) (botRoleHelperEnv, func(), error) {
	token, err := generateToken()
	if err != nil {
		return botRoleHelperEnv{}, nil, fmt.Errorf("generating bot-role helper token: %w", err)
	}
	// The token reaches host-side scripts, whose stdout/stderr is tee'd to
	// the Actions log and can flow into the completion comment: register
	// it with the runtime-secret redaction registry like every other
	// runner-held credential (see the pre-mint workflow token), and ask
	// Actions to mask it in the job log the same way.
	security.RegisterRuntimeSecret(token)
	maskActionsValue(token)

	errLog := log.New(log.Writer(), "botrole: ", log.LstdFlags)
	handler := withBearerAuth(token, &botRoleHelper{resolver: resolver, target: target, entity: entity, comments: comments, errLog: errLog})
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
		WriteTimeout: 120 * time.Second, // beyond the helper's whole lookup: the GitHub comment lookup (githubGraphQLTimeout) and the OIDC fetch (internal/botidentity bounds to 30s)
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

	if comments == nil {
		entity = ""
	}
	return botRoleHelperEnv{url: "http://" + ln.Addr().String() + "/bot-role", token: token, entity: entity}, shutdownFn, nil
}
