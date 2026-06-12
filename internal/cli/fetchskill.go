package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
)

const fetchSkillTimeout = 120 * time.Second

func newFetchSkillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch-skill <url>",
		Short: "Fetch a skill at runtime from inside the sandbox",
		Long: `Requests a skill directory from the runner-side fetch service. The URL must
include a #sha256=<tree-hash> integrity hash and match the harness's
allowed_remote_resources prefixes.

On success, prints the sandbox-local skill directory path to stdout.
On failure, prints the error to stderr and exits with code 1.

This command is intended to be called by agents running inside a sandbox.
It requires the FULLSEND_FETCH_URL and FULLSEND_FETCH_TOKEN environment
variables, which are set automatically during sandbox bootstrap.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFetchSkill(args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func runFetchSkill(skillURL string, stdout, stderr io.Writer) error {
	fetchURL := os.Getenv("FULLSEND_FETCH_URL")
	if fetchURL == "" {
		fmt.Fprintln(stderr, "FULLSEND_FETCH_URL is not set (is runtime fetch enabled in the harness?)")
		return fmt.Errorf("FULLSEND_FETCH_URL not set")
	}

	token := os.Getenv("FULLSEND_FETCH_TOKEN")
	if token == "" {
		fmt.Fprintln(stderr, "FULLSEND_FETCH_TOKEN is not set")
		return fmt.Errorf("FULLSEND_FETCH_TOKEN not set")
	}

	body, err := json.Marshal(fetchsvc.FetchRequest{URL: skillURL})
	if err != nil {
		fmt.Fprintf(stderr, "failed to marshal request: %v\n", err)
		return err
	}

	req, err := http.NewRequest(http.MethodPost, fetchURL, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(stderr, "failed to create request: %v\n", err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: fetchSkillTimeout}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "fetch request failed: %v\n", err)
		return err
	}
	defer resp.Body.Close()

	var fetchResp fetchsvc.FetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&fetchResp); err != nil {
		fmt.Fprintf(stderr, "failed to decode response: %v\n", err)
		return err
	}

	if resp.StatusCode != http.StatusOK || fetchResp.Error != "" {
		msg := fetchResp.Error
		if msg == "" {
			msg = fmt.Sprintf("fetch service returned status %d", resp.StatusCode)
		}
		fmt.Fprintln(stderr, msg)
		return fmt.Errorf("%s", msg)
	}

	fmt.Fprintln(stdout, fetchResp.LocalPath)
	return nil
}
