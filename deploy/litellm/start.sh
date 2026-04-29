#!/bin/sh
set -eu

: "${TS_AUTHKEY:?TS_AUTHKEY env var is required}"
TS_HOSTNAME="${TS_HOSTNAME:-host-litellm}"
LITELLM_PORT="${PORT:-4000}"

mkdir -p /var/run/tailscale /var/cache/tailscale /var/lib/tailscale

tailscaled \
    --tun=userspace-networking \
    --outbound-http-proxy-listen=localhost:1055 \
    --state=mem: &

until tailscale up \
    --authkey="${TS_AUTHKEY}" \
    --hostname="${TS_HOSTNAME}" \
    --accept-dns=true; do
    echo "waiting for tailscaled..."
    sleep 1
done

echo "tailscale up: $(tailscale ip -4 || true)"

# Use the HTTP CONNECT proxy that tailscaled exposes. httpx (used by
# litellm) supports HTTP proxies natively; routing via SOCKS would
# require the `socksio` extra which is not bundled in the litellm image.
export HTTP_PROXY="http://localhost:1055/"
export HTTPS_PROXY="http://localhost:1055/"
export NO_PROXY="localhost,127.0.0.1"

exec litellm --config /app/config.yaml --port "${LITELLM_PORT}" --host 0.0.0.0
