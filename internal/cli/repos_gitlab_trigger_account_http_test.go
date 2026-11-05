package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// These tests drive the GitLab trigger-only service account lifecycle through
// the real CLI wiring (maybeConvergeGitLabTriggerAccount,
// appendGitLabTriggerAccountStatus and the uninstall token adapter) with a real
// gitlab.LiveClient talking to a stateful httptest server that emulates the
// GitLab REST endpoints the lifecycle uses. Any request the emulation does not
// handle fails the test, so a gap in the emulation is visible instead of being
// read by the client as a 404.

const (
	tahProject     = "/projects/g%2Fp"
	tahAdminToken  = "admin-token"
	tahAdminUserID = 1
	tahBranch      = "main"
)

var tahSink = forge.SecretGitLabTriggerAccountToken

type tahAccount struct {
	ID   int
	Name string
}

type tahPAT struct {
	ID        int
	UserID    int
	Name      string
	Token     string
	ExpiresAt string
	Scopes    []string
	Revoked   bool
}

type tahDeployKey struct {
	ID          int
	OwnerID     int
	Title       string
	Fingerprint string
	CanPush     bool
}

type tahTrigger struct {
	ID          int64
	OwnerID     int
	Description string
	Token       string
}

type tahVar struct {
	Value     string
	Masked    bool
	Protected bool
	VarType   string
}

type tahPushEntry struct {
	ID          int
	DeployKeyID int // zero for an ordinary role entry
	AccessLevel int
}

// tahServer is a mutex-guarded, in-memory GitLab project g/p.
type tahServer struct {
	t   *testing.T
	srv *httptest.Server

	mu           sync.Mutex
	hits         map[string]int
	events       []string
	vars         map[string]*tahVar
	accounts     map[int]*tahAccount
	members      map[int]int
	levelHistory map[int][]int
	pats         map[int]*tahPAT
	keys         map[int]*tahDeployKey
	triggers     map[int64]*tahTrigger
	push         []tahPushEntry
	protected    bool

	accountNext int
	patNext     int
	keyNext     int
	trigNext    int64
	entryNext   int

	// Fault injection.
	failDeleteAccount bool
	deleteAttempts    int
	varGetStatus      map[string]int
	// repoStatus, when non-zero, fails the project read with that status.
	repoStatus int
	// overrideRole is the project's ci_pipeline_variables_minimum_override_role.
	overrideRole string
	// rejectMergeGrant makes the per-user protected-branch merge grant fail
	// with 403 (GitLab.com Free), counting each attempt in mergeGrantAttempts.
	rejectMergeGrant   bool
	mergeGrantAttempts int
	// mergeRole is the branch's role-based merge grant. The default Developer
	// grant lets the trigger account own schedules in the activation fixtures.
	mergeRole int
	// mergeUsers are the per-user merge grants on the default branch.
	mergeUsers []int
	// fallback answers requests outside the trigger-account emulation; it
	// reports whether it handled the request.
	fallback func(w http.ResponseWriter, r *http.Request) bool
}

func newTAHServer(t *testing.T) *tahServer {
	t.Helper()
	s := &tahServer{
		t:            t,
		hits:         map[string]int{},
		vars:         map[string]*tahVar{},
		accounts:     map[int]*tahAccount{},
		members:      map[int]int{},
		levelHistory: map[int][]int{},
		pats:         map[int]*tahPAT{},
		keys:         map[int]*tahDeployKey{},
		triggers:     map[int64]*tahTrigger{},
		protected:    true,
		accountNext:  100,
		patNext:      1000,
		keyNext:      2000,
		trigNext:     3000,
		entryNext:    10,
		varGetStatus: map[string]int{},
		overrideRole: forge.PipelineVarOverrideNoOneAllowed,
		mergeRole:    30,
	}
	// The administrator's own rule for the default branch: a maintainer entry
	// that the lifecycle must never touch.
	s.push = []tahPushEntry{{ID: 1, AccessLevel: 40}}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *tahServer) logf(format string, args ...any) {
	s.events = append(s.events, fmt.Sprintf(format, args...))
}

func tahJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (s *tahServer) unhandled(w http.ResponseWriter, r *http.Request) {
	if s.fallback != nil && s.fallback(w, r) {
		return
	}
	s.t.Errorf("unhandled GitLab request %s %s", r.Method, r.URL.RequestURI())
	tahJSON(w, http.StatusNotImplemented, map[string]any{"message": "unhandled by the test emulation"})
}

func (s *tahServer) decode(r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		s.t.Errorf("decoding the body of %s %s: %v", r.Method, r.URL.Path, err)
	}
}

// authenticate maps the PRIVATE-TOKEN header to the acting user, or zero when
// GitLab would answer 401.
func (s *tahServer) authenticate(r *http.Request) int {
	tok := r.Header.Get("PRIVATE-TOKEN")
	if tok == tahAdminToken {
		return tahAdminUserID
	}
	for _, p := range s.pats {
		if p.Token == tok && !p.Revoked {
			if _, ok := s.accounts[p.UserID]; ok {
				return p.UserID
			}
		}
	}
	return 0
}

func (s *tahServer) canAdminister(uid int) bool {
	return uid == tahAdminUserID || s.members[uid] >= 40
}

func (s *tahServer) protectedBranchPayload() map[string]any {
	levels := make([]any, 0, len(s.push))
	for _, e := range s.push {
		lvl := map[string]any{"id": e.ID, "access_level": e.AccessLevel, "user_id": nil, "group_id": nil, "deploy_key_id": nil}
		if e.DeployKeyID != 0 {
			lvl["deploy_key_id"] = e.DeployKeyID
		}
		levels = append(levels, lvl)
	}
	merges := make([]any, 0, len(s.mergeUsers)+1)
	if s.mergeRole > 0 {
		merges = append(merges, map[string]any{"id": 499, "access_level": s.mergeRole, "user_id": nil, "group_id": nil, "deploy_key_id": nil})
	}
	for i, uid := range s.mergeUsers {
		merges = append(merges, map[string]any{"id": 500 + i, "access_level": nil, "user_id": uid, "group_id": nil, "deploy_key_id": nil})
	}
	return map[string]any{"name": tahBranch, "push_access_levels": levels, "merge_access_levels": merges}
}

var tahMaskable = regexp.MustCompile(`^[a-zA-Z0-9@:.+/=_~-]+$`)

func tahRouteKey(method string, seg []string) string {
	parts := make([]string, len(seg))
	for i, p := range seg {
		switch {
		case isTAHNumber(p):
			parts[i] = ":id"
		case i == 1 && (seg[0] == "variables" || seg[0] == "protected_branches"):
			parts[i] = ":name"
		default:
			parts[i] = p
		}
	}
	return method + " /" + strings.Join(parts, "/")
}

func isTAHNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func tahSegID(seg []string, i int) int {
	n, _ := strconv.Atoi(seg[i])
	return n
}

func (s *tahServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4")
	uid := s.authenticate(r)
	if uid == 0 {
		s.hits["401 "+r.Method+" "+p]++
		tahJSON(w, http.StatusUnauthorized, map[string]any{"message": "401 Unauthorized"})
		return
	}
	switch {
	case p == "/user" && r.Method == http.MethodGet:
		s.hits["GET /user"]++
		tahJSON(w, http.StatusOK, map[string]any{"id": uid, "username": fmt.Sprintf("user%d", uid)})
	case p == "/personal_access_tokens/self" && r.Method == http.MethodGet:
		s.hits["GET /personal_access_tokens/self"]++
		tok := r.Header.Get("PRIVATE-TOKEN")
		if tok == tahAdminToken {
			tahJSON(w, http.StatusOK, map[string]any{"id": 1, "name": "installer", "active": true, "scopes": []string{"api"}, "user_id": tahAdminUserID})
			return
		}
		for _, pat := range s.pats {
			if pat.Token == tok {
				tahJSON(w, http.StatusOK, map[string]any{
					"id": pat.ID, "name": pat.Name, "active": !pat.Revoked, "revoked": pat.Revoked,
					"user_id": pat.UserID, "scopes": pat.Scopes, "expires_at": pat.ExpiresAt,
				})
				return
			}
		}
		tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	case p == tahProject && r.Method == http.MethodGet:
		s.hits["GET /project"]++
		if s.repoStatus != 0 {
			tahJSON(w, s.repoStatus, map[string]any{"message": "injected repository read failure"})
			return
		}
		tahJSON(w, http.StatusOK, map[string]any{
			"id": 7, "name": "p", "path_with_namespace": "g/p",
			"default_branch": tahBranch, "visibility": "private",
			"ci_pipeline_variables_minimum_override_role": s.overrideRole,
			"ci_push_repository_for_job_token_allowed":    false,
		})
	case p == "/admin/ci/variables" && r.Method == http.MethodGet:
		// The activation gate inventories inherited variable overrides. The
		// emulated instance and namespace define none.
		s.hits["GET /admin/ci/variables"]++
		tahJSON(w, http.StatusOK, []any{})
	case strings.HasPrefix(p, "/groups/") && strings.HasSuffix(p, "/variables") && r.Method == http.MethodGet:
		// The project lives in a personal namespace, which has no group.
		s.hits["GET /groups/:id/variables"]++
		tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Group Not Found"})
	case strings.HasPrefix(p, "/users/") && strings.HasSuffix(p, "/keys") && r.Method == http.MethodGet:
		s.hits["GET /users/:id/keys"]++
		tahJSON(w, http.StatusOK, []any{})
	case strings.HasPrefix(p, tahProject+"/"):
		s.project(w, r, uid, strings.Split(strings.TrimPrefix(p, tahProject+"/"), "/"))
	default:
		s.unhandled(w, r)
	}
}

