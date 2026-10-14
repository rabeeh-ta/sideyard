# Sideyard starter

A Go + React + SQLite app that deploys to [Sideyard](../../README.md) as a single binary. It ships with a small notes app to show the pieces working together; replace it with your own.

## Start a new app from this template

```sh
cp -r templates/starter ~/code/my-app && cd ~/code/my-app
sed -i '' 's/^module app$/module my-app/' go.mod   # optional
cd web && npm install && cd ..
git init && git add -A && git commit -m "Start from Sideyard starter"
```

## Develop

```sh
make api       # Go server on :3000, data in ./data
make web-dev   # Vite on :5173, forwards /api to :3000
```

Open http://localhost:5173.

## Deploy

1. Create a project in the Sideyard dashboard. It shows the deploy token once.
2. In this repo, set the GitHub secret and variables the dashboard lists (`SIDEYARD_TOKEN`, `SIDEYARD_URL`, `SIDEYARD_PROJECT`, plus `SIDEYARD_ARCH=arm64` if your server is ARM).
3. Push to `main`. `.github/workflows/deploy.yml` builds the frontend, compiles a Linux binary with it embedded, and uploads it. The workflow fails if the release fails its health check, and the previous release keeps serving.

To deploy from your laptop instead, put the same three values in `.env.deploy` and run `make deploy`.

## What Sideyard gives the app

| Variable | Meaning |
|---|---|
| `PORT` | Listen on `127.0.0.1:$PORT` |
| `DATA_DIR` | Persistent folder; survives deploys |
| `DATABASE_PATH` | The SQLite file, `$DATA_DIR/app.db` |
| `SIDEYARD_PROJECT`, `SIDEYARD_RELEASE` | Project name and release number |
| `SIDEYARD_TOKEN`, `SIDEYARD_API_URL` | Authenticates job calls and the email API (`jobs.go`, `email.go`) |
| `AWS_*`, `S3_BUCKET` | Only when a bucket is connected in the Storage tab |

Everything else comes from the project's Variables tab. `AGENTS.md` has the full contract.
