package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/input"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch/output"
	"github.com/fullsend-ai/fullsend/internal/normevent"
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
}

func runDispatch(ctx context.Context, opts dispatchOpts) error {
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

	result, err := harnessdispatch.DispatchResult(ctx, harnessdispatch.Options{
		ConfigDir: opts.configDir,
		Event:     event,
	})
	if err != nil {
		return err
	}
	// Only gha-event has live GitHub authorization context. JSON evaluation
	// remains read-only and never writes workflow notification metadata.
	if inDriver == "gha-event" {
		if err := writeAuthorizationOutput(event, result.AuthorizationDenied); err != nil {
			return err
		}
	}

	switch strings.ToLower(opts.outputDriver) {
	case "gha-matrix", "":
		data, err := output.WriteGHAMatrix(result.Refs)
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
		if err := output.WriteJSON(os.Stdout, result.Refs); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown output driver %q", opts.outputDriver)
	}
	return nil
}

func writeAuthorizationOutput(event *normevent.Event, denied bool) error {
	if !isExplicitHumanSlashCommand(event) || !denied {
		return nil
	}
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" || event.Entity.ID < 1 || event.Actor.NumericID < 1 || !validGitHubLogin(event.Actor.ID) {
		return nil
	}

	outputs := [][2]string{
		{"authorization_denied", "true"},
		{"authorization_actor", event.Actor.ID},
		{"authorization_actor_id", strconv.FormatInt(event.Actor.NumericID, 10)},
		{"authorization_number", strconv.Itoa(event.Entity.ID)},
	}
	for _, output := range outputs {
		if err := writeGitHubOutput(path, output[0], output[1]); err != nil {
			return fmt.Errorf("write authorization output %s: %w", output[0], err)
		}
	}
	return nil
}

func isExplicitHumanSlashCommand(event *normevent.Event) bool {
	if event == nil || event.Source.RawType != "issue_comment" ||
		event.Transition.Kind != normevent.TransitionCommentAdded ||
		event.Actor.Kind != normevent.ActorHuman || event.Transition.Comment == nil {
		return false
	}
	switch event.Transition.Comment.Command {
	case "/fs-triage", "/fs-code", "/fs-review", "/fs-fix", "/fs-retro", "/fs-prioritize":
		return true
	default:
		return false
	}
}

func validGitHubLogin(login string) bool {
	if login == "" {
		return false
	}
	for _, r := range login {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
