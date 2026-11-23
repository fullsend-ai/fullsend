package executionplan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The test binary itself acts as an external driver, exercising the actual
// subprocess I/O boundary without a shell or a live forge credential.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FULLSEND_TEST_PLAN_DRIVER"); mode != "" {
		var q DriverRequest
		if err := json.NewDecoder(os.Stdin).Decode(&q); err != nil {
			os.Exit(2)
		}
		switch mode {
		case "fail":
			fmt.Fprintln(os.Stderr, "sensitive driver diagnostics")
			os.Exit(1)
		case "hang":
			time.Sleep(time.Minute)
		case "large":
			fmt.Print(strings.Repeat("x", maxDriverOutput+1))
		case "trailing":
			fmt.Print(`{"authorized":true} {}`)
		case "unknown-field":
			fmt.Print(`{"authorized":true,"unexpected":1}`)
		default:
			r := DriverResponse{Authorized: mode != "denied"}
			if q.Attempt != nil && q.Operation != "lookup" {
				a := q.Attempt
				r.Receipt = &Receipt{AttemptID: a.ID, PlanDigest: a.PlanDigest, TargetID: a.Target.ID, SourceSHA: a.Target.SHA, RunID: "external-run", State: Succeeded}
			}
			_ = json.NewEncoder(os.Stdout).Encode(r)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCommandDriverRunsSourceBoundPlan(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("FULLSEND_TEST_PLAN_DRIVER", "success")
	executable, err := os.Executable()
	require.NoError(t, err)
	p := examplePlan()
	d := &CommandDriver{Executable: executable, Timeout: time.Second}
	state, err := Reconcile(context.Background(), p, &FileStore{Dir: t.TempDir()}, d)
	require.NoError(t, err)
	require.True(t, state.Complete())
	receipt, err := d.Lookup(context.Background(), p, state.Targets[0].Attempts[0])
	require.NoError(t, err)
	require.Nil(t, receipt)
}

func TestCommandDriverFailsClosed(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	p := examplePlan()
	for _, mode := range []string{"denied", "fail", "hang", "large", "trailing", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			t.Setenv("FULLSEND_TEST_PLAN_DRIVER", mode)
			d := &CommandDriver{Executable: executable, Timeout: 100 * time.Millisecond}
			err := d.Authorize(context.Background(), p, p.Targets[0])
			require.Error(t, err)
			require.NotContains(t, err.Error(), "sensitive")
		})
	}
	d := &CommandDriver{Executable: "relative-driver"}
	require.Error(t, d.Authorize(context.Background(), p, p.Targets[0]))
}

func TestDriverOutputIsBoundedBeforeDecode(t *testing.T) {
	var b boundedBuffer
	n, err := b.Write([]byte(strings.Repeat(" ", maxDriverOutput)))
	require.NoError(t, err)
	require.Equal(t, maxDriverOutput, n)
	n, err = b.Write([]byte("x"))
	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, maxDriverOutput, b.buffer.Len())
	d := &CommandDriver{Executable: "/nonexistent", Timeout: -time.Second}
	require.Error(t, d.Authorize(context.Background(), examplePlan(), examplePlan().Targets[0]))
}

func TestCommandDriverConfiguration(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, d := range []*CommandDriver{{Executable: "relative"}, {Executable: "/nonexistent"}, {Executable: executable, Timeout: -time.Second}, {Executable: executable, Timeout: 6 * time.Minute}} {
		require.Error(t, d.Validate())
	}
	require.NoError(t, (&CommandDriver{Executable: executable}).Validate())
}
