FROM ghcr.io/berriai/litellm:main-stable

USER root

COPY start.sh /start.sh
COPY config.yaml /app/config.yaml
RUN chmod +x /start.sh

EXPOSE 4000

ENTRYPOINT ["/start.sh"]
