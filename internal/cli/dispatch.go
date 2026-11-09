package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/output"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newDispatchCmd() *cobra.Command {
	var (
		inputDriver  string
		outputDriver string
		inputFile    string
		configDir    string
		repository   string
		eventName    string
		eventAction  string
		forgeToken   string
		route        string
	)

	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Evaluate harness CEL triggers and produce dispatch matrix",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDispatch(cmd.Context(), dispatchOpts{
				inputDriver:  inputDriver,
				outputDriver: outputDriver,
				inputFile:    inputFile,
				configDir:    configDir,
				repository:   repository,
				eventName:    eventName,
				eventAction:  eventAction,
				forgeToken:   forgeToken,
				route:        route,
			})
		},
	}

	cmd.Flags().StringVar(&inputDriver, "input-driver", "", "Input driver: gha-event, json (default: gha-event when GITHUB_EVENT_PATH is set)")
	cmd.Flags().StringVar(&outputDriver, "output-driver", "gha-matrix", "Output driver: gha-matrix, json")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "JSON input file for json driver (use - for stdin)")
	cmd.Flags().StringVar(&configDir, "config-dir", ".fullsend", "Fullsend config directory")
	cmd.Flags().StringVar(&repository, "repo", "", "Repository owner/repo (default: GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&eventName, "event-name", "", "GitHub event name (default: GITHUB_EVENT_NAME)")
	cmd.Flags().StringVar(&eventAction, "event-action", "", "GitHub event action")
	cmd.Flags().StringVar(&forgeToken, "forge-token", "", "Forge API token (default: GH_TOKEN, GITHUB_TOKEN, or gh auth token)")
	cmd.Flags().StringVar(&route, "route", "", "Decide routing for one built-in agent with a CEL route (prioritize, retro, review or triage) and print one JSON line with decision run, skip or bash; --output-driver is ignored")

	return cmd
}

type dispatchOpts struct {
	inputDriver  string
	outputDriver string
	inputFile    string
	configDir    string
	repository   string
	eventName    string
	eventAction  string
	forgeToken   string
	// route, when set, names a built-in agent (routableAgents) to decide
	// routing for instead of producing a dispatch matrix.
	route string
}

// newDispatchFetchPolicy returns the fetch policy `fullsend dispatch`
// uses when it resolves and loads harnesses, for both the dispatch matrix
// and --route. It is the default policy extended to admit the configured
// GitHub host, derived from the base URL of the same fallback client
// fullsend run uses (GITHUB_API_URL and the manifest are honoured; the
// token is optional, as fullsend-ai/agents is public and the client here
// only names the host). On github.com the default policy is returned
// unchanged.
func newDispatchFetchPolicy(forgeToken string) fetch.FetchPolicy {
	token, err := resolveGitHubToken(forgeToken)
	if err != nil {
		token = ""
	}
	return withAgentsRepoHostAllowed(fetch.DefaultPolicy, newGitHubLiveClient(token, ""))
}

func runDispatch(ctx context.Context, opts dispatchOpts) error {
	route := strings.ToLower(strings.TrimSpace(opts.route))
	if _, ok := routableAgents[route]; opts.route != "" && !ok {
		return fmt.Errorf("unsupported --route agent %q (supported: %s)", opts.route, routableAgentNames())
	}

	inDriver := opts.inputDriver
	if inDriver == "" {
		if os.Getenv("GITHUB_EVENT_PATH") != "" {
			inDriver = "gha-event"
		} else {
			return fmt.Errorf("input driver required (set --input-driver or GITHUB_EVENT_PATH)")
		}
	}

	// GitHub credentials enrich gha-event actor/PR lookups. The json
	// driver never needs a client, so missing credentials are not fatal.
	var client forge.Client
	if c, err := newAuthenticatedGitHubClient(opts.forgeToken, ""); err == nil {
		client = c
	}

	// The harnesses dispatch enumerates and routes load can pin base:
	// chains and companion files to the configured GitHub host — the same
	// host fullsend run's agents-repo fallback resolves through
	// newGitHubLiveClient. The default fetch policy names only github.com
	// hosts, so it is extended from the fallback client's base URL exactly
	// as runAgent does; without that, loading such a harness fails with
	// "domain not in allowlist" before any agent job is reached.
	dispatchFetchPolicy := newDispatchFetchPolicy(opts.forgeToken)

	var event *normevent.Event
	var err error
	switch inDriver {
	case "gha-event":
		event, err = input.LoadGHAEvent(ctx, input.GHAEventOptions{
			Repository:  opts.repository,
			EventName:   opts.eventName,
			EventAction: opts.eventAction,
			Forge:       client,
		})
	case "json":
		event, err = input.LoadJSONEvent(opts.inputFile)
	default:
		return fmt.Errorf("unknown input driver %q", inDriver)
	}
	if err != nil {
		return err
	}

	printer := ui.New(os.Stderr)
	// Resolve a GitHub bot actor's registered role before CEL routing and
	// harness dispatch authorization (ADR 0107). A failed lookup denies
	// the bot instead of failing the command.
	if inDriver == "gha-event" {
		applyBotRoleLookup(ctx, event, newDispatchBotRoleResolverFn(opts.configDir), printer)
	}

	if route != "" {
		decision, err := routeBuiltinAgent(ctx, routeOpts{
			agent:       route,
			configDir:   opts.configDir,
			event:       event,
			forgeClient: client,
			fetchPolicy: &dispatchFetchPolicy,
			printer:     printer,
		})
		if err != nil {
			return err
		}
		data, err := json.Marshal(decision)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(data))
		return err
	}

	refs, err := harnessdispatch.Dispatch(ctx, harnessdispatch.Options{
		ConfigDir:   opts.configDir,
		Event:       event,
		FetchPolicy: &dispatchFetchPolicy,
	})
	if err != nil {
		return err
	}

	switch strings.ToLower(opts.outputDriver) {
	case "gha-matrix", "":
		data, err := output.WriteGHAMatrix(refs)
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(data); err != nil {
			return err
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Println()
		}
	case "json":
		if err := output.WriteJSON(os.Stdout, refs); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown output driver %q", opts.outputDriver)
	}
	return nil
}
