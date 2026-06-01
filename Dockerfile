# Stage 1: build nsjail from source (submodule pinned to tag 3.4)
FROM ubuntu:22.04 AS nsjail-builder

# Build dependencies for nsjail. Versions are intentionally unpinned: these are
# the base image's own packages, and exact patch pins break across architectures
# and over time as Ubuntu ships security updates (e.g. git's patch version
# differs between amd64 and arm64). Reproducibility comes from the FROM tag.
RUN apt-get update && apt-get install -y --no-install-recommends \
    bison \
    flex \
    g++ \
    gcc \
    git \
    libcap-dev \
    libnl-route-3-dev \
    libprotobuf-dev \
    libtool \
    make \
    pkg-config \
    protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*

COPY external/nsjail /build/nsjail
WORKDIR /build/nsjail
RUN make -j"$(nproc)" && strip nsjail

# Stage 2: build the Go binary
FROM golang:1.26-bookworm AS go-builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.goVersion=$(go version | awk '{print $3}')" \
    -o /goboxd ./cmd/goboxd

# Stage 3: runtime image
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libnl-route-3-200 \
    libprotobuf23 \
    && rm -rf /var/lib/apt/lists/*

# Install language toolchains. Every script in scripts/lang_install/ runs; adding
# a language means dropping a script here and a YAML block in configs/ — no Go and
# no Dockerfile changes.
COPY scripts/lang_install/ /tmp/lang_install/
RUN apt-get update \
    && for f in /tmp/lang_install/*.sh; do echo "== running $f ==" && bash "$f"; done \
    && rm -rf /var/lib/apt/lists/* /tmp/lang_install

# Install nsjail and goboxd binaries
COPY --from=nsjail-builder /build/nsjail/nsjail /usr/local/bin/nsjail
COPY --from=go-builder /goboxd /usr/local/bin/goboxd
COPY configs/ /etc/goboxd/

RUN mkdir -p /tmp/goboxd

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/goboxd", "-config", "/etc/goboxd/languages.yaml"]
