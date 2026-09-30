package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type Report struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

var reports = []Report{
	{Name: "monthly-summary.csv", CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	{Name: "quarterly-review.pdf", CreatedAt: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)},
}

func listReportsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reports)
}

func main() {
	http.HandleFunc("/reports", listReportsHandler)
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