func (s *tahServer) accountName(id int) string {
	if a, ok := s.accounts[id]; ok {
		return a.Name
	}
	return fmt.Sprintf("user-%d", id)
}

func (s *tahServer) setLevel(uid, level int) {
	s.members[uid] = level
	s.levelHistory[uid] = append(s.levelHistory[uid], level)
	s.logf("member:%d:%d", uid, level)
}

func (s *tahServer) project(w http.ResponseWriter, r *http.Request, uid int, seg []string) {
	key := tahRouteKey(r.Method, seg)
	s.hits[key]++
	// The activation gate reads the committed CI files (#8438) to check that
	// no unrelated job runs in trigger or agent child pipelines. This
	// emulation commits none, so every file is absent.
	if len(seg) >= 3 && seg[0] == "repository" && seg[1] == "files" && r.Method == http.MethodGet {
		tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 File Not Found"})
		return
	}
	switch key {
	// ---- CI/CD variables (ledger, sink, lease, Poller state) ----
	case "POST /variables":
		var b struct {
			Key       string `json:"key"`
			Value     string `json:"value"`
			Protected bool   `json:"protected"`
			Masked    bool   `json:"masked"`
			VarType   string `json:"variable_type"`
			Scope     string `json:"environment_scope"`
		}
		s.decode(r, &b)
		if b.Scope != "*" {
			s.t.Errorf("variable %s created with environment scope %q", b.Key, b.Scope)
		}
		if _, ok := s.vars[b.Key]; ok {
			tahJSON(w, http.StatusConflict, map[string]any{"message": "Key (" + b.Key + ") has already been taken"})
			return
		}
		if b.Masked && (len(b.Value) < 8 || !tahMaskable.MatchString(b.Value)) {
			tahJSON(w, http.StatusBadRequest, map[string]any{"message": map[string]any{"value": []string{"cannot be masked"}}})
			return
		}
		s.vars[b.Key] = &tahVar{Value: b.Value, Masked: b.Masked, Protected: b.Protected, VarType: b.VarType}
		s.logf("var-create:%s", b.Key)
		tahJSON(w, http.StatusCreated, map[string]any{"key": b.Key})
	case "GET /variables":
		names := make([]string, 0, len(s.vars))
		for n := range s.vars {
			names = append(names, n)
		}
		sort.Strings(names)
		out := make([]any, 0, len(names))
		for _, n := range names {
			out = append(out, map[string]any{"key": n, "value": s.vars[n].Value, "environment_scope": "*"})
		}
		tahJSON(w, http.StatusOK, out)
	case "GET /variables/:name", "PUT /variables/:name", "DELETE /variables/:name":
		if r.URL.Query().Get("filter[environment_scope]") != "*" {
			s.t.Errorf("variable request without the wildcard scope filter: %s", r.URL.RequestURI())
		}
		name := seg[1]
		v, ok := s.vars[name]
		switch r.Method {
		case http.MethodGet:
			if code := s.varGetStatus[name]; code != 0 {
				tahJSON(w, code, map[string]any{"message": "injected failure"})
				return
			}
			if !ok {
				tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			tahJSON(w, http.StatusOK, map[string]any{
				"key": name, "value": v.Value, "masked": v.Masked, "protected": v.Protected,
				"variable_type": v.VarType, "environment_scope": "*",
			})
		case http.MethodPut:
			if !ok {
				tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			var b struct {
				Value     *string `json:"value"`
				Protected *bool   `json:"protected"`
				Masked    *bool   `json:"masked"`
				VarType   *string `json:"variable_type"`
			}
			s.decode(r, &b)
			if b.Value != nil {
				v.Value = *b.Value
			}
			if b.Protected != nil {
				v.Protected = *b.Protected
			}
			if b.Masked != nil {
				v.Masked = *b.Masked
			}
			if b.VarType != nil {
				v.VarType = *b.VarType
			}
			s.logf("var-update:%s", name)
			tahJSON(w, http.StatusOK, map[string]any{"key": name})
		case http.MethodDelete:
			if !ok {
				tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Variable Not Found"})
				return
			}
			delete(s.vars, name)
			s.logf("var-delete:%s", name)
			w.WriteHeader(http.StatusNoContent)
		}

	// ---- Project service accounts and their personal access tokens ----
	case "GET /service_accounts":
		ids := make([]int, 0, len(s.accounts))
		for id := range s.accounts {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		out := make([]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]any{"id": id, "username": fmt.Sprintf("sa_%d", id), "name": s.accounts[id].Name})
		}
		tahJSON(w, http.StatusOK, out)
	case "POST /service_accounts":
		var b struct {
			Name string `json:"name"`
		}
		s.decode(r, &b)
		id := s.accountNext
		s.accountNext++
		s.accounts[id] = &tahAccount{ID: id, Name: b.Name}
		s.logf("account-create:%d", id)
		tahJSON(w, http.StatusCreated, map[string]any{"id": id, "username": fmt.Sprintf("sa_%d", id), "name": b.Name})
	case "DELETE /service_accounts/:id":
		id := tahSegID(seg, 1)
		s.deleteAttempts++
		if s.failDeleteAccount {
			tahJSON(w, http.StatusInternalServerError, map[string]any{"message": "injected failure"})
			return
		}
		if _, ok := s.accounts[id]; !ok {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 User Not Found"})
			return
		}
		delete(s.accounts, id)
		delete(s.members, id)
		for _, p := range s.pats {
			if p.UserID == id {
				p.Revoked = true
			}
		}
		s.logf("account-delete:%d", id)
		w.WriteHeader(http.StatusNoContent)
	case "GET /service_accounts/:id/personal_access_tokens":
		id := tahSegID(seg, 1)
		ids := make([]int, 0)
		for pid, p := range s.pats {
			if p.UserID == id {
				ids = append(ids, pid)
			}
		}
		sort.Ints(ids)
		out := make([]any, 0, len(ids))
		for _, pid := range ids {
			p := s.pats[pid]
			out = append(out, map[string]any{
				"id": p.ID, "name": p.Name, "active": !p.Revoked, "revoked": p.Revoked,
				"user_id": p.UserID, "expires_at": p.ExpiresAt, "scopes": p.Scopes,
			})
		}
		tahJSON(w, http.StatusOK, out)
	case "POST /service_accounts/:id/personal_access_tokens":
		id := tahSegID(seg, 1)
		if _, ok := s.accounts[id]; !ok {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 User Not Found"})
			return
		}
		var b struct {
			Name      string   `json:"name"`
			Scopes    []string `json:"scopes"`
			ExpiresAt string   `json:"expires_at"`
		}
		s.decode(r, &b)
		pid := s.patNext
		s.patNext++
		p := &tahPAT{ID: pid, UserID: id, Name: b.Name, Token: fmt.Sprintf("glpat-bootstrap-%06d", pid), ExpiresAt: b.ExpiresAt, Scopes: b.Scopes}
		s.pats[pid] = p
		s.logf("pat-create:%d", id)
		tahJSON(w, http.StatusCreated, map[string]any{
			"id": pid, "name": p.Name, "token": p.Token, "active": true, "user_id": id,
			"expires_at": p.ExpiresAt, "scopes": p.Scopes,
		})
	case "DELETE /service_accounts/:id/personal_access_tokens/:id":
		uidParam, pid := tahSegID(seg, 1), tahSegID(seg, 3)
		p, ok := s.pats[pid]
		if !ok || p.UserID != uidParam {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
			return
		}
		p.Revoked = true
		s.logf("pat-revoke:%d", p.UserID)
		w.WriteHeader(http.StatusNoContent)

	// ---- Project membership ----
	case "GET /members/all":
		ids := make([]int, 0, len(s.members))
		for id := range s.members {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		out := make([]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]any{"id": id, "username": fmt.Sprintf("m%d", id), "name": s.accountName(id), "access_level": s.members[id]})
		}
		tahJSON(w, http.StatusOK, out)
	case "GET /members/all/:id":
		id := tahSegID(seg, 2)
		lvl, ok := s.members[id]
		if !ok {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return
		}
		tahJSON(w, http.StatusOK, map[string]any{"id": id, "access_level": lvl})
	case "POST /members":
		var b struct {
			UserID      int `json:"user_id"`
			AccessLevel int `json:"access_level"`
		}
		s.decode(r, &b)
		if _, ok := s.members[b.UserID]; ok {
			tahJSON(w, http.StatusConflict, map[string]any{"message": "Member already exists"})
			return
		}
		s.setLevel(b.UserID, b.AccessLevel)
		tahJSON(w, http.StatusCreated, map[string]any{"id": b.UserID, "access_level": b.AccessLevel})
	case "PUT /members/:id":
		id := tahSegID(seg, 1)
		if _, ok := s.members[id]; !ok {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return
		}
		var b struct {
			AccessLevel int `json:"access_level"`
		}
		s.decode(r, &b)
		s.setLevel(id, b.AccessLevel)
		tahJSON(w, http.StatusOK, map[string]any{"id": id, "access_level": b.AccessLevel})

	// ---- Pipeline trigger tokens (owned by the authenticated user) ----
	case "GET /triggers":
		ids := make([]int64, 0, len(s.triggers))
		for id := range s.triggers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		out := make([]any, 0, len(ids))
		for _, id := range ids {
			tr := s.triggers[id]
			var owner any = map[string]any{"id": tr.OwnerID}
			if tr.OwnerID == 0 {
				owner = nil // GitLab reports owner:null for some legacy tokens
			}
			out = append(out, map[string]any{"id": tr.ID, "description": tr.Description, "owner": owner})
		}
		tahJSON(w, http.StatusOK, out)
	case "POST /triggers":
		if !s.canAdminister(uid) {
			tahJSON(w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
			return
		}
		var b struct {
			Description string `json:"description"`
		}
		s.decode(r, &b)
		id := s.trigNext
		s.trigNext++
		tr := &tahTrigger{ID: id, OwnerID: uid, Description: b.Description, Token: fmt.Sprintf("glptt-%016d", id)}
		s.triggers[id] = tr
		s.logf("trigger-create:%d", id)
		tahJSON(w, http.StatusCreated, map[string]any{"id": id, "description": tr.Description, "token": tr.Token, "owner": map[string]any{"id": uid}})
	case "DELETE /triggers/:id":
		id := int64(tahSegID(seg, 1))
		if _, ok := s.triggers[id]; !ok {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
			return
		}
		delete(s.triggers, id)
		s.logf("trigger-revoke:%d", id)
		w.WriteHeader(http.StatusNoContent)

	// ---- Deploy keys (owned by the authenticated user) ----
	case "GET /deploy_keys":
		ids := make([]int, 0, len(s.keys))
		for id := range s.keys {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		out := make([]any, 0, len(ids))
		for _, id := range ids {
			k := s.keys[id]
			out = append(out, map[string]any{"id": k.ID, "title": k.Title, "fingerprint_sha256": k.Fingerprint, "can_push": k.CanPush})
		}
		tahJSON(w, http.StatusOK, out)
	case "POST /deploy_keys":
		if !s.canAdminister(uid) {
			tahJSON(w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
			return
		}
		var b struct {
			Title   string `json:"title"`
			Key     string `json:"key"`
			CanPush bool   `json:"can_push"`
		}
		s.decode(r, &b)
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(b.Key))
		if err != nil {
			tahJSON(w, http.StatusBadRequest, map[string]any{"message": "key is invalid"})
			return
		}
		id := s.keyNext
		s.keyNext++
		k := &tahDeployKey{ID: id, OwnerID: uid, Title: b.Title, Fingerprint: strings.TrimPrefix(ssh.FingerprintSHA256(pub), "SHA256:"), CanPush: b.CanPush}
		s.keys[id] = k
		s.logf("key-create:%d", id)
		tahJSON(w, http.StatusCreated, map[string]any{"id": id, "title": k.Title, "fingerprint_sha256": k.Fingerprint, "can_push": k.CanPush})
	case "DELETE /deploy_keys/:id":
		id := tahSegID(seg, 1)
		if _, ok := s.keys[id]; !ok {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
			return
		}
		delete(s.keys, id)
		// GitLab removes a protected-branch grant that names the key.
		s.push = slices.DeleteFunc(s.push, func(e tahPushEntry) bool { return e.DeployKeyID == id })
		s.logf("key-delete:%d", id)
		w.WriteHeader(http.StatusNoContent)

	// ---- Protected branch rule ----
	case "GET /protected_branches":
		if !s.protected {
			tahJSON(w, http.StatusOK, []any{})
			return
		}
		tahJSON(w, http.StatusOK, []any{s.protectedBranchPayload()})
	case "GET /protected_tags":
		tahJSON(w, http.StatusOK, []any{})
	case "GET /protected_branches/:name":
		if seg[1] != tahBranch || !s.protected {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return
		}
		tahJSON(w, http.StatusOK, s.protectedBranchPayload())
	case "PATCH /protected_branches/:name":
		if seg[1] != tahBranch || !s.protected {
			tahJSON(w, http.StatusNotFound, map[string]any{"message": "404 Not found"})
			return
		}
		var b struct {
			AllowedToPush []struct {
				ID          int  `json:"id"`
				DeployKeyID int  `json:"deploy_key_id"`
				Destroy     bool `json:"_destroy"`
			} `json:"allowed_to_push"`
			AllowedToMerge []struct {
				UserID int `json:"user_id"`
			} `json:"allowed_to_merge"`
		}
		s.decode(r, &b)
		if len(b.AllowedToMerge) > 0 {
			s.mergeGrantAttempts++
			if s.rejectMergeGrant {
				tahJSON(w, http.StatusForbidden, map[string]any{"message": "403 Forbidden - per-user protected branch access requires a paid tier"})
				return
			}
			for _, m := range b.AllowedToMerge {
				s.mergeUsers = append(s.mergeUsers, m.UserID)
			}
		}
		for _, e := range b.AllowedToPush {
			switch {
			case e.Destroy:
				before := len(s.push)
				var gone []int
				s.push = slices.DeleteFunc(s.push, func(p tahPushEntry) bool {
					if p.ID == e.ID {
						gone = append(gone, p.DeployKeyID)
						return true
					}
					return false
				})
				if len(s.push) == before {
					s.t.Errorf("destroyed push entry %d that does not exist", e.ID)
				}
				for _, k := range gone {
					s.logf("grant-revoke:%d", k)
				}
			case e.DeployKeyID != 0:
				if _, ok := s.keys[e.DeployKeyID]; !ok {
					tahJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "deploy key not found"})
					return
				}
				s.push = append(s.push, tahPushEntry{ID: s.entryNext, DeployKeyID: e.DeployKeyID, AccessLevel: 40})
				s.entryNext++
				s.logf("grant-add:%d", e.DeployKeyID)
			default:
				s.t.Errorf("unexpected allowed_to_push element %+v", e)
			}
		}
		tahJSON(w, http.StatusOK, map[string]any{"name": tahBranch})

	// ---- Inventories the account-ownership checks read ----
	case "GET /jobs", "GET /pipeline_schedules":
		tahJSON(w, http.StatusOK, []any{})

	default:
		s.unhandled(w, r)
	}
}

