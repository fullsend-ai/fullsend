package maputil

import "testing"

func TestSortedKeys(t *testing.T) {
	m := map[string]string{"c": "3", "a": "1", "b": "2"}
	keys := SortedKeys(m)
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	if keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
		t.Errorf("expected [a b c], got %v", keys)
	}
}

func TestSortedKeys_Empty(t *testing.T) {
	keys := SortedKeys(map[string]string{})
	if len(keys) != 0 {
		t.Errorf("expected empty slice, got %v", keys)
	}
}
