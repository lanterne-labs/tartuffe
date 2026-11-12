#!/bin/sh
# The worker's warnings and errors over the audit window, grouped by message.
# Usage: worker_errors.sh <worker service id> <out dir> [lines per deployment]
# The log command returns the last N lines of ONE deployment: a burst (every
# seat at its limit logs 2 000 lines in 3 minutes) hides the rest, so the seat
# noise is filtered out on the server and every deployment of the window is read.
set -eu
svc="$1"; out="$2"; n="${3:-5000}"
mkdir -p "$out"
host deployment list --service "$svc" | awk '/SUCCESS|REMOVED/ {print $1}' | head -6 > "$out/deployments.txt"
: > "$out/worker_errors.log"; : > "$out/xbot.log"
while read -r d; do
  host logs "$d" --service "$svc" -n "$n" --filter '-"LLM unavailable"' 2>&1 \
    | grep -E '^[^ ]+ (WARN|ERROR|FATAL|PANIC)' | grep -v 'sk-ant' >> "$out/worker_errors.log" || true
  host logs "$d" --service "$svc" -n "$n" --filter 'xbot' 2>&1 | grep 'xbot' | grep -v 'sk-ant' \
    | sed -E 's#bot[0-9]+:[A-Za-z0-9_-]+#bot<redacted>#g' >> "$out/xbot.log" || true
done < "$out/deployments.txt"
echo "== warnings and errors by message (numbers and URLs folded)"
sed -E 's/^[^ ]+ //; s/[0-9]{2,}/N/g; s#https?://[^ "]+#URL#g' "$out/worker_errors.log" | cut -c1-240 | sort | uniq -c | sort -rn | head -60
echo "== per hour"
cut -c1-13 "$out/worker_errors.log" | sort | uniq -c
echo "== xbot, by message"
sed -E 's/^[^ ]+ //; s/[0-9]{2,}/N/g' "$out/xbot.log" | cut -c1-200 | sort | uniq -c | sort -rn | head -30
