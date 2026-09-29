# Lanscape agent. Needs NET_RAW and NET_ADMIN (network tests) and usually host networking.
FROM gcr.io/distroless/static-debian12
ARG TARGETARCH
ARG TARGETVARIANT
LABEL org.opencontainers.image.title="lanscape-agent" \
      org.opencontainers.image.description="Lanscape agent: inventory, network tests, discovery and checks" \
      org.opencontainers.image.source="https://github.com/retreat-community/lanscape" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
COPY linux-${TARGETARCH}${TARGETVARIANT}/lanscape-agent /usr/bin/lanscape-agent
ENV LANSCAPE_DATA_DIR=/var/lib/lanscape-agent
VOLUME ["/var/lib/lanscape-agent"]
ENTRYPOINT ["/usr/bin/lanscape-agent"]
