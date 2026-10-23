# Move this app onto Sideyard

This project is moving to **Sideyard**, a self-hosted platform that runs many small apps on one server. Sideyard runs one kind of app: a Go backend with a React + TypeScript frontend (built with Vite) embedded in it and SQLite for data, compiled into a single Linux binary. It receives that binary, starts it, waits for its health check and only then sends it traffic. Your job is to convert this project into that shape without losing data or behaviour.

The contract is `AGENTS.md` under "Sideyard files" at the end of this message, followed by the Sideyard-facing code from the starter template. Treat both as fixed: adapt this project to them, not the other way round.

Work in four phases. Do phase 1 only, then stop and wait for my approval.

## Phase 1: Audit and plan (change no code)

Read the whole project and write `SIDEYARD_MIGRATION.md` with:

- **Fit.** Start by saying plainly whether this project fits one Sideyard app: one process on one server, 512 MB of memory unless raised, and SQLite as its database (one writer at a time; comfortable at hundreds of writes a second and tens of GB). In a monorepo, say which app moves. If parts don't fit, such as a large analytics or event pipeline, traffic that needs several servers or regions, or services that must stay in another language, name them and propose a way through: keep them as external services the Go app calls, cut them, or don't migrate. If the project doesn't fit, write that report instead of a plan and stop.
- **Inventory.** The backend's language and framework; the frontend and how it renders (single-page app, server-rendered, static); the database, every table or collection and its rough size; caches; queues and workers; scheduled tasks; file uploads and where they live; email; auth and sessions; WebSockets or streaming; environment variables and secrets; third-party APIs; how it's built and deployed today.
- **Mapping.** What each item becomes on Sideyard, using the table below. Call out anything with no clean equivalent and propose the closest fit.
- **Routes.** Every route the backend serves, as a checklist to tick off while porting.
- **Data plan.** Each table's SQLite schema and type changes, and how its rows and stored files get copied.
- **What changes for users.** Anything that can't behave exactly as before: a page that was rendered on the server and now renders in the browser, accounts that can't be exported from a hosted auth service, a feature that's cut. I approve these with the plan.
- **Order of work, risks, and what you need from me:** credentials, read access to the production database, decisions.

| Today | On Sideyard |
|---|---|
| A Node, Python, Ruby, PHP or Java backend; serverless functions; Next.js API routes or server actions | Go handlers on `http.ServeMux` in the one binary, keeping each route's path, method, status codes and JSON shape so the frontend barely changes |
| A server-rendered frontend (Next.js, Remix, Nuxt, SvelteKit, Rails or Django views) | A Vite + React + TypeScript single-page app in `web/`, embedded in the binary; whatever the server rendered comes from the Go API instead |
| Pages that must be right before JavaScript runs: SEO, link previews (Open Graph tags, generated social images), `robots.txt`, sitemaps | Go serves them: it fills in `index.html`'s title and meta tags for those routes, and serves generated images as static files or draws them in Go |
| Redirects, rewrites and middleware (Next.js middleware, `vercel.json`, `_redirects`) | Go handlers and middleware registered before the frontend, returning the same status codes |
| A single-page app that already builds to static files | Move it into `web/` so its build lands in `web/dist`. Create React App moves to Vite. A Vue or Svelte app keeps its framework, since Sideyard only serves the built files; update the Stack line in `AGENTS.md` to say so |
| Postgres, MySQL, MongoDB, or a hosted database (Supabase, PlanetScale, Firebase) | SQLite at `$DATABASE_PATH` through `modernc.org/sqlite` and `database/sql`; the schema is the `migrations` list in `db.go` |
| The browser reading and writing the database directly (Firebase or Supabase client SDKs, guarded by security rules) | Go API routes that enforce the same rules, which the browser calls instead |
| Realtime subscriptions (Firestore listeners, Supabase Realtime, Pusher) | Server-sent events or WebSockets from the Go server, or polling where a few seconds' delay is fine |
| An ORM (Prisma, TypeORM, Sequelize, ActiveRecord, SQLAlchemy) | Plain SQL through `database/sql` |
| Redis or Memcached as a cache | Memory in the process (a map behind a mutex) or a SQLite table |
| Sessions in Redis or a database | A `sessions` table in SQLite behind an `HttpOnly`, `Secure` cookie |
| Queues and workers (Sidekiq, Celery, BullMQ, SQS, a separate worker process) | A tasks table in SQLite worked through by a goroutine started in `main`, or a Sideyard job if the work runs on a schedule |
| Cron, scheduled functions, timers that run tasks | A function in the `jobs` map in `jobs.go`; Sideyard calls `POST /_jobs/<name>` on the schedule set in the dashboard's Jobs tab |
| Files on local disk, S3, Cloudinary, Supabase or Firebase Storage | The project's S3-compatible bucket (`S3_BUCKET` and the `AWS_*` variables, read by the AWS SDK for Go v2), with metadata in SQLite |
| An email SDK or SMTP (SendGrid, Resend, SES, Postmark, nodemailer) | `sendEmail` in `email.go`; Sideyard holds the provider's credentials and the sender address |
| WebSockets, server-sent events | Keep them, served by the Go server |
| Hosted auth (Auth0, Clerk, Firebase Auth, Supabase Auth), payments, LLM APIs, analytics and other HTTP APIs | Keep them; call them from Go or the browser, with keys in the project's Variables tab. Go checks a hosted auth service's tokens against its published signing keys |
| Accounts with passwords in your own database | A `users` table in SQLite. Password hashes move as they are when Go can check that format (bcrypt, argon2, scrypt); otherwise plan a password reset |
| `.env` files and the old host's secrets | The project's Variables tab in the Sideyard dashboard, read with `os.Getenv` |
| Dockerfile, docker-compose, Procfile, PM2, the old host's config | Removed once the binary deploys; the binary is the whole deployable |