// ---- State accessors for assertions ----

func (s *tahServer) with(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (s *tahServer) varValue(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vars[name]
	if !ok {
		return "", false
	}
	return v.Value, true
}

func (s *tahServer) hitCount(route string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[route]
}

func (s *tahServer) triggerAccountIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []int
	for id, a := range s.accounts {
		if strings.HasPrefix(a.Name, "fullsend-trigger-") {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids
}

func (s *tahServer) keyIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []int{}
	for id := range s.keys {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func (s *tahServer) triggerIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []int64{}
	for id := range s.triggers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *tahServer) pushKeyIDs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []int{}
	for _, e := range s.push {
		if e.DeployKeyID != 0 {
			ids = append(ids, e.DeployKeyID)
		}
	}
	sort.Ints(ids)
	return ids
}

func (s *tahServer) history(uid int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.levelHistory[uid])
}

func (s *tahServer) takeEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.events
	s.events = nil
	return out
}

func (s *tahServer) allEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// assertHit fails unless every named route was invoked at least once, so a
// mismatched path cannot pass by way of a default answer.
func (s *tahServer) assertHit(t *testing.T, routes ...string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range routes {
		assert.Positive(t, s.hits[r], "handler for %q was not called", r)
	}
}

func tahAssertOrder(t *testing.T, events []string, want ...string) {
	t.Helper()
	pos := -1
	for _, w := range want {
		found := -1
		for i := pos + 1; i < len(events); i++ {
			if events[i] == w {
				found = i
				break
			}
		}
		if !assert.GreaterOrEqual(t, found, 0, "event %q not found after position %d in %v", w, pos, events) {
			return
		}
		pos = found
	}
}

// ---- Test environment ----

type tahLedger struct {
	Phase            string      `json:"phase"`
	Branch           string      `json:"branch"`
	Nonce            string      `json:"nonce"`
	AccountID        int         `json:"account_id"`
	Elevated         bool        `json:"elevated"`
	PATID            int         `json:"pat_id"`
	PATPending       bool        `json:"pat_pending"`
	DeployKeyID      int         `json:"deploy_key_id"`
	GrantEntryID     int         `json:"grant_entry_id"`
	TriggerTokenID   int64       `json:"trigger_token_id"`
	TriggerPublished bool        `json:"trigger_published"`
	PublishStarted   bool        `json:"publish_started"`
	Retired          []tahLedger `json:"retired"`
}

type tahEnv struct {
	t      *testing.T
	s      *tahServer
	client *gitlab.LiveClient
	out    *bytes.Buffer
}

func newTAHEnv(t *testing.T) *tahEnv {
	t.Helper()
	s := newTAHServer(t)
	immediate := func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	c, err := gitlab.New(tahAdminToken, gitlab.WithBaseURL(s.srv.URL), gitlab.WithAfterFunc(immediate))
	require.NoError(t, err)
	return &tahEnv{t: t, s: s, client: c, out: &bytes.Buffer{}}
}

func (e *tahEnv) converge(rotate bool) error {
	e.t.Helper()
	var client forge.Client = e.client
	return maybeConvergeGitLabTriggerAccount(context.Background(), &reposInstallConfig{}, client, ui.New(e.out), "g", "p", rotate, false, nil)
}

