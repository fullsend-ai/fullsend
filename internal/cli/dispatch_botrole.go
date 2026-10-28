package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/mintcore/mintconsts"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// newDispatchBotRoleResolverFn builds the resolver `fullsend dispatch` uses
// for bot actors of gha-event input (ADR 0107): the mint's GitHub bot-role
// lookup at FULLSEND_MINT_URL, authenticated with the job's GitHub Actions
// OIDC token. An unset mint URL or OIDC environment is a lookup failure,
// which denies the bot. Override in tests to supply a stub or fake resolver.
var newDispatchBotRoleResolverFn = func() botidentity.BotRoleResolver {
	return &botidentity.MintResolver{
		MintURL:   os.Getenv(forge.VarMintURL),
		OIDCToken: actionsMintOIDCToken,
	}
}

// actionsMintOIDCToken fetches the job's GitHub Actions OIDC token for the
// mint audience. Errors never include the token.
func actionsMintOIDCToken(ctx context.Context) (string, error) {
	a, err := actionsoidc.FetchAssertion(ctx, actionsoidc.AssertionConfig{
		Audience:         mintconsts.OIDCAudience,
		OIDCRequestURL:   os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"),
		OIDCRequestToken: os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"),
	})
	if err != nil {
		return "", err
	}
	return a.Value, nil
}

// isGitHubLabelAdded reports whether event takes the permanent GitHub
// label-added exception, which does not need a bot-role lookup (ADR 0107
// rule 2): actor.role_verified stays unset there, meaning not applicable.
func isGitHubLabelAdded(event *normevent.Event) bool {
	return event.Source.System == normevent.SystemGitHub &&
		event.Transition.Kind == normevent.TransitionLabelChanged &&
		event.Transition.Label != nil &&
		event.Transition.Label.Action == "added"
}

// applyBotRoleLookup resolves the registered Fullsend role of a GitHub bot
// actor and projects the outcome onto event.Actor (actor.role "none",
// actor.bot_role, actor.role_verified), so both CEL routing and harness
// dispatch authorization see it. Humans, non-GitHub events and label-added
// events are left unchanged.
//
// A lookup failure is never returned as an error: it sets role_verified
// false, which the dispatch gate denies before CEL, so a failed lookup
// cannot make the workflow fall back to bash routing. The failure is
// reported on the printer to keep it apart from an expected no-match.
func applyBotRoleLookup(ctx context.Context, event *normevent.Event, resolver botidentity.BotRoleResolver, printer *ui.Printer) {
	if event == nil || event.Actor.Kind != normevent.ActorBot ||
		event.Source.System != normevent.SystemGitHub || isGitHubLabelAdded(event) {
		return
	}
	var (
		res botidentity.BotRoleResult
		err error
	)
	if resolver == nil {
		err = fmt.Errorf("%w: no bot-role resolver", botidentity.ErrLookup)
	} else {
		res, err = resolver.ResolveBotRole(ctx, botidentity.BotRoleQuery{
			Forge:  string(normevent.SystemGitHub),
			Target: event.Repo,
			Login:  event.Actor.ID,
		})
	}
	botidentity.ApplyBotRoleResult(&event.Actor, res, err)
	if printer == nil {
		return
	}
	switch {
	case err != nil:
		printer.StepWarn(fmt.Sprintf("Bot-role lookup failed for %s on %s; denying the bot: %v", event.Actor.ID, event.Repo, err))
	case res.Recognized():
		printer.StepInfo(fmt.Sprintf("Bot %s is registered as %s on %s", event.Actor.ID, res.Role, event.Repo))
	default:
		printer.StepInfo(fmt.Sprintf("Bot %s has no registered role on %s; denying non-label dispatch", event.Actor.ID, event.Repo))
	}
}

// botRoleDenialReason names why a bot actor carrying a lookup result is
// denied, keeping a failed lookup apart from an unregistered bot. It
// returns "" for every other actor.
func botRoleDenialReason(actor normevent.Actor) string {
	if actor.Kind != normevent.ActorBot || actor.RoleVerified == nil {
		return ""
	}
	if !*actor.RoleVerified {
		return "bot-role lookup failed"
	}
	if actor.BotRole == "" {
		return "bot has no registered role"
	}
	return ""
}
