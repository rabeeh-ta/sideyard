# Stack benchmark

The numbers on the landing page come from here. The same small notes API (list the newest 100 notes, create one, a health check that touches the database) is written the way a coding agent typically writes it on each popular stack, then each one is put under the same rising load while `measure` samples the memory and CPU of every process the stack needs, database server included.

| Stack | App | Database | Served by |
|---|---|---|---|
| `sideyard` | `templates/starter`, unchanged | SQLite (in process) | the Go binary |
| `next` | Next.js route handlers, Prisma 7 | Postgres 16 | `next start` |
| `mern` | Express, Mongoose | MongoDB 9 | `node server.js` |
| `flask` | Flask, Flask-SQLAlchemy | Postgres 16 | gunicorn, 4 workers |
| `django` | Django defaults, `CONN_MAX_AGE=600` | Postgres 16 | gunicorn, 4 workers |
| `fastapi` | FastAPI, SQLAlchemy | Postgres 16 | uvicorn, 4 workers |
| `spring` | Spring Boot from start.spring.io, Spring Data JPA | Postgres 16 | the JRE, sized for a 4 GB server |
| `rails` | `rails new --api`, Active Record | Postgres 16 | Puma, 4 workers of 3 threads |

Every stack uses its framework's defaults for a production run, except that servers which scale by process (gunicorn, uvicorn, Puma) run 4 workers, one per core of the test machine; Node apps run as one process, as they usually do. Runtimes that size themselves from the machine's memory (the JVM, MongoDB's cache) are told the machine has 4 GB, the size of a typical small VPS.

## What `measure` does

1. Creates 10,000 notes through the API.
2. Runs stages of 30 seconds at a fixed request rate: 0 (idle), 25, 50, 100, 200, 400, 800 and 1,600 requests a second. 80% of requests list notes and 20% create one. The load is open-loop, like independent users: requests go out on schedule whether or not earlier ones have finished.
3. Skips the first 10 seconds of each stage, then samples every second:
   - **memory**: the sum of PSS over the process trees of the app and its database server, so pages shared between processes count once
   - **CPU**: CPU-seconds used per second across those processes (1.0 is one core kept busy)
4. Records successful requests per second and the p50, p95 and p99 latency of the successful ones.

It also records the time from starting the app (with its database already running) to its first healthy `/healthz`, and the size on disk of what the server needs: the language runtime, dependencies, the built app and the database server.

## Running it

On a Linux x86_64 machine with `curl`, `python3` and `apt-get` (for Ubuntu's Postgres packages, unpacked, not installed). For the Rails stack, also Ruby 3.2+ with headers, `libpq-dev` and `libyaml-dev`; without Ruby it's skipped.

```sh
# on your machine
(cd templates/starter && make build)        # the starter as ./app, linux/amd64
(cd bench && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o measure-linux ./measure)
rsync -a bench/ host:/scratch/src/bench/
rsync -a templates/starter/app host:/scratch/src/sideyard-app
rsync -a bench/measure-linux host:/scratch/src/measure

# on the test machine
BENCH_DIR=/scratch /scratch/src/bench/bench.sh setup
BENCH_DIR=/scratch /scratch/src/bench/bench.sh run /scratch/results       # or name stacks: run /scratch/results flask next
BENCH_DIR=/scratch /scratch/src/bench/bench.sh clean                      # removes everything under /scratch
```

Downloads and package caches all stay under `BENCH_DIR`. Results are one JSON file per stack plus `versions.txt`.

## Reading the results

The test machine matters. Memory and CPU-seconds per request barely change between machines; latency and the highest rate a stack sustains depend on free cores, so compare those only between stacks run on the same machine at the same time. `hostLoad` in each stage records the machine's load average, so noise from other work shows up.

`publish.py` combines runs: memory and CPU are the mean across runs, while latency and requests served come from the best run of each stage, since other work on a shared machine can only slow a stack down.
