package input

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInt64Field(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int64
	}{
		{name: "float", value: float64(1234), want: 1234},
		{name: "fractional float", value: 12.5},
		{name: "too large float", value: float64(math.MaxInt64)},
		{name: "int", value: 1234, want: 1234},
		{name: "negative int", value: -1},
		{name: "int64", value: int64(1234), want: 1234},
		{name: "negative int64", value: int64(-1)},
		{name: "json number", value: json.Number("1234"), want: 1234},
		{name: "invalid json number", value: json.Number("not-a-number")},
		{name: "unsupported", value: "1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, int64Field(map[string]any{"id": tt.value}, "id"))
		})
	}
	assert.Zero(t, int64Field(nil, "id"))
}
