# Move this app onto Sideyard

This project is moving to **Sideyard**, a self-hosted platform that runs many small apps on one server. Sideyard runs one kind of app: a Go backend with a React + TypeScript frontend (built with Vite) embedded in it and SQLite for data, compiled into a single Linux binary. It receives that binary, starts it, waits for its health check and only then sends it traffic. Your job is to convert this project into that shape without losing data or behaviour.

The contract is `AGENTS.md` under "Sideyard files" at the end of this message, followed by the Sideyard-facing code from the starter template. Treat both as fixed: adapt this project to them, not the other way round.

Work in four phases. Do phase 1 only, then stop and wait for my approval.

## Phase 1: Audit and plan (change no code)

Read the whole project and write `SIDEYARD_MIGRATION.md` with:

- **Inventory.** The backend's language and framework; the frontend and how it renders (single-page app, server-rendered, static); the database, every table or collection and its rough size; caches; queues and workers; scheduled tasks; file uploads and where they live; email; auth and sessions; WebSockets or streaming; environment variables and secrets; third-party APIs; how it's built and deployed today.
- **Mapping.** What each item becomes on Sideyard, using the table below. Call out anything with no clean equivalent and propose the closest fit.
- **Routes.** Every route the backend serves, as a checklist to tick off while porting.
- **Data plan.** Each table's SQLite schema and type changes, and how its rows and stored files get copied.
- **Order of work, risks, and what you need from me:** credentials, read access to the production database, decisions.

| Today | On Sideyard |
|---|---|
| A Node, Python, Ruby, PHP or Java backend; serverless functions; Next.js API routes or server actions | Go handlers on `http.ServeMux` in the one binary, keeping each route's path, method, status codes and JSON shape so the frontend barely changes |
| A server-rendered frontend (Next.js, Remix, Nuxt, SvelteKit, Rails or Django views) | A Vite + React + TypeScript single-page app in `web/`, embedded in the binary; whatever the server rendered comes from the Go API instead |
| A single-page app that already builds to static files | Move it into `web/` so its build lands in `web/dist`. Create React App moves to Vite. A Vue or Svelte app keeps its framework, since Sideyard only serves the built files; update the Stack line in `AGENTS.md` to say so |
| Postgres, MySQL, MongoDB, or a hosted database (Supabase, PlanetScale, Firebase) | SQLite at `$DATABASE_PATH` through `modernc.org/sqlite` and `database/sql`; the schema is the `migrations` list in `db.go` |
| An ORM (Prisma, TypeORM, Sequelize, ActiveRecord, SQLAlchemy) | Plain SQL through `database/sql` |
| Redis or Memcached as a cache | Memory in the process (a map behind a mutex) or a SQLite table |
| Sessions in Redis or a database | A `sessions` table in SQLite behind an `HttpOnly`, `Secure` cookie |
| Queues and workers (Sidekiq, Celery, BullMQ, SQS, a separate worker process) | A tasks table in SQLite worked through by a goroutine started in `main`, or a Sideyard job if the work runs on a schedule |
| Cron, scheduled functions, timers that run tasks | A function in the `jobs` map in `jobs.go`; Sideyard calls `POST /_jobs/<name>` on the schedule set in the dashboard's Jobs tab |
| Files on local disk, S3, Cloudinary, Supabase or Firebase Storage | The project's S3-compatible bucket (`S3_BUCKET` and the `AWS_*` variables, read by the AWS SDK for Go v2), with metadata in SQLite |
| An email SDK or SMTP (SendGrid, Resend, SES, Postmark, nodemailer) | `sendEmail` in `email.go`; Sideyard holds the provider's credentials and the sender address |
| WebSockets, server-sent events | Keep them, served by the Go server |
| Hosted auth (Auth0, Clerk, Supabase Auth), payments, LLM APIs, analytics and other HTTP APIs | Keep them; call them from Go or the browser, with keys in the project's Variables tab |
| `.env` files and the old host's secrets | The project's Variables tab in the Sideyard dashboard, read with `os.Getenv` |
| Dockerfile, docker-compose, Procfile, PM2, the old host's config | Removed once the binary deploys; the binary is the whole deployable |

