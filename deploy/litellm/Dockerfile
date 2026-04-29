FROM ghcr.io/berriai/litellm:main-stable

USER root

RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates gnupg && \
    curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.noarmor.gpg \
        | tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null && \
    curl -fsSL https://pkgs.tailscale.com/stable/debian/bookworm.tailscale-keyring.list \
        | tee /etc/apt/sources.list.d/tailscale.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends tailscale && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY start.sh /start.sh
COPY config.yaml /app/config.yaml
RUN chmod +x /start.sh

EXPOSE 4000

ENTRYPOINT ["/start.sh"]
