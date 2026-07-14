package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
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
	cmd.Flags().StringVar(&forgeToken, "forge-token", "", "Forge API token (default: GH_TOKEN or GITHUB_TOKEN)")

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

	var client forge.Client
	token := opts.forgeToken
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token != "" {
		client = gh.New(token)
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
