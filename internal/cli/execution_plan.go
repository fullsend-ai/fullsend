package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/executionplan"
)

func newExecutionPlanCmd() *cobra.Command {
	var planPath, stateDir, driverPath string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "execution-plan",
		Short: "Reconcile an experimental multi-target execution plan once",
		Long:  "Advance a source-bound execution plan using a trusted host driver and local durable state. Native CI adapters and distributed journal storage are not included in this experimental command.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := os.Open(planPath)
			if err != nil {
				return fmt.Errorf("open execution plan: %w", err)
			}
			defer file.Close() //nolint:errcheck
			input, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
			if err != nil || len(input) > 4<<20 {
				return errors.New("execution plan must be within 4 MiB")
			}
			decoder := json.NewDecoder(bytes.NewReader(input))
			decoder.DisallowUnknownFields()
			var plan executionplan.Plan
			if err := decoder.Decode(&plan); err != nil {
				return errors.New("invalid execution plan JSON")
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				return errors.New("execution plan must contain exactly one JSON object within 4 MiB")
			}
			if err := plan.Validate(); err != nil {
				return err
			}
			driver := &executionplan.CommandDriver{Executable: driverPath, Timeout: timeout}
			if err := driver.Validate(); err != nil {
				return err
			}
			state, err := executionplan.Reconcile(cmd.Context(), plan, &executionplan.FileStore{Dir: stateDir}, driver)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Complete bool                `json:"complete"`
				State    executionplan.State `json:"state"`
			}{Complete: state.Complete(), State: state})
		},
	}
	cmd.Flags().StringVar(&planPath, "plan", "", "Path to the reviewed, immutable JSON plan")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "Private persistent local state directory (not a copied CI artifact)")
	cmd.Flags().StringVar(&driverPath, "driver", "", "Absolute path to an operator-controlled host driver executable")
	cmd.Flags().DurationVar(&timeout, "driver-timeout", 30*time.Second, "Timeout per driver call (maximum 5m)")
	for _, name := range []string{"plan", "state-dir", "driver"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
