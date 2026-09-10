# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -checklinkname=0" -o /out/llard ./cmd/llard

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        cmake \
        curl \
        git \
        pkg-config \
    && curl --fail --silent --show-error --location \
        https://apt.llvm.org/llvm-snapshot.gpg.key \
        -o /usr/share/keyrings/apt.llvm.org.asc \
    && echo "deb [signed-by=/usr/share/keyrings/apt.llvm.org.asc] https://apt.llvm.org/bookworm/ llvm-toolchain-bookworm-19 main" \
        > /etc/apt/sources.list.d/llvm19.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        clang-19 lld-19 llvm-19 \
    && rm -rf /var/lib/apt/lists/*

ENV PATH="/usr/lib/llvm-19/bin:${PATH}"

RUN useradd --create-home --uid 10001 --user-group llard

ENV HOME=/home/llard \
    XDG_CACHE_HOME=/home/llard/.cache \
    LLARD_ADDR=0.0.0.0:8080

COPY --from=build /out/llard /usr/local/bin/llard

USER llard
WORKDIR /home/llard

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/llard"]
