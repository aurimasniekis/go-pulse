#!/usr/bin/env bash
# PL-BAN-007: banagent against real nftables, inside a privileged Linux container (its own network namespace).
set -euo pipefail
cd "$(dirname "$0")/.."
docker run --rm --privileged \
  -v "$PWD:/src:ro" -v pulse-gomod:/go/pkg/mod -v pulse-gocache:/root/.cache/go-build \
  -w /src -e GOFLAGS=-buildvcs=false golang:1.27.3-trixie \
  go test -tags nft -count=1 -v -run TestNFT ./internal/banagent/
