# Sizing

> **Status: spike tooling ready; measurements pending an operator-triggered run.**
> The spike is heavy (it writes ~10 GB into a Docker volume and takes several minutes), so it is never run automatically.

## Target box

2 vCPU, 2 GB RAM, 50–100 GB disk, Debian 13. Memory budget (compose `mem_limit` / `GOMEMLIMIT`):

| Service | mem_limit | GOMEMLIMIT |
|---|---|---|
| nats | 900m | 600MiB |
| pulsed | 448m | 300MiB |
| banagent | 48m | — |
| alloy | 192m | — |
| init | 128m (one-shot) | — |

A 2 GB swapfile with `swappiness=10` absorbs short spikes (set up by the deployment, not by pulsed).

## The sizing spike

The spike checks that NATS stays within its memory budget with about 10 GiB of events, and measures restart and replay
times. `make spike` (= `scripts/spike.sh`) runs `init` + `nats` from `deploy/spike/compose.yaml` with `mem_limit: 900m`,
`GOMEMLIMIT=600MiB` and 2 CPUs, then `cmd/pulse-spike` in a Go container on the same network:

1. Publishes `TARGET` logical bytes (default 10 GiB) as ~1 KiB incompressible-ish payloads with envelope headers across
   `SUBJECTS` (default 5000) subjects `evt.spike.eNNNN.updated`, sampling NATS RSS from `VARZ` every 0.5 s.
2. Restarts nats and records time until the stream is available and RSS after restart (index rebuild).
3. Measures replay: time to first message and 100k-message throughput by start time at the 10/50/90% marks for `evt.>`,
   the same for a single sparse subject, single-subject replay from the start, and last-by-subject direct get latency.

Outputs: `tmp/spike/results.json`, `tmp/spike/docker-stats.csv` (container memory every 5 s), `tmp/spike/restart.txt`.
Smaller runs: `TARGET=2147483648 SUBJECTS=5000 make spike`. Clean up: `docker compose -f deploy/spike/compose.yaml -p pulse-spike down -v`.

## Results

| Metric | Value |
|---|---|
| Publish throughput | pending |
| NATS RSS peak during publish | pending |
| Restart: stream available after | pending |
| NATS RSS after restart | pending |
| Replay by time, first message (p50 mark) | pending |
| Single sparse subject by time, first message | pending |
| Direct get last-by-subject, avg | pending |

Record the run date, commit, host and the numbers above, and check the NATS memory peaks against its budget (900 MB
with `GOMEMLIMIT=600MiB`).

## Subject cardinality guideline (provisional)

Until the spike numbers exist: keep distinct subjects per stream in the low tens of thousands (the subject index caps at
50k subjects). Put high-cardinality identifiers in the payload or a single trailing `<key>` token, not in several
tokens. Revisit after the spike.
