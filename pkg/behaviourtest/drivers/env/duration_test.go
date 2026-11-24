package env

import (
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	const name = "BEHAVIOUR_TEST_DURATION_KNOB"
	tests := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{value: "", want: 90 * time.Second},
		{value: "  ", want: 90 * time.Second},
		{value: "5s", want: 5 * time.Second},
		{value: "1m30s", want: 90 * time.Second},
		{value: "five", wantErr: true},
		{value: "0s", wantErr: true},
		{value: "-1s", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(name, tt.value)
			got, err := Duration(name, 90*time.Second)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Duration(%q) = %v, want an error", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Duration(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("Duration(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}
