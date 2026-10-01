# Lanscape agent. Needs NET_RAW and NET_ADMIN (network tests) and usually host networking.
# distroless has no linux/arm/v6 image, so its architecture-independent files are copied into scratch.
FROM --platform=$BUILDPLATFORM gcr.io/distroless/static-debian12 AS base

FROM scratch
ARG TARGETARCH
ARG TARGETVARIANT
LABEL org.opencontainers.image.title="lanscape-agent" \
      org.opencontainers.image.description="Lanscape agent: inventory, network tests, discovery and checks" \
      org.opencontainers.image.source="https://github.com/retreat-community/lanscape" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"
COPY --from=base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=base /etc/passwd /etc/group /etc/nsswitch.conf /etc/
COPY --from=base /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=base /tmp /tmp
COPY --chmod=0755 linux-${TARGETARCH}${TARGETVARIANT}/lanscape-agent /usr/bin/lanscape-agent
ENV LANSCAPE_DATA_DIR=/var/lib/lanscape-agent
VOLUME ["/var/lib/lanscape-agent"]
ENTRYPOINT ["/usr/bin/lanscape-agent"]
