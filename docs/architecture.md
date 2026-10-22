# Architecture

Sideyard turns one VPS into a home for many small apps. Every app uses the same stack: Go, React embedded in the Go binary, SQLite, and S3-compatible storage for files. Because the stack is fixed and light, one small server runs many apps, and the dashboard can understand each app (its database, jobs and storage) rather than just running a container.

**One server. One dashboard. One standardized stack. All your side projects in one place.**

## Decisions

| Decision | Why |
|---|---|
| Apps are built in CI or on a laptop, never on the server | Node and the Go toolchain never touch the VPS, and builds can't starve running apps. The server only ever receives a binary. |
| One Linux binary per release, frontend embedded with `go:embed` | A deploy is one file upload. Rollback means starting an older file. |
| Pure-Go SQLite (`modernc.org/sqlite`) in apps | `CGO_ENABLED=0 GOOS=linux` cross-compiles from any machine. |
| Caddy embedded as a Go library | The platform is one binary. Caddy handles certificates, renewals and graceful config swaps. WebSockets stay open through config swaps (`stream_close_delay`), so one app's deploy doesn't disconnect another app's clients. |
| Each release is a transient systemd unit, run as the project's own Unix user | Isolation, memory and CPU limits, and crash restarts come from the kernel and systemd instead of a container runtime. Apps outlive Sideyard, so upgrading Sideyard doesn't restart them. |
| Each project brings its own S3 bucket | The user pastes bucket credentials per project. A key scoped to one bucket makes the provider enforce isolation, so Sideyard doesn't have to. |
| Jobs are HTTP calls from Sideyard to the app | One scheduler means no duplicate runs, and apps need no SDK: `POST /_jobs/<name>`. |
| Backups are scheduled snapshots, not continuous replication | They reuse the consistent-snapshot path the Database tab already has and add no process per app. The cost is losing up to one interval of data if the server dies. |
| Email goes through Sideyard's API, over SMTP to any provider | Provider credentials stay in Sideyard; apps send with their own token and each project gets a delivery log. SMTP covers every major provider with one integration. |
| Fixed stack, permanently | Postgres, MySQL, Redis, queues, Kubernetes, mandatory Docker, other runtimes, autoscaling and GPUs are out of scope by design. New capabilities fit inside Go + React + SQLite + S3. |

## How a deploy works

```
CI or laptop                             Sideyard on the VPS
────────────                             ───────────────────
npm run build → web/dist
go build (embeds dist) → ./app
POST /api/projects/<name>/deploys ──→    save releases/<n>/app, check it's a linux/<arch> executable
  (Bearer deploy token)                  start it on a free port with the project's environment
                                         poll http://127.0.0.1:<port><health path> (60 s limit)
                                         healthy → point the proxy at the new port, stop the old process
                                         unhealthy → stop the new process; the old one keeps serving
                              ←──        200 with the release log, or 422 if it failed
```

Restarts, rollbacks and variable changes use the same path, so none of them cause downtime. A test drives this lifecycle end to end: `internal/platform/platform_test.go`.

## Runtime contract

Each release runs with a constructed environment; Sideyard's own environment is never inherited. On a Linux server the systemd runner starts it as unit `sy-<instance>-<project>-r<n>-<id>.service` in `sideyard.slice`, as user `sy<instance>-<project id>-<name>`, with:

- `ProtectSystem=strict` and write access only to `DATA_DIR`. Other projects' data dirs and Sideyard's files aren't readable either: they belong to other users or to root.
- `MemoryMax` (512 MB by default) and an optional `CPUQuota`, set per project in Settings.
- `Restart=on-failure` every 3 seconds.
- The environment in a root-only `EnvironmentFile`, never as unit properties, which any local user can read over D-Bus.
- Output in journald, tagged `SIDEYARD_INSTANCE`, `SIDEYARD_PROJECT_ID` and `SIDEYARD_RELEASE`; the Logs tab matches the instance too, since project IDs repeat across installations on one machine. Its search runs in journald (`journalctl --grep`, as a literal, ignoring case), so it covers the whole journal rather than the lines on screen; under the process runner it reads `app.log` and its rotated copy.

Sideyard records each project's live unit and port in its database. On boot it reattaches to units that are still running, starts the rest, and stops any of its own units it doesn't recognise (left over from a crash mid-deploy). `<instance>` is a short random ID created on first boot and kept in `sideyard.db`, so two Sideyards on one machine (a test next to a real one, say) never touch each other's apps or users. In dev mode, and on hosts without systemd, the process runner runs apps as child processes of Sideyard instead, logging to `logs/app.log`, without limits.

