#!/bin/sh
set -eu

: "${TS_AUTHKEY:?TS_AUTHKEY env var is required}"
TS_HOSTNAME="${TS_HOSTNAME:-host-litellm}"
LITELLM_PORT="${PORT:-4000}"

mkdir -p /var/run/tailscale /var/cache/tailscale /var/lib/tailscale

# Persist tailscale state on a host volume mounted at /var/lib/tailscale.
# Without persistence (e.g. --state=mem:) every container restart generates a
# new machine key and tailscale auto-numbers the hostname (host-...-1, -2,
# -3, ...), polluting the tailnet admin.
tailscaled \
    --tun=userspace-networking \
    --socks5-server=localhost:1055 \
    --outbound-http-proxy-listen=localhost:1056 \
    --statedir=/var/lib/tailscale &

until tailscale up \
    --authkey="${TS_AUTHKEY}" \
    --hostname="${TS_HOSTNAME}" \
    --accept-dns=true; do
    echo "waiting for tailscaled..."
    sleep 1
done

echo "tailscale up: $(tailscale ip -4 || true)"

# Route outbound through tailscaled's SOCKS5 proxy. The HTTP CONNECT
# proxy (port 1056) is also available for tools that don't speak SOCKS,
# but the userspace netstack truncates larger return payloads for HTTP
# CONNECT (e.g. embedding vectors) — SOCKS5 is a raw byte stream that
# avoids that. The Dockerfile installs httpx[socks] so litellm's httpx
# can speak it.
export HTTP_PROXY="socks5://localhost:1055"
export HTTPS_PROXY="socks5://localhost:1055"
export ALL_PROXY="socks5://localhost:1055"
export NO_PROXY="localhost,127.0.0.1"

exec litellm --config /app/config.yaml --port "${LITELLM_PORT}" --host 0.0.0.0
