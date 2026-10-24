# New app for Sideyard

This app will run on **Sideyard**, a self-hosted platform that runs many small apps on one server. Every Sideyard app has the same shape: a Go backend, a React + TypeScript frontend built with Vite, and SQLite for data, compiled together into one Linux binary. Sideyard receives that binary, starts it, waits for its health check and only then sends it traffic. An app that keeps to the contract in `AGENTS.md` below deploys with `git push`. One that doesn't won't run.

Do this in order:

1. **Scaffold.** Create every file under "Files" below in the current directory, exactly as given, plus an empty `web/dist/.gitkeep` (the binary embeds `web/dist`, so it must exist). Make `scripts/deploy.sh` executable. These files are Sideyard's starter template: the parts that talk to Sideyard (environment, health check, graceful shutdown, migrations, jobs, email, the embedded frontend, the deploy workflow) are already right, and a small notes feature shows them working together. You may rename the module in `go.mod`; keep the binary's name, `app`.
2. **Install and check.** Run `go mod tidy` and `cd web && npm install`, then the checks at the end of `AGENTS.md`. Start the API with `make api` and confirm `curl http://127.0.0.1:3000/healthz` prints `ok`.
3. **Build.** Build the app I describe at the end of this message, replacing the notes example: `notes.go`, its routes in `main.go`, its migration in `db.go`, its job in `jobs.go` and `web/src/App.tsx`. Nothing has been deployed yet, so rewrite the notes migration rather than appending one that drops it. If I haven't described an app, stop after step 2, tell me the scaffold is ready and ask what we're building.
4. **Hand over.** When it works locally, tell me how to deploy: create a project in the Sideyard dashboard (and connect a bucket in its Storage tab if the app stores files), set the GitHub secret and variables listed at the top of `.github/workflows/deploy.yml` (`SIDEYARD_ARCH` too if the server is ARM), and push to `main`. If Sideyard's MCP tools are available to you, deploy it yourself instead: `create_project`, then `deploy`, then check it with `get_project` and `get_logs`, and give me its URL.

`AGENTS.md` is the contract and stays in the repo, so every later session follows it too. When a feature seems to need something outside the stack (Postgres, Redis, a queue, a separate worker, a Node server, Docker, cron), build it inside the stack the way `AGENTS.md` describes, and tell me if something truly can't fit.

## Files

@file AGENTS.md

@file CLAUDE.md

@file .gitignore

@file go.mod

@file main.go

@file db.go

@file jobs.go

@file email.go

@file frontend.go

@file notes.go

@file Makefile

@file scripts/deploy.sh

@file .github/workflows/deploy.yml

@file web/package.json

@file web/tsconfig.json

@file web/vite.config.ts

@file web/index.html

@file web/src/main.tsx

@file web/src/App.tsx

@file web/src/styles.css

## What I want to build

(Describe your app here, or delete this section to set up the scaffold only.)
