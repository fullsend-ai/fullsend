package intmath

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}

func TestEven(t *testing.T) {
	if !Even(2) {
		t.Fatal("Even(2) = false, want true")
	}
	if Even(3) {
		t.Fatal("Even(3) = true, want false")
	}
}
