// Package botidentity resolves the provider-registered Fullsend identity of
// bot actors (ADR 0107). It holds the forge-neutral contract the dispatch
// path and the behaviour tests share; provider clients implement it.
//
// The package is named for bot identity management in general: bot-role
// lookup is its first concern, and other identity operations (such as
// credential retrieval) may join it later. It must not import
// internal/mintcore, so pkg/behaviourtest can use it.
package botidentity

import (
	"context"
	"errors"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// ErrLookup marks a bot-role lookup that did not complete: a provider,
// transport, authentication or protocol failure. Callers must fail closed,
// and must keep it distinct from a successful no-match.
var ErrLookup = errors.New("bot-role lookup failed")

// BotRoleQuery identifies the bot whose registered role is looked up.
//
// Every field comes from the verified source system: Forge is the source
// system ("github"), Target the repository the event belongs to
// ("owner/repo") and Login the verified actor login (for GitHub Apps
// "<slug>[bot]"). Roles or identity claims found in event content are never
// part of a query.
type BotRoleQuery struct {
	Forge  string
	Target string
	Login  string
}

// BotRoleResult is a completed lookup. Role is the canonical Fullsend role
// of a recognized bot, or empty for a successful no-match.
type BotRoleResult struct {
	Role string
}

// Recognized reports whether the lookup matched a registered role.
func (r BotRoleResult) Recognized() bool { return r.Role != "" }

// BotRoleResolver resolves a verified bot identity to its registered
// Fullsend role for one target. A nil error with an empty Role is a
// successful no-match; any error is a lookup failure and wraps ErrLookup.
type BotRoleResolver interface {
	ResolveBotRole(ctx context.Context, q BotRoleQuery) (BotRoleResult, error)
}

// ApplyBotRoleResult projects a lookup outcome onto the normalized actor
// fields (ADR 0107): the actor is a bot with actor.role "none"; a
// recognized role sets actor.bot_role; actor.role_verified is true for a
// completed lookup (recognized or no-match) and false for a failure, which
// never carries a bot_role.
func ApplyBotRoleResult(actor *normevent.Actor, res BotRoleResult, lookupErr error) {
	verified := lookupErr == nil
	actor.Kind = normevent.ActorBot
	actor.Role = normevent.RoleNone
	actor.RoleVerified = &verified
	actor.BotRole = ""
	if verified {
		actor.BotRole = res.Role
	}
}