## Phase 2: Restructure (after I approve the plan)

- Work on a new branch. Leave the current app and its database untouched: they keep serving until the cutover in phase 4.
- Add the Sideyard files below at the repo root: `AGENTS.md`, `CLAUDE.md`, `go.mod` (rename the module if you like; keep the binary's name, `app`), `main.go`, `db.go`, `jobs.go`, `email.go`, `frontend.go`, `Makefile`, `scripts/deploy.sh` (executable), `.github/workflows/deploy.yml` and the `.gitignore` entries. If the project already has agent instructions, keep what's specific to it in a section after the contract.
- The frontend lives in `web/` with the scripts in `web/package.json` (`build`, `typecheck`) and builds into `web/dist`, which the binary embeds; keep an empty `web/dist/.gitkeep`. In development Vite forwards `/api` to the Go server, as in `web/vite.config.ts`. The frontend calls relative paths on the same origin, so CORS settings and API base URLs go away.
- The starter's notes example is a placeholder: register this project's routes in `main.go`, its jobs in `jobs.go` and its schema in `db.go` instead.
- Port the backend route by route, ticking off the checklist and keeping request and response shapes.
- Write the current schema as the first migrations; nothing has been deployed to Sideyard yet. SQLite types:
  - auto-increment ids → `INTEGER PRIMARY KEY`; UUIDs → `TEXT`
  - timestamps → `TEXT` in UTC ISO 8601, such as `2026-01-31T09:30:00Z`
  - booleans → `INTEGER` 0 or 1
  - JSON and JSONB → `TEXT`, queried with SQLite's JSON functions; arrays → JSON text or a join table
  - enums → `TEXT` with a `CHECK` constraint
  - money and decimals → `INTEGER` in minor units, such as cents
  - full-text search → an FTS5 virtual table

  Keep the old database's indexes, unique constraints and foreign keys.
- Delete what the mapping replaces only after its replacement works, and list each deletion in `SIDEYARD_MIGRATION.md`.

## Phase 3: Copy the data

- Write a re-runnable exporter (a Go program under `cmd/`, or a script) that reads the current database **read-only** and builds a fresh SQLite file. It opens the file with the app's `openDB`, so the migrations create the schema and set its version, then inserts every row in foreign-key order, keeping primary keys.
- Check the result and record the numbers in `SIDEYARD_MIGRATION.md`: row counts match per table, sampled rows match, `PRAGMA integrity_check` returns `ok` and `PRAGMA foreign_key_check` returns nothing.
- Copy stored files into the bucket under the keys the new code expects (with `aws s3 sync` or `rclone`, say) and compare object counts. Never delete or change the originals.

## Phase 4: Verify and hand over

- Run the checks at the end of `AGENTS.md`. Then run the app locally against a copy of the migrated database (`make web`, then `DATABASE_PATH=<copy> go run .`), confirm `/healthz` answers `ok` and go through each main flow.
- Finish `SIDEYARD_MIGRATION.md` with the cutover steps for me:
  1. In the Sideyard dashboard, create the project, add the variables, connect a bucket in the Storage tab if the app stores files, choose the email sender if it sends email, and create each job with its schedule (list them, with the schedules the old cron used).
  2. Import the migrated SQLite file in the project's Database tab. An import before the first deploy seeds the project.
  3. Set the GitHub secret and variables listed at the top of `.github/workflows/deploy.yml` and push to `main`. The workflow fails if the release fails its health check.
  4. Test the app at its Sideyard address. To switch over, stop writes on the old app, re-run the exporter, import the fresh file (Sideyard keeps the database it replaces in `.sideyard/backups`), copy any new files, then add the custom domain to the project and point its DNS at the server.
  5. Rollback: point DNS back at the old host. Its code and data were never changed.

Stop when phase 4 is done. Don't merge, deploy or change DNS yourself.

## Sideyard files

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

User uploads and large files go in the project's bucket, with small metadata in SQLite. Connecting a bucket in the dashboard's Storage tab sets `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `S3_BUCKET`; the AWS SDK for Go v2 reads the `AWS_` ones by itself.

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
