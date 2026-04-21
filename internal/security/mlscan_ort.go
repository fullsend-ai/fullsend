//go:build ORT

package security

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/options"
	"github.com/knights-analytics/hugot/pipelines"
)

var (
	mlScanner     *ONNXGuardScanner
	mlSession     *hugot.Session
	mlScannerOnce sync.Once
	mlScannerErr  error
	mlWarnOnce    sync.Once
)

func initMLScanner() {
	os.Setenv("HF_HUB_OFFLINE", "1")

	modelPath := os.Getenv("MODEL_PATH")
	if modelPath == "" {
		modelPath = "/opt/fullsend/models/protectai-deberta-v3/onnx"
	}
	ortLibPath := os.Getenv("ORT_LIB_PATH")
	if ortLibPath == "" {
		ortLibPath = "/usr/lib"
	}

	ctx := context.Background()
	session, err := hugot.NewORTSession(ctx,
		options.WithOnnxLibraryPath(ortLibPath),
		options.WithIntraOpNumThreads(4),
	)
	if err != nil {
		mlScannerErr = fmt.Errorf("creating ORT session: %w", err)
		return
	}

	config := hugot.TextClassificationConfig{
		ModelPath: modelPath,
		Name:      "injection-scanner",
		Options: []hugot.TextClassificationOption{
			pipelines.WithSoftmax(),
			pipelines.WithSingleLabel(),
		},
	}
	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		mlScannerErr = fmt.Errorf("creating pipeline: %w", err)
		return
	}

	scanner, err := NewONNXGuardScanner(pipeline, modelPath, 0, "")
	if err != nil {
		session.Destroy()
		mlScannerErr = fmt.Errorf("creating scanner: %w", err)
		return
	}

	mlSession = session
	mlScanner = scanner
}

// MLScanAvailable reports whether the native ONNX ML scanner is compiled in.
func MLScanAvailable() bool { return true }

// RunMLScan runs the ONNX-based prompt injection scanner. Initializes the
// model session on first call (lazy singleton).
//
// When required is false (Path A / CLI pre-step), initialization failures
// are fail-open: returns Safe=true with a stderr warning.
//
// When required is true (sandbox / Path B), initialization failures are
// fail-closed: returns Safe=false with a critical finding, since a missing
// or broken scanner inside the sandbox indicates possible tampering.
func RunMLScan(text string, required bool) ScanResult {
	mlScannerOnce.Do(initMLScanner)

	if mlScannerErr != nil {
		if required {
			return ScanResult{
				Safe: false,
				Findings: []Finding{{
					Scanner:  "llm_guard",
					Name:     "scanner_unavailable",
					Severity: "critical",
					Detail:   fmt.Sprintf("ML scanner required but unavailable: %v", mlScannerErr),
					Position: -1,
				}},
			}
		}
		mlWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "WARN: ML scanner unavailable: %v\n", mlScannerErr)
		})
		return ScanResult{Safe: true}
	}

	return mlScanner.Scan(text)
}

// DestroyMLScanner releases the ORT session resources.
func DestroyMLScanner() {
	if mlSession != nil {
		mlSession.Destroy()
		mlSession = nil
	}
}
