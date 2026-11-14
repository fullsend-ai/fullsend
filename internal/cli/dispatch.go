package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

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
		printEvent   bool
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
				printEvent:   printEvent,
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
	cmd.Flags().BoolVar(&printEvent, "print-event", false, "Print the loaded event's complete normalized form as one JSON object and exit — the form buildEventPayload embeds as _normalized_event; --output-driver is ignored")

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
	// printEvent, when set, prints the loaded event's complete normalized
	// form (the map buildEventPayload embeds as _normalized_event) and
	// exits, so workflow callers that build event payloads outside the
	// harness matrix — the route job of reusable-dispatch.yml for the
	// built-in stage jobs — can embed the same normalized event (#6748,
	// AISDLC-505).
	printEvent bool
}

func runDispatch(ctx context.Context, opts dispatchOpts) error {
	route := strings.ToLower(strings.TrimSpace(opts.route))
	if _, ok := routableAgents[route]; opts.route != "" && !ok {
		return fmt.Errorf("unsupported --route agent %q (supported: %s)", opts.route, routableAgentNames())
	}
	if opts.printEvent && opts.route != "" {
		return fmt.Errorf("--print-event cannot be combined with --route")
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

	// --print-event stops here: the printed event carries the same
	// bot-role resolution the harness dispatch path applies, so a payload
	// embedding it cannot smuggle an unresolved bot past a trigger that
	// keys on actor.bot_role. The JSON is the same form buildEventPayload
	// embeds as _normalized_event (ToMap round-trips the same marshal).
	if opts.printEvent {
		data, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("marshaling normalized event: %w", err)
		}
		_, err = fmt.Fprintln(os.Stdout, string(data))
		return err
	}

	if route != "" {
		decision, err := routeBuiltinAgent(ctx, routeOpts{
			agent:       route,
			configDir:   opts.configDir,
			event:       event,
			forgeClient: client,
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
		ConfigDir: opts.configDir,
		Event:     event,
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