func (e *tahEnv) install() error { return e.converge(false) }

func (e *tahEnv) status() (*repos.RepoStatus, bool) {
	e.t.Helper()
	st := &repos.RepoStatus{Owner: "g", Repo: "p"}
	var client forge.Client = e.client
	drift := appendGitLabTriggerAccountStatus(context.Background(), client, st)
	require.NotNil(e.t, st.GitLabTriggerAccount)
	return st, drift
}

func (e *tahEnv) requireStatus(want string) *repos.GitLabTriggerAccountStatus {
	e.t.Helper()
	st, drift := e.status()
	assert.Equal(e.t, want, st.GitLabTriggerAccount.State, "reasons: %v", st.GitLabTriggerAccount.Reasons)
	switch want {
	case repos.GitLabTriggerStateReady, repos.GitLabTriggerStateNotInstalled, repos.GitLabTriggerStateDeferred:
		// Informational states: polling keeps working, so they are not drift.
		assert.False(e.t, drift)
		assert.Empty(e.t, st.Drifts)
	default:
		assert.True(e.t, drift, "state %q must be reported as drift", want)
	}
	return st.GitLabTriggerAccount
}

// uninstall removes the trigger account the way repos.Uninstall does once the
// project lease is held: through the uninstall token adapter under a lease
// from repos.LockGitLabProjectLease.
func (e *tahEnv) uninstall() (err error) {
	e.t.Helper()
	ctx := context.Background()
	var client forge.Client = e.client
	lease, release, lockErr := repos.LockGitLabProjectLease(ctx, client, "g", "p")
	require.NoError(e.t, lockErr)
	defer release(&err)
	tokens := gitlablifecycle.NewUninstallTokenClient(e.client, repos.ServiceAccountTokenClient{})
	var remover repos.GitLabTriggerAccountUninstaller = tokens
	return remover.RemoveGitLabTriggerAccountForUninstall(ctx, client, lease, "g", "p")
}

func (e *tahEnv) ledger() tahLedger {
	e.t.Helper()
	raw, ok := e.s.varValue(forge.VarGitLabTriggerAccount)
	require.True(e.t, ok, "the ledger %s must exist", forge.VarGitLabTriggerAccount)
	var l tahLedger
	require.NoError(e.t, json.Unmarshal([]byte(raw), &l))
	return l
}

func (e *tahEnv) requireNoLedger() {
	e.t.Helper()
	_, ok := e.s.varValue(forge.VarGitLabTriggerAccount)
	require.False(e.t, ok, "the ledger must be gone")
}

func (e *tahEnv) requireLeaseReleased() {
	e.t.Helper()
	_, ok := e.s.varValue(repos.GitLabProjectLeaseVar)
	assert.False(e.t, ok, "the project lease must be released")
}

func (e *tahEnv) triggerToken(id int64) string {
	e.t.Helper()
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	tr, ok := e.s.triggers[id]
	require.True(e.t, ok, "trigger %d must exist", id)
	return tr.Token
}

func (e *tahEnv) seedVar(name, value string, v tahVar) {
	e.s.with(func() {
		v.Value = value
		e.s.vars[name] = &v
	})
}

func (e *tahEnv) mutateVar(name string, fn func(*tahVar)) {
	e.t.Helper()
	e.s.with(func() {
		v, ok := e.s.vars[name]
		require.True(e.t, ok, "variable %s must exist", name)
		fn(v)
	})
}

func (e *tahEnv) deleteVarOutOfBand(name string) {
	e.s.with(func() { delete(e.s.vars, name) })
}

// requireReadyGeneration asserts the whole contract of one ready generation
// against the live server state, and that nothing else of the lifecycle is left.
func (e *tahEnv) requireReadyGeneration(led tahLedger) {
	e.t.Helper()
	t, s := e.t, e.s
	name := "fullsend-trigger-" + led.Nonce

	assert.Equal(t, "ready", led.Phase)
	assert.Equal(t, tahBranch, led.Branch)
	assert.True(t, led.TriggerPublished)
	assert.True(t, led.PublishStarted)
	assert.False(t, led.Elevated)
	assert.Zero(t, led.PATID)
	assert.False(t, led.PATPending)
	assert.Empty(t, led.Retired, "no retired generation may remain in the ledger")
	require.NotZero(t, led.AccountID)

	s.mu.Lock()
	defer s.mu.Unlock()

	acct, ok := s.accounts[led.AccountID]
	require.True(t, ok, "the recorded account must exist")
	assert.Equal(t, name, acct.Name)
	assert.Equal(t, 30, s.members[led.AccountID], "the account ends at Developer")

	hist := s.levelHistory[led.AccountID]
	assert.Equal(t, []int{40, 30}, hist, "Maintainer is held only for the bootstrap, once")

	key, ok := s.keys[led.DeployKeyID]
	require.True(t, ok, "the recorded deploy key must exist")
	assert.Equal(t, name, key.Title)
	assert.True(t, key.CanPush)
	assert.Equal(t, led.AccountID, key.OwnerID, "the trigger account owns the deploy key")

	trig, ok := s.triggers[led.TriggerTokenID]
	require.True(t, ok, "the recorded trigger must exist")
	assert.Equal(t, name, trig.Description)
	assert.Equal(t, led.AccountID, trig.OwnerID, "the trigger account owns the trigger token")

	var pats []*tahPAT
	for _, p := range s.pats {
		if p.UserID == led.AccountID {
			pats = append(pats, p)
		}
	}
	require.Len(t, pats, 1, "exactly one bootstrap PAT was minted")
	assert.Equal(t, "fullsend-trigger-bootstrap-"+led.Nonce, pats[0].Name)
	assert.Equal(t, []string{"api"}, pats[0].Scopes)
	assert.True(t, pats[0].Revoked, "the bootstrap PAT is revoked")
	now := time.Now().UTC()
	assert.Contains(t, []string{now.AddDate(0, 0, 1).Format("2006-01-02"), now.Add(-time.Minute).AddDate(0, 0, 1).Format("2006-01-02")}, pats[0].ExpiresAt, "the bootstrap PAT lives one day")

	var grants []tahPushEntry
	var adminEntry bool
	for _, p := range s.push {
		switch {
		case p.DeployKeyID == led.DeployKeyID:
			grants = append(grants, p)
		case p.ID == 1 && p.DeployKeyID == 0:
			adminEntry = true
		}
	}
	require.Len(t, grants, 1, "the deploy key has exactly one push entry")
	assert.Equal(t, led.GrantEntryID, grants[0].ID)
	assert.True(t, adminEntry, "the administrator's own push entry is untouched")

	sink, ok := s.vars[tahSink]
	require.True(t, ok, "the trigger token sink must be published")
	assert.Equal(t, trig.Token, sink.Value)
	assert.True(t, sink.Masked)
	assert.True(t, sink.Protected)
	assert.Equal(t, "env_var", sink.VarType)

	ledgerVar := s.vars[forge.VarGitLabTriggerAccount]
	require.NotNil(t, ledgerVar)
	assert.True(t, ledgerVar.Protected, "the ledger is protected")
	assert.False(t, ledgerVar.Masked, "the ledger is unmasked")

	assert.Equal(t, []int{led.AccountID}, tahIDsLocked(s, func(a *tahAccount) bool { return strings.HasPrefix(a.Name, "fullsend-trigger-") }), "only this generation's account remains")
	assert.Len(t, s.keys, 1)
	assert.Len(t, s.triggers, 1)
	_, leaseHeld := s.vars[repos.GitLabProjectLeaseVar]
	assert.False(t, leaseHeld, "the project lease is released")
}

