# Sideyard

A lightweight, self-hosted PaaS that turns one VPS into a home for all your side projects. Push from GitHub, and each app gets its own subdomain with HTTPS, its own SQLite database, encrypted variables, logs and zero-downtime deploys, all managed from one dashboard.

Every app uses the same stack: **Go + React (embedded in the binary) + SQLite**. That's what keeps it light. An app is one binary using tens of MB of RAM, so a 4 GB VM holds plenty of them. No Docker, no Postgres, no Node at runtime.

## How it works

1. Start an app from [`templates/starter`](templates/starter) and build it with Claude Code (or by hand).
2. Create a project in the Sideyard dashboard and add the deploy token to your GitHub repo.
3. Push. CI builds one Linux binary with the frontend embedded and uploads it. Sideyard starts it, waits for `/healthz`, then switches traffic. If the health check fails, the previous release keeps serving.

Sideyard itself is one Go binary with Caddy embedded for routing and certificates. See [docs/architecture.md](docs/architecture.md) for the design and roadmap.

## Install on a server

Point `*.example.com` at a fresh Ubuntu or Debian server, then run:

```sh
curl -fsSL https://github.com/rabeeh-ta/sideyard/releases/latest/download/install.sh | sudo bash
```

It asks for your domain (or offers a free sslip.io name for the server's IP) and an admin password, then starts Sideyard as a systemd service and prints the dashboard's address. New versions show up in the dashboard (**Platform settings → Updates**) and install with one click, or automatically. [docs/install.md](docs/install.md) has the details: what the installer does, unattended installs, settings, backups, uninstalling, and publishing releases (`git tag v0.1.0 && git push origin v0.1.0`).

## Run locally

Needs Go 1.26+ and Node 22+.

```sh
make dev
```

Open http://sideyard.localhost:8080 and sign in with the admin password printed at startup. Projects are served at `http://<name>.localhost:8080`, and data lives in `.sideyard-data/`.

To work on the dashboard with hot reload, keep `make dev` running and start `cd web && npm run dev` (http://localhost:5173).

```sh
make test   # go vet, Go tests with -race, dashboard type check
```
