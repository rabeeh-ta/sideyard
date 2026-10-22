#!/usr/bin/env python3
"""Averages one or more `bench.sh run` result directories into the landing
page's data file:

    python3 bench/publish.py "<machine and date>" results/run1 results/run2 > site/data/bench.json
"""
import json
import pathlib
import statistics
import sys

STACKS = ["sideyard", "next", "mern", "flask", "django", "fastapi", "spring", "rails"]
# Memory and CPU are averaged across runs. Latency and requests served come from
# the best run of each stage: other work on a shared machine can only slow a
# stack down, so the best run is closest to what the stack can do.
MEAN = ["memMB", "cpu"]
BEST = ["sent", "ok", "failed", "throughput", "p50ms", "p95ms", "p99ms"]


def mean(values, digits):
    return round(statistics.fmean(values), digits)


def main():
    machine, dirs = sys.argv[1], [pathlib.Path(d) for d in sys.argv[2:]]
    stacks = []
    for key in STACKS:
        runs = [json.loads((d / f"{key}.json").read_text()) for d in dirs if (d / f"{key}.json").exists()]
        if not runs:
            continue
        stages = []
        for i, first in enumerate(runs[0]["stages"]):
            same = [r["stages"][i] for r in runs]
            stage = {"rate": first["rate"]}
            best = max(same, key=lambda s: (s["ok"] / s["sent"] if s["sent"] else 1, -s["p95ms"]))
            for f in BEST:
                stage[f] = round(best[f], 3)
            for f in MEAN:
                stage[f] = mean([s[f] for s in same], 3)
            stage["parts"] = {
                part: {m: mean([s["parts"][part][m] for s in same], 3) for m in ("memMB", "cpu")}
                for part in first["parts"]
            }
            stages.append(stage)
        stacks.append(
            {
                "key": key,
                "name": runs[0]["name"],
                "runs": len(runs),
                "startupMs": mean([r["startupMs"] for r in runs], 0),
                "diskMB": mean([r["diskMB"] for r in runs], 1),
                "stages": stages,
            }
        )
    runs = max(s["runs"] for s in stacks)
    method = (
        f"{machine}. Memory and CPU are the mean of {runs} runs; response time and requests served are the best run, "
        "since other work on the machine can only slow a stack down. Every stack ran alone with its framework's "
        "production defaults (4 workers where the server scales by process), and the JVM and MongoDB were sized for "
        "a 4 GB server. The apps, the load tool and how to rerun it are in bench/ in the repository."
    )
    json.dump({"method": method, "stacks": stacks}, sys.stdout, indent=1)
    sys.stdout.write("\n")


main()
