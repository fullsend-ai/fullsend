package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

var items []string

func init() {
	for i := 1; i <= 25; i++ {
		items = append(items, "item-"+strconv.Itoa(i))
	}
}

func listItems(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 10
	}

	start := (page - 1) * perPage
	end := start + perPage
	if start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"page":     page,
		"per_page": perPage,
		"total":    len(items),
		"items":    items[start:end],
	})
}

func main() {
	http.HandleFunc("/items", listItems)
	http.ListenAndServe(":8080", nil)
}
