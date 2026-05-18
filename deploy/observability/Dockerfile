# Colocated Prometheus + Grafana, supervised by s6-overlay.
# One the host service, one volume at /data, Grafana exposed on port 3000.

FROM grafana/grafana-oss:11.3.0

USER root

ARG S6_OVERLAY_VERSION=3.2.0.2
ARG PROM_VERSION=2.55.1
ARG TARGETARCH=amd64

# Base image is Alpine; install fetch/extract tools for the bootstrapping below.
RUN apk add --no-cache curl xz tar ca-certificates tzdata su-exec

# s6-overlay v3 (init system) — noarch + arch-specific tarballs into /
RUN curl -fsSL "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz" \
      | tar -C / -Jxpf - \
 && S6_ARCH="x86_64"; \
    if [ "$TARGETARCH" = "arm64" ]; then S6_ARCH="aarch64"; fi; \
    curl -fsSL "https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${S6_ARCH}.tar.xz" \
      | tar -C / -Jxpf -

# Prometheus binaries
RUN PROM_ARCH="amd64"; \
    if [ "$TARGETARCH" = "arm64" ]; then PROM_ARCH="arm64"; fi; \
    curl -fsSL "https://github.com/prometheus/prometheus/releases/download/v${PROM_VERSION}/prometheus-${PROM_VERSION}.linux-${PROM_ARCH}.tar.gz" \
      | tar -C /tmp -xzf - \
 && mv "/tmp/prometheus-${PROM_VERSION}.linux-${PROM_ARCH}/prometheus" /usr/local/bin/ \
 && mv "/tmp/prometheus-${PROM_VERSION}.linux-${PROM_ARCH}/promtool"   /usr/local/bin/ \
 && rm -rf /tmp/prometheus-*

# Configs
COPY prometheus.yml /etc/prometheus/prometheus.yml
COPY grafana/provisioning/ /etc/grafana/provisioning/
COPY grafana/dashboards/   /etc/grafana/dashboards/

# s6-overlay service tree
COPY s6-rc.d/ /etc/s6-overlay/s6-rc.d/
RUN chmod +x /etc/s6-overlay/s6-rc.d/init-perms/up \
             /etc/s6-overlay/s6-rc.d/prometheus/run \
             /etc/s6-overlay/s6-rc.d/grafana/run

# Grafana paths point at the persistent volume; provisioning paths stay in /etc.
ENV GF_PATHS_DATA=/data/grafana \
    GF_PATHS_LOGS=/data/grafana/log \
    GF_PATHS_PLUGINS=/data/grafana/plugins \
    GF_PATHS_PROVISIONING=/etc/grafana/provisioning \
    GF_USERS_ALLOW_SIGN_UP=false \
    GF_AUTH_ANONYMOUS_ENABLED=false \
    S6_KEEP_ENV=1 \
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2

EXPOSE 3000

ENTRYPOINT ["/init"]
