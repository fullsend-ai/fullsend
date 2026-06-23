package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/authorization"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestAuthCheck_BlockedExitCode(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{"workflow-change-needed"}},
	}
	cmd := newAuthCheckTestCmd(client)
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "pre-run",
	})
	err := cmd.Execute()
	require.Error(t, err)
	var ec *exitError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, AuthExitBlocked, ec.ExitCode())
}

func TestAuthCheck_MintJSONElevations(t *testing.T) {
	client := forge.NewFakeClient()
	client.Issues = map[string]forge.Issue{
		"o/r/1": {Number: 1, Labels: []string{"workflow-change-allowed"}},
	}
	client.LabelAppliedAt = map[string]time.Time{
		"o/r/1/workflow-change-allowed": time.Now().Add(-time.Hour),
	}

	var buf bytes.Buffer
	cmd := newAuthCheckTestCmd(client)
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--gate", "workflow-change",
		"--repo", "o/r",
		"--number", "1",
		"--phase", "mint",
		"--json",
	})
	require.NoError(t, cmd.Execute())

	var payload struct {
		Status     authorization.Status `json:"status"`
		Elevations []string             `json:"elevations"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &payload))
	assert.Equal(t, authorization.StatusOK, payload.Status)
	assert.Equal(t, []string{"workflow-change"}, payload.Elevations)
}

func newAuthCheckTestCmd(client forge.Client) *cobra.Command {
	var gateName, repo, phase string
	var number int
	var jsonOut bool
	cmd := &cobra.Command{
		Use: "check",
		RunE: func(cmd *cobra.Command, _ []string) error {
			g := authorization.GateByName(gateName)
			owner, repoName, _ := splitOwnerRepo(repo)
			result, err := authorization.Evaluate(context.Background(), client, *g, authorization.Target{
				Owner: owner, Repo: repoName, Number: number,
			}, authorization.Phase(phase), authorization.Options{})
			if err != nil {
				return err
			}
			if result.Status != authorization.StatusOK {
				return newExitError(authExitCode(result.Status), "%s", result.Status)
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"status":     result.Status,
					"elevations": result.Elevations,
				})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&gateName, "gate", "", "")
	cmd.Flags().StringVar(&repo, "repo", "", "")
	cmd.Flags().IntVar(&number, "number", 0, "")
	cmd.Flags().StringVar(&phase, "phase", "", "")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "")
	return cmd
}

func splitOwnerRepo(repo string) (owner, name string, ok bool) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			return repo[:i], repo[i+1:], true
		}
	}
	return "", "", false
}
