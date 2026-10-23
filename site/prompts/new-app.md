# New app for Sideyard

This app will run on **Sideyard**, a self-hosted platform that runs many small apps on one server. Every Sideyard app has the same shape: a Go backend, a React + TypeScript frontend built with Vite, and SQLite for data, compiled together into one Linux binary. Sideyard receives that binary, starts it, waits for its health check and only then sends it traffic. An app that keeps to the contract in `AGENTS.md` below deploys with `git push`. One that doesn't won't run.

Do this in order:

1. **Scaffold.** Create every file under "Files" below in the current directory, exactly as given, plus an empty `web/dist/.gitkeep` (the binary embeds `web/dist`, so it must exist). Make `scripts/deploy.sh` executable. These files are Sideyard's starter template: the parts that talk to Sideyard (environment, health check, graceful shutdown, migrations, jobs, email, the embedded frontend, the deploy workflow) are already right, and a small notes feature shows them working together. You may rename the module in `go.mod`; keep the binary's name, `app`.
2. **Install and check.** Run `go mod tidy` and `cd web && npm install`, then the checks at the end of `AGENTS.md`. Start the API with `make api` and confirm `curl http://127.0.0.1:3000/healthz` prints `ok`.
3. **Build.** Build the app I describe at the end of this message, replacing the notes example: `notes.go`, its routes in `main.go`, its migration in `db.go`, its job in `jobs.go` and `web/src/App.tsx`. Nothing has been deployed yet, so rewrite the notes migration rather than appending one that drops it. If I haven't described an app, stop after step 2, tell me the scaffold is ready and ask what we're building.
4. **Hand over.** When it works locally, tell me how to deploy: create a project in the Sideyard dashboard (and connect a bucket in its Storage tab if the app stores files), set the GitHub secret and variables listed at the top of `.github/workflows/deploy.yml` (`SIDEYARD_ARCH` too if the server is ARM), and push to `main`.

`AGENTS.md` is the contract and stays in the repo, so every later session follows it too. When a feature seems to need something outside the stack (Postgres, Redis, a queue, a separate worker, a Node server, Docker, cron), build it inside the stack the way `AGENTS.md` describes, and tell me if something truly can't fit.

## Files

### `AGENTS.md`

```markdown
# Agent guide

This app deploys to **Sideyard**, a self-hosted PaaS that runs many small apps on one VPS, each as a single Go binary. The rules below are the contract with Sideyard: code that keeps to them deploys on `git push`.

## Stack

Go backend, React + TypeScript frontend (Vite), SQLite for data. `web/dist` is embedded into the binary with `go:embed`, so production is one file. Build every feature inside this stack: SQLite tables for data, goroutines for background work, the Go server for APIs and WebSockets. Postgres, Redis, a Node server or Docker can't run on Sideyard.

## Runtime contract

- Listen on `127.0.0.1:$PORT`. Sideyard's proxy owns the public domain and HTTPS.
- Persistent files go in `$DATA_DIR`. The SQLite database is `$DATABASE_PATH`. Everything outside `$DATA_DIR` is replaced on each deploy, and on the server it's read-only apart from a private `/tmp`. Memory is capped too: 512 MB unless raised in the project's Settings.
- `GET /healthz` answers 200 once the app can serve. A release goes live only after it passes, so check real dependencies there (the starter pings the database).
- On SIGTERM, finish in-flight requests and exit within 10 seconds. `main.go` already does this.
- Config and secrets arrive as environment variables set in the Sideyard dashboard. Read them with `os.Getenv` and keep them out of the repo.
- Use `modernc.org/sqlite`, the pure-Go driver. Production builds use `CGO_ENABLED=0 GOOS=linux`, so any package that needs cgo breaks the deploy.

## Migrations

Migrations are the `migrations` slice in `db.go` and run at startup. Append new ones; existing entries are history and stay exactly as they are. During a deploy the previous release keeps serving for a few seconds against the new schema, and a rollback doesn't undo a migration. So every migration must work with the previous release's code: add tables and columns now, and drop or rename in a later deploy once no code reads the old shape.

## Background jobs

Scheduled work runs through Sideyard, not goroutine timers: Sideyard calls `POST /_jobs/<name>` on the app on the schedule set in the dashboard's Jobs tab, with `Authorization: Bearer $SIDEYARD_TOKEN`. Add a job as a function in the `jobs` map in `jobs.go`; `jobHandler` checks the token. A returned error fails the run and the returned text is its output, both shown in the dashboard. Keep a job idempotent: a run can be retried, and one catch-up run happens after downtime.

## Email

Send with `sendEmail` in `email.go`. It posts to Sideyard's API with the app's token; Sideyard holds the provider's credentials and the sender address, and logs every email in the project's Email tab.

## Files and uploads

User uploads and large files go in the project's bucket, with small metadata in SQLite. Connecting a bucket in the dashboard's Storage tab sets `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `S3_BUCKET`; the AWS SDK for Go v2 reads the `AWS_` ones by itself. The bucket may be any S3-compatible service, so give the S3 client `UsePathStyle` when the endpoint isn't AWS's, and `RequestChecksumCalculation` and `ResponseChecksumValidation` set to `WhenRequired` (newer SDKs send checksums some of these services reject). Stream uploads to the bucket with the SDK's upload manager rather than reading them into memory, and serve files through the app or with presigned URLs; the bucket itself stays private.

