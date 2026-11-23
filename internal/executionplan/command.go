package executionplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const maxDriverOutput = 1 << 20

// DriverRequest is sent on stdin to the operator-selected executable. It
// contains no credentials; installation credentials are inherited via environment.
type DriverRequest struct {
	Version   int      `json:"version"`
	Operation string   `json:"operation"`
	Plan      Plan     `json:"plan"`
	Target    *Target  `json:"target,omitempty"`
	Attempt   *Attempt `json:"attempt,omitempty"`
}

// DriverResponse is the driver's single JSON stdout object. An unknown Lookup
// returns an absent receipt. Authorize succeeds only with authorized=true.
type DriverResponse struct {
	Authorized bool     `json:"authorized"`
	Receipt    *Receipt `json:"receipt,omitempty"`
}

// CommandDriver runs a trusted host adapter directly, without shell expansion.
// The executable, its environment and working directory are operator-controlled;
// never select them from a plan, target checkout, issue body or model output.
// Stderr is discarded because adapters may emit credentials in diagnostics.
type CommandDriver struct {
	Executable string
	Timeout    time.Duration
}

// Authorize asks the adapter to verify the plan grant and target permissions.
func (d *CommandDriver) Authorize(ctx context.Context, p Plan, t Target) error {
	r, err := d.call(ctx, DriverRequest{Version: 1, Operation: "authorize", Plan: p, Target: &t})
	if err != nil {
		return err
	}
	if !r.Authorized {
		return errors.New("driver denied authorization")
	}
	return nil
}

// Submit launches an exact attempt after fullsend has persisted its intent.
func (d *CommandDriver) Submit(ctx context.Context, p Plan, a Attempt) (*Receipt, error) {
	r, err := d.call(ctx, DriverRequest{Version: 1, Operation: "submit", Plan: p, Attempt: &a})
	return r.Receipt, err
}

// Lookup observes an exact attempt without creating work or retrying dispatch.
func (d *CommandDriver) Lookup(ctx context.Context, p Plan, a Attempt) (*Receipt, error) {
	r, err := d.call(ctx, DriverRequest{Version: 1, Operation: "lookup", Plan: p, Attempt: &a})
	return r.Receipt, err
}

// Validate checks operator configuration before a plan is admitted.
func (d *CommandDriver) Validate() error {
	if !filepath.IsAbs(d.Executable) {
		return errors.New("driver must be an absolute path to a trusted executable")
	}
	info, err := os.Stat(d.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("driver must be an existing executable file")
	}
	if d.Timeout < 0 || d.Timeout > 5*time.Minute {
		return errors.New("driver timeout must be positive and at most five minutes")
	}
	return nil
}

func (d *CommandDriver) call(ctx context.Context, q DriverRequest) (DriverResponse, error) {
	var response DriverResponse
	if err := d.Validate(); err != nil {
		return response, err
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	input, err := json.Marshal(q)
	if err != nil {
		return response, err
	}
	cmd := exec.CommandContext(ctx, d.Executable)
	cmd.Stdin = bytes.NewReader(input)
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return response, ctx.Err()
		}
		return response, errors.New("execution-plan driver failed; external outcome may be uncertain")
	}
	decoder := json.NewDecoder(bytes.NewReader(output.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return DriverResponse{}, errors.New("invalid execution-plan driver response")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return DriverResponse{}, errors.New("driver must return exactly one JSON object")
	}
	return response, nil
}

type boundedBuffer struct{ buffer bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > maxDriverOutput-b.buffer.Len() {
		return 0, errors.New("driver response exceeds limit")
	}
	return b.buffer.Write(p)
}
