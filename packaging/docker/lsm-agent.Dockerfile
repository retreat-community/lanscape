# Lanscape Mini agent: a single static binary, image size ≈ binary size.
# Build context: a directory with mini binaries per target (linux-amd64/, linux-armv7/, ...).
FROM scratch
ARG TARGETARCH
ARG TARGETVARIANT
LABEL org.opencontainers.image.title="lsm-agent" \
      org.opencontainers.image.description="Lanscape Mini agent" \
      org.opencontainers.image.source="https://github.com/retreat-community/lanscape" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
COPY --chmod=0755 linux-${TARGETARCH}${TARGETVARIANT}/lsm-agent /lsm-agent
ENTRYPOINT ["/lsm-agent"]
