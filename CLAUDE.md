# Sideyard

Self-hosted PaaS: one Go binary (Caddy embedded) that runs many Go + React + SQLite apps on a VPS. Design, decisions and roadmap: `docs/architecture.md`. The stack restriction is the product: features fit inside Go + React + SQLite + S3.

## Invariants

- **Deploy safety.** A new process must pass its health check before `applyRoutes` sends traffic to it. On failure the old process keeps serving and only a brand-new release is marked failed. `platform.activate` is the single path for deploy, restart, rollback and variable changes; keep it that way.
- **Per-project lock.** Every operation that starts or stops a project's process holds `p.lock(id)`. `applyRoutes` takes no project lock, so it's safe to call while holding one.
- **Whole-table routing.** `applyRoutes` rebuilds the entire Caddy config from the store plus live processes. Change routing by changing that state.
- **Clean app environment.** Apps get only `environment()`'s list. Sideyard's own environment can hold secrets and is never inherited. The systemd runner passes it in a root-only `EnvironmentFile`; unit properties are readable by every local user, so secrets never go there.
- **Two runners, one interface** (`internal/runner`). Platform code stays runner-agnostic: `Systemd` instances are detached (they outlive Sideyard and are adopted on boot from `projects.live_instance`), `Process` instances die with it. Ownership of anything Sideyard writes into a project goes through `runner.Own`. Unit and user names carry the installation's instance ID; anything that lists or cleans up units must stay within its own prefix.
- **SQLite files of an app are touched as the app's user** (`sideyard sqlite-snapshot` and `sqlite-query` under the systemd runner; the table and SQL editors go through `platform.dbHelper`). Opening them as root can leave root-owned `-shm`/`-wal` files the app can't write.
- **Editor SQL is built, never concatenated from input.** Table and column names are checked against the schema and quoted; values are bound parameters; types and defaults pass `AlterSQL`'s validation. Only the SQL Editor runs raw SQL, read-only unless the request says `write`.
- **Background work runs under `p.bg`** and is tracked by `p.bgWG`: the job scheduler, backups and health checks. `Shutdown` cancels and waits for it before stopping instances.
- **`/_jobs/*` is internal.** The proxy 404s it on every app domain; only Sideyard calls it, on the app's loopback port, with the app's `SIDEYARD_TOKEN`. The same token authenticates the app to the email API.
- **Secrets in settings are write-only.** Saved credentials (variables, bucket keys, SMTP password) are sealed with `master.key`, and API responses say only whether one is set.
- **Updates replace the running binary and `exec` it** (`internal/update`, `platform/update.go`, `reexec` in main). The binary must stay 0755 (apps' users run it as the SQLite helper) and live where the unit lets Sideyard write (`/opt/sideyard/bin`; `ProtectSystem` makes `/usr` read-only). `deploy/install.sh` is the only place the systemd unit is defined.
- **One SQLite connection** (`SetMaxOpenConns(1)`). Fully drain a `rows` cursor before calling another store method, or the call deadlocks.
- Caddy redirects Go's standard logger into its JSON log once loaded. Messages meant for a human at startup (the admin password) go straight to stderr.

## Working on it

- `make dev` runs it locally at http://sideyard.localhost:8080; `templates/starter` is the app to deploy against it (`SIDEYARD_URL=http://sideyard.localhost:8080 ./scripts/deploy.sh app` from the starter after `go build -o app .`).
- `make test` before finishing: vet, `go test -race` (end-to-end tests of deploys, the API, storage against an in-memory S3, email against a recording SMTP server, jobs, backups) and the dashboard type check.
- The systemd runner can only be exercised on Linux as root: `make vm-test` builds a kit, and `TestSystemdRunner`'s comment says how to run it on a VM.
- The dashboard is embedded, so rebuild it (`make web`) before building the binary when `web/` changes.
- `site/` is the static landing page. Its two agent prompts (`site/prompts/`) are built by `make site` from `scripts/prompts/*.md` and the files of `templates/starter`, so rebuild them after changing either; `make test` fails when they're stale.
- Releases: `make release VERSION=v1.2.3 SOURCE=<repo URL>` builds `dist/`; pushing a tag runs `.github/workflows/release.yml`. To test the installer or an update, serve `dist/` on the test machine's loopback, laid out like GitHub Releases (`releases/download/<v>/…`, `releases/latest/download/…`), and build with `SOURCE=http://127.0.0.1:<port>`: plain http is refused anywhere else. docs/install.md has the details.
