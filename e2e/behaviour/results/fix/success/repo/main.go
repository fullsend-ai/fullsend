package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

var (
	cacheMu sync.RWMutex
	cache   = make(map[string]cacheEntry)
)

type cacheEntry struct {
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
}

func getHandler(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "missing key parameter", http.StatusBadRequest)
		return
	}

	cacheMu.RLock()
	entry, ok := cache[key]
	cacheMu.RUnlock()

	if !ok || time.Now().After(entry.ExpiresAt) {
		if ok {
			// Re-check under the write lock before deleting: between the
			// RUnlock above and acquiring Lock here, setHandler may have
			// written a fresh entry for this key. Only delete if the entry
			// is still the one we just read as expired, so a concurrent
			// write is never silently discarded (TOCTOU fix).
			cacheMu.Lock()
			if current, stillPresent := cache[key]; stillPresent && current.ExpiresAt.Equal(entry.ExpiresAt) {
				delete(cache, key)
			}
			cacheMu.Unlock()
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entry)
}

func setHandler(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	value := r.URL.Query().Get("value")
	if key == "" || value == "" {
		http.Error(w, "missing key or value parameter", http.StatusBadRequest)
		return
	}

	cacheMu.Lock()
	cache[key] = cacheEntry{
		Value:     value,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	cacheMu.Unlock()

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "cached %s\n", key)
}

func main() {
	http.HandleFunc("/get", getHandler)
	http.HandleFunc("/set", setHandler)
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
