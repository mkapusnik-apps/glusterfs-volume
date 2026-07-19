FROM golang:1.20-bullseye AS builder

ARG VERSION=dev
ARG VCS_REF

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      libglusterfs-dev uuid-dev glusterfs-client ca-certificates pkg-config build-essential && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY . .
RUN set -eu; \
    test -n "${VCS_REF}"; \
    test "${VCS_REF}" != "unknown"; \
    unformatted="$(gofmt -l *.go)"; \
    if [ -n "${unformatted}" ]; then \
      printf 'Unformatted Go files:\n%s\n' "${unformatted}" >&2; \
      gofmt -d ${unformatted} >&2; \
      exit 1; \
    fi; \
    go vet ./...; \
    go build -trimpath \
      -ldflags "-X main.version=${VERSION} -X main.revision=${VCS_REF}" \
      -o /out/docker-volume-glusterfs

FROM scratch AS artifact
COPY --from=builder /out/docker-volume-glusterfs /docker-volume-glusterfs

FROM debian:bullseye-slim AS plugin

ARG VERSION=dev
ARG VCS_REF

LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.source="https://github.com/mkapusnik/glusterfs-volume"

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      glusterfs-client libgfapi0 tini ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN mkdir -p /var/lib/glusterfs-volume

COPY --from=builder /out/docker-volume-glusterfs /docker-volume-glusterfs
RUN ln -sf /usr/bin/tini /tini

ENTRYPOINT ["/tini", "--"]
CMD ["/docker-volume-glusterfs"]