| Variable | Value |
|---|---|
| `PORT` | Listen on `127.0.0.1:$PORT` |
| `DATA_DIR` | `projects/<name>/data`: persistent, also the working directory |
| `DATABASE_PATH` | `$DATA_DIR/app.db` |
| `SIDEYARD_PROJECT`, `SIDEYARD_RELEASE` | Project name and release number |
| `SIDEYARD_TOKEN` | Per-project secret. Sideyard sends it with job calls; the app sends it to the email API |
| `SIDEYARD_API_URL` | Sideyard's API on loopback |
| `AWS_ENDPOINT_URL_S3`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `S3_BUCKET` | Only while a bucket is connected in the Storage tab |
| everything in Variables | Encrypted at rest with AES-256-GCM (`master.key`) |

The app must answer its health check path (default `/healthz`) and exit cleanly on SIGTERM within 10 seconds. The start arguments setting passes flags with `$VARS` expanded, which is how PocketBase runs (`serve --http=127.0.0.1:$PORT --dir=$DATA_DIR/pb_data`, health path `/api/health`).

## Data layout

```
/var/lib/sideyard/
├── sideyard.db              platform database (never project data)
├── master.key               decrypts variables
├── caddy/                   certificates
└── projects/<name>/
    ├── data/                DATA_DIR, owned by the project's user: app.db and anything else it keeps
    │   └── .sideyard/backups/   databases replaced by imports (the last 10)
    ├── releases/<n>/app     the last 5 binaries, root-owned, readable by the project's group
    ├── run/<unit>.env       each running unit's environment, root-only (systemd runner)
    └── logs/app.log         process runner only; the systemd runner logs to journald
```

## Databases

The dashboard finds SQLite files in `DATA_DIR`. Every operation on one runs in `sideyard sqlite-query` as the project's user under the systemd runner, so SQLite's `-shm` and `-wal` files stay owned by the app; the platform and the helper speak `store.HelperRequest`/`HelperResponse`.

- **Table Editor.** Schema from `pragma_table_list` and `pragma_table_xinfo`: columns, types, keys, defaults, generated columns, indexes, row counts. Rows come a page at a time with server-side sort and filters, each with its key: the rowid, or the primary key for a `WITHOUT ROWID` table. Values are selected as `+col`, which drops the declared type so the driver returns what's stored (DATETIME text stays text). Blobs travel as their size; integers beyond JavaScript's range as strings. Edits are parameterised `INSERT`/`UPDATE`/`DELETE` against that key, with values bound as text so SQLite's column affinity decides their type. Structure changes (create, rename and drop tables and columns, add columns) are built from validated names, types and literal defaults. Every write is logged to the project's log.
- **SQL Editor.** Runs a script and returns the last statement's rows (up to 1,000). It runs with `query_only` first; a script that would write comes back as `needsWrite` with nothing changed, and the dashboard asks before running it again with writes allowed. Statements run in order without a wrapping transaction. Saved queries live in the browser.

- **Download** makes a snapshot with `VACUUM INTO`, which is consistent while the app keeps writing. Under the systemd runner, `sideyard sqlite-snapshot` runs as the project's user, so SQLite's `-shm` and `-wal` files never end up root-owned.
- **Import** checks the upload with `PRAGMA quick_check`, stops the app, moves the current file (with its WAL) to `.sideyard/backups/`, puts the upload in place and restarts the app. If the app then fails its health check, the old file goes back. Importing before the first deploy seeds a new project, which is how an existing app's data moves in.

## Storage, jobs, email, backups

- **Storage tab.** Connecting a bucket checks the credentials by listing it, refuses if variables already use the names it sets, and restarts the app with the new variables. The browser lists one "folder" at a time; downloads redirect to a presigned URL, so files don't pass through Sideyard. Usage is counted on demand (up to 100,000 objects).
- **Jobs.** The scheduler wakes every 10 seconds and, per job, finds the latest scheduled time since its last run (or since the job was created or its schedule changed). Several missed slots collapse into one catch-up run, looking back at most a week. A run is skipped if the previous one is still going. Each call has a timeout and optional retries with backoff; runs and their output (first 64 KB of the response) are kept, the last 100 per job. The proxy answers 404 for `/_jobs/*` on every app domain, so only Sideyard, calling the app's loopback port, can trigger them.
- **Email.** Settings hold one SMTP server, a default sender and the sender domains verified with the provider. Projects pick a sender on those domains. `POST /api/projects/<name>/email/send` with `Authorization: Bearer $SIDEYARD_TOKEN` sends; at most 50 recipients and 60 emails a minute per project. The last 500 attempts per project are logged.
- **Backups.** When a backup bucket is enabled, every few hours (6 by default) Sideyard snapshots `sideyard.db` and every SQLite file in every `DATA_DIR`, gzips them and uploads them to `<folder>/sideyard/sideyard.db/<UTC time>.db.gz` and `<folder>/projects/<name>/<path>/<UTC time>.db.gz`. Copies older than the retention (14 days by default) are deleted, except the newest three of each database. Saving the settings writes and deletes a test object first, so a key that can only read is caught. The Database tab lists a database's copies and restores one through the import path. `master.key` is never uploaded; Platform settings offers it as a download behind the password.
- **Monitoring.** Sideyard samples the server and every app every 15 seconds and keeps a day of samples in memory (a restart starts afresh); charts ask for a range and get at most 240 points, averaged. Server figures come from `/proc` and `/sys` (`internal/sysinfo`): CPU split into busy, I/O wait and steal (time the hypervisor gave away, the sign of a noisy neighbour), load, memory and swap, disk I/O summed over whole disks, network summed over interfaces backed by a device, and every disk-backed filesystem. The System page shows them with each app's share, Sideyard's own process, and what the machine is (OS, kernel, CPU, the hypervisor's product name). CPU per app comes from samples of its CPU time (systemd's `CPUUsageNSec`), memory from its cgroup. Every 30 seconds Sideyard requests each live app's health path and logs when an app turns unhealthy or recovers; the dashboard shows it. Domains show whether DNS resolves to this server (compared with the dashboard host's addresses, the machine's interfaces and `SIDEYARD_PUBLIC_IP`) and the certificate Caddy holds for them.