func tahIDsLocked(s *tahServer, match func(*tahAccount) bool) []int {
	var ids []int
	for id, a := range s.accounts {
		if match(a) {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids
}

// installReady installs once and returns the ready generation's ledger.
func (e *tahEnv) installReady() tahLedger {
	e.t.Helper()
	require.NoError(e.t, e.install())
	led := e.ledger()
	e.requireReadyGeneration(led)
	e.requireStatus(repos.GitLabTriggerStateReady)
	return led
}

// requireReplacement asserts that a reinstall or rotation produced a fresh
// generation and fully retired the previous one, which never reached
// Maintainer again.
func (e *tahEnv) requireReplacement(old tahLedger, oldTokenValue string, events []string) tahLedger {
	e.t.Helper()
	t, s := e.t, e.s
	cur := e.ledger()
	e.requireReadyGeneration(cur)

	assert.NotEqual(t, old.Nonce, cur.Nonce, "a fresh nonce names the new account")
	assert.NotEqual(t, old.AccountID, cur.AccountID)
	assert.NotEqual(t, old.DeployKeyID, cur.DeployKeyID)
	assert.NotEqual(t, old.TriggerTokenID, cur.TriggerTokenID)
	newToken := e.triggerToken(cur.TriggerTokenID)
	assert.NotEqual(t, oldTokenValue, newToken, "the sink carries the new trigger value")
	if v, ok := s.varValue(tahSink); assert.True(t, ok) {
		assert.Equal(t, newToken, v)
	}

	assert.NotContains(t, s.triggerAccountIDs(), old.AccountID, "the old account is deleted")
	assert.NotContains(t, s.keyIDs(), old.DeployKeyID, "the old deploy key is deleted")
	assert.NotContains(t, s.triggerIDs(), old.TriggerTokenID, "the old trigger is revoked")
	assert.NotContains(t, s.pushKeyIDs(), old.DeployKeyID, "the old push entry is revoked")
	assert.Equal(t, []int{40, 30}, s.history(old.AccountID), "the old account never reaches Maintainer again")
	assert.NotContains(t, events, fmt.Sprintf("member:%d:40", old.AccountID))

	tahAssertOrder(t, events,
		fmt.Sprintf("member:%d:40", cur.AccountID),
		fmt.Sprintf("key-create:%d", cur.DeployKeyID),
		fmt.Sprintf("trigger-create:%d", cur.TriggerTokenID),
		fmt.Sprintf("pat-revoke:%d", cur.AccountID),
		fmt.Sprintf("member:%d:30", cur.AccountID),
		"var-create:"+tahSink,
		fmt.Sprintf("grant-add:%d", cur.DeployKeyID),
	)
	oldRevoke := fmt.Sprintf("trigger-revoke:%d", old.TriggerTokenID)
	revokeIdx, elevateIdx := slices.Index(events, oldRevoke), slices.Index(events, fmt.Sprintf("member:%d:40", cur.AccountID))
	if revokeIdx >= 0 && revokeIdx < elevateIdx {
		// A missing or unsafe sink: the exposed generation's trigger is revoked
		// before the replacement is elevated or created, and the rest of the
		// old generation is retired once the replacement is published.
		tahAssertOrder(t, events, oldRevoke, fmt.Sprintf("member:%d:40", cur.AccountID))
		tahAssertOrder(t, events,
			"var-create:"+tahSink,
			fmt.Sprintf("grant-revoke:%d", old.DeployKeyID),
			fmt.Sprintf("key-delete:%d", old.DeployKeyID),
			fmt.Sprintf("account-delete:%d", old.AccountID),
		)
		return cur
	}
	// The previous generation is retired only after the replacement sink is
	// published, in the documented order.
	tahAssertOrder(t, events,
		"var-create:"+tahSink,
		oldRevoke,
		fmt.Sprintf("grant-revoke:%d", old.DeployKeyID),
		fmt.Sprintf("key-delete:%d", old.DeployKeyID),
		fmt.Sprintf("account-delete:%d", old.AccountID),
	)
	return cur
}

// ---- Tests ----

func TestGitLabTriggerAccountHTTP_InstallAndIdempotentReinstall(t *testing.T) {
	e := newTAHEnv(t)

	// Before install the project can host the account and status is informational.
	e.requireStatus(repos.GitLabTriggerStateNotInstalled)

	require.NoError(t, e.install())
	led := e.ledger()
	e.requireReadyGeneration(led)
	assert.Contains(t, e.out.String(), "GitLab trigger account ready")

	// Order: elevate, mint PAT, create key and trigger as the account, revoke the
	// PAT, demote, only then publish and grant.
	tahAssertOrder(t, e.s.allEvents(),
		fmt.Sprintf("account-create:%d", led.AccountID),
		fmt.Sprintf("member:%d:40", led.AccountID),
		fmt.Sprintf("pat-create:%d", led.AccountID),
		fmt.Sprintf("key-create:%d", led.DeployKeyID),
		fmt.Sprintf("trigger-create:%d", led.TriggerTokenID),
		fmt.Sprintf("pat-revoke:%d", led.AccountID),
		fmt.Sprintf("member:%d:30", led.AccountID),
		"var-create:"+tahSink,
		fmt.Sprintf("grant-add:%d", led.DeployKeyID),
	)
	e.requireStatus(repos.GitLabTriggerStateReady)

	// Reinstall with nothing wrong is a no-op: same IDs, no new mutation.
	e.s.takeEvents()
	require.NoError(t, e.install())
	again := e.ledger()
	assert.Equal(t, led, again, "the ledger is unchanged")
	e.requireReadyGeneration(again)
	for _, ev := range e.s.takeEvents() {
		assert.NotContains(t, []string{"account-create", "key-create", "trigger-create", "pat-create", "member", "grant-add"},
			strings.SplitN(ev, ":", 2)[0], "unexpected mutation on a no-op reinstall: %s", ev)
	}
	e.requireLeaseReleased()
	e.s.assertHit(t, "GET /project", "POST /variables", "GET /variables/:name", "PUT /variables/:name",
		"GET /service_accounts", "POST /service_accounts", "POST /members", "PUT /members/:id", "GET /members/all/:id",
		"POST /service_accounts/:id/personal_access_tokens", "GET /service_accounts/:id/personal_access_tokens",
		"DELETE /service_accounts/:id/personal_access_tokens/:id",
		"POST /deploy_keys", "GET /deploy_keys", "POST /triggers", "GET /triggers",
		"GET /protected_branches/:name", "PATCH /protected_branches/:name", "GET /users/:id/keys",
		"GET /jobs", "GET /pipeline_schedules", "DELETE /variables/:name")
}

// A legacy repository that still allows pipeline variable overrides never
// receives a live trigger credential: the lifecycle defers on install, which is
// the call every install path makes (including after a Poller failure), and on
// rotation.
func TestGitLabTriggerAccountHTTP_WeakOverrideRoleDefersInstallAndRotation(t *testing.T) {
	e := newTAHEnv(t)
	e.s.with(func() { e.s.overrideRole = forge.PipelineVarOverrideDeveloper })

	require.NoError(t, e.install(), "a deferral is not a failure")
	assert.Contains(t, e.out.String(), "GitLab trigger account deferred")
	assert.Contains(t, e.out.String(), "not no_one_allowed")
	assert.NotContains(t, e.out.String(), "GitLab trigger account ready")
	e.requireNoLedger()
	_, sinkExists := e.s.varValue(tahSink)
	assert.False(t, sinkExists, "no trigger credential is published")
	for _, ev := range e.s.allEvents() {
		prefix := strings.SplitN(ev, ":", 2)[0]
		assert.NotContains(t, []string{"account-create", "key-create", "trigger-create", "pat-create", "grant-add"}, prefix,
			"nothing is created for a deferral: %s", ev)
	}
	e.requireLeaseReleased()

	// A trigger account installed while the restriction held is not rotated
	// while it is weakened, and its already published credential is removed
	// and revoked whatever the sink's attributes are.
	e.s.with(func() { e.s.overrideRole = forge.PipelineVarOverrideNoOneAllowed })
	led := e.installReady()
	e.s.with(func() { e.s.overrideRole = forge.PipelineVarOverrideDeveloper })
	e.s.takeEvents()
	require.NoError(t, e.converge(true))
	assert.Equal(t, led.TriggerTokenID, e.ledger().TriggerTokenID, "no replacement trigger is created")
	_, sinkExists = e.s.varValue(tahSink)
	assert.False(t, sinkExists, "the published credential is removed while overrides are allowed")
	assert.NotContains(t, e.s.triggerIDs(), led.TriggerTokenID, "the owned trigger is revoked")
	for _, ev := range e.s.takeEvents() {
		assert.False(t, strings.HasPrefix(ev, "trigger-create:") || strings.HasPrefix(ev, "key-create:"), "no new credential on a weakened project: %s", ev)
	}
	e.requireStatus(repos.GitLabTriggerStateDrifted)
}

func TestGitLabTriggerAccountHTTP_PollerInventoryResolvedFromServer(t *testing.T) {
	e := newTAHEnv(t)
	// A recorded current and retiring Poller generation, an administrator
	// supplied coder credential owner, and the managed Poller account itself.
	e.s.with(func() { e.s.accounts[11] = &tahAccount{ID: 11, Name: gitlabroles.PollerTokenName} })
	e.seedVar(forge.VarGitLabPollerGenerations, `{"version":1,"current_user_id":11,"retiring_user_id":12}`, tahVar{Protected: true, VarType: "env_var"})
	e.seedVar(forge.VarGitLabRoleRotation, `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_distributed":true,"supplied_user_id":501,"supplied_token_id":13,"excluded_user_ids":[501]}}}`, tahVar{Protected: true, VarType: "env_var"})

	ids, err := gitlablifecycle.NewTriggerOwner(e.client).VerifiedPollerUserIDs(context.Background(), "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{11, 12, 501}, ids, "current, retiring and supplied-owner identities come from the server")

	// Installation proceeds and the trigger account is none of them.
	require.NoError(t, e.install())
	led := e.ledger()
	e.requireReadyGeneration(led)
	assert.NotContains(t, ids, led.AccountID)
	e.s.assertHit(t, "GET /service_accounts", "GET /variables/:name")
	e.requireStatus(repos.GitLabTriggerStateReady)
}

// A recorded Poller identity is excluded end to end: when GitLab hands the new
// trigger account an ID that the Poller generation document records, install
// refuses it before any elevation.
func TestGitLabTriggerAccountHTTP_PollerIdentityCollisionFailsBeforeElevation(t *testing.T) {
	e := newTAHEnv(t)
	e.s.with(func() {
		e.s.accounts[11] = &tahAccount{ID: 11, Name: gitlabroles.PollerTokenName}
		e.s.accountNext = 12
	})
	e.seedVar(forge.VarGitLabPollerGenerations, `{"version":1,"current_user_id":11,"retiring_user_id":12}`, tahVar{Protected: true, VarType: "env_var"})

	err := e.install()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Poller role identity")
	assert.Empty(t, e.s.history(12), "the colliding account is never added to the project, let alone elevated")
	assert.Empty(t, e.s.keyIDs())
	assert.Empty(t, e.s.triggerIDs())
	_, sink := e.s.varValue(tahSink)
	assert.False(t, sink)
	e.requireLeaseReleased()
	e.s.assertHit(t, "POST /service_accounts")
}

// When the Poller identity inventory cannot be verified, install fails closed
// before it creates any trigger account resource.
func TestGitLabTriggerAccountHTTP_UnverifiedPollerInventoryFailsClosed(t *testing.T) {
	cases := map[string]struct {
		setup   func(e *tahEnv)
		wantErr string
		hit     string
	}{
		"generation document unreadable": {
			setup: func(e *tahEnv) {
				e.s.with(func() { e.s.varGetStatus[forge.VarGitLabPollerGenerations] = http.StatusInternalServerError })
			},
			wantErr: forge.VarGitLabPollerGenerations,
			hit:     "GET /variables/:name",
		},
		"generation document malformed": {
			setup: func(e *tahEnv) {
				e.seedVar(forge.VarGitLabPollerGenerations, `{"version":1,"current_user_id":`, tahVar{Protected: true, VarType: "env_var"})
			},
			wantErr: forge.VarGitLabPollerGenerations,
			hit:     "GET /variables/:name",
		},
		"supplied credential owner unresolved": {
			setup: func(e *tahEnv) {
				// The rotation state records the Poller credential as supplied
				// without an owner, and the installed token no longer authenticates.
				e.seedVar(forge.VarGitLabRoleRotation, `{"version":2,"roles":{"poller":{"supplied":true}}}`, tahVar{Protected: true, VarType: "env_var"})
				e.seedVar(forge.SecretGitLabPollerToken, "supplied-poller-token", tahVar{Masked: true, Protected: true, VarType: "env_var"})
			},
			wantErr: "supplied",
			hit:     "401 GET /user",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTAHEnv(t)
			tc.setup(e)

			err := e.install()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			e.s.assertHit(t, tc.hit)

			assert.Empty(t, e.s.triggerAccountIDs(), "no service account is created")
			assert.Empty(t, e.s.keyIDs())
			assert.Empty(t, e.s.triggerIDs())
			assert.Empty(t, e.s.pushKeyIDs())
			assert.Empty(t, e.s.takeEventsWith("member:", "account-create:", "pat-create:", "key-create:", "trigger-create:", "grant-add:"))
			_, sink := e.s.varValue(tahSink)
			assert.False(t, sink)
			_, ledger := e.s.varValue(forge.VarGitLabTriggerAccount)
			assert.False(t, ledger, "no ledger is written before the inventory is verified")
			e.requireLeaseReleased()
		})
	}
}

// takeEventsWith returns the recorded events that start with any prefix.
func (s *tahServer) takeEventsWith(prefixes ...string) []string {
	var out []string
	for _, ev := range s.allEvents() {
		for _, p := range prefixes {
			if strings.HasPrefix(ev, p) {
				out = append(out, ev)
				break
			}
		}
	}
	return out
}

func TestGitLabTriggerAccountHTTP_SinkDeletedStartsFreshGeneration(t *testing.T) {
	e := newTAHEnv(t)
	old := e.installReady()
	oldToken := e.triggerToken(old.TriggerTokenID)

	// The sink is deleted out of band: its value is unrecoverable.
	e.deleteVarOutOfBand(tahSink)
	reasons := e.requireStatus(repos.GitLabTriggerStateDrifted).Reasons
	assert.Contains(t, strings.Join(reasons, "; "), tahSink+" is missing")

	e.s.takeEvents()
	require.NoError(t, e.install())
	cur := e.requireReplacement(old, oldToken, e.s.takeEvents())
	assert.NotEqual(t, old.AccountID, cur.AccountID)
	e.requireStatus(repos.GitLabTriggerStateReady)
	e.requireLeaseReleased()
	e.s.assertHit(t, "DELETE /service_accounts/:id", "DELETE /triggers/:id", "DELETE /deploy_keys/:id", "PATCH /protected_branches/:name")
}

func TestGitLabTriggerAccountHTTP_UnsafeSinkReportsDriftAndIsReplaced(t *testing.T) {
	cases := map[string]struct {
		mutate func(*tahVar)
		reason string
	}{
		"unprotected": {func(v *tahVar) { v.Protected = false }, tahSink + " is not protected"},
		"unmasked":    {func(v *tahVar) { v.Masked = false }, tahSink + " is not masked"},
		"file type":   {func(v *tahVar) { v.VarType = "file" }, tahSink + " is a file-type variable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTAHEnv(t)
			old := e.installReady()
			oldToken := e.triggerToken(old.TriggerTokenID)

			e.mutateVar(tahSink, tc.mutate)
			status, drift := e.status()
			assert.True(t, drift)
			assert.Equal(t, repos.GitLabTriggerStateDrifted, status.GitLabTriggerAccount.State)
			assert.Contains(t, strings.Join(status.GitLabTriggerAccount.Reasons, "; "), tc.reason)
			require.Len(t, status.Drifts, 1)
			assert.Equal(t, "gitlab-trigger-account", status.Drifts[0].Field)
			assert.Contains(t, status.Drifts[0].Actual, tc.reason)

			e.s.takeEvents()
			require.NoError(t, e.install())
			e.requireReplacement(old, oldToken, e.s.takeEvents())
			e.requireStatus(repos.GitLabTriggerStateReady)
			e.requireLeaseReleased()
		})
	}
}

func TestGitLabTriggerAccountHTTP_AdministratorOwnedSinkIsNeverTouched(t *testing.T) {
	e := newTAHEnv(t)
	e.seedVar(tahSink, "administrator-value", tahVar{Masked: true, Protected: true, VarType: "env_var"})

	err := e.install()
	require.Error(t, err)
	assert.Contains(t, err.Error(), tahSink)
	assert.Contains(t, err.Error(), "did not create it")

	v, ok := e.s.varValue(tahSink)
	require.True(t, ok, "the administrator's variable still exists")
	assert.Equal(t, "administrator-value", v, "and keeps its value")
	for _, ev := range e.s.allEvents() {
		assert.NotContains(t, []string{"var-create:" + tahSink, "var-update:" + tahSink, "var-delete:" + tahSink}, ev, "the administrator's variable was modified")
	}
	assert.Empty(t, e.s.takeEventsWith("member:", "pat-create:", "key-create:", "trigger-create:", "grant-add:"))
	assert.Empty(t, e.s.keyIDs())
	assert.Empty(t, e.s.triggerIDs())
	e.requireLeaseReleased()

	// Once the administrator removes the variable, install completes.
	e.deleteVarOutOfBand(tahSink)
	require.NoError(t, e.install())
	e.requireReadyGeneration(e.ledger())
}

func TestGitLabTriggerAccountHTTP_RotationReplacesGenerationAfterPublication(t *testing.T) {
	e := newTAHEnv(t)
	old := e.installReady()
	oldToken := e.triggerToken(old.TriggerTokenID)

	e.s.takeEvents()
	require.NoError(t, e.converge(true))
	cur := e.requireReplacement(old, oldToken, e.s.takeEvents())
	assert.Equal(t, []int{40, 30}, e.s.history(cur.AccountID))
	e.requireStatus(repos.GitLabTriggerStateReady)
	e.requireLeaseReleased()

	// Rotation without a ready account is an ordinary install.
	e2 := newTAHEnv(t)
	require.NoError(t, e2.converge(true))
	e2.requireReadyGeneration(e2.ledger())
}

func TestGitLabTriggerAccountHTTP_InterruptedRetirementKeepsLedgerAndResumes(t *testing.T) {
	e := newTAHEnv(t)
	old := e.installReady()

	// The new generation is published, then deleting the old account fails.
	e.deleteVarOutOfBand(tahSink)
	e.s.with(func() { e.s.failDeleteAccount = true })
	e.s.takeEvents()
	err := e.install()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retiring a previous GitLab trigger account generation")
	e.s.with(func() { assert.Positive(t, e.s.deleteAttempts) })
	e.requireLeaseReleased()

	led := e.ledger()
	require.Len(t, led.Retired, 1, "the ledger keeps the retired entry")
	assert.Equal(t, old.AccountID, led.Retired[0].AccountID)
	assert.Zero(t, led.Retired[0].DeployKeyID, "the old key was already removed")
	assert.Zero(t, led.Retired[0].TriggerTokenID, "the old trigger was already revoked")
	assert.False(t, led.Retired[0].Elevated)
	assert.Equal(t, "ready", led.Phase, "the new generation is ready and published")
	assert.Contains(t, e.s.triggerAccountIDs(), old.AccountID, "the old account still exists")
	assert.Equal(t, []int{40, 30}, e.s.history(old.AccountID), "the old account stays at Developer")
	v, ok := e.s.varValue(tahSink)
	require.True(t, ok)
	assert.Equal(t, e.triggerToken(led.TriggerTokenID), v, "the replacement was published before cleanup failed")

	reasons := e.requireStatus(repos.GitLabTriggerStateDrifted).Reasons
	joined := strings.Join(reasons, "; ")
	assert.Contains(t, joined, "unfinished")
	assert.Contains(t, joined, fmt.Sprintf("account %d", old.AccountID))

	// After the fault clears, reinstall finishes the retirement without a new
	// generation.
	e.s.with(func() { e.s.failDeleteAccount = false })
	e.s.takeEvents()
	require.NoError(t, e.install())
	events := e.s.takeEvents()
	after := e.ledger()
	assert.Equal(t, led.Nonce, after.Nonce, "retirement resumes without starting another generation")
	assert.Empty(t, after.Retired)
	assert.NotContains(t, e.s.triggerAccountIDs(), old.AccountID)
	assert.Contains(t, events, fmt.Sprintf("account-delete:%d", old.AccountID))
	assert.NotContains(t, events, fmt.Sprintf("member:%d:40", old.AccountID))
	assert.Equal(t, []int{40, 30}, e.s.history(old.AccountID))
	e.requireReadyGeneration(after)
	e.requireStatus(repos.GitLabTriggerStateReady)
}

func TestGitLabTriggerAccountHTTP_StatusReportsLiveDriftAndInstallRepairs(t *testing.T) {
	t.Run("push entry removed out of band", func(t *testing.T) {
		e := newTAHEnv(t)
		led := e.installReady()
		e.s.with(func() {
			e.s.push = slices.DeleteFunc(e.s.push, func(p tahPushEntry) bool { return p.DeployKeyID == led.DeployKeyID })
		})
		reasons := e.requireStatus(repos.GitLabTriggerStateDrifted).Reasons
		assert.Contains(t, strings.Join(reasons, "; "), "push entry recorded on main is missing or changed")

		require.NoError(t, e.install())
		after := e.ledger()
		assert.Equal(t, led.AccountID, after.AccountID, "the generation is kept and only the grant is re-created")
		assert.Equal(t, led.DeployKeyID, after.DeployKeyID)
		assert.Equal(t, []int{led.DeployKeyID}, e.s.pushKeyIDs())
		assert.Equal(t, []int{40, 30}, e.s.history(led.AccountID))
		e.requireStatus(repos.GitLabTriggerStateReady)
	})

	t.Run("account elevated out of band", func(t *testing.T) {
		e := newTAHEnv(t)
		led := e.installReady()
		e.s.with(func() { e.s.members[led.AccountID] = 40 })
		reasons := e.requireStatus(repos.GitLabTriggerStateDrifted).Reasons
		assert.Contains(t, strings.Join(reasons, "; "), "access level 40, expected Developer (30)")
	})

	t.Run("default branch protection removed", func(t *testing.T) {
		e := newTAHEnv(t)
		e.installReady()
		e.s.with(func() { e.s.protected = false })
		reasons := e.requireStatus(repos.GitLabTriggerStateDrifted).Reasons
		assert.Contains(t, strings.Join(reasons, "; "), "no longer has an exact-name protected-branch rule")
	})

	t.Run("install defers while the default branch is unprotected", func(t *testing.T) {
		e := newTAHEnv(t)
		e.s.with(func() { e.s.protected = false })
		require.NoError(t, e.install(), "a deferral is not a failure")
		assert.Contains(t, e.out.String(), "deferred")
		assert.Empty(t, e.s.triggerAccountIDs())
		assert.Empty(t, e.s.takeEventsWith("account-create:", "member:"))
		st := e.requireStatus(repos.GitLabTriggerStateDeferred)
		assert.NotEmpty(t, st.Reasons)
	})
}

func TestGitLabTriggerAccountHTTP_UninstallRemovesEverything(t *testing.T) {
	e := newTAHEnv(t)

	// Without a ledger uninstall does nothing, and an administrator's variable
	// that happens to carry the sink's name is left alone.
	e.seedVar(tahSink, "administrator-value", tahVar{Masked: true, Protected: true, VarType: "env_var"})
	require.NoError(t, e.uninstall())
	v, ok := e.s.varValue(tahSink)
	require.True(t, ok)
	assert.Equal(t, "administrator-value", v)
	e.deleteVarOutOfBand(tahSink)

	led := e.installReady()
	e.s.takeEvents()
	require.NoError(t, e.uninstall())
	events := e.s.takeEvents()

	e.requireNoLedger()
	_, sink := e.s.varValue(tahSink)
	assert.False(t, sink, "the sink is removed")
	assert.Empty(t, e.s.triggerAccountIDs(), "the account is deleted")
	assert.Empty(t, e.s.keyIDs(), "the deploy key is deleted")
	assert.Empty(t, e.s.triggerIDs(), "the trigger token is revoked")
	assert.Empty(t, e.s.pushKeyIDs(), "the push entry is revoked")
	e.s.with(func() {
		require.Len(t, e.s.push, 1)
		assert.Equal(t, 1, e.s.push[0].ID, "the administrator's own push entry remains")
	})
	assert.Equal(t, []int{40, 30}, e.s.history(led.AccountID), "uninstall never re-elevates the account")
	tahAssertOrder(t, events,
		"var-delete:"+tahSink,
		fmt.Sprintf("trigger-revoke:%d", led.TriggerTokenID),
		fmt.Sprintf("grant-revoke:%d", led.DeployKeyID),
		fmt.Sprintf("key-delete:%d", led.DeployKeyID),
		fmt.Sprintf("account-delete:%d", led.AccountID),
		"var-delete:"+forge.VarGitLabTriggerAccount,
	)
	e.requireLeaseReleased()
	e.requireStatus(repos.GitLabTriggerStateNotInstalled)
}

func TestGitLabTriggerAccountHTTP_UninstallInterruptedRemovalPreservesLedgerAndRetries(t *testing.T) {
	e := newTAHEnv(t)
	led := e.installReady()

	e.s.with(func() { e.s.failDeleteAccount = true })
	err := e.uninstall()
	require.Error(t, err)
	e.s.with(func() { assert.Positive(t, e.s.deleteAttempts) })
	e.requireLeaseReleased()

	kept := e.ledger()
	assert.Equal(t, "removing", kept.Phase, "the ledger records the unfinished removal")
	assert.Equal(t, led.AccountID, kept.AccountID, "the ledger keeps the account obligation")
	assert.Contains(t, e.s.triggerAccountIDs(), led.AccountID)
	_, sink := e.s.varValue(tahSink)
	assert.False(t, sink, "the sink was already removed")
	assert.Empty(t, e.s.triggerIDs(), "the trigger was already revoked")
	assert.Empty(t, e.s.pushKeyIDs())
	assert.Empty(t, e.s.keyIDs())
	reasons := e.requireStatus(repos.GitLabTriggerStateCleanup).Reasons
	assert.Contains(t, strings.Join(reasons, "; "), fmt.Sprintf("service account %d is recorded", led.AccountID))

	// A reinstall is refused while the removal is unfinished.
	err = e.install()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uninstall is still in progress")

	e.s.with(func() { e.s.failDeleteAccount = false })
	require.NoError(t, e.uninstall())
	e.requireNoLedger()
	assert.Empty(t, e.s.triggerAccountIDs())
	assert.Equal(t, []int{40, 30}, e.s.history(led.AccountID))
	e.requireLeaseReleased()
	e.requireStatus(repos.GitLabTriggerStateNotInstalled)
}

// Generic role-identity cleanup must never remove the ledger or the sink: only
// the dedicated removal, which proves ownership first, may.
func TestGitLabTriggerAccountHTTP_GenericRoleCleanupLeavesLedgerAndSink(t *testing.T) {
	e := newTAHEnv(t)
	led := e.installReady()
	ctx := context.Background()

	var client forge.Client = e.client
	_, release, err := repos.LockGitLabProjectLease(ctx, client, "g", "p")
	require.NoError(t, err)
	_, cleanupErr := repos.CleanupGitLabRoleIdentityLocked(ctx, repos.GitLabRoleCleanupConfig{Owner: "g", Repo: "p", Client: client})
	var relErr error
	release(&relErr)
	require.NoError(t, relErr)
	require.NoError(t, cleanupErr)

	assert.Equal(t, led, e.ledger(), "the ledger survives generic role cleanup")
	_, sink := e.s.varValue(tahSink)
	assert.True(t, sink, "the sink survives generic role cleanup")
	e.requireReadyGeneration(led)

	// The static uninstall lists never name them either.
	for _, name := range append(repos.UninstallVarsForForge(repos.ForgeGitLab), repos.UninstallSecretsForForge(repos.ForgeGitLab)...) {
		assert.NotEqual(t, forge.VarGitLabTriggerAccount, name)
		assert.NotEqual(t, tahSink, name)
	}
}

// GitLab reports owner:null for some legacy trigger tokens. Install refuses to
// elevate the account while one exists, names its ID, and never revokes it.
func TestGitLabTriggerAccountHTTP_OwnerNullTriggerFailsClosedBeforeElevation(t *testing.T) {
	e := newTAHEnv(t)
	e.s.with(func() { e.s.triggers[9000] = &tahTrigger{ID: 9000, OwnerID: 0, Description: "legacy"} })

	err := e.install()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown owner")
	assert.Contains(t, err.Error(), "9000")
	e.s.with(func() {
		assert.Contains(t, e.s.triggers, int64(9000), "an unknown-owner trigger is never revoked by inference")
		for id, h := range e.s.levelHistory {
			assert.NotContains(t, h, 40, "account %d must never be elevated", id)
		}
	})
	e.requireLeaseReleased()
}

// Live re-diagnosis adjusts Summary.Drifted in both directions.
func TestGitLabTriggerAccountHTTP_StatusSummaryAdjustsInBothDirections(t *testing.T) {
	e := newTAHEnv(t)
	led := e.installReady()
	var client forge.Client = e.client

	// Ledger-only drift that GitLab disproves is removed and uncounted.
	st := &repos.RepoStatus{Owner: "g", Repo: "p"}
	repos.ApplyGitLabTriggerAccountStatus(st, repos.GitLabTriggerAccountStatus{State: repos.GitLabTriggerStateInProgress, Reasons: []string{"stale ledger-only reason"}})
	require.NotEmpty(t, st.Drifts)
	assert.False(t, appendGitLabTriggerAccountStatus(context.Background(), client, st))
	assert.Empty(t, st.Drifts)
	assert.Equal(t, repos.GitLabTriggerStateReady, st.GitLabTriggerAccount.State)

	// Live drift on a ready ledger-only row is added.
	e.s.with(func() { delete(e.s.keys, led.DeployKeyID) })
	st = &repos.RepoStatus{Owner: "g", Repo: "p"}
	repos.ApplyGitLabTriggerAccountStatus(st, repos.GitLabTriggerAccountStatus{State: repos.GitLabTriggerStateReady})
	assert.True(t, appendGitLabTriggerAccountStatus(context.Background(), client, st))
	assert.NotEmpty(t, st.Drifts)
}

func TestGitLabTriggerAccountHTTP_SummaryDriftedAfterLiveCountsBothDirections(t *testing.T) {
	assert.Equal(t, 1, summaryDriftedAfterLive(0, false, true), "no drift -> drift counts once")
	assert.Equal(t, 0, summaryDriftedAfterLive(1, true, false), "drift disproved -> uncounted")
	assert.Equal(t, 1, summaryDriftedAfterLive(1, true, true), "unchanged drift stays counted once")
	assert.Equal(t, 0, summaryDriftedAfterLive(0, false, false))
	assert.Equal(t, 0, summaryDriftedAfterLive(0, true, false), "never goes negative")
}

// A failed live GetRepo read cannot leave a ledger-only "ready" result.
func TestGitLabTriggerAccountHTTP_FailedLiveRepoReadIsNotReady(t *testing.T) {
	e := newTAHEnv(t)
	e.installReady()
	e.s.with(func() { e.s.repoStatus = http.StatusInternalServerError })

	st := &repos.RepoStatus{Owner: "g", Repo: "p"}
	repos.ApplyGitLabTriggerAccountStatus(st, repos.GitLabTriggerAccountStatus{State: repos.GitLabTriggerStateReady})
	assert.True(t, appendGitLabTriggerAccountStatus(context.Background(), e.client, st), "an unverified ready row is drift")
	assert.Equal(t, repos.GitLabTriggerStateUnreadable, st.GitLabTriggerAccount.State)
	require.Len(t, st.Drifts, 1)
	assert.Equal(t, "gitlab-trigger-account", st.Drifts[0].Field)
}

// Containment-only recovery restores Developer access and revokes the recorded
// bootstrap PAT without creating anything, and leaves the rest untouched.
func TestGitLabTriggerAccountHTTP_ContainmentRecoveryCreatesNothing(t *testing.T) {
	e := newTAHEnv(t)
	led := e.installReady()
	e.s.with(func() {
		e.s.members[led.AccountID] = 40
		e.s.patNext++
		id := e.s.patNext
		e.s.pats[id] = &tahPAT{ID: id, UserID: led.AccountID, Name: "fullsend-trigger-bootstrap-" + led.Nonce}
	})
	var patID int
	e.s.with(func() {
		for id, p := range e.s.pats {
			if !p.Revoked {
				patID = id
			}
		}
	})
	raw, ok := e.s.varValue(forge.VarGitLabTriggerAccount)
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	m["elevated"], m["pat_id"] = true, patID
	b, err := json.Marshal(m)
	require.NoError(t, err)
	e.seedVar(forge.VarGitLabTriggerAccount, string(b), tahVar{Protected: true, VarType: "env_var"})

	e.s.takeEvents()
	var client forge.Client = e.client
	require.NoError(t, containGitLabTriggerAccount(context.Background(), &reposInstallConfig{}, client, ui.New(e.out), "g", "p"))
	events := e.s.takeEvents()
	for _, ev := range events {
		assert.False(t, strings.HasPrefix(ev, "account-create:") || strings.HasPrefix(ev, "key-create:") ||
			strings.HasPrefix(ev, "trigger-create:") || strings.HasPrefix(ev, "pat-create:") ||
			strings.HasPrefix(ev, "grant-add:"), "containment must not create: %s", ev)
	}
	e.s.with(func() { assert.Equal(t, 30, e.s.members[led.AccountID]) })
	after := e.ledger()
	assert.False(t, after.Elevated)
	assert.Zero(t, after.PATID)
	assert.Equal(t, led.AccountID, after.AccountID)
	assert.Equal(t, led.TriggerTokenID, after.TriggerTokenID)
	e.requireLeaseReleased()
}

// Install's early containment pass, which runs before any other install stage
// can fail or skip it, removes an already published trigger credential and
// revokes its owned trigger when the override restriction is weakened. It
// creates nothing and needs neither the Poller inventory nor convergence.
func TestGitLabTriggerAccountHTTP_ContainmentRemovesPublishedCredentialWhenOverrideRoleWeakened(t *testing.T) {
	e := newTAHEnv(t)
	led := e.installReady()
	_, sinkExists := e.s.varValue(tahSink)
	require.True(t, sinkExists)
	require.Contains(t, e.s.triggerIDs(), led.TriggerTokenID)

	e.s.with(func() { e.s.overrideRole = forge.PipelineVarOverrideDeveloper })
	e.s.takeEvents()
	var client forge.Client = e.client
	require.NoError(t, containGitLabTriggerAccount(context.Background(), &reposInstallConfig{}, client, ui.New(e.out), "g", "p"))

	_, sinkExists = e.s.varValue(tahSink)
	assert.False(t, sinkExists, "the published credential is removed")
	assert.NotContains(t, e.s.triggerIDs(), led.TriggerTokenID, "the owned trigger is revoked")
	after := e.ledger()
	assert.Equal(t, led.AccountID, after.AccountID, "the ledger and account stay for the later install")
	for _, ev := range e.s.takeEvents() {
		assert.False(t, strings.HasPrefix(ev, "account-create:") || strings.HasPrefix(ev, "key-create:") ||
			strings.HasPrefix(ev, "trigger-create:") || strings.HasPrefix(ev, "pat-create:") ||
			strings.HasPrefix(ev, "grant-add:"), "containment must not create: %s", ev)
	}
	e.requireLeaseReleased()
}

// Uninstall contains an interrupted bootstrap before the Poller inventory is
// read: with the inventory unreadable the removal still fails closed and keeps
// the ledger, yet the bootstrap PAT is revoked and the account is Developer.
func TestGitLabTriggerAccountHTTP_UninstallContainsBootstrapWhenInventoryUnreadable(t *testing.T) {
	e := newTAHEnv(t)
	led := e.installReady()
	var patID int
	e.s.with(func() {
		e.s.members[led.AccountID] = 40
		e.s.patNext++
		patID = e.s.patNext
		e.s.pats[patID] = &tahPAT{ID: patID, UserID: led.AccountID, Name: "fullsend-trigger-bootstrap-" + led.Nonce}
	})
	raw, ok := e.s.varValue(forge.VarGitLabTriggerAccount)
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	m["elevated"], m["pat_id"] = true, patID
	b, err := json.Marshal(m)
	require.NoError(t, err)
	e.seedVar(forge.VarGitLabTriggerAccount, string(b), tahVar{Protected: true, VarType: "env_var"})
	e.s.with(func() { e.s.varGetStatus[forge.VarGitLabPollerGenerations] = http.StatusInternalServerError })

	err = e.uninstall()
	require.Error(t, err)
	assert.Contains(t, err.Error(), forge.VarGitLabPollerGenerations)

	e.s.with(func() {
		assert.Equal(t, 30, e.s.members[led.AccountID], "Developer access is restored")
		assert.True(t, e.s.pats[patID].Revoked, "the bootstrap PAT is revoked")
	})
	after := e.ledger()
	assert.False(t, after.Elevated)
	assert.Zero(t, after.PATID)
	assert.Equal(t, led.AccountID, after.AccountID, "the ledger and account are kept for a retry")
	e.requireLeaseReleased()
}

// The early schedule repair is a no-op without a ledger, and with a ready
// account that owns no schedule it takes and releases the project lease and
// changes nothing.
func TestRepairGitLabTriggerSchedules_LiveClientNoops(t *testing.T) {
	e := newTAHEnv(t)
	var client forge.Client = e.client
	require.NoError(t, repairGitLabTriggerSchedules(context.Background(), &reposInstallConfig{}, client, ui.New(e.out), "g", "p"))
	e.requireNoLedger()

	e.installReady()
	e.out.Reset()
	require.NoError(t, repairGitLabTriggerSchedules(context.Background(), &reposInstallConfig{}, client, ui.New(e.out), "g", "p"))
	assert.NotContains(t, e.out.String(), "repaired")
	e.requireLeaseReleased()

	require.NoError(t, repairGitLabTriggerSchedules(context.Background(), &reposInstallConfig{dryRun: true}, client, ui.New(e.out), "g", "p"))
}
