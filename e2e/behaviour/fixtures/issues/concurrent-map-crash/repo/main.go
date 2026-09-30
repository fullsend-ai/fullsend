package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

var cache = make(map[string]cacheEntry)

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

	entry, ok := cache[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		if ok {
			delete(cache, key)
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

func setHandler(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	value := r.URL.Query().Get("value")
	if key == "" || value == "" {
		http.Error(w, "missing key or value parameter", http.StatusBadRequest)
		return
	}

	cache[key] = cacheEntry{
		Value:     value,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "cached %s\n", key)
}

func main() {
	http.HandleFunc("/get", getHandler)
	http.HandleFunc("/set", setHandler)
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
