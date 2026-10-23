#!/usr/bin/env bash
# M2 gate spike runner (PL-TST-006): fills EVENTS with TARGET bytes over SUBJECTS subjects while nats runs under
# mem_limit 900m / 2 CPUs, restarts nats, measures replay. Results: tmp/spike/results.json and tmp/spike/docker-stats.csv.
set -euo pipefail
cd "$(dirname "$0")/.."
TARGET=${TARGET:-10737418240}
SUBJECTS=${SUBJECTS:-5000}
P="docker compose -f deploy/spike/compose.yaml -p pulse-spike"
OUT=$PWD/tmp/spike
mkdir -p "$OUT"; rm -f "$OUT"/pause "$OUT"/results.json
[ -f deploy/.dev/master.key ] || make deploy/.dev/master.key
docker image inspect pulsed:dev >/dev/null 2>&1 || make image IMAGE=pulsed:dev
$P up -d --wait nats
NATS=$($P ps -q nats)

( while true; do
    printf '%s,%s\n' "$(date +%s)" "$(docker stats --no-stream --format '{{.MemUsage}},{{.CPUPerc}}' "$NATS" 2>/dev/null || echo)" >>"$OUT/docker-stats.csv"
    sleep 5
  done ) &
STATS=$!
trap 'kill $STATS 2>/dev/null || true' EXIT

docker run --rm --network pulse-spike_default \
  -v pulse-spike_pulsed-data:/data/pulsed:ro \
  -v "$PWD/deploy/.dev/master.key:/run/secrets/pulse_master_key:ro" \
  -v "$PWD:/src:ro" -v "$OUT:/out" -v pulse-spike-gomod:/go/pkg/mod -v pulse-spike-gocache:/root/.cache/go-build \
  -w /src -e GOFLAGS=-buildvcs=false golang:1.27.3-trixie \
  sh -c 'go build -o /tmp/spike ./cmd/pulse-spike && /tmp/spike -target-bytes '"$TARGET"' -subjects '"$SUBJECTS"' -pause-file /out/pause -marks /out/marks.json -out /out/results.json' &
SPIKE=$!

while [ ! -f "$OUT/pause" ]; do
  kill -0 $SPIKE 2>/dev/null || { echo "spike exited early"; wait $SPIKE; exit 1; }
  sleep 2
done
echo "publish done; restarting nats"
docker exec "$NATS" sh -c 'du -sh /data/nats/js' || true
START=$(date +%s)
$P restart nats
until [ "$(docker inspect -f '{{.State.Health.Status}}' "$NATS")" = healthy ]; do sleep 1; done
echo "nats healthy after $(( $(date +%s) - START ))s" | tee "$OUT/restart.txt"
docker stats --no-stream --format '{{.MemUsage}}' "$NATS" | tee -a "$OUT/restart.txt"
rm -f "$OUT/pause"
wait $SPIKE
echo "results in $OUT/results.json"
