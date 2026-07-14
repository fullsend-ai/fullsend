package output

import (
	"encoding/json"
	"io"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
)

// WriteJSON writes execution refs as indented JSON to w.
func WriteJSON(w io.Writer, refs []harnessdispatch.ExecutionRef) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(refs)
}
