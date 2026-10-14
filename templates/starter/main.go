// Command app is a Sideyard app: one Go binary that serves a JSON API and the
// React frontend embedded from web/dist.
package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed all:web/dist
var webDist embed.FS

func main() {
	// Sideyard sets these. The fallbacks are for running locally.
	port := env("PORT", "3000")
	dataDir := env("DATA_DIR", "data")
	dbPath := env("DATABASE_PATH", filepath.Join(dataDir, "app.db"))

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatal(err)
	}
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	notes := &notesAPI{db: db}
	mux.HandleFunc("GET /api/notes", notes.list)
	mux.HandleFunc("POST /api/notes", notes.create)
	mux.HandleFunc("DELETE /api/notes/{id}", notes.delete)
	mux.HandleFunc("POST /_jobs/{name}", jobHandler(db))
	mux.Handle("/", frontend(webDist))

	// 127.0.0.1 only: Sideyard's proxy is the public entry point.
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("listening on http://%s", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	// Sideyard sends SIGTERM when a new release takes over. Finish in-flight
	// requests, then exit.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