## Phase 2: Restructure (after I approve the plan)

- Work on a new branch, `sideyard`. Leave the current app and its database untouched: they keep serving until the cutover in phase 4.
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
- If you can't reach the production data yet, build and test the exporter against a sample you create, and list what you need from me to run it for real.

## Phase 4: Verify and hand over

- Run the checks at the end of `AGENTS.md`. Then run the app locally against a copy of the migrated database (`make web`, then `DATABASE_PATH=<copy> go run .`), confirm `/healthz` answers `ok` and go through each main flow.
- Finish `SIDEYARD_MIGRATION.md` with the cutover steps for me:
  1. In the Sideyard dashboard, create the project, add the variables, connect a bucket in the Storage tab if the app stores files, choose the email sender if it sends email, and create each job with its schedule (list them, with the schedules the old cron used).
  2. In the project's Database tab, use **Add a database** to upload the migrated SQLite file as `app.db`. An import before the first deploy seeds the project.
  3. Set the GitHub secret and variables listed at the top of `.github/workflows/deploy.yml` (`SIDEYARD_ARCH` too if the server is ARM). Deploy from the `sideyard` branch first by setting the workflow's `on.push.branches` to it, so `main` stays as it is while you test. If the old host builds `main` on every push (Vercel, Netlify, Cloudflare, Render), note that its builds will fail after the merge, and disconnect it at cutover. The workflow fails if the release fails its health check.
  4. Test the app at its Sideyard address. To switch over, stop writes on the old app, re-run the exporter, replace `app.db` with the fresh file in the Database tab (Sideyard keeps the one it replaces as a previous version), copy any new files, then add the custom domain to the project and point its DNS at the server: an `A` record, DNS only (not proxied), since Sideyard gets the certificate itself. Then merge `sideyard` into `main` and point the workflow back at `main`.
  5. Rollback: point DNS back at the old host. Its code and data were never changed.

Stop when phase 4 is done. Don't merge, deploy or change DNS yourself.

## Sideyard files

@file AGENTS.md

@file CLAUDE.md

@file .gitignore

@file go.mod

@file main.go

@file db.go

@file jobs.go

@file email.go

@file frontend.go

@file Makefile

@file scripts/deploy.sh

@file .github/workflows/deploy.yml

@file web/package.json

@file web/vite.config.ts
