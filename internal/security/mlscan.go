//go:build !ORT

package security

// MLScanAvailable reports whether the native ONNX ML scanner is compiled in.
func MLScanAvailable() bool { return false }

// RunMLScan is a no-op stub when ONNX runtime is not available.
func RunMLScan(_ string, _ bool) ScanResult {
	return ScanResult{Safe: true}
}

// DestroyMLScanner is a no-op stub when ONNX runtime is not available.
func DestroyMLScanner() {}
