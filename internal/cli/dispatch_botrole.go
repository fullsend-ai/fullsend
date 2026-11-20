package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/botidentity"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/mintcore/mintconsts"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// newDispatchBotRoleResolverFn builds the resolver `fullsend dispatch` uses
// for bot actors of gha-event input (ADR 0107): the mint's GitHub bot-role
// lookup, authenticated with the job's GitHub Actions OIDC token. The mint
// URL is resolved like `run` and `reconcile-status` do: a layered mint_url in
// configDir's config.yaml/config.base.yaml wins over FULLSEND_MINT_URL. An
// absent config is no file layer, so the environment variable still applies.
// A config that exists but fails to load or parse is a lookup failure that
// denies the bot, never a silent fallback to FULLSEND_MINT_URL. An unset mint
// URL or OIDC environment is a lookup failure too. Each of these setup
// failures also wraps errBotRoleConfig so the denial can name it as a
// configuration problem. Override in tests to supply a stub or fake resolver.
var newDispatchBotRoleResolverFn = func(configDir string) botidentity.BotRoleResolver {
	var orgCfg config.ConfigReader
	if configDir != "" {
		cfg, err := config.LoadConfig(configDir, config.LoadOpts{MissingOK: true})
		if err != nil {
			return failedBotRoleResolver{err: fmt.Errorf("%w: %w: loading config to resolve the mint URL: %w", botidentity.ErrLookup, errBotRoleConfig, err)}
		}
		orgCfg = cfg
	}
	mintURL, _ := resolveMintURL("", orgCfg, os.Getenv)
	if mintURL == "" {
		return failedBotRoleResolver{err: fmt.Errorf("%w: %w: no mint URL (set mint_url in config.yaml or FULLSEND_MINT_URL)", botidentity.ErrLookup, errBotRoleConfig)}
	}
	return &botidentity.MintResolver{
		MintURL:   mintURL,
		OIDCToken: actionsMintOIDCToken,
	}
}

// errBotRoleConfig marks a bot-role lookup that failed because this
// repository's Fullsend setup could not be resolved (config, mint URL or the
// Actions OIDC environment), as opposed to a mint lookup that errored.
var errBotRoleConfig = errors.New("mint configuration unresolved")

// failedBotRoleResolver answers every lookup with err, which wraps
// botidentity.ErrLookup, so the bot is denied without contacting the mint.
type failedBotRoleResolver struct{ err error }

func (f failedBotRoleResolver) ResolveBotRole(context.Context, botidentity.BotRoleQuery) (botidentity.BotRoleResult, error) {
	return botidentity.BotRoleResult{}, f.err
}

// actionsMintOIDCToken fetches the job's GitHub Actions OIDC token for the
// mint audience. Errors never include the token.
// A missing Actions OIDC request environment wraps errBotRoleConfig: the
// job lacks id-token: write, which is a setup problem, not a mint outage.
func actionsMintOIDCToken(ctx context.Context) (string, error) {
	if !hasActionsOIDCEnv(os.Getenv) {
		return "", fmt.Errorf("%w: GitHub Actions OIDC request environment is not set (the job needs id-token: write)", errBotRoleConfig)
	}
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
//
// Every denial (no registered role, a failed mint lookup, or an unresolved
// mint configuration) is also reported as a GitHub Actions warning
// annotation and a job step summary line (reportBotRoleDenial), because the
// denial otherwise ends in exit 0, an empty matrix and a green run. The
// deny decision and the exit status are unchanged.
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
	case errors.Is(err, errBotRoleConfig):
		msg := fmt.Sprintf("Bot-role lookup for %s on %s could not resolve the mint configuration; denying the bot. "+
			"Fix this repository's Fullsend setup (mint_url or FULLSEND_MINT_URL, config.yaml, id-token: write): %v", event.Actor.ID, event.Repo, err)
		printer.StepWarn(msg)
		reportBotRoleDenial("Fullsend bot-role configuration unresolved", msg)
	case err != nil:
		msg := fmt.Sprintf("Bot-role lookup failed for %s on %s; denying the bot. "+
			"The mint lookup errored, which is usually transient: re-run the workflow, or check mint availability: %v", event.Actor.ID, event.Repo, err)
		printer.StepWarn(msg)
		reportBotRoleDenial("Fullsend bot-role lookup failed", msg)
	case res.Recognized():
		printer.StepInfo(fmt.Sprintf("Bot %s is registered as %s on %s", event.Actor.ID, res.Role, event.Repo))
	default:
		printer.StepInfo(fmt.Sprintf("Bot %s has no registered role on %s; denying non-label dispatch", event.Actor.ID, event.Repo))
		reportBotRoleDenial("Fullsend bot has no registered role", fmt.Sprintf(
			"Bot %s has no registered role on %s; denying non-label dispatch, so no agent runs for its events. "+
				"Register %s with the mint to give it agent coverage.", event.Actor.ID, event.Repo, event.Actor.ID))
	}
}

// botRoleAnnotationWriter receives the workflow commands reportBotRoleDenial
// writes. The runner reads them from the step's stderr; stdout carries the
// dispatch matrix or route decision. Override in tests.
var botRoleAnnotationWriter io.Writer = os.Stderr

// reportBotRoleDenial makes a bot-role denial visible on the run page: a
// ::warning:: annotation when running in GitHub Actions, and a line in the
// job step summary when GITHUB_STEP_SUMMARY is set. msg carries event data
// (actor, repository, mint error text), so it is escaped for the workflow
// command and folded onto one line for the summary, and cannot start a
// second command. Write failures are ignored: reporting never changes the
// deny decision or the exit status.
func reportBotRoleDenial(title, msg string) {
	line := strings.Join(strings.Fields(msg), " ")
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Fprintf(botRoleAnnotationWriter, "::warning title=%s::%s\n", title, actionsCommandEscaper.Replace(line))
	}
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "- :warning: **%s**: %s\n", title, line)
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
