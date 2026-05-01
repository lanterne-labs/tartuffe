#!/bin/sh
set -eu

LITELLM_PORT="${PORT:-4000}"

exec litellm --config /app/config.yaml --port "${LITELLM_PORT}" --host 0.0.0.0
