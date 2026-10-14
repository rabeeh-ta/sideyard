// A minimal app for platform tests. It refuses to start when FAIL=1.
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if os.Getenv("FAIL") == "1" {
		fmt.Println("refusing to start: FAIL=1")
		os.Exit(1)
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Lets tests import a database that makes the app unhealthy.
		if db, _ := os.ReadFile(os.Getenv("DATABASE_PATH")); bytes.Contains(db, []byte("BREAK-HEALTH")) {
			http.Error(w, "bad database", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "ok")
	})
	http.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, os.Getuid()) })
	http.HandleFunc("/env", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, os.Getenv(r.URL.Query().Get("key"))) })
	// Jobs: Sideyard calls these with the app's token.
	http.HandleFunc("POST /_jobs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+os.Getenv("SIDEYARD_TOKEN") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch name := r.PathValue("name"); name {
		case "fail":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "slow":
			time.Sleep(2 * time.Second)
			fmt.Fprint(w, "slow done")
		default:
			fmt.Fprintf(w, "ran %s for %s", name, r.Header.Get("X-Sideyard-Scheduled-At"))
		}
	})
	// Lets tests check what the app's sandbox can see.
	http.HandleFunc("/read", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.ReadFile(r.URL.Query().Get("path")); err != nil {
			fmt.Fprint(w, err)
			return
		}
		fmt.Fprint(w, "readable")
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "release %s greeting %s", os.Getenv("SIDEYARD_RELEASE"), os.Getenv("GREETING"))
	})
	// An upgraded connection that echoes bytes back, standing in for a WebSocket.
	http.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		rw.Flush()
		io.Copy(conn, rw)
	})
	http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), nil)
}
