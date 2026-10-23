#!/usr/bin/env bash
# PL-EDGE-006: ACME DNS-01 against pebble + challtestsrv (Docker), install into nats.conf, RELOAD a real nats-server
# and verify the served certificate by SNI. Containers and the network are removed when the test ends.
set -euo pipefail
cd "$(dirname "$0")/.."
go test -tags acme -race -count=1 -v -run TestPebble ./internal/acme/
