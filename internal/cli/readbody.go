package cli

import (
	"fmt"
	"io"
	"os"
)

const maxBodyBytes = 2 << 20 // 2 MB

// readBody reads content from a file path or stdin (when path is "-").
// Input is capped at maxBodyBytes to prevent resource exhaustion.
func readBody(path string) (string, error) {
	return readBodyFrom(path, os.Stdin)
}

// readBodyFrom is the testable core of readBody. It accepts an explicit
// stdin reader so callers can inject test data.
func readBodyFrom(path string, stdin io.Reader) (string, error) {
	if path == "-" {
		data, err := io.ReadAll(io.LimitReader(stdin, maxBodyBytes+1))
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		if len(data) > maxBodyBytes {
			return "", fmt.Errorf("input exceeds maximum size of %d bytes", maxBodyBytes)
		}
		return string(data), nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxBodyBytes {
		return "", fmt.Errorf("file %s exceeds maximum size of %d bytes", path, maxBodyBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
