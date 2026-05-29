# Stage 1: build nsjail from source (submodule pinned to tag 3.4)
FROM ubuntu:22.04 AS nsjail-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    bison=2:3.8.2+dfsg-1build1 \
    flex=2.6.4-8build2 \
    g++=4:11.2.0-1ubuntu1 \
    gcc=4:11.2.0-1ubuntu1 \
    git=1:2.34.1-1ubuntu1.11 \
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
FROM golang:1.22-bookworm AS go-builder

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

# Install language toolchains
COPY scripts/lang_install/ /tmp/lang_install/
RUN apt-get update \
    && bash /tmp/lang_install/py3.sh \
    && bash /tmp/lang_install/cpp.sh \
    && rm -rf /var/lib/apt/lists/* /tmp/lang_install

# Install nsjail and goboxd binaries
COPY --from=nsjail-builder /build/nsjail/nsjail /usr/local/bin/nsjail
COPY --from=go-builder /goboxd /usr/local/bin/goboxd
COPY configs/ /etc/goboxd/

RUN mkdir -p /tmp/goboxd

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/goboxd", "-config", "/etc/goboxd/languages.yaml"]
