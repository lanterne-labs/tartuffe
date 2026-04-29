# Stage 1: fetch Tailscale static binaries from a clean Debian builder.
# The litellm base image is minimal and does not have apt-get / package managers
# we can rely on, so we pull tailscale + tailscaled directly and copy them in.
FROM debian:bookworm-slim AS tsbuilder

ARG TS_VERSION=1.78.1
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates && \
    curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${TS_VERSION}_amd64.tgz" -o /tmp/ts.tgz && \
    tar -xzf /tmp/ts.tgz -C /tmp && \
    mv "/tmp/tailscale_${TS_VERSION}_amd64/tailscale"  /usr/local/bin/tailscale && \
    mv "/tmp/tailscale_${TS_VERSION}_amd64/tailscaled" /usr/local/bin/tailscaled && \
    chmod +x /usr/local/bin/tailscale /usr/local/bin/tailscaled

# Stage 2: litellm + tailscale.
FROM ghcr.io/berriai/litellm:main-stable

USER root

COPY --from=tsbuilder /usr/local/bin/tailscale  /usr/local/bin/tailscale
COPY --from=tsbuilder /usr/local/bin/tailscaled /usr/local/bin/tailscaled

COPY start.sh /start.sh
COPY config.yaml /app/config.yaml
RUN chmod +x /start.sh

EXPOSE 4000

ENTRYPOINT ["/start.sh"]
