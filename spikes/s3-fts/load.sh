#!/usr/bin/env bash
# Moov Mail — Spike S3: load the 5M-message corpus and build the indexes.
#
# Run on the VPS under nohup; it takes tens of minutes:
#   nohup bash /root/moov-s3/load.sh > /root/moov-s3/logs/load.log 2>&1 &
#
# Safety: aborts if free space on / drops below the threshold.
set -euo pipefail

N=${N:-5000000}
SEED=${SEED:-20260808}
CONTAINER=${CONTAINER:-moov-s3-pg}
DB=${DB:-moov_s3}
WORK=/root/moov-s3
MIN_FREE_GB=${MIN_FREE_GB:-60}

psql() { docker exec -i "$CONTAINER" psql -U postgres -d "$DB" "$@"; }

check_space() {
  local free_gb
  free_gb=$(df -BG --output=avail / | tail -1 | tr -dc '0-9')
  echo "[$(date +%T)] free space: ${free_gb} GB"
  if [ "$free_gb" -lt "$MIN_FREE_GB" ]; then
    echo "ABORT: free space ${free_gb} GB below ${MIN_FREE_GB} GB threshold"
    exit 1
  fi
}

echo "=== [$(date +%T)] Spike S3 load: N=$N seed=$SEED ==="
check_space

echo "=== [$(date +%T)] schema ==="
psql -v ON_ERROR_STOP=1 -f /work/schema.sql

echo "=== [$(date +%T)] COPY (streaming from generator, never lands on disk) ==="
COPY_START=$(date +%s)
# The generator writes COPY text format on stdout; piping it straight into
# psql's \copy keeps the ~7 GB of intermediate text out of the filesystem.
"$WORK/scripts/gen" -n "$N" -seed "$SEED" 2> "$WORK/logs/gen.stderr" \
  | docker exec -i "$CONTAINER" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
      -c "COPY messages (account_id, mailbox_id, uid, date, flags, from_addr, to_addrs, subject, body_text) FROM STDIN"
COPY_END=$(date +%s)
echo "COPY elapsed: $((COPY_END - COPY_START)) s"
cat "$WORK/logs/gen.stderr"
check_space

echo "=== [$(date +%T)] row count ==="
psql -c "SELECT count(*) FROM messages;"

echo "=== [$(date +%T)] index build (timed individually) ==="
psql -v ON_ERROR_STOP=1 -c '\timing on' -f /work/indexes.sql
check_space

echo "=== [$(date +%T)] ANALYZE ==="
psql -v ON_ERROR_STOP=1 -c '\timing on' -c "VACUUM (ANALYZE) messages;"

echo "=== [$(date +%T)] sizes ==="
psql -c "SELECT pg_size_pretty(pg_relation_size('messages')) heap,
                pg_size_pretty(pg_relation_size('messages_tsv_gin')) gin,
                pg_size_pretty(pg_total_relation_size('messages')) total;"
check_space
echo "=== [$(date +%T)] DONE ==="
