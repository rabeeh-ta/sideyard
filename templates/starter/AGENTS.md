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