## Checks before finishing a change

- `go vet ./...`
- `cd web && npm run typecheck`
- `make build` produces `./app` (this is exactly what CI deploys)

Local development: `make api` and `make web-dev`, then open http://localhost:5173.
```

### `CLAUDE.md`

```markdown
@AGENTS.md
```

### `.gitignore`

```
data/
app
.env.deploy
web/node_modules/
web/dist/*
!web/dist/.gitkeep
```

### `go.mod`

```
module app

go 1.26.0

require modernc.org/sqlite v1.60.1

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.48.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
```

### `main.go`

```go
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
```

### `db.go`

```go
package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure Go, so CGO_ENABLED=0 cross-compiles
)

func openDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// migrations run in order, once each. Only ever append to this list: the
// previous release may still be running against the new schema for a few
// seconds during a deploy, and rolling back doesn't undo a migration.
var migrations = []string{
	`CREATE TABLE notes (
		id INTEGER PRIMARY KEY,
		body TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
	)`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

### `jobs.go`

```go
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"time"
)

// jobs are run by Sideyard on the schedules set in the dashboard's Jobs tab:
// it calls POST /_jobs/<name> with the app's SIDEYARD_TOKEN. To add one,
// write a function here, deploy, then create the job in the dashboard.
// A returned error fails the run; the returned text is shown as its output.
var jobs = map[string]func(ctx context.Context, db *sql.DB) (string, error){
	"prune-notes": pruneNotes,
}

func pruneNotes(ctx context.Context, db *sql.DB) (string, error) {
	cutoff := time.Now().AddDate(0, 0, -90).UTC().Format("2006-01-02T15:04:05Z")
	res, err := db.ExecContext(ctx, `DELETE FROM notes WHERE created_at < ?`, cutoff)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	return fmt.Sprintf("deleted %d notes older than 90 days", n), nil
}

func jobHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("SIDEYARD_TOKEN")
		got := r.Header.Get("Authorization")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		job, ok := jobs[r.PathValue("name")]
		if !ok {
			http.Error(w, "no such job", http.StatusNotFound)
			return
		}
		out, err := job(r.Context(), db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, out)
	}
}
```

### `email.go`

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// sendEmail sends through Sideyard, which holds the email provider's
// credentials: the app authenticates with its SIDEYARD_TOKEN. The sender is
// the one set in the project's Email tab. html may be empty.
func sendEmail(ctx context.Context, to []string, subject, text, html string) error {
	body, err := json.Marshal(map[string]any{"to": to, "subject": subject, "text": text, "html": html})
	if err != nil {
		return err
	}
	url := os.Getenv("SIDEYARD_API_URL") + "/api/projects/" + os.Getenv("SIDEYARD_PROJECT") + "/email/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SIDEYARD_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sending email: %s", bytes.TrimSpace(msg))
	}
	return nil
}
```

### `frontend.go`

```go
package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// frontend serves the built React app. Paths that aren't files get
// index.html, so client-side routes work on reload.
func frontend(dist embed.FS) http.Handler {
	files, _ := fs.Sub(dist, "web/dist")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if _, err := fs.Stat(files, name); name == "" || err != nil {
			name = "index.html"
		}
		if _, err := fs.Stat(files, "index.html"); err != nil {
			http.Error(w, "Frontend not built. For development run `npm run dev` in web/ and open http://localhost:5173.", http.StatusNotFound)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, files, name)
	})
}
```

### `notes.go`

```go
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
```

### `Makefile`

```make
# Local development:   make api   (Go on :3000)   and   make web-dev   (Vite on :5173)
# Deploy from laptop:  put SIDEYARD_URL/PROJECT/TOKEN in .env.deploy, then make deploy

GOARCH ?= amd64

.PHONY: api web-dev web build deploy

api:
	go run .

web-dev:
	cd web && npm run dev

web:
	cd web && npm ci && npm run build

# A Linux binary for the server, with the frontend embedded.
build: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) go build -trimpath -ldflags="-s -w" -o app .

deploy: build
	@set -a; [ -f .env.deploy ] && . ./.env.deploy; set +a; ./scripts/deploy.sh app
```

### `scripts/deploy.sh`

```sh
#!/usr/bin/env sh
# Uploads a built binary to Sideyard and waits for the release to go live.
# Fails (non-zero exit) if the release fails its health check.
# Needs SIDEYARD_URL, SIDEYARD_PROJECT and SIDEYARD_TOKEN.
set -eu

bin="${1:-app}"
: "${SIDEYARD_URL:?set SIDEYARD_URL, e.g. https://sideyard.example.com}"
: "${SIDEYARD_PROJECT:?set SIDEYARD_PROJECT to the Sideyard project name}"
: "${SIDEYARD_TOKEN:?set SIDEYARD_TOKEN to the project deploy token}"

commit="$(git rev-parse HEAD 2>/dev/null || true)"
message="$(git log -1 --pretty=%s 2>/dev/null || true)"

curl --fail-with-body -sS \
  -H "Authorization: Bearer $SIDEYARD_TOKEN" \
  -F "binary=@$bin" \
  --form-string "commit=$commit" \
  --form-string "message=$message" \
  "$SIDEYARD_URL/api/projects/$SIDEYARD_PROJECT/deploys"
echo
```

### `.github/workflows/deploy.yml`

```yaml
# Builds the app into one Linux binary and uploads it to Sideyard.
# Repo setup (once):
#   gh secret set SIDEYARD_TOKEN --body "<deploy token>"
#   gh variable set SIDEYARD_URL --body "https://sideyard.example.com"
#   gh variable set SIDEYARD_PROJECT --body "<project name>"
#   gh variable set SIDEYARD_ARCH --body "arm64"   # only if the server is ARM
name: Deploy to Sideyard

on:
  push:
    branches: [main]
  workflow_dispatch:

concurrency:
  group: deploy
  cancel-in-progress: false

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7

      - uses: actions/setup-node@v7
        with:
          node-version: 24
          cache: npm
          cache-dependency-path: web/package-lock.json

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - name: Build frontend
        working-directory: web
        run: npm ci && npm run build

      - name: Build binary
        run: go build -trimpath -ldflags="-s -w" -o app .
        env:
          CGO_ENABLED: "0"
          GOOS: linux
          GOARCH: ${{ vars.SIDEYARD_ARCH || 'amd64' }}

      - name: Deploy
        run: ./scripts/deploy.sh app
        env:
          SIDEYARD_URL: ${{ vars.SIDEYARD_URL }}
          SIDEYARD_PROJECT: ${{ vars.SIDEYARD_PROJECT }}
          SIDEYARD_TOKEN: ${{ secrets.SIDEYARD_TOKEN }}
```

### `web/package.json`

```json
{
  "name": "app-web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build && touch dist/.gitkeep",
    "typecheck": "tsc --noEmit"
  },
  "dependencies": {
    "react": "^19.3.0",
    "react-dom": "^19.3.0"
  },
  "devDependencies": {
    "@types/react": "^19.3.0",
    "@types/react-dom": "^19.3.0",
    "@vitejs/plugin-react": "^6.1.2",
    "typescript": "^7.0.2",
    "vite": "^8.3.4"
  }
}
```

### `web/tsconfig.json`

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "skipLibCheck": true,
    "noEmit": true,
    "types": ["vite/client"]
  },
  "include": ["src", "vite.config.ts"]
}
```

### `web/vite.config.ts`

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development, run `go run .` (API on :3000) and `npm run dev` here, then
// open http://localhost:5173. Vite forwards /api to the Go server.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/api': 'http://127.0.0.1:3000' },
  },
})
```

### `web/index.html`

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>App</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

### `web/src/main.tsx`

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
```

### `web/src/App.tsx`

```tsx
import { useEffect, useState, type FormEvent } from 'react'

type Note = { id: number; body: string; createdAt: string }

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await res.text())
  return (res.status === 204 ? null : await res.json()) as T
}

export function App() {
  const [notes, setNotes] = useState<Note[]>([])
  const [body, setBody] = useState('')
  const [error, setError] = useState<string | null>(null)

  const load = () => request<Note[]>('GET', '/api/notes').then(setNotes, (e) => setError(String(e)))
  useEffect(() => {
    load()
  }, [])

  async function add(e: FormEvent) {
    e.preventDefault()
    await request('POST', '/api/notes', { body })
    setBody('')
    load()
  }

  async function remove(id: number) {
    await request('DELETE', `/api/notes/${id}`)
    load()
  }

  return (
    <main>
      <h1>Notes</h1>
      <p className="muted">Go + React + SQLite, deployed with Sideyard.</p>
      <form onSubmit={add}>
        <input value={body} onChange={(e) => setBody(e.target.value)} placeholder="Write a note…" autoFocus />
        <button disabled={!body.trim()}>Add</button>
      </form>
      {error && <p className="error">{error}</p>}
      <ul>
        {notes.map((n) => (
          <li key={n.id}>
            <span>{n.body}</span>
            <button className="ghost" onClick={() => remove(n.id)}>
              Delete
            </button>
          </li>
        ))}
      </ul>
    </main>
  )
}
```

### `web/src/styles.css`

```css
:root {
  color-scheme: light dark;
  font: 15px/1.5 -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
}

main {
  max-width: 560px;
  margin: 64px auto;
  padding: 0 20px;
}

h1 {
  margin: 0;
}

.muted {
  opacity: 0.6;
  margin-top: 4px;
}

.error {
  color: #d33;
}

form {
  display: flex;
  gap: 8px;
  margin: 24px 0;
}

input {
  flex: 1;
  font: inherit;
  padding: 8px 10px;
  border: 1px solid #8884;
  border-radius: 6px;
  background: transparent;
  color: inherit;
}

button {
  font: inherit;
  padding: 8px 14px;
  border: 1px solid #8884;
  border-radius: 6px;
  background: transparent;
  color: inherit;
  cursor: pointer;
}

button.ghost {
  border: none;
  opacity: 0.5;
  padding: 4px 8px;
}

ul {
  list-style: none;
  padding: 0;
}

li {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid #8883;
}
```

## What I want to build

(Describe your app here, or delete this section to set up the scaffold only.)
