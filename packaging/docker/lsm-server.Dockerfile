# Lanscape Mini server: a single static binary with the embedded page.
FROM scratch
ARG TARGETARCH
ARG TARGETVARIANT
LABEL org.opencontainers.image.title="lsm-server" \
      org.opencontainers.image.description="Lanscape Mini server" \
      org.opencontainers.image.source="https://github.com/retreat-community/lanscape" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
COPY linux-${TARGETARCH}${TARGETVARIANT}/lsm-server /lsm-server
ENV LSM_DATA_FILE=/data/runs.json
VOLUME ["/data"]
EXPOSE 8080 47701
ENTRYPOINT ["/lsm-server"]
