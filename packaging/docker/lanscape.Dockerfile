# Lanscape server. Build context: see scripts/docker-context.sh.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETARCH
ARG TARGETVARIANT
LABEL org.opencontainers.image.title="lanscape" \
      org.opencontainers.image.description="Lanscape server: network paths, map and service uptime" \
      org.opencontainers.image.source="https://github.com/retreat-community/lanscape" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
COPY linux-${TARGETARCH}${TARGETVARIANT}/lanscape /usr/bin/lanscape
ENV LANSCAPE_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080 8443 47701
USER nonroot:nonroot
ENTRYPOINT ["/usr/bin/lanscape"]
CMD ["serve"]
