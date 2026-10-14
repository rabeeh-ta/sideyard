package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

type note struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

type notesAPI struct {
	db *sql.DB
}

func (a *notesAPI) list(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, body, created_at FROM notes ORDER BY id DESC LIMIT 100`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	notes := []note{}
	for rows.Next() {
		var n note
		if err := rows.Scan(&n.ID, &n.Body, &n.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		notes = append(notes, n)
	}
	writeJSON(w, http.StatusOK, notes)
}

func (a *notesAPI) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Body) == "" {
		http.Error(w, "body is required", http.StatusBadRequest)
		return
	}
	var n note
	err := a.db.QueryRowContext(r.Context(),
		`INSERT INTO notes (body) VALUES (?) RETURNING id, body, created_at`, strings.TrimSpace(in.Body),
	).Scan(&n.ID, &n.Body, &n.CreatedAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (a *notesAPI) delete(w http.ResponseWriter, r *http.Request) {
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM notes WHERE id = ?`, r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
