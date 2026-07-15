package input

import (
	"fmt"
	"io"
	"os"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// LoadJSONEvent reads a NormalizedEvent from a JSON file or stdin ("-").
func LoadJSONEvent(path string) (*normevent.Event, error) {
	var r io.Reader
	switch {
	case path == "" || path == "-":
		r = os.Stdin
	default:
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("reading input: %w", err)
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading input: %w", err)
	}
	return normevent.ParseJSON(data)
}