## Install and updates

`deploy/install.sh` is the install path: published with each release, it installs the binary as `/opt/sideyard/bin/sideyard` and writes the systemd unit and `/etc/sideyard/sideyard.env`. The binary lives in `/opt` because the unit's `ProtectSystem=true` makes `/usr` read-only to Sideyard, and an update replaces the binary in place.

A release (`scripts/release.sh`, run by the tag workflow) stamps the version and its source into the binary and publishes `release.json` with each binary's size and SHA-256, laid out like GitHub Releases. Sideyard checks the source every six hours. Installing an update downloads the binary next to the current one, checks size, checksum and that it runs and reports the promised version, keeps the old one as `.prev`, and renames the new one over it. It then takes every project's lock, so no deploy is half done, and main shuts down gracefully and `exec`s the new binary: same PID, so systemd sees no restart, and under the systemd runner the apps' units are adopted as on any boot. The binary is 0755 whatever the umask, since apps' users run it as the SQLite helper.

Before the `exec`, the new binary is marked pending next to itself. Each start of it counts an attempt and a successful boot clears the mark; if systemd has restarted it three times without a boot, the next start puts `.prev` back and `exec`s that, records the rollback for the dashboard, and automatic updates skip the version. The installer does the same for upgrades it runs: if the new version doesn't stay up, it restores the old binary.

Integrity rests on HTTPS to the source (plain http is refused except on loopback, for testing) and the checksums published beside the binaries; releases aren't signed separately. The release workflow builds with a read-only token and publishes from a separate job.

## Code map

| Package | Role |
|---|---|
| `cmd/sideyard` | Entry point: serving (and `exec`ing into an update), `help`, `status`, `version`, `set-password`, `reset-password`, and the `sqlite-snapshot`/`sqlite-query` helpers |
| `internal/update` | Finds releases at the update source and installs one over the running binary |
| `internal/sysinfo` | Server and process figures from `/proc` and `/sys` |
| `internal/version` | The build's version and release source, set at release time |
| `internal/platform` | The orchestrator: deploy, rollback, restart, stop, variables, domains, routing, storage, jobs, backups, email, monitoring, updates |
| `internal/s3` | The slice of S3 Sideyard uses (minio-go); `s3test` is an in-memory server for tests |
| `internal/mail` | SMTP sending and MIME; `mailtest` is a recording SMTP server for tests |
| `internal/runner` | Runs releases: `Systemd` (units, users, limits, journald) or `Process` (child processes, file log) |
| `internal/proxy` | Builds the Caddy config from a route table and loads it |
| `internal/store` | Platform SQLite database; `editor.go` and `sqlitefile.go` work on apps' SQLite files for the dashboard |
| `internal/api` | JSON API, auth, deploy upload, embedded dashboard |
| `web/` | React dashboard |
| `templates/starter` | The app template, with CI workflow and agent guide |
| `site/` | Static landing page; its agent prompts embed the starter (`scripts/prompts.sh`) |
| `bench/` | Measures the starter against the same app on other stacks, for the landing page (its own Go module) |

## Roadmap

Everything in the original spec is built: deploys, domains and HTTPS, variables, logs, the systemd runner, the Database page with import, download and S3 restore, a Table Editor and SQL Editor that can edit data and structure, the Storage tab, jobs, email, backups, monitoring with a System page, a one-command installer, and updates from the dashboard.

Ideas for later, none started: signed releases, point-in-time recovery (continuous WAL shipping) for apps that can't lose an interval of data, more than one admin user, and alerts (email when an app turns unhealthy or a backup fails).

## Known limits

- Migrations don't roll back. Rolling back the binary leaves the schema as the newer release left it, so app migrations must stay backward compatible for one release (the starter's `AGENTS.md` spells this out).
- An import stops the app for a few seconds. A bad import is undone only after the health check times out (60 seconds).
- Backups lose up to one interval of data (6 hours by default) if the server dies.
- Single admin user, password only. No alerts yet: health and backup failures show in the dashboard and logs only.
- The process runner (dev mode) saves limits but doesn't enforce them, and its memory figures are Linux-only.
