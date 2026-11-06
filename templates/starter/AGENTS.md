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

Send with `sendEmail` in `email.go`. It posts to Sideyard's API with the app's token (`sideyardAPI` in `sideyard.go`); Sideyard holds the provider's credentials and the sender address, and logs every email in the project's Email tab.

## Sign-in

People sign in with Google or GitHub through Sideyard, which holds the OAuth clients (set up once on the dashboard's OAuth page, shared by every app on the server). `auth.go` has the two halves: route `GET /auth/{provider}` to `startSignIn` and link "Sign in with Google" to `/auth/google` (or `/auth/github`); in `GET /auth/callback`, `finishSignIn` returns who signed in (`Provider`, `ID`, `Email`, `EmailVerified`, `Name`, `AvatarURL`) or an error to show with a link to try again. The app owns users and sessions: a `users` table keyed by `(provider, provider_id)`, never by email alone (it can change, and GitHub's can be empty), and a `sessions` table holding a random token behind an `HttpOnly`, `Secure`, `SameSite=Lax` cookie. Link a second provider to an existing user only when both emails are verified and equal. The callback must stay at `/auth/callback` on the app's own domain: Sideyard refuses to send the browser anywhere else. Sign-in works only on Sideyard, since it needs `SIDEYARD_API_URL`; in local development, fake the signed-in user behind a flag rather than calling Google. Passwords, magic links and other providers are still the app's own code, with keys in Variables.

## Files and uploads

User uploads and large files go in the project's bucket, with small metadata in SQLite. Connecting a bucket in the dashboard's Storage tab sets `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `S3_BUCKET`; the AWS SDK for Go v2 reads the `AWS_` ones by itself. The bucket may be any S3-compatible service, so give the S3 client `UsePathStyle` when the endpoint isn't AWS's, and `RequestChecksumCalculation` and `ResponseChecksumValidation` set to `WhenRequired` (newer SDKs send checksums some of these services reject). Stream uploads to the bucket with the SDK's upload manager rather than reading them into memory, and serve files through the app or with presigned URLs; the bucket itself stays private.

## Checks before finishing a change

- `go vet ./...`
- `cd web && npm run typecheck`
- `make build` produces `./app` (this is exactly what CI deploys)

Local development: `make api` and `make web-dev`, then open http://localhost:5173.
