#!/usr/bin/env bash
# Moov Mail — Spike S3: run the bench driver against the moov-s3-pg container.
#
# The Postgres container publishes no ports (the database must not be reachable
# from outside the box), so the driver runs inside the container's network
# namespace.
#
#   ./run-bench.sh needles
#   ./run-bench.sh warm -runs 50
set -euo pipefail

CONTAINER=${CONTAINER:-moov-s3-pg}
MODE=${1:?usage: run-bench.sh MODE [extra bench flags...]}
shift || true

exec docker run --rm --network "container:$CONTAINER" \
  -v /root/moov-s3:/work -w /work \
  -e PGDSN='postgres://postgres:s3bench_throwaway@127.0.0.1:5432/moov_s3' \
  debian:bookworm-slim /work/scripts/bench -mode "$MODE" "$@"
