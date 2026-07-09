package output

import (
	"encoding/json"

	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
)

// GHAMatrix is the GitHub Actions matrix output shape.
type GHAMatrix struct {
	Include []harnessdispatch.ExecutionRef `json:"include"`
}

// WriteGHAMatrix encodes execution refs as a GHA matrix JSON object.
func WriteGHAMatrix(refs []harnessdispatch.ExecutionRef) ([]byte, error) {
	if refs == nil {
		refs = []harnessdispatch.ExecutionRef{}
	}
	return json.Marshal(GHAMatrix{Include: refs})
}
